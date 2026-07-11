package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/provider"
	"ai-agent-go-play/internal/session"
)

// echoTurns is a TurnRunner that appends the user turn to the prior history and
// returns it, so a test can prove prior context is threaded and persisted.
func echoTurns() TurnRunnerFunc {
	return func(_ context.Context, _, _ string, prior []provider.Message, text string, _ RunOptions, _ agent.Observer) (string, []provider.Message, error) {
		updated := append(append([]provider.Message{}, prior...), provider.UserText(text))
		return "ok:" + text, updated, nil
	}
}

// recordingTurns is a TurnRunner that records the RunOptions of the most recent turn, so a
// test can assert what the engine merged (per-turn override over session sticky).
type recordingTurns struct {
	mu   sync.Mutex
	opts RunOptions
}

func (r *recordingTurns) RunTurn(_ context.Context, _, _ string, prior []provider.Message, text string, opts RunOptions, _ agent.Observer) (string, []provider.Message, error) {
	r.mu.Lock()
	r.opts = opts
	r.mu.Unlock()
	return "ok", append(append([]provider.Message{}, prior...), provider.UserText(text)), nil
}

func (r *recordingTurns) last() RunOptions {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.opts
}

func strptr(s string) *string { return &s }

// TestEngine_SessionStickyModelTier proves the sticky-override lifecycle: StartSession
// persists initial model/tier, PostTurn merges them under empty turn fields (a per-turn
// override winning), and UpdateSession changes them live.
func TestEngine_SessionStickyModelTier(t *testing.T) {
	store := session.NewFileStore(t.TempDir())
	rec := &recordingTurns{}
	e := NewEngine(RunnerFunc(fakeRunner))
	e.EnableSessions(store, rec)

	sid, err := e.StartSession(RunOptions{Model: "m1", Tier: "safe"})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	// The initial stickies were persisted on the session.
	got, _ := store.Get(sid)
	if got.Model != "m1" || got.Tier != "safe" {
		t.Fatalf("StartSession stored model/tier = %q/%q, want m1/safe", got.Model, got.Tier)
	}

	// A turn with no override inherits the session's stickies.
	r1, err := e.PostTurn(sid, "a", RunOptions{})
	if err != nil {
		t.Fatalf("PostTurn 1: %v", err)
	}
	waitRunState(t, e, r1, StateDone)
	if opts := rec.last(); opts.Model != "m1" || opts.Tier != "safe" {
		t.Fatalf("turn 1 opts = %+v, want {m1 safe} from session stickies", opts)
	}

	// A per-turn override wins for the field it sets; the other still comes from the session.
	r2, err := e.PostTurn(sid, "b", RunOptions{Model: "m2"})
	if err != nil {
		t.Fatalf("PostTurn 2: %v", err)
	}
	waitRunState(t, e, r2, StateDone)
	if opts := rec.last(); opts.Model != "m2" || opts.Tier != "safe" {
		t.Fatalf("turn 2 opts = %+v, want {m2 safe} (turn model over session, session tier)", opts)
	}

	// UpdateSession changes the stickies live and returns the updated Info.
	info, err := e.UpdateSession(sid, strptr("m3"), strptr("permissive"))
	if err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}
	if info.Model != "m3" || info.Tier != "permissive" {
		t.Fatalf("UpdateSession returned %+v, want model m3 tier permissive", info)
	}
	r3, err := e.PostTurn(sid, "c", RunOptions{})
	if err != nil {
		t.Fatalf("PostTurn 3: %v", err)
	}
	waitRunState(t, e, r3, StateDone)
	if opts := rec.last(); opts.Model != "m3" || opts.Tier != "permissive" {
		t.Fatalf("turn 3 opts = %+v, want {m3 permissive} after UpdateSession", opts)
	}
}

// TestHTTP_SessionStickyOverrides drives the sticky model/tier through the real HTTP
// transport + client: POST /sessions with initial stickies, PATCH to change one, and the
// boundary rejections (bad tier ⇒ 400, unknown session ⇒ 404).
func TestHTTP_SessionStickyOverrides(t *testing.T) {
	e := NewEngine(RunnerFunc(fakeRunner))
	e.EnableSessions(session.NewFileStore(t.TempDir()), echoTurns())
	srv := httptest.NewServer(NewServer(e, nil, nil, nil, nil))
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()

	sid, err := c.StartSession(ctx, RunOptions{Model: "gpt-x", Tier: "safe"})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	infos, err := c.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(infos) != 1 || infos[0].Model != "gpt-x" || infos[0].Tier != "safe" {
		t.Fatalf("listing did not reflect POST stickies: %+v", infos)
	}

	info, err := c.UpdateSession(ctx, sid, nil, strptr("balanced"))
	if err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}
	if info.Model != "gpt-x" || info.Tier != "balanced" {
		t.Fatalf("PATCH result = %+v, want model kept, tier balanced", info)
	}

	if _, err := c.UpdateSession(ctx, sid, nil, strptr("bogus")); err == nil {
		t.Fatal("UpdateSession with a bad tier returned nil error, want a 400")
	}
	if _, err := c.UpdateSession(ctx, "missing", strptr("m"), nil); err == nil {
		t.Fatal("UpdateSession on an unknown session returned nil error, want a 404")
	}
}

// TestEngine_UpdateSessionUnknown proves a PATCH to a missing session is ErrNotFound and a
// partial update leaves the untouched field alone.
func TestEngine_UpdateSessionUnknown(t *testing.T) {
	e := NewEngine(RunnerFunc(fakeRunner))
	e.EnableSessions(session.NewFileStore(t.TempDir()), echoTurns())

	if _, err := e.UpdateSession("nope", strptr("m"), nil); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("UpdateSession unknown = %v, want ErrNotFound", err)
	}

	sid, _ := e.StartSession(RunOptions{Model: "keep", Tier: "safe"})
	// A nil field is left unchanged; a present field is set.
	info, err := e.UpdateSession(sid, nil, strptr("balanced"))
	if err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}
	if info.Model != "keep" || info.Tier != "balanced" {
		t.Fatalf("partial update = %+v, want model kept as 'keep', tier 'balanced'", info)
	}
}

func waitRunState(t *testing.T, e *Engine, runID, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		info, err := e.RunStatus(runID)
		if err == nil && info.State == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run %s did not reach state %q", runID, want)
}

func TestEngine_SessionTurnsRetainAndPersist(t *testing.T) {
	store := session.NewFileStore(t.TempDir())
	e := NewEngine(RunnerFunc(fakeRunner))
	e.EnableSessions(store, echoTurns())

	sid, err := e.StartSession(RunOptions{})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	r1, err := e.PostTurn(sid, "one", RunOptions{})
	if err != nil {
		t.Fatalf("PostTurn 1: %v", err)
	}
	waitRunState(t, e, r1, StateDone)

	r2, err := e.PostTurn(sid, "two", RunOptions{})
	if err != nil {
		t.Fatalf("PostTurn 2: %v", err)
	}
	waitRunState(t, e, r2, StateDone)

	// The stored history accumulated both turns — turn 2 could only produce this by
	// receiving turn 1's message as prior context, and the engine persisted it.
	got, err := store.Get(sid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("session history has %d messages, want 2 (one per turn): %+v", len(got.Messages), got.Messages)
	}
	if got.Messages[0].Content[0].Text != "one" || got.Messages[1].Content[0].Text != "two" {
		t.Fatalf("unexpected accumulated history: %+v", got.Messages)
	}
}

// TestCloseSessionFreesTurnLock verifies the per-session turn lock is released with the
// session — ids are never reused, so a leaked entry would live for the process lifetime.
func TestCloseSessionFreesTurnLock(t *testing.T) {
	e := NewEngine(RunnerFunc(fakeRunner))
	e.EnableSessions(session.NewFileStore(t.TempDir()), echoTurns())

	sid, err := e.StartSession(RunOptions{})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	runID, err := e.PostTurn(sid, "hi", RunOptions{})
	if err != nil {
		t.Fatalf("PostTurn: %v", err)
	}
	waitRunState(t, e, runID, StateDone)

	if err := e.CloseSession(sid); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	e.sessLocksMu.Lock()
	_, held := e.sessLocks[sid]
	e.sessLocksMu.Unlock()
	if held {
		t.Errorf("turn lock for closed session %s still held in sessLocks", sid)
	}
}

// TestCloseSessionFiresHook verifies the scratch-reap seam: closing a session invokes the
// registered close hook with the session id (the cmd layer uses this to remove the session's
// scratch cache).
func TestCloseSessionFiresHook(t *testing.T) {
	e := NewEngine(RunnerFunc(fakeRunner))
	e.EnableSessions(session.NewFileStore(t.TempDir()), echoTurns())

	type reap struct {
		id    string
		purge bool
	}
	got := make(chan reap, 1)
	e.SetSessionCloseHook(func(id string, purge bool) { got <- reap{id, purge} })

	sid, err := e.StartSession(RunOptions{})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := e.CloseSession(sid); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	select {
	case r := <-got:
		if r.id != sid {
			t.Fatalf("close hook got id %q, want %q", r.id, sid)
		}
		if r.purge {
			t.Fatal("close hook fired with purge=true, want false for an archive close")
		}
	default:
		t.Fatal("close hook was not fired")
	}
}

func TestEngine_PostTurnUnknownSession(t *testing.T) {
	e := NewEngine(RunnerFunc(fakeRunner))
	e.EnableSessions(session.NewFileStore(t.TempDir()), echoTurns())

	if _, err := e.PostTurn("nope", "hi", RunOptions{}); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("PostTurn unknown = %v, want ErrNotFound", err)
	}

	sid, _ := e.StartSession(RunOptions{})
	if err := e.CloseSession(sid); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	if _, err := e.PostTurn(sid, "hi", RunOptions{}); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("PostTurn after close = %v, want ErrNotFound", err)
	}
}

// TestEngine_PurgeSession proves the destructive path: purge removes a session for good
// (Get is ErrNotFound after, and it cannot be restored), fires the scratch-reap hook, and
// records a session_purged audit event; an unknown id is ErrNotFound.
func TestEngine_PurgeSession(t *testing.T) {
	store := session.NewFileStore(t.TempDir())
	rec := &audit.MemoryRecorder{}
	e := NewEngine(RunnerFunc(fakeRunner))
	e.EnableSessions(store, echoTurns())
	e.SetAuditRecorder(rec)

	type reap struct {
		id    string
		purge bool
	}
	reaped := make(chan reap, 1)
	e.SetSessionCloseHook(func(id string, purge bool) { reaped <- reap{id, purge} })

	sid, err := e.StartSession(RunOptions{})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := e.PurgeSession(sid); err != nil {
		t.Fatalf("PurgeSession: %v", err)
	}
	// The bytes are gone (not archived), so it can be neither fetched nor restored.
	if _, err := store.Get(sid); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("Get after purge = %v, want ErrNotFound", err)
	}
	if err := e.RestoreSession(sid); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("RestoreSession after purge = %v, want ErrNotFound", err)
	}
	// The scratch-reap hook fired with the purged id and purge=true (take everything).
	select {
	case r := <-reaped:
		if r.id != sid {
			t.Fatalf("reap hook got %q, want %q", r.id, sid)
		}
		if !r.purge {
			t.Fatal("purge fired the reap hook with purge=false, want true")
		}
	default:
		t.Fatal("purge did not fire the scratch-reap hook")
	}
	// The purge is on the audit log.
	events, _ := rec.Tail(0, audit.Filter{Type: audit.EventSessionPurged})
	if len(events) != 1 || events[0].Fields["session"] != sid {
		t.Fatalf("audit events = %+v, want one session_purged for %s", events, sid)
	}
	// Turn lock released (ids never reused).
	e.sessLocksMu.Lock()
	_, held := e.sessLocks[sid]
	e.sessLocksMu.Unlock()
	if held {
		t.Errorf("turn lock for purged session %s still held", sid)
	}

	if err := e.PurgeSession("nope"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("PurgeSession unknown = %v, want ErrNotFound", err)
	}
}

// TestEngine_RestoreSession proves close→restore round-trips: a closed (archived) session
// is invisible to Get/List but comes back live after Restore; purging an archived session
// then makes Restore impossible.
func TestEngine_RestoreSession(t *testing.T) {
	store := session.NewFileStore(t.TempDir())
	e := NewEngine(RunnerFunc(fakeRunner))
	e.EnableSessions(store, echoTurns())

	sid, _ := e.StartSession(RunOptions{})
	if err := e.CloseSession(sid); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	// Archived: not live.
	if _, err := store.Get(sid); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("Get after close = %v, want ErrNotFound (archived)", err)
	}
	// Restore brings it back.
	if err := e.RestoreSession(sid); err != nil {
		t.Fatalf("RestoreSession: %v", err)
	}
	if _, err := store.Get(sid); err != nil {
		t.Fatalf("Get after restore = %v, want it live", err)
	}
	if err := e.RestoreSession("nope"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("RestoreSession unknown = %v, want ErrNotFound", err)
	}

	// Close then purge the archived copy: no restore afterwards.
	_ = e.CloseSession(sid)
	if err := e.PurgeSession(sid); err != nil {
		t.Fatalf("PurgeSession archived: %v", err)
	}
	if err := e.RestoreSession(sid); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("RestoreSession after purge-of-archived = %v, want ErrNotFound", err)
	}
}

// TestHTTP_PurgeAndRestoreSession drives the destructive/recovery verbs through the real
// HTTP transport + client, including the boundary cases (bad id ⇒ 400, unknown ⇒ 404).
func TestHTTP_PurgeAndRestoreSession(t *testing.T) {
	e := NewEngine(RunnerFunc(fakeRunner))
	e.EnableSessions(session.NewFileStore(t.TempDir()), echoTurns())
	srv := httptest.NewServer(NewServer(e, nil, nil, nil, nil))
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()

	sid, err := c.StartSession(ctx, RunOptions{})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	// Close (archive) then restore over the wire.
	if err := c.CloseSession(ctx, sid); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	if err := c.RestoreSession(ctx, sid); err != nil {
		t.Fatalf("RestoreSession: %v", err)
	}
	infos, _ := c.ListSessions(ctx)
	if len(infos) != 1 || infos[0].ID != sid {
		t.Fatalf("restored session not live in listing: %+v", infos)
	}
	// Purge over the wire, then it is gone (restore 404s).
	if err := c.PurgeSession(ctx, sid); err != nil {
		t.Fatalf("PurgeSession: %v", err)
	}
	if err := c.RestoreSession(ctx, sid); err == nil {
		t.Fatal("RestoreSession after purge returned nil, want a 404")
	}
	// A malformed id (non-hex) is rejected at the boundary (400, not a disk touch).
	if err := c.PurgeSession(ctx, "not-a-hex-id"); err == nil {
		t.Fatal("PurgeSession with a malformed id returned nil, want a 400")
	}
}

func TestEngine_SessionsDisabled(t *testing.T) {
	e := NewEngine(RunnerFunc(fakeRunner)) // no EnableSessions
	if _, err := e.StartSession(RunOptions{}); !errors.Is(err, ErrSessionsDisabled) {
		t.Fatalf("StartSession = %v, want ErrSessionsDisabled", err)
	}
	if e.SessionsEnabled() {
		t.Fatal("SessionsEnabled true without EnableSessions")
	}
}
