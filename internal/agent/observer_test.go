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

// TestUsageObserver_Accumulates checks that only EvResponse events contribute to the
// running token total and step count.
func TestUsageObserver_Accumulates(t *testing.T) {
	u := NewUsageObserver()
	u.Emit(Event{Kind: EvStart}) // ignored
	u.Emit(Event{Kind: EvResponse, Usage: provider.Usage{InputTokens: 100, OutputTokens: 20, CachedTokens: 10}})
	u.Emit(Event{Kind: EvToolResult}) // ignored
	u.Emit(Event{Kind: EvResponse, Usage: provider.Usage{InputTokens: 5, OutputTokens: 3}})

	got := u.Total()
	if got.InputTokens != 105 || got.OutputTokens != 23 || got.CachedTokens != 10 {
		t.Fatalf("Total() = %+v, want {105 23 10}", got)
	}
	if u.Steps() != 2 {
		t.Fatalf("Steps() = %d, want 2 (one per EvResponse)", u.Steps())
	}
}

// TestGatedObserver_ForwardsOnlyWhenEnabled checks that a gate suppresses events while
// off and passes them while on — the mechanism behind chat's live /verbose toggle.
func TestGatedObserver_ForwardsOnlyWhenEnabled(t *testing.T) {
	inner := &captureObserver{}
	g := NewGatedObserver(inner, false)

	g.Emit(Event{Kind: EvStart}) // dropped: off
	if len(inner.events) != 0 {
		t.Fatalf("expected no events while off, got %d", len(inner.events))
	}
	if g.Enabled() {
		t.Fatal("Enabled() = true, want false")
	}

	g.SetEnabled(true)
	g.Emit(Event{Kind: EvResponse}) // forwarded
	g.SetEnabled(false)
	g.Emit(Event{Kind: EvToolResult}) // dropped again

	if len(inner.events) != 1 || inner.events[0].Kind != EvResponse {
		t.Fatalf("forwarded events = %v, want one EvResponse", inner.kinds())
	}
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
	exec := NewExecutor(ExecutorConfig{
		Provider: prov, WorkDir: t.TempDir(), Observer: obs, Registry: reg,
		Audit: &audit.MemoryRecorder{}, Tier: capability.TierBalanced,
	})

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
