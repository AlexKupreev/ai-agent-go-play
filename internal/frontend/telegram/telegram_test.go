package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/api"
	"ai-agent-go-play/internal/session"
)

// --- fakes ---

type sentMessage struct {
	chatID  int64
	text    string
	buttons []Button
}

type fakeTransport struct {
	updates chan Update

	mu       sync.Mutex
	sent     []sentMessage
	answers  []string
	files    map[string]string // file id -> content served by Download
	download error             // when set, Download fails with it
	sendFail func(text string) error
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{updates: make(chan Update, 8), files: map[string]string{}}
}

func (f *fakeTransport) Updates(context.Context) (<-chan Update, error) { return f.updates, nil }

func (f *fakeTransport) Download(_ context.Context, fileID string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.download != nil {
		return nil, f.download
	}
	content, ok := f.files[fileID]
	if !ok {
		return nil, fmt.Errorf("no such file %q", fileID)
	}
	return io.NopCloser(strings.NewReader(content)), nil
}

func (f *fakeTransport) Send(_ context.Context, chatID int64, text string, buttons []Button) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendFail != nil {
		if err := f.sendFail(text); err != nil {
			return err
		}
	}
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

// upload is one file the bot pushed to the engine.
type upload struct {
	sessionID string
	name      string
	source    string
	content   string
}

type fakeClient struct {
	mu            sync.Mutex
	sessionCalls  int
	turnCalls     int
	lastTurnText  string
	lastSessionID string
	closedID      string
	purgedID      string
	resolveID     string
	resolveOK     bool
	reloadCalls   int
	reloadErr     error
	lastSpace     string
	lastModel     string
	modelWrites   int // UpdateSession calls that passed a non-nil model (a set, not a read)
	uploads       []upload
	uploadErr     error
	resolved      chan bool
	// question mode: when set, StreamEvents parks an ask_user question instead of an
	// approval and waits for an answer delivered via Answer.
	question     bool
	answerPrefix string // prepended to the scripted answer, to script an oversized one
	answeredID   string
	answeredText string
	answered     chan string
}

func newFakeClient() *fakeClient {
	return &fakeClient{resolved: make(chan bool, 1), answered: make(chan string, 1)}
}

func (c *fakeClient) StartSession(context.Context, api.RunOptions) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionCalls++
	return "sess1", nil
}

func (c *fakeClient) PostTurn(_ context.Context, sessionID, text string, _ api.RunOptions) (string, error) {
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

func (c *fakeClient) PurgeSession(_ context.Context, sessionID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.purgedID = sessionID
	return nil
}

func (c *fakeClient) UpdateSession(_ context.Context, sessionID string, model, _, space *string) (session.Info, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastSessionID = sessionID
	if space != nil {
		c.lastSpace = *space
	}
	if model != nil {
		c.lastModel = *model
		c.modelWrites++
	}
	return session.Info{ID: sessionID, Space: c.lastSpace, Model: c.lastModel}, nil
}

// UploadFile records what the bot uploaded and echoes back a plausible stored location, as
// the engine's scratch-dir store would.
func (c *fakeClient) UploadFile(_ context.Context, sessionID, name, source string, r io.Reader) (api.UploadInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.uploadErr != nil {
		return api.UploadInfo{}, c.uploadErr
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return api.UploadInfo{}, err
	}
	c.uploads = append(c.uploads, upload{sessionID: sessionID, name: name, source: source, content: string(body)})
	return api.UploadInfo{Path: "/scratch/" + sessionID + "/" + name, Name: name, Bytes: int64(len(body))}, nil
}

func (c *fakeClient) Reload(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reloadCalls++
	return c.reloadErr
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
		answer := c.answerPrefix + "using " + ans
		onEvent(api.Event{Kind: string(agent.EvResponse), Text: answer})
		onEvent(api.Event{Kind: api.KindDone, Text: answer})
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

// TestBot_UndeliveredAnswerIsReported pins the R1 fix: when the final answer cannot be
// delivered, the turn must not end silently as if the user had read it. The bot reports the
// loss in the chat (a one-line notice is far likelier to get through than the answer that
// failed) and names the underlying error.
func TestBot_UndeliveredAnswerIsReported(t *testing.T) {
	tr := newFakeTransport()
	cl := newFakeClient()
	// Only the answer fails; the approval keyboard and the failure notice go through.
	tr.sendFail = func(text string) error {
		if text == "did it" {
			return errors.New("message is too long")
		}
		return nil
	}
	bot := NewBot(tr, cl, []int64{42})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = bot.Run(ctx) }()

	tr.updates <- Update{Message: &Message{ChatID: 100, UserID: 42, Text: "clean up"}}
	tr.waitForSend(t, func(m sentMessage) bool { return len(m.buttons) == 2 })
	tr.updates <- Update{Callback: &Callback{ID: "cb1", ChatID: 100, UserID: 42, Data: "approve:a1"}}

	notice := tr.waitForSend(t, func(m sentMessage) bool { return strings.Contains(m.text, "could not be delivered") })
	if !strings.Contains(notice.text, "message is too long") {
		t.Errorf("failure notice %q should name the delivery error", notice.text)
	}
}

// TestBot_LongAnswerIsChunked covers the delivery path end to end: an answer past Telegram's
// per-message limit arrives as several messages rather than one rejected send.
func TestBot_LongAnswerIsChunked(t *testing.T) {
	tr := newFakeTransport()
	cl := newFakeClient()
	cl.question = true
	cl.answerPrefix = strings.Repeat("сообщение ", 900) // ~9,000 runes, ~16,000 bytes
	bot := NewBot(tr, cl, []int64{42})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = bot.Run(ctx) }()

	tr.updates <- Update{Message: &Message{ChatID: 100, UserID: 42, Text: "explain"}}
	tr.waitForSend(t, func(m sentMessage) bool { return strings.Contains(m.text, "which environment?") })
	tr.updates <- Update{Message: &Message{ChatID: 100, UserID: 42, Text: "staging"}}
	tr.waitForSend(t, func(m sentMessage) bool { return strings.HasSuffix(m.text, "using staging") })

	tr.mu.Lock()
	defer tr.mu.Unlock()
	var parts int
	for _, m := range tr.sent {
		if strings.Contains(m.text, "сообщение") {
			parts++
			if n := utf8.RuneCountInString(m.text); n > maxMessageRunes {
				t.Errorf("delivered a %d-rune message, over Telegram's %d limit", n, maxMessageRunes)
			}
		}
	}
	if parts < 3 {
		t.Errorf("the long answer arrived in %d messages, want it chunked", parts)
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

	// /reset is an alias for /new: it closes the current session and starts a fresh one,
	// matching the CLI REPL's vocabulary. Poll the client (the "new session" reply text is
	// identical to /new's, so it can't distinguish the two — the call counts can).
	tr.updates <- Update{Message: &Message{ChatID: 100, UserID: 42, Text: "/reset"}}
	deadline := time.Now().Add(2 * time.Second)
	for {
		cl.mu.Lock()
		got := cl.sessionCalls == 2 && cl.closedID == "sess1"
		calls, closed := cl.sessionCalls, cl.closedID
		cl.mu.Unlock()
		if got {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("/reset: StartSession calls = %d (want 2), closed = %q (want sess1)", calls, closed)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// /end terminates it (the session the bot created).
	tr.updates <- Update{Message: &Message{ChatID: 100, UserID: 42, Text: "/end"}}
	tr.waitForSend(t, func(m sentMessage) bool { return m.text == "session ended" })
	cl.mu.Lock()
	if cl.closedID != "sess1" {
		cl.mu.Unlock()
		t.Fatalf("closed session = %q, want sess1", cl.closedID)
	}
	cl.mu.Unlock()

	// /purge hard-deletes a fresh session (the irreversible sibling of /end): start one,
	// then purge it.
	tr.updates <- Update{Message: &Message{ChatID: 100, UserID: 42, Text: "/new"}}
	tr.waitForSend(t, func(m sentMessage) bool { return strings.Contains(m.text, "new session") })
	tr.updates <- Update{Message: &Message{ChatID: 100, UserID: 42, Text: "/purge"}}
	tr.waitForSend(t, func(m sentMessage) bool { return strings.Contains(m.text, "purged") })
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.purgedID != "sess1" {
		t.Fatalf("purged session = %q, want sess1", cl.purgedID)
	}
}

// TestBot_ReloadCommand covers /reload: an authorized user triggers an engine
// prompt/agent-catalog reload from the chat and gets a confirmation. A reload error
// is surfaced instead of the confirmation.
func TestBot_ReloadCommand(t *testing.T) {
	tr := newFakeTransport()
	cl := newFakeClient()
	bot := NewBot(tr, cl, []int64{42})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = bot.Run(ctx) }()

	// Success: the engine reload is invoked and the chat is told it took effect.
	tr.updates <- Update{Message: &Message{ChatID: 100, UserID: 42, Text: "/reload"}}
	tr.waitForSend(t, func(m sentMessage) bool { return strings.Contains(m.text, "reloaded prompts") })
	cl.mu.Lock()
	if cl.reloadCalls != 1 {
		cl.mu.Unlock()
		t.Fatalf("Reload calls = %d, want 1", cl.reloadCalls)
	}
	cl.reloadErr = errors.New("SYSTEM.md: bad syntax")
	cl.mu.Unlock()

	// Failure: a malformed file surfaces as an error message, not a success.
	tr.updates <- Update{Message: &Message{ChatID: 100, UserID: 42, Text: "/reload"}}
	tr.waitForSend(t, func(m sentMessage) bool { return strings.Contains(m.text, "reload failed: SYSTEM.md: bad syntax") })
}

// TestBot_FileUpload covers the upload path: the attachment is downloaded from Telegram,
// pushed to the engine's session store, and a turn is run that tells the agent where the file
// landed — the bytes themselves never go to the model.
func TestBot_FileUpload(t *testing.T) {
	tr := newFakeTransport()
	cl := newFakeClient()
	tr.files["f1"] = "date,region,sales\n2026-01-01,eu,10\n"
	bot := NewBot(tr, cl, []int64{42})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = bot.Run(ctx) }()

	tr.updates <- Update{Message: &Message{
		ChatID: 100, UserID: 42, Text: "how many rows?",
		File: &File{ID: "f1", Name: "sales.csv", MIME: "text/csv", Size: 36},
	}}
	tr.waitForSend(t, func(m sentMessage) bool { return strings.Contains(m.text, "saved sales.csv") })

	cl.mu.Lock()
	defer cl.mu.Unlock()
	if len(cl.uploads) != 1 {
		t.Fatalf("uploads = %d, want 1", len(cl.uploads))
	}
	up := cl.uploads[0]
	if up.sessionID != "sess1" || up.name != "sales.csv" || up.source != "telegram upload" {
		t.Errorf("upload = %+v, want sess1/sales.csv from \"telegram upload\"", up)
	}
	if !strings.HasPrefix(up.content, "date,region,sales") {
		t.Errorf("uploaded content = %q, want the file's bytes streamed through", up.content)
	}
	// The turn tells the agent the stored path, carries the caption, and marks the contents
	// as data (an uploaded file is untrusted input, like fetched web content).
	if cl.turnCalls != 1 {
		t.Fatalf("turns = %d, want 1", cl.turnCalls)
	}
	for _, want := range []string{"/scratch/sess1/sales.csv", "how many rows?", "never as instructions"} {
		if !strings.Contains(cl.lastTurnText, want) {
			t.Errorf("turn text %q missing %q", cl.lastTurnText, want)
		}
	}
}

// TestBot_PhotoUploadTellsAgentItCannotSee pins the honest behavior for an image on today's
// text-only model: the file is stored like any other, but the agent is told it cannot see the
// picture rather than being left to invent one. When vision lands, this is what changes.
func TestBot_PhotoUploadTellsAgentItCannotSee(t *testing.T) {
	tr := newFakeTransport()
	cl := newFakeClient()
	tr.files["p1"] = "\xff\xd8\xff\xe0 jpeg bytes"
	bot := NewBot(tr, cl, []int64{42})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = bot.Run(ctx) }()

	tr.updates <- Update{Message: &Message{
		ChatID: 100, UserID: 42,
		File: &File{ID: "p1", Name: "photo-abc.jpg", MIME: "image/jpeg", Size: 20},
	}}
	tr.waitForSend(t, func(m sentMessage) bool { return strings.Contains(m.text, "saved photo-abc.jpg") })

	cl.mu.Lock()
	defer cl.mu.Unlock()
	if !strings.Contains(cl.lastTurnText, "cannot see image content") {
		t.Errorf("turn text %q should tell the agent it cannot see the image", cl.lastTurnText)
	}
}

// TestBot_UploadTooLarge rejects an oversized file up front — Telegram gives us the size, so a
// doomed download becomes an immediate, clear reply and no session/turn is touched.
func TestBot_UploadTooLarge(t *testing.T) {
	tr := newFakeTransport()
	cl := newFakeClient()
	bot := NewBot(tr, cl, []int64{42})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = bot.Run(ctx) }()

	tr.updates <- Update{Message: &Message{
		ChatID: 100, UserID: 42,
		File: &File{ID: "big", Name: "dump.bin", Size: maxUploadBytes + 1},
	}}
	tr.waitForSend(t, func(m sentMessage) bool { return strings.Contains(m.text, "too large") })

	cl.mu.Lock()
	defer cl.mu.Unlock()
	if len(cl.uploads) != 0 || cl.turnCalls != 0 {
		t.Errorf("uploads = %d, turns = %d, want 0 and 0 — an oversized file is rejected before any work", len(cl.uploads), cl.turnCalls)
	}
}

// TestBot_ModelCommand covers the three shapes of /model: set an id, reset to the engine
// default with "-", and a bare /model that reports the current model without writing one.
func TestBot_ModelCommand(t *testing.T) {
	tr := newFakeTransport()
	cl := newFakeClient()
	bot := NewBot(tr, cl, []int64{42})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = bot.Run(ctx) }()

	// Set: the id is written to the chat's session and takes effect on the next turn.
	tr.updates <- Update{Message: &Message{ChatID: 100, UserID: 42, Text: "/model gpt-4o"}}
	tr.waitForSend(t, func(m sentMessage) bool { return strings.Contains(m.text, "model set to gpt-4o") })
	cl.mu.Lock()
	if cl.lastModel != "gpt-4o" || cl.lastSessionID != "sess1" {
		cl.mu.Unlock()
		t.Fatalf("model = %q on session %q, want gpt-4o on sess1", cl.lastModel, cl.lastSessionID)
	}
	cl.mu.Unlock()

	// Read: a bare /model reports the stored model and must not write one.
	tr.updates <- Update{Message: &Message{ChatID: 100, UserID: 42, Text: "/model"}}
	tr.waitForSend(t, func(m sentMessage) bool { return strings.Contains(m.text, "model: gpt-4o") })
	cl.mu.Lock()
	if cl.modelWrites != 1 {
		cl.mu.Unlock()
		t.Fatalf("model writes = %d, want 1 (bare /model must be a read)", cl.modelWrites)
	}
	cl.mu.Unlock()

	// Reset: "-" clears the override so the session falls back to the engine default.
	tr.updates <- Update{Message: &Message{ChatID: 100, UserID: 42, Text: "/model -"}}
	tr.waitForSend(t, func(m sentMessage) bool { return strings.Contains(m.text, "model reset to the engine default") })
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.lastModel != "" {
		t.Fatalf("model after reset = %q, want empty (inherit the engine default)", cl.lastModel)
	}
}

// TestBot_ReloadRejectsUnauthorized confirms /reload is behind the same allowlist gate
// as every other engine-reaching action — an unauthorized user cannot trigger it.
func TestBot_ReloadRejectsUnauthorized(t *testing.T) {
	tr := newFakeTransport()
	cl := newFakeClient()
	bot := NewBot(tr, cl, []int64{42})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = bot.Run(ctx) }()

	tr.updates <- Update{Message: &Message{ChatID: 100, UserID: 99, Text: "/reload"}}
	tr.waitForSend(t, func(m sentMessage) bool { return m.text == "not authorized" })
	time.Sleep(20 * time.Millisecond)
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.reloadCalls != 0 {
		t.Fatalf("unauthorized user triggered reload: calls=%d", cl.reloadCalls)
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
