package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/provider"
	"ai-agent-go-play/internal/session"
)

// echoTurns is a TurnRunner that appends the user turn to the prior history and
// returns it, so a test can prove prior context is threaded and persisted.
func echoTurns() TurnRunnerFunc {
	return func(_ context.Context, _ string, prior []provider.Message, text string, _ agent.Observer) (string, []provider.Message, error) {
		updated := append(append([]provider.Message{}, prior...), provider.UserText(text))
		return "ok:" + text, updated, nil
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

	sid, err := e.StartSession()
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	r1, err := e.PostTurn(sid, "one")
	if err != nil {
		t.Fatalf("PostTurn 1: %v", err)
	}
	waitRunState(t, e, r1, StateDone)

	r2, err := e.PostTurn(sid, "two")
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

func TestEngine_PostTurnUnknownSession(t *testing.T) {
	e := NewEngine(RunnerFunc(fakeRunner))
	e.EnableSessions(session.NewFileStore(t.TempDir()), echoTurns())

	if _, err := e.PostTurn("nope", "hi"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("PostTurn unknown = %v, want ErrNotFound", err)
	}

	sid, _ := e.StartSession()
	if err := e.CloseSession(sid); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	if _, err := e.PostTurn(sid, "hi"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("PostTurn after close = %v, want ErrNotFound", err)
	}
}

func TestEngine_SessionsDisabled(t *testing.T) {
	e := NewEngine(RunnerFunc(fakeRunner)) // no EnableSessions
	if _, err := e.StartSession(); !errors.Is(err, ErrSessionsDisabled) {
		t.Fatalf("StartSession = %v, want ErrSessionsDisabled", err)
	}
	if e.SessionsEnabled() {
		t.Fatal("SessionsEnabled true without EnableSessions")
	}
}
