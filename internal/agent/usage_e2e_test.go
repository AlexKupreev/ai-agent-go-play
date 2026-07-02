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

type stubLedger struct{}

func (stubLedger) Session(string) (provider.Usage, int) { return provider.Usage{}, 0 }
func (stubLedger) Today() (provider.Usage, int)         { return provider.Usage{}, 0 }

func TestUsageTool_OfferedWhenLedgerWired(t *testing.T) {
	build := func(uc tools.UsageContext) []string {
		prov := &scriptedProvider{steps: []provider.StepResponse{textStep("done")}}
		exec := NewExecutor(prov, t.TempDir(), "", "", nil, tools.NewMemoryRegistry(), nil, nil,
			&audit.MemoryRecorder{}, capability.TierBalanced, nil, uc, nil)
		if _, err := exec.Run(context.Background(), "hi"); err != nil {
			t.Fatalf("run: %v", err)
		}
		return prov.seenTools[0]
	}

	if slices.Contains(build(tools.UsageContext{}), "usage") {
		t.Error("usage tool offered even though no ledger was wired")
	}
	if !slices.Contains(build(tools.UsageContext{SessionID: "s1", Ledger: stubLedger{}}), "usage") {
		t.Error("usage tool not offered even though a ledger was wired")
	}
}
