package tools

import (
	"context"
	"strings"
	"testing"

	"ai-agent-go-play/internal/provider"
	"ai-agent-go-play/internal/session"
)

func findTool(ts []Tool, name string) Tool {
	for _, t := range ts {
		if t.Name == name {
			return t
		}
	}
	return Tool{}
}

func seedSession(t *testing.T, store *session.FileStore, msgs ...provider.Message) session.Session {
	t.Helper()
	s, err := store.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	s.Messages = msgs
	if err := store.Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return s
}

func TestSessionTools(t *testing.T) {
	store := session.NewFileStore(t.TempDir())
	s1 := seedSession(t, store,
		provider.UserText("teach me Go generics"),
		provider.AssistantText("sure — generics let you parameterize over types"))
	s2 := seedSession(t, store,
		provider.UserText("plan a trip to Rome"),
		provider.AssistantText("here is a Rome itinerary"))

	ts := NewSessionTools(SessionToolDeps{Reader: store, CurrentID: s2.ID})
	ctx := context.Background()

	t.Run("list marks the current session and shows both", func(t *testing.T) {
		out, err := findTool(ts, "list_sessions").Run(ctx, map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, s1.ID) || !strings.Contains(out, s2.ID) {
			t.Fatalf("listing missing a session id:\n%s", out)
		}
		if !strings.Contains(out, "(current)") {
			t.Fatalf("current session not marked:\n%s", out)
		}
	})

	t.Run("search finds the relevant session by topic", func(t *testing.T) {
		out, err := findTool(ts, "search_sessions").Run(ctx, map[string]any{"query": "go generics"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, s1.ID) {
			t.Fatalf("search for 'go generics' didn't surface s1:\n%s", out)
		}
		if strings.Contains(out, s2.ID) {
			t.Fatalf("search for 'go generics' should not match the Rome session:\n%s", out)
		}
	})

	t.Run("search rejects an empty query", func(t *testing.T) {
		if _, err := findTool(ts, "search_sessions").Run(ctx, map[string]any{"query": "   "}); err == nil {
			t.Fatal("empty query should error")
		}
	})

	t.Run("read returns the transcript", func(t *testing.T) {
		out, err := findTool(ts, "read_session").Run(ctx, map[string]any{"id": s1.ID})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "generics") {
			t.Fatalf("read_session missing content:\n%s", out)
		}
	})

	t.Run("read of an unknown id is a friendly message, not an error", func(t *testing.T) {
		out, err := findTool(ts, "read_session").Run(ctx, map[string]any{"id": "does-not-exist"})
		if err != nil {
			t.Fatalf("unknown id should not error: %v", err)
		}
		if !strings.Contains(out, "No session with id") {
			t.Fatalf("unexpected message for unknown id:\n%s", out)
		}
	})
}

func TestSessionTools_EmptyStore(t *testing.T) {
	store := session.NewFileStore(t.TempDir())
	ts := NewSessionTools(SessionToolDeps{Reader: store})
	out, err := findTool(ts, "list_sessions").Run(context.Background(), map[string]any{})
	if err != nil || !strings.Contains(out, "No stored sessions") {
		t.Fatalf("empty store list = (%q, %v), want a 'no sessions' message", out, err)
	}
}
