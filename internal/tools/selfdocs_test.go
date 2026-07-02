package tools

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"ai-agent-go-play/internal/selfdocs"
)

func TestReadSelfDocsTool(t *testing.T) {
	fsys := fstest.MapFS{
		"README.md":     {Data: []byte("# ai-agent\n\nAn agent you run.")},
		"docs/usage.md": {Data: []byte("# Usage — operating the agent\n\nTrust tiers and approvals.")},
	}
	docs, err := selfdocs.New(fsys)
	if err != nil {
		t.Fatal(err)
	}
	tool := NewReadSelfDocsTool(docs)
	ctx := context.Background()

	// No args → a listing of available docs, tagged by kind.
	out, _ := tool.Run(ctx, map[string]any{})
	if !strings.Contains(out, "usage") || !strings.Contains(out, "[reference]") {
		t.Fatalf("empty call listing = %q, want a tagged doc list", out)
	}

	// topic → the doc body.
	out, _ = tool.Run(ctx, map[string]any{"topic": "usage"})
	if !strings.Contains(out, "Trust tiers and approvals") {
		t.Fatalf("topic=usage = %q, want the doc body", out)
	}

	// query → ranked listing.
	out, _ = tool.Run(ctx, map[string]any{"query": "trust tiers"})
	if !strings.Contains(out, "usage") {
		t.Fatalf("query = %q, want usage in the results", out)
	}

	// Unknown topic → the error guides back with available topics (not a Go error).
	out, err = tool.Run(ctx, map[string]any{"topic": "bogus"})
	if err != nil {
		t.Fatalf("unknown topic returned a Go error: %v", err)
	}
	if !strings.Contains(out, "no doc") {
		t.Fatalf("unknown topic = %q, want a guiding message", out)
	}
}
