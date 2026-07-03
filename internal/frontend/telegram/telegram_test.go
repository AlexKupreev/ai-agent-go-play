package telegram

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-agent-go-play/internal/agent"
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
	mu            sync.Mutex
	sessionCalls  int
	turnCalls     int
	lastTurnText  string
	lastSessionID string
	closedID      string
	resolveID     string
	resolveOK     bool
	resolved      chan bool
	// question mode: when set, StreamEvents parks an ask_user question instead of an
	// approval and waits for an answer delivered via Answer.
	question     bool
	answeredID   string
	answeredText string
	answered     chan string
}

func newFakeClient() *fakeClient {
	return &fakeClient{resolved: make(chan bool, 1), answered: make(chan string, 1)}
}

func (c *fakeClient) StartSession(context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionCalls++
	return "sess1", nil
}

func (c *fakeClient) PostTurn(_ context.Context, sessionID, text string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.turnCalls++
	c.lastSessionID = sessionID
	c.lastTurnText = text
	return "run1", nil
}

func (c *fakeClient) CloseSession(_ context.Context, sessionID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closedID = sessionID
	return nil
}

// StreamEvents scripts a run that parks an approval, waits for the decision (via
// Resolve), then finishes — mirroring the real engine's approval flow.
func (c *fakeClient) StreamEvents(ctx context.Context, _ string, onEvent func(api.Event)) error {
	if c.question {
		return c.streamQuestion(ctx, onEvent)
	}
	onEvent(api.Event{
		Kind: api.KindApprovalRequested, ApprovalID: "a1",
		Tool: "shell.destructive", Text: "rm -rf build", Input: "rm -rf ./build",
	})
	select {
	case d := <-c.resolved:
		onEvent(api.Event{Kind: api.KindApprovalResolved, ApprovalID: "a1", Approved: &d})
		// Mirror the real engine: the final answer arrives as a response event, then
		// the done event is just a terminal marker (its text duplicates the response
		// and must not be forwarded again).
		answer := "did it"
		if !d {
			answer = "declined"
		}
		onEvent(api.Event{Kind: string(agent.EvResponse), Text: answer})
		onEvent(api.Event{Kind: api.KindDone, Text: answer})
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

// streamQuestion scripts a run that parks an ask_user question, waits for the answer
// (via Answer), then finishes.
func (c *fakeClient) streamQuestion(ctx context.Context, onEvent func(api.Event)) error {
	onEvent(api.Event{Kind: api.KindQuestionRequested, ApprovalID: "q1", Text: "which environment?"})
	select {
	case ans := <-c.answered:
		onEvent(api.Event{Kind: api.KindQuestionAnswered, ApprovalID: "q1", Text: ans})
		onEvent(api.Event{Kind: string(agent.EvResponse), Text: "using " + ans})
		onEvent(api.Event{Kind: api.KindDone, Text: "using " + ans})
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *fakeClient) Answer(_ context.Context, id, text string) error {
	c.mu.Lock()
	c.answeredID = id
	c.answeredText = text
	c.mu.Unlock()
	c.answered <- text
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
	if cl.lastTurnText != "clean up" {
		t.Fatalf("turn text = %q, want %q", cl.lastTurnText, "clean up")
	}
	if cl.sessionCalls != 1 || cl.lastSessionID != "sess1" {
		t.Fatalf("expected one session created and used, got calls=%d id=%q", cl.sessionCalls, cl.lastSessionID)
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

// TestBot_QuestionFlow drives the ask_user path: a run parks a free-text question, the
// bot relays it, and the user's next reply is delivered as the answer to that question
// (not started as a new turn).
func TestBot_QuestionFlow(t *testing.T) {
	tr := newFakeTransport()
	cl := newFakeClient()
	cl.question = true
	bot := NewBot(tr, cl, []int64{42})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = bot.Run(ctx) }()

	// Allowed user sends a task; the run parks a question relayed to the chat.
	tr.updates <- Update{Message: &Message{ChatID: 100, UserID: 42, Text: "deploy"}}
	q := tr.waitForSend(t, func(m sentMessage) bool { return strings.Contains(m.text, "which environment?") })
	if q.chatID != 100 || len(q.buttons) != 0 {
		t.Fatalf("question should be a plain prompt with no buttons: %+v", q)
	}
	if cl.turnCalls != 1 {
		t.Fatalf("turnCalls = %d, want 1 (the initial task)", cl.turnCalls)
	}

	// The user's next message is the answer — routed to Answer, not a new turn.
	tr.updates <- Update{Message: &Message{ChatID: 100, UserID: 42, Text: "staging"}}
	tr.waitForSend(t, func(m sentMessage) bool { return m.text == "using staging" })

	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.answeredID != "q1" || cl.answeredText != "staging" {
		t.Fatalf("answer = (%q, %q), want (q1, staging)", cl.answeredID, cl.answeredText)
	}
	if cl.turnCalls != 1 {
		t.Fatalf("turnCalls = %d, want 1 — the answer must not start a new turn", cl.turnCalls)
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
	// Give the bot a moment; it must not have started a session or run a turn.
	time.Sleep(20 * time.Millisecond)
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.sessionCalls != 0 || cl.turnCalls != 0 {
		t.Fatalf("unauthorized user reached engine: sessions=%d turns=%d", cl.sessionCalls, cl.turnCalls)
	}
}

// TestBot_NewAndEndCommands covers the session control commands: /new creates a
// session, /end terminates it.
func TestBot_NewAndEndCommands(t *testing.T) {
	tr := newFakeTransport()
	cl := newFakeClient()
	bot := NewBot(tr, cl, []int64{42})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = bot.Run(ctx) }()

	// /new starts a session.
	tr.updates <- Update{Message: &Message{ChatID: 100, UserID: 42, Text: "/new"}}
	tr.waitForSend(t, func(m sentMessage) bool { return strings.Contains(m.text, "new session") })
	cl.mu.Lock()
	if cl.sessionCalls != 1 {
		cl.mu.Unlock()
		t.Fatalf("StartSession calls = %d, want 1", cl.sessionCalls)
	}
	cl.mu.Unlock()

	// /end terminates it (the session the bot created).
	tr.updates <- Update{Message: &Message{ChatID: 100, UserID: 42, Text: "/end"}}
	tr.waitForSend(t, func(m sentMessage) bool { return m.text == "session ended" })
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.closedID != "sess1" {
		t.Fatalf("closed session = %q, want sess1", cl.closedID)
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
