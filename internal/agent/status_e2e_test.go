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

// The agent calls status, then answers. Proves the tool is always offered and returns a
// live self-report; -v prints the real host snapshot for this box.
func TestStatus_EndToEnd(t *testing.T) {
	prov := &scriptedProvider{steps: []provider.StepResponse{
		toolCallStep("c1", "status", map[string]any{}),
		textStep("reported"),
	}}
	obs := &captureObserver{}
	exec := NewExecutor(ExecutorConfig{
		Provider: prov, WorkDir: t.TempDir(), Model: "gpt-4o-mini", RunID: "run-abc12345",
		Observer: obs, Registry: tools.NewMemoryRegistry(),
		Audit: &audit.MemoryRecorder{}, Tier: capability.TierBalanced,
	})

	if _, err := exec.Run(context.Background(), "what's your status?"); err != nil {
		t.Fatalf("run: %v", err)
	}

	offered := false
	for _, name := range prov.seenTools[0] {
		if name == "status" {
			offered = true
		}
	}
	if !offered {
		t.Fatalf("status not offered; tools = %v", prov.seenTools[0])
	}

	var report string
	for _, e := range obs.events {
		if e.Kind == EvToolResult && e.Call != nil && e.Call.Name == "status" {
			report = e.Result
		}
	}
	for _, want := range []string{"model: gpt-4o-mini", "tier: balanced", "Host", "cpu:"} {
		if !strings.Contains(report, want) {
			t.Fatalf("status report missing %q; got:\n%s", want, report)
		}
	}
	t.Logf("live status report:\n%s", report)
}

// TestStatus_ContextGauge proves the end-to-end context gauge: the loop records the input
// tokens of the request that produced a step, and the status tool (same step) reports it as a
// percentage of the model's window. gpt-4o-mini → 128k window; 64k in ⇒ 50%.
func TestStatus_ContextGauge(t *testing.T) {
	call := toolCallStep("c1", "status", map[string]any{})
	call.Usage = provider.Usage{InputTokens: 64000}
	prov := &scriptedProvider{steps: []provider.StepResponse{call, textStep("ok")}}
	obs := &captureObserver{}
	exec := NewExecutor(ExecutorConfig{
		Provider: prov, WorkDir: t.TempDir(), Model: "gpt-4o-mini", RunID: "run-ctx",
		Observer: obs, Registry: tools.NewMemoryRegistry(),
		Audit: &audit.MemoryRecorder{}, Tier: capability.TierBalanced,
	})
	if _, err := exec.Run(context.Background(), "how full is my context?"); err != nil {
		t.Fatalf("run: %v", err)
	}
	var report string
	for _, e := range obs.events {
		if e.Kind == EvToolResult && e.Call != nil && e.Call.Name == "status" {
			report = e.Result
		}
	}
	if !strings.Contains(report, "64,000 of 128,000 tokens used (50%)") {
		t.Fatalf("status report missing the context gauge; got:\n%s", report)
	}
}
