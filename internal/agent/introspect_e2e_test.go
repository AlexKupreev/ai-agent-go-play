package agent

import (
	"context"
	"slices"
	"testing"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/provider"
	"ai-agent-go-play/internal/tools"
)

// tool_catalog is always offered; recent_activity is offered only when an audit reader
// is wired.
func TestIntrospectTools_Wiring(t *testing.T) {
	offered := func(reader audit.Reader) []string {
		prov := &scriptedProvider{steps: []provider.StepResponse{textStep("done")}}
		exec := NewExecutor(prov, t.TempDir(), "", "", nil, tools.NewMemoryRegistry(), nil, nil,
			&audit.MemoryRecorder{}, capability.TierBalanced, nil, tools.UsageContext{}, reader)
		if _, err := exec.Run(context.Background(), "hi"); err != nil {
			t.Fatalf("run: %v", err)
		}
		return prov.seenTools[0]
	}

	if !slices.Contains(offered(nil), "tool_catalog") {
		t.Error("tool_catalog should always be offered")
	}
	if slices.Contains(offered(nil), "recent_activity") {
		t.Error("recent_activity offered without an audit reader")
	}
	if !slices.Contains(offered(&audit.MemoryRecorder{}), "recent_activity") {
		t.Error("recent_activity not offered even though a reader was wired")
	}
}
