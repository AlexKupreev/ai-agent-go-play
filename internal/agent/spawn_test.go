package agent

import (
	"context"
	"strings"
	"testing"

	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/provider"
)

// spawnCoordinator builds an executor wired with a catalog so spawn_agent is present. The
// scripted provider drives the coordinator; the sub-agent shares it (each Step is replayed
// in order, so the coordinator and its child interleave on one script).
func spawnCoordinator(t *testing.T, prov provider.Provider, depth int) *Agent {
	t.Helper()
	return NewExecutor(ExecutorConfig{
		Provider:     prov,
		WorkDir:      t.TempDir(),
		Tier:         capability.TierBalanced,
		AgentCatalog: NewAgentCatalog(),
		SpawnDepth:   depth,
	})
}

// The coordinator delegates a research question to a "researcher" sub-agent, which does its
// own model step and returns; the coordinator then answers using the sub-run's result.
func TestSpawnAgent_ForegroundDelegation(t *testing.T) {
	prov := &scriptedProvider{steps: []provider.StepResponse{
		// Step 0 (coordinator): spawn a researcher.
		toolCallStep("c1", "spawn_agent", map[string]any{
			"type": "researcher",
			"task": "what is the capital of France?",
		}),
		// Step 1 (child researcher run): it answers directly (no tools).
		textStep("The capital of France is Paris."),
		// Step 2 (coordinator, after the child returns): final answer.
		textStep("Paris, per the researcher."),
	}}

	coord := spawnCoordinator(t, prov, 1)

	// spawn_agent must be offered to the coordinator and be trusted-but-unexposed.
	if _, ok := coord.byName["spawn_agent"]; !ok {
		t.Fatal("spawn_agent not wired into the coordinator")
	}
	if exposedBuiltins["spawn_agent"] {
		t.Fatal("spawn_agent must not be exposed to the sandbox")
	}

	out, err := coord.Run(context.Background(), "find the capital of France")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "Paris") {
		t.Fatalf("coordinator answer = %q, want it to use the sub-run result", out)
	}
	// The child ran as its own step between the two coordinator steps.
	if prov.calls != 3 {
		t.Fatalf("provider steps = %d, want 3 (coordinator, child, coordinator)", prov.calls)
	}
}

// The child researcher sees only its two read-only tools, not the coordinator's shell etc.
func TestSpawnAgent_ChildIsToolRestricted(t *testing.T) {
	var childTools []string
	prov := &stepFuncProvider{onStep: func(req provider.StepRequest, call int) provider.StepResponse {
		switch call {
		case 0: // coordinator spawns
			return toolCallStep("c1", "spawn_agent", map[string]any{"type": "researcher", "task": "q"})
		case 1: // child's only step — capture its tool set
			for _, d := range req.Tools {
				childTools = append(childTools, d.Name)
			}
			return textStep("child done")
		default: // coordinator finishes
			return textStep("coordinator done")
		}
	}}

	coord := spawnCoordinator(t, prov, 1)
	if _, err := coord.Run(context.Background(), "go"); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := strings.Join(childTools, ",")
	if got != "web_search,web_fetch" {
		t.Fatalf("child tools = %q, want web_search,web_fetch (restricted, no shell/spawn_agent)", got)
	}
	for _, banned := range []string{"shell", "spawn_agent", "author_tool"} {
		if strings.Contains(got, banned) {
			t.Fatalf("child was offered %q, but must not be", banned)
		}
	}
}

// At depth 0 the coordinator still offers spawn_agent but the call refuses, so delegation is
// terminating by construction.
func TestSpawnAgent_DepthGuardRefuses(t *testing.T) {
	prov := &scriptedProvider{steps: []provider.StepResponse{
		toolCallStep("c1", "spawn_agent", map[string]any{"type": "researcher", "task": "q"}),
		textStep("done anyway"),
	}}

	coord := spawnCoordinator(t, prov, 0)
	if _, err := coord.Run(context.Background(), "go"); err != nil {
		t.Fatalf("run: %v", err)
	}
	// The tool-result for the refused spawn is fed back to the model; assert the child never ran.
	// With depth 0, only the two coordinator steps execute (no child step).
	if prov.calls != 2 {
		t.Fatalf("provider steps = %d, want 2 (no child ran)", prov.calls)
	}
	// The refusal message must have reached the conversation as a tool error.
	var sawRefusal bool
	for _, m := range coord.Messages() {
		for _, blk := range m.Content {
			if blk.ToolResult != nil && strings.Contains(blk.ToolResult.Output, "spawn budget exhausted") {
				sawRefusal = true
			}
		}
	}
	if !sawRefusal {
		t.Fatal("expected a 'spawn budget exhausted' tool result in the conversation")
	}
}

// An unknown type name is reported back to the model (with the available types) rather than
// crashing the run.
func TestSpawnAgent_UnknownType(t *testing.T) {
	prov := &scriptedProvider{steps: []provider.StepResponse{
		toolCallStep("c1", "spawn_agent", map[string]any{"type": "nope", "task": "q"}),
		textStep("recovered"),
	}}
	coord := spawnCoordinator(t, prov, 1)
	if _, err := coord.Run(context.Background(), "go"); err != nil {
		t.Fatalf("run: %v", err)
	}
	var sawErr bool
	for _, m := range coord.Messages() {
		for _, blk := range m.Content {
			if blk.ToolResult != nil && strings.Contains(blk.ToolResult.Output, `unknown agent type "nope"`) {
				sawErr = true
			}
		}
	}
	if !sawErr {
		t.Fatal("expected an 'unknown agent type' tool result")
	}
}

// No catalog wired ⇒ spawn_agent is omitted entirely.
func TestSpawnAgent_OmittedWithoutCatalog(t *testing.T) {
	coord := NewExecutor(ExecutorConfig{Provider: &scriptedProvider{}, WorkDir: t.TempDir(), Tier: capability.TierBalanced})
	if _, ok := coord.byName["spawn_agent"]; ok {
		t.Fatal("spawn_agent must be omitted when no AgentCatalog is wired")
	}
}

// stepFuncProvider drives steps through a callback so a test can both inspect the request
// (e.g. the child's tool set) and choose the response per step.
type stepFuncProvider struct {
	onStep func(req provider.StepRequest, call int) provider.StepResponse
	calls  int
}

func (p *stepFuncProvider) Step(_ context.Context, req provider.StepRequest) (provider.StepResponse, error) {
	r := p.onStep(req, p.calls)
	p.calls++
	return r, nil
}
