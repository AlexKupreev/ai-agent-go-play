package agent

import (
	"context"
	"strings"
	"testing"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/memory"
	"ai-agent-go-play/internal/provider"
	"ai-agent-go-play/internal/tools"
)

// A fact remembered in one run is recalled in a later run sharing the same store —
// the Phase 4d acceptance criterion, with no live API.
func TestMemory_PersistsAcrossRuns(t *testing.T) {
	store := memory.NewMemoryStore()
	rec := &audit.MemoryRecorder{}

	// Run 1: remember a fact, then finish.
	run1 := &scriptedProvider{steps: []provider.StepResponse{
		toolCallStep("c1", "remember", map[string]any{"key": "user.editor", "value": "neovim", "tags": []any{"preference"}}),
		textStep("noted your editor"),
	}}
	exec1 := NewExecutor(run1, t.TempDir(), "", "run-1", nil, tools.NewMemoryRegistry(), store, nil, rec, capability.TierBalanced, nil, tools.UsageContext{})
	if _, err := exec1.Run(context.Background(), "remember my editor is neovim"); err != nil {
		t.Fatalf("run 1: %v", err)
	}

	// The write landed in the store and was audited.
	if got, ok := store.Get("user.editor"); !ok || got.Value != "neovim" {
		t.Fatalf("store after run 1 = %+v, %v; want neovim", got, ok)
	}
	if !memoryWritten(rec, "user.editor") {
		t.Errorf("missing memory_write audit event: %+v", rec.Snapshot())
	}

	// Run 2: a fresh executor over the SAME store recalls it.
	obs := &captureObserver{}
	run2 := &scriptedProvider{steps: []provider.StepResponse{
		toolCallStep("c2", "recall", map[string]any{"key": "user.editor"}),
		textStep("your editor is neovim"),
	}}
	exec2 := NewExecutor(run2, t.TempDir(), "", "run-2", obs, tools.NewMemoryRegistry(), store, nil, rec, capability.TierBalanced, nil, tools.UsageContext{})
	if _, err := exec2.Run(context.Background(), "what editor do I use?"); err != nil {
		t.Fatalf("run 2: %v", err)
	}

	if got := recallResult(obs, "recall"); !strings.Contains(got, "neovim") {
		t.Errorf("recall result = %q, want it to contain neovim", got)
	}
}

func memoryWritten(rec *audit.MemoryRecorder, key string) bool {
	for _, e := range rec.Snapshot() {
		if e.Type == audit.EventMemoryWrite && e.Fields["key"] == key {
			return true
		}
	}
	return false
}

// recallResult returns the tool result fed back for the named tool call.
func recallResult(obs *captureObserver, tool string) string {
	for _, e := range obs.events {
		if e.Kind == EvToolResult && e.Call != nil && e.Call.Name == tool {
			return e.Result
		}
	}
	return ""
}
