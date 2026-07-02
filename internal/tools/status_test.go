package tools

import (
	"context"
	"strings"
	"testing"
)

func TestStatusTool(t *testing.T) {
	tool := NewStatusTool(StatusDeps{
		Model:    "gpt-4o-mini",
		Tier:     "balanced",
		RunID:    "adbc6b79365b6f99",
		Version:  "dev",
		WorkDir:  ".",
		Registry: NewMemoryRegistry(), // no authored tools
		Memory:   nil,                 // no memory store
	})

	out, err := tool.Run(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"model: gpt-4o-mini",
		"tier: balanced",
		"run: adbc6b79", // truncated to 8 chars
		"build: dev",
		"authored tools: 0",
		"memory entries: 0",
		"Host",
		"cpu:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q; got:\n%s", want, out)
		}
	}
}

func TestStatusTool_DefaultModelLabel(t *testing.T) {
	tool := NewStatusTool(StatusDeps{Model: "", Tier: "safe", WorkDir: ".", Registry: NewMemoryRegistry()})
	out, _ := tool.Run(context.Background(), map[string]any{})
	if !strings.Contains(out, "(provider default)") {
		t.Errorf("empty model should render as (provider default); got:\n%s", out)
	}
	if !strings.Contains(out, "run: (none)") {
		t.Errorf("empty run id should render as (none); got:\n%s", out)
	}
}
