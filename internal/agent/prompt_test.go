package agent

import (
	"context"
	"strings"
	"testing"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/provider"
	"ai-agent-go-play/internal/tools"
)

func TestComposeSystemPrompt(t *testing.T) {
	tests := []struct {
		name        string
		base        string
		replaceWith string
		appends     []string
		want        string
	}{
		{
			name: "base only",
			base: "BASE",
			want: "BASE",
		},
		{
			name:        "override replaces base entirely",
			base:        "BASE",
			replaceWith: "OVERRIDE",
			want:        "OVERRIDE",
		},
		{
			name:    "appends concatenate in order under a separator",
			base:    "BASE",
			appends: []string{"A1", "A2"},
			want:    "BASE\n\n---\n\nA1\n\n---\n\nA2",
		},
		{
			name:    "empty and whitespace appends are skipped",
			base:    "BASE",
			appends: []string{"", "   ", "A1"},
			want:    "BASE\n\n---\n\nA1",
		},
		{
			name:        "override and appends combine",
			base:        "BASE",
			replaceWith: "OVERRIDE",
			appends:     []string{"A1"},
			want:        "OVERRIDE\n\n---\n\nA1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := composeSystemPrompt(tt.base, tt.replaceWith, tt.appends...)
			if got != tt.want {
				t.Errorf("composeSystemPrompt() = %q, want %q", got, tt.want)
			}
		})
	}
}

// systemCapture records the system message text of the first Step, so a test can assert
// what prompt the executor actually sent to the model.
type systemCapture struct {
	system string
}

func (p *systemCapture) Step(_ context.Context, req provider.StepRequest) (provider.StepResponse, error) {
	if p.system == "" && len(req.Messages) > 0 && req.Messages[0].Role == provider.RoleSystem {
		p.system = req.Messages[0].Content[0].Text
	}
	return textStep("done"), nil
}

func TestNewExecutor_PromptOverrideAndAppends(t *testing.T) {
	prov := &systemCapture{}
	exec := NewExecutor(ExecutorConfig{
		Provider: prov, WorkDir: t.TempDir(), Registry: tools.NewMemoryRegistry(),
		Audit: &audit.MemoryRecorder{}, Tier: capability.TierBalanced,
		SystemPromptOverride: "OPERATOR PROMPT",
		PromptAppends:        []string{"PROJECT RULE ONE", "PROJECT RULE TWO"},
	})
	if _, err := exec.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("run: %v", err)
	}

	// The override replaced the built-in base entirely.
	if strings.Contains(prov.system, "helpful AI agent") {
		t.Errorf("override did not replace the base prompt; system = %q", prov.system)
	}
	if !strings.HasPrefix(prov.system, "OPERATOR PROMPT") {
		t.Errorf("system prompt did not start with the override; system = %q", prov.system)
	}
	// Appends land after the base, in order.
	one := strings.Index(prov.system, "PROJECT RULE ONE")
	two := strings.Index(prov.system, "PROJECT RULE TWO")
	if one < 0 || two < 0 || one > two {
		t.Errorf("appends missing or out of order; system = %q", prov.system)
	}
}

// With no prompt files wired, the system prompt is the built-in base — the cached prefix is
// unchanged from before prompt composition existed.
func TestNewExecutor_NoPromptFilesUsesBase(t *testing.T) {
	prov := &systemCapture{}
	exec := NewExecutor(ExecutorConfig{
		Provider: prov, WorkDir: t.TempDir(), Registry: tools.NewMemoryRegistry(),
		Audit: &audit.MemoryRecorder{}, Tier: capability.TierBalanced,
	})
	if _, err := exec.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.HasPrefix(prov.system, executorPrompt) {
		t.Errorf("base prompt not used unmodified; system = %q", prov.system)
	}
}
