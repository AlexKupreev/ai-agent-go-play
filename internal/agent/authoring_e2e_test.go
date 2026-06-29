package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/provider"
	"ai-agent-go-play/internal/tools"
)

// scriptedProvider replays a fixed sequence of step responses and records the
// tool names offered on each step (to prove an authored tool becomes visible).
type scriptedProvider struct {
	steps     []provider.StepResponse
	calls     int
	seenTools [][]string
}

func (p *scriptedProvider) Step(_ context.Context, req provider.StepRequest) (provider.StepResponse, error) {
	names := make([]string, len(req.Tools))
	for i, d := range req.Tools {
		names[i] = d.Name
	}
	p.seenTools = append(p.seenTools, names)
	r := p.steps[p.calls]
	p.calls++
	return r, nil
}

func toolCallStep(id, name string, input map[string]any) provider.StepResponse {
	raw, _ := json.Marshal(input)
	return provider.StepResponse{
		Stop: provider.StopToolCalls,
		Content: []provider.ContentBlock{{
			Kind:     provider.BlockToolCall,
			ToolCall: &provider.ToolCall{ID: id, Name: name, Input: raw},
		}},
	}
}

func textStep(s string) provider.StepResponse {
	return provider.StepResponse{Stop: provider.StopEndTurn,
		Content: []provider.ContentBlock{{Kind: provider.BlockText, Text: s}}}
}

// The agent authors a tool, then calls it in the same run, then finishes.
// Exercises 3a–3c end to end with no live API.
func TestAuthoring_EndToEnd(t *testing.T) {
	prov := &scriptedProvider{steps: []provider.StepResponse{
		// Step 0: author a "triple" tool.
		toolCallStep("c1", "author_tool", map[string]any{
			"name":         "triple",
			"description":  "multiply input.n by three",
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"type": "number"}}},
			"code":         "return input.n * 3",
			"test":         "assert(tool({n=2}) == 6); return true",
		}),
		// Step 1: call the freshly authored tool.
		toolCallStep("c2", "triple", map[string]any{"n": 4}),
		// Step 2: final answer.
		textStep("the triple of 4 is 12"),
	}}

	reg := tools.NewMemoryRegistry()
	rec := &audit.MemoryRecorder{}
	exec := NewExecutor(prov, t.TempDir(), "", "", "", nil, reg, nil, rec, capability.TierBalanced, nil)

	out, err := exec.Run(context.Background(), "triple some numbers")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "12") {
		t.Errorf("final answer = %q, want it to mention 12", out)
	}

	// The authored tool was registered and audited.
	if _, ok := reg.Get("triple"); !ok {
		t.Error("triple was not registered")
	}
	if !authored(rec, "triple") {
		t.Errorf("missing tool_authored event: %+v", rec.Snapshot())
	}

	// It was NOT offered to the model on step 0 (didn't exist yet) but WAS on step 1.
	if len(prov.seenTools) < 2 {
		t.Fatalf("expected at least 2 steps, got %d", len(prov.seenTools))
	}
	if slices.Contains(prov.seenTools[0], "triple") {
		t.Error("triple should not be offered before it was authored")
	}
	if !slices.Contains(prov.seenTools[1], "triple") {
		t.Errorf("triple should be offered after authoring; step-1 tools = %v", prov.seenTools[1])
	}
}

// A tool that fails its smoke test is never registered, and the model is told.
func TestAuthoring_TestFailureSurfacedToModel(t *testing.T) {
	prov := &scriptedProvider{steps: []provider.StepResponse{
		toolCallStep("c1", "author_tool", map[string]any{
			"name":         "bad",
			"description":  "wrong math",
			"input_schema": map[string]any{"type": "object"},
			"code":         "return 1",
			"test":         "assert(tool({}) == 2); return true",
		}),
		textStep("ok, that tool failed"),
	}}
	reg := tools.NewMemoryRegistry()
	exec := NewExecutor(prov, t.TempDir(), "", "", "", nil, reg, nil, &audit.MemoryRecorder{}, capability.TierBalanced, nil)

	if _, err := exec.Run(context.Background(), "make a bad tool"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, ok := reg.Get("bad"); ok {
		t.Error("a tool that failed its test must not be registered")
	}
}

func authored(rec *audit.MemoryRecorder, name string) bool {
	for _, e := range rec.Snapshot() {
		if e.Type == audit.EventToolAuthored && e.Fields["name"] == name {
			return true
		}
	}
	return false
}
