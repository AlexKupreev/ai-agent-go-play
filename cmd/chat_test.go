package cmd

import (
	"testing"

	"ai-agent-go-play/internal/agent"
)

// TestModelLabel: an empty model renders as the built-in default (with a note), a set model
// renders verbatim. Backs the /model command's "show current" output.
func TestModelLabel(t *testing.T) {
	if got := modelLabel(""); got != agent.DefaultModel+" (built-in default)" {
		t.Errorf("modelLabel(\"\") = %q, want the built-in default label", got)
	}
	if got := modelLabel("gpt-4o"); got != "gpt-4o" {
		t.Errorf("modelLabel(gpt-4o) = %q, want it verbatim", got)
	}
}
