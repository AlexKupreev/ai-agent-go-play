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

	// topic → the doc body (small docs come back whole).
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

func TestReadSelfDocsTool_SectionsAndCap(t *testing.T) {
	big := "# Usage\n\nIntro.\n\n## Spaces\n\nSwitchable contexts.\n\n## Filler\n\n" +
		strings.Repeat("padding ", maxFetchChars/4)
	docs, err := selfdocs.New(fstest.MapFS{"docs/usage.md": {Data: []byte(big)}})
	if err != nil {
		t.Fatal(err)
	}
	tool := NewReadSelfDocsTool(docs)
	ctx := context.Background()

	// A doc over the cap comes back as an outline, not 14k tokens of body.
	out, _ := tool.Run(ctx, map[string]any{"topic": "usage"})
	if len(out) > maxFetchChars {
		t.Fatalf("outline is %d chars, want under the cap", len(out))
	}
	for _, want := range []string{"section=<name>", "- spaces — Spaces", "- intro"} {
		if !strings.Contains(out, want) {
			t.Errorf("outline missing %q:\n%s", want, out)
		}
	}

	// topic+section → just that section.
	out, _ = tool.Run(ctx, map[string]any{"topic": "usage", "section": "spaces"})
	if !strings.Contains(out, "Switchable contexts") || strings.Contains(out, "padding") {
		t.Fatalf("section read = %q, want only the Spaces section", out)
	}

	// An oversized section is still capped.
	out, _ = tool.Run(ctx, map[string]any{"topic": "usage", "section": "filler"})
	if !strings.Contains(out, "[truncated") {
		t.Fatalf("oversized section not truncated (%d chars)", len(out))
	}

	// query → section refs, not whole documents.
	out, _ = tool.Run(ctx, map[string]any{"query": "switchable contexts"})
	if !strings.Contains(out, "usage#spaces") {
		t.Fatalf("query = %q, want a usage#spaces ref", out)
	}

	// Unknown section guides back with the section list.
	out, _ = tool.Run(ctx, map[string]any{"topic": "usage", "section": "bogus"})
	if !strings.Contains(out, "sections:") {
		t.Fatalf("unknown section = %q, want the section list", out)
	}
}
