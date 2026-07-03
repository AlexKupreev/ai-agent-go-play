package agent

import (
	"context"
	"testing"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/provider"
	"ai-agent-go-play/internal/tools"
)

// TestExecutor_AskUserRoutesThroughGate proves the executor is offered ask_user and that a
// mid-run question is routed to the injected HumanGate, whose answer flows back to the model.
func TestExecutor_AskUserRoutesThroughGate(t *testing.T) {
	prov := &scriptedProvider{steps: []provider.StepResponse{
		toolCallStep("c1", "ask_user", map[string]any{"question": "which color?"}),
		textStep("ok, blue"),
	}}
	obs := &captureObserver{}

	var asked, askedRunID string
	gate := tools.GateFuncs{
		AskFn: func(_ context.Context, q tools.Question) (string, error) {
			asked, askedRunID = q.Prompt, q.RunID
			return "blue", nil
		},
	}

	exec := NewExecutor(ExecutorConfig{
		Provider: prov, WorkDir: t.TempDir(), Observer: obs,
		Audit: &audit.MemoryRecorder{}, Tier: capability.TierBalanced,
		Gate: gate, RunID: "r1",
	})
	out, err := exec.Run(context.Background(), "pick a color")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "ok, blue" {
		t.Errorf("answer = %q, want 'ok, blue'", out)
	}
	if asked != "which color?" || askedRunID != "r1" {
		t.Errorf("gate saw (%q, %q), want (which color?, r1)", asked, askedRunID)
	}

	// The ask_user tool result carried the gate's answer back into the loop.
	var sawAnswer bool
	for _, e := range obs.events {
		if e.Kind == EvToolResult && e.Call != nil && e.Call.Name == "ask_user" && e.Result == "blue" {
			sawAnswer = true
		}
	}
	if !sawAnswer {
		t.Error("ask_user tool result did not carry the gate's answer")
	}
}
