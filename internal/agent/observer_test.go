package agent

import (
	"context"
	"testing"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/provider"
	"ai-agent-go-play/internal/tools"
)

// captureObserver records every event it receives.
type captureObserver struct{ events []Event }

func (c *captureObserver) Emit(e Event) { c.events = append(c.events, e) }

func (c *captureObserver) kinds() []EventKind {
	out := make([]EventKind, len(c.events))
	for i, e := range c.events {
		out[i] = e.Kind
	}
	return out
}

// A run that makes one tool call then answers emits the full event sequence
// through the observer — no direct stdout/stderr from the loop.
func TestRun_EmitsEventSequence(t *testing.T) {
	prov := &scriptedProvider{steps: []provider.StepResponse{
		toolCallStep("c1", "run_code", map[string]any{"code": "return 1"}),
		textStep("done"),
	}}
	obs := &captureObserver{}
	reg := tools.NewMemoryRegistry()
	exec := NewExecutor(prov, t.TempDir(), "", "", "", obs, reg, nil, &audit.MemoryRecorder{}, capability.TierBalanced, nil)

	out, err := exec.Run(context.Background(), "compute something")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "done" {
		t.Errorf("answer = %q, want done", out)
	}

	want := []EventKind{EvStart, EvRequest, EvResponse, EvToolStart, EvToolResult, EvRequest, EvResponse}
	got := obs.kinds()
	if len(got) != len(want) {
		t.Fatalf("event kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}

	// The tool_result event carries the tool name and a result.
	for _, e := range obs.events {
		if e.Kind == EvToolResult {
			if e.Call == nil || e.Call.Name != "run_code" {
				t.Errorf("tool_result missing call name: %+v", e.Call)
			}
			if e.Result == "" {
				t.Error("tool_result has empty result")
			}
		}
	}
}
