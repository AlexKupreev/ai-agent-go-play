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
	exec := NewExecutor(prov, t.TempDir(), "gpt-4o-mini", "run-abc12345", obs, tools.NewMemoryRegistry(), nil, nil, &audit.MemoryRecorder{}, capability.TierBalanced, nil, tools.UsageContext{}, nil)

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
