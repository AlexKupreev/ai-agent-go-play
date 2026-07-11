package cmd

import (
	"context"
	"net/http/httptest"
	"testing"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/api"
	"ai-agent-go-play/internal/provider"
	"ai-agent-go-play/internal/session"
)

// newSessionTestClient spins up a real engine + HTTP server with sessions enabled and
// returns a client pointing at it, so the remote-chat helpers exercise the actual wire.
func newSessionTestClient(t *testing.T) *api.Client {
	t.Helper()
	e := api.NewEngine(api.RunnerFunc(func(context.Context, string, string, api.RunOptions, agent.Observer) (string, error) {
		return "ok", nil
	}))
	e.EnableSessions(session.NewFileStore(t.TempDir()),
		api.TurnRunnerFunc(func(_ context.Context, _, _ string, prior []provider.Message, text string, _ api.RunOptions, _ agent.Observer) (string, []provider.Message, error) {
			return "ok", append(prior, provider.UserText(text)), nil
		}))
	srv := httptest.NewServer(api.NewServer(e, nil, nil, nil, nil))
	t.Cleanup(srv.Close)
	return api.NewClient(srv.URL)
}

func TestAttachSession(t *testing.T) {
	orig := chatSessionFlag
	t.Cleanup(func() { chatSessionFlag = orig })
	c := newSessionTestClient(t)
	ctx := context.Background()

	t.Run("no --session starts a new one", func(t *testing.T) {
		chatSessionFlag = ""
		id, resumed, turns, _, _, _, err := attachSession(ctx, c, "test", api.RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if id == "" || resumed || turns != 0 {
			t.Fatalf("attachSession() = (%q, resumed=%v, turns=%d), want a fresh non-empty id", id, resumed, turns)
		}
	})

	t.Run("--session resumes an existing session", func(t *testing.T) {
		existing, err := c.StartSession(ctx, api.RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		chatSessionFlag = existing
		id, resumed, _, _, _, _, err := attachSession(ctx, c, "test", api.RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if id != existing || !resumed {
			t.Fatalf("attachSession() = (%q, resumed=%v), want (%q, resumed=true)", id, resumed, existing)
		}
	})

	t.Run("--session with an unknown id errors", func(t *testing.T) {
		chatSessionFlag = "does-not-exist"
		if _, _, _, _, _, _, err := attachSession(ctx, c, "test", api.RunOptions{}); err == nil {
			t.Fatal("attachSession() with unknown id returned nil error, want a not-found error")
		}
	})
}

func TestListRemoteSessions_Empty(t *testing.T) {
	c := newSessionTestClient(t)
	// No sessions yet — should not error.
	if err := listRemoteSessions(context.Background(), c, "test"); err != nil {
		t.Fatalf("listRemoteSessions on empty engine: %v", err)
	}
}
