package telegram

import (
	"context"
	"sync"
	"testing"
	"time"

	"ai-agent-go-play/internal/api"
)

// --- fakes ---

type sentMessage struct {
	chatID  int64
	text    string
	buttons []Button
}

type fakeTransport struct {
	updates chan Update

	mu      sync.Mutex
	sent    []sentMessage
	answers []string
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{updates: make(chan Update, 8)}
}

func (f *fakeTransport) Updates(context.Context) (<-chan Update, error) { return f.updates, nil }

func (f *fakeTransport) Send(_ context.Context, chatID int64, text string, buttons []Button) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentMessage{chatID, text, buttons})
	return nil
}

func (f *fakeTransport) Answer(_ context.Context, _, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, text)
	return nil
}

// waitForSend polls until a sent message satisfies pred, returning it.
func (f *fakeTransport) waitForSend(t *testing.T, pred func(sentMessage) bool) sentMessage {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, m := range f.sent {
			if pred(m) {
				f.mu.Unlock()
				return m
			}
		}
		f.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for matching sent message")
	return sentMessage{}
}

type fakeClient struct {
	mu          sync.Mutex
	startCalls  int
	startedTask string
	resolveID   string
	resolveOK   bool
	resolved    chan bool
}

func newFakeClient() *fakeClient { return &fakeClient{resolved: make(chan bool, 1)} }

func (c *fakeClient) StartRun(_ context.Context, task string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.startCalls++
	c.startedTask = task
	return "run1", nil
}

// StreamEvents scripts a run that parks an approval, waits for the decision (via
// Resolve), then finishes — mirroring the real engine's approval flow.
func (c *fakeClient) StreamEvents(ctx context.Context, _ string, onEvent func(api.Event)) error {
	onEvent(api.Event{
		Kind: api.KindApprovalRequested, ApprovalID: "a1",
		Tool: "shell.destructive", Text: "rm -rf build", Input: "rm -rf ./build",
	})
	select {
	case d := <-c.resolved:
		onEvent(api.Event{Kind: api.KindApprovalResolved, ApprovalID: "a1", Approved: &d})
		if d {
			onEvent(api.Event{Kind: api.KindDone, Text: "did it"})
		} else {
			onEvent(api.Event{Kind: api.KindDone, Text: "declined"})
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *fakeClient) Resolve(_ context.Context, id string, approved bool) error {
	c.mu.Lock()
	c.resolveID = id
	c.resolveOK = approved
	c.mu.Unlock()
	c.resolved <- approved
	return nil
}

// --- tests ---

// TestBot_ApprovalFlow drives the whole peer-frontend loop: an allowed user's message
// starts a run; the parked escalation arrives as an Approve/Deny keyboard; pressing
// Approve resolves it over the client and the run finishes back in the chat.
func TestBot_ApprovalFlow(t *testing.T) {
	tr := newFakeTransport()
	cl := newFakeClient()
	bot := NewBot(tr, cl, []int64{42})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = bot.Run(ctx) }()

	// Allowed user sends a task.
	tr.updates <- Update{Message: &Message{ChatID: 100, UserID: 42, Text: "clean up"}}

	// The escalation surfaces as a message carrying an inline keyboard.
	kb := tr.waitForSend(t, func(m sentMessage) bool { return len(m.buttons) == 2 })
	if kb.chatID != 100 || kb.buttons[0].Data != "approve:a1" || kb.buttons[1].Data != "deny:a1" {
		t.Fatalf("unexpected approval keyboard: %+v", kb)
	}
	if cl.startedTask != "clean up" {
		t.Fatalf("run task = %q, want %q", cl.startedTask, "clean up")
	}

	// User presses Approve.
	tr.updates <- Update{Callback: &Callback{ID: "cb1", ChatID: 100, UserID: 42, Data: "approve:a1"}}

	// The run finishes and the result lands in the chat.
	tr.waitForSend(t, func(m sentMessage) bool { return m.text == "did it" })

	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.resolveID != "a1" || !cl.resolveOK {
		t.Fatalf("resolve = (%q, %v), want (a1, true)", cl.resolveID, cl.resolveOK)
	}
}

func TestBot_RejectsUnauthorizedMessage(t *testing.T) {
	tr := newFakeTransport()
	cl := newFakeClient()
	bot := NewBot(tr, cl, []int64{42}) // 99 is not allowed

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = bot.Run(ctx) }()

	tr.updates <- Update{Message: &Message{ChatID: 100, UserID: 99, Text: "do something"}}

	tr.waitForSend(t, func(m sentMessage) bool { return m.text == "not authorized" })
	// Give the bot a moment; it must not have started a run.
	time.Sleep(20 * time.Millisecond)
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.startCalls != 0 {
		t.Fatalf("StartRun called %d times for unauthorized user, want 0", cl.startCalls)
	}
}

func TestBot_RejectsUnauthorizedCallback(t *testing.T) {
	tr := newFakeTransport()
	cl := newFakeClient()
	bot := NewBot(tr, cl, []int64{42})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = bot.Run(ctx) }()

	tr.updates <- Update{Callback: &Callback{ID: "cb1", ChatID: 100, UserID: 99, Data: "approve:a1"}}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tr.mu.Lock()
		n := len(tr.answers)
		var last string
		if n > 0 {
			last = tr.answers[n-1]
		}
		tr.mu.Unlock()
		if n > 0 {
			if last != "not authorized" {
				t.Fatalf("callback answer = %q, want %q", last, "not authorized")
			}
			cl.mu.Lock()
			defer cl.mu.Unlock()
			if cl.resolveID != "" {
				t.Fatalf("Resolve called for unauthorized callback")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for callback answer")
}

func TestParseCallback(t *testing.T) {
	cases := []struct {
		data       string
		action, id string
		ok         bool
	}{
		{"approve:a1", "approve", "a1", true},
		{"deny:a9", "deny", "a9", true},
		{"approve:", "", "", false},
		{"bogus:a1", "", "", false},
		{"noseparator", "", "", false},
	}
	for _, c := range cases {
		a, id, ok := parseCallback(c.data)
		if a != c.action || id != c.id || ok != c.ok {
			t.Errorf("parseCallback(%q) = (%q,%q,%v), want (%q,%q,%v)", c.data, a, id, ok, c.action, c.id, c.ok)
		}
	}
}

// Compile-time proof that the real client satisfies the bot's Client interface, so
// serve can pass *api.Client directly.
var _ Client = (*api.Client)(nil)
