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

// TestTierPolicyNote_Buckets checks the tier permission manifest renders the correct
// permitted / needs-approval split per tier (derived from the enforced policy), always
// states the forbidden boundaries, and is embedded in the system prompt.
func TestTierPolicyNote_Buckets(t *testing.T) {
	cases := []struct {
		tier         capability.Tier
		autoContains []string // capabilities that must appear as auto-granted
		needApproval []string // capabilities that must appear as needing approval
	}{
		{capability.TierSafe, nil, []string{"read files", "write files", "fetch over the network", "call other tools"}},
		{capability.TierBalanced, []string{"read files", "read the clock"}, []string{"write files", "fetch over the network", "call other tools"}},
		{capability.TierPermissive, []string{"read files", "write files", "fetch over the network"}, nil},
	}
	for _, tc := range cases {
		note := tierPolicyNote(tc.tier)
		if !strings.Contains(note, "trust tier: "+string(tc.tier)) {
			t.Errorf("%s: note missing tier header\n%s", tc.tier, note)
		}
		// The forbidden boundaries are stated at every tier.
		for _, want := range []string{"FORBIDDEN", "cannot call shell", "pure computation only"} {
			if !strings.Contains(note, want) {
				t.Errorf("%s: note missing forbidden clause %q", tc.tier, want)
			}
		}
		auto, approve, _ := strings.Cut(note, "REQUIRE the user's approval first")
		if !ok(auto, approve) {
			t.Fatalf("%s: note not shaped as expected:\n%s", tc.tier, note)
		}
		for _, w := range tc.autoContains {
			if !strings.Contains(auto, w) {
				t.Errorf("%s: %q should be in the auto-granted section", tc.tier, w)
			}
		}
		for _, w := range tc.needApproval {
			if !strings.Contains(approve, w) {
				t.Errorf("%s: %q should be in the needs-approval section", tc.tier, w)
			}
		}
	}
}

// ok reports that Cut split the note into two non-empty halves.
func ok(a, b string) bool { return a != "" && b != "" }

// TestNewExecutor_EmbedsTierPolicy confirms the manifest is actually part of the system
// prompt handed to the provider.
func TestNewExecutor_EmbedsTierPolicy(t *testing.T) {
	prov := &systemCapture{}
	exec := NewExecutor(ExecutorConfig{
		Provider: prov, WorkDir: t.TempDir(), Registry: tools.NewMemoryRegistry(),
		Audit: &audit.MemoryRecorder{}, Tier: capability.TierBalanced,
	})
	if _, err := exec.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(prov.system, "capabilities and approval policy (trust tier: balanced)") {
		t.Errorf("system prompt missing tier policy manifest; system = %q", prov.system)
	}
}

// TestNewExecutor_EmbedsToolRoster checks the executor's own system prompt carries the
// generated tool inventory (replacing the old hardcoded list, so it can't drift).
func TestNewExecutor_EmbedsToolRoster(t *testing.T) {
	exec := NewExecutor(ExecutorConfig{
		Provider: &systemCapture{}, WorkDir: t.TempDir(), Registry: tools.NewMemoryRegistry(),
		Audit: &audit.MemoryRecorder{}, Tier: capability.TierBalanced,
	})
	if !strings.Contains(exec.systemPrompt, "Your available tools:") {
		t.Fatalf("executor prompt missing generated tool roster:\n%s", exec.systemPrompt)
	}
	for _, w := range []string{"shell", "run_code", "author_tool"} {
		if !strings.Contains(exec.systemPrompt, w) {
			t.Errorf("tool roster missing %q", w)
		}
	}
}

// TestEnvironmentSummary_GeneratedTierHostAndDynamic checks the planner-facing environment
// summary lists the real tools, the tier, and live host resources — and that it is dynamic:
// a tool registered after construction shows up on the next call (no rebuild).
func TestEnvironmentSummary_GeneratedTierHostAndDynamic(t *testing.T) {
	reg := tools.NewMemoryRegistry()
	exec := NewExecutor(ExecutorConfig{
		Provider: &systemCapture{}, WorkDir: t.TempDir(), Registry: reg,
		Audit: &audit.MemoryRecorder{}, Tier: capability.TierBalanced,
	})
	env := exec.EnvironmentSummary()
	for _, w := range []string{"run_code", "author_tool", "Trust tier: balanced", "Host resources (live)"} {
		if !strings.Contains(env, w) {
			t.Errorf("EnvironmentSummary missing %q\n%s", w, env)
		}
	}

	// Dynamic: register a new tool, and it appears in the next summary without a rebuild.
	if _, err := reg.Register(tools.ToolSpec{
		Name: "widget_maker", Description: "makes widgets on demand",
		InputSchema: map[string]any{"type": "object"},
		Impl:        tools.Impl{Kind: tools.ImplScript, Lang: "lua", Source: "return 1"},
		Scope:       tools.ScopeShared,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if env2 := exec.EnvironmentSummary(); !strings.Contains(env2, "widget_maker") {
		t.Errorf("EnvironmentSummary is not dynamic — widget_maker missing after registration:\n%s", env2)
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
