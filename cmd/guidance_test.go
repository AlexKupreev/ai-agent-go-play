package cmd

import (
	"strings"
	"testing"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/capability"
)

func TestWithGuidancePrecedenceAndIsolation(t *testing.T) {
	operator := []string{"operator global", "operator workspace"}
	got := withGuidance(operator, "user global", "space notes", "session rule")
	if len(got) != 5 {
		t.Fatalf("withGuidance returned %d layers, want 5: %#v", len(got), got)
	}
	want := []string{"operator global", "operator workspace", "Workspace guidance", "space notes", "Session guidance"}
	for i, marker := range want {
		if !strings.Contains(got[i], marker) {
			t.Fatalf("layer %d = %q, want marker %q", i, got[i], marker)
		}
	}
	if len(operator) != 2 {
		t.Fatalf("input appends mutated: %#v", operator)
	}

	// The real executor composition preserves immutable kernel blocks ahead of all user
	// guidance while retaining the same specificity order.
	ex := agent.NewExecutor(agent.ExecutorConfig{Tier: capability.TierBalanced, PromptAppends: got})
	prompt := ex.SystemPrompt()
	markers := []string{
		"Do NOT run Python, Node.js, Ruby, or R via shell",
		"[END UNTRUSTED WEB CONTENT]",
		"operator global",
		"operator workspace",
		"Workspace guidance",
		"space notes",
		"Session guidance",
	}
	last := -1
	for _, marker := range markers {
		at := strings.Index(prompt, marker)
		if at < 0 || at <= last {
			t.Fatalf("prompt marker %q missing or out of order; previous index %d, got %d", marker, last, at)
		}
		last = at
	}
}

func TestWithGuidanceSkipsEmptyScopes(t *testing.T) {
	got := withGuidance([]string{"operator"}, " \n", "", "")
	if len(got) != 1 || got[0] != "operator" {
		t.Fatalf("empty guidance added prompt layers: %#v", got)
	}
}
