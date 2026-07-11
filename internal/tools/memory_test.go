package tools

import (
	"context"
	"strings"
	"testing"

	"ai-agent-go-play/internal/memory"
)

func runTool(t *testing.T, tool Tool, args map[string]any) string {
	t.Helper()
	out, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("%s hard error: %v", tool.Name, err)
	}
	return out
}

// TestRecall_QueryMissFallsBackToList is the regression for the live failure: a Russian
// query over an English note ("The user's level is A1") shares no literal words, so the
// token-overlap Search returns nothing. recall must NOT report "nothing" — it falls back
// to listing recent entries so the agent can read the one it wants (phrased differently).
func TestRecall_QueryMissFallsBackToList(t *testing.T) {
	store := memory.NewMemoryStore()
	if err := store.Put(memory.Entry{Key: "user.level", Value: "The user's level is A1.", Tags: []string{"profile", "language"}}); err != nil {
		t.Fatal(err)
	}
	recall := NewRecallTool(store)

	// A cross-language query misses the search index but must still surface the entry.
	out := runTool(t, recall, map[string]any{"query": "уровень польского"})
	if !strings.Contains(out, "user.level") || !strings.Contains(out, "A1") {
		t.Fatalf("cross-language recall did not surface the entry via fallback:\n%s", out)
	}
	if !strings.Contains(out, "no direct match") {
		t.Fatalf("fallback should flag that it's a recency list, not a match:\n%s", out)
	}

	// A literal match still returns directly (no fallback framing).
	if out := runTool(t, recall, map[string]any{"query": "level"}); strings.Contains(out, "no direct match") {
		t.Fatalf("a real match should not use the fallback path:\n%s", out)
	}

	// Truly empty store reports empty, not a false match.
	if out := runTool(t, NewRecallTool(memory.NewMemoryStore()), map[string]any{"query": "anything"}); !strings.Contains(out, "empty") {
		t.Fatalf("empty store recall = %q, want an 'empty' report", out)
	}
}

// TestRecall_KeyAndList covers the direct-key and no-query listing paths.
func TestRecall_KeyAndList(t *testing.T) {
	store := memory.NewMemoryStore()
	_ = store.Put(memory.Entry{Key: "user.editor", Value: "vim"})
	recall := NewRecallTool(store)

	if out := runTool(t, recall, map[string]any{"key": "user.editor"}); !strings.Contains(out, "vim") {
		t.Fatalf("key lookup = %q", out)
	}
	if out := runTool(t, recall, map[string]any{"key": "missing"}); !strings.Contains(out, "no memory entry") {
		t.Fatalf("missing key = %q", out)
	}
	if out := runTool(t, recall, nil); !strings.Contains(out, "user.editor") {
		t.Fatalf("no-query list = %q", out)
	}
}
