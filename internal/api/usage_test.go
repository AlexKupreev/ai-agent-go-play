package api

import (
	"context"
	"testing"
	"time"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/provider"
)

// TestRunUsage_AggregatedIntoInfoAndAudit checks Phase 6a end to end at the engine
// level: a run's per-step Usage is summed into RunInfo, and a run_usage audit event is
// recorded with the same totals.
func TestRunUsage_AggregatedIntoInfoAndAudit(t *testing.T) {
	// A runner that emits two model responses carrying usage, then finishes. The
	// engine fans a UsageObserver alongside the hub, so these are accumulated.
	runner := RunnerFunc(func(_ context.Context, _ string, _ string, _ RunOptions, obs agent.Observer) (string, error) {
		obs.Emit(agent.Event{Kind: agent.EvResponse, Usage: provider.Usage{InputTokens: 1000, OutputTokens: 200, CachedTokens: 64}})
		obs.Emit(agent.Event{Kind: agent.EvResponse, Usage: provider.Usage{InputTokens: 30, OutputTokens: 8}})
		return "final answer", nil
	})

	rec := &audit.MemoryRecorder{}
	e := NewEngine(runner)
	e.SetAuditRecorder(rec)

	id := e.StartRun("do the thing", RunOptions{})
	waitRunState(t, e, id, StateDone)

	info, err := e.RunStatus(id)
	if err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if info.Usage.InputTokens != 1030 || info.Usage.OutputTokens != 208 || info.Usage.CachedTokens != 64 {
		t.Fatalf("RunInfo.Usage = %+v, want {1030 208 64}", info.Usage)
	}
	if info.Steps != 2 {
		t.Fatalf("RunInfo.Steps = %d, want 2", info.Steps)
	}

	// A run_usage event landed in the audit log with matching totals.
	events, err := rec.Tail(0, audit.Filter{Type: audit.EventRunUsage})
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("run_usage events = %d, want 1", len(events))
	}
	ev := events[0]
	if ev.Run != id {
		t.Errorf("run_usage.Run = %q, want %q", ev.Run, id)
	}
	// JSON round-trip is not involved (MemoryRecorder keeps the map as-is), so the
	// ints are still int64.
	if ev.Fields["input_tokens"] != int64(1030) || ev.Fields["output_tokens"] != int64(208) {
		t.Errorf("run_usage.Fields = %v, want input=1030 output=208", ev.Fields)
	}
	if ev.Fields["steps"] != 2 {
		t.Errorf("run_usage steps = %v, want 2", ev.Fields["steps"])
	}
}

func TestRunUsageRecordedWhenModelOutputLimitFails(t *testing.T) {
	runner := RunnerFunc(func(_ context.Context, _ string, _ string, _ RunOptions, obs agent.Observer) (string, error) {
		usage := provider.Usage{InputTokens: 42, OutputTokens: 128000}
		obs.Emit(agent.Event{Kind: agent.EvResponse, Stop: provider.StopMaxTokens, Usage: usage})
		return "", &agent.ModelOutputLimitError{Usage: usage}
	})
	rec := &audit.MemoryRecorder{}
	e := NewEngine(runner)
	e.SetAuditRecorder(rec)
	id := e.StartRun("trigger cap", RunOptions{})
	waitRunState(t, e, id, StateError)

	info, err := e.RunStatus(id)
	if err != nil {
		t.Fatal(err)
	}
	if info.Usage.OutputTokens != 128000 || info.Steps != 1 || info.Error != "model output limit reached" {
		t.Fatalf("failed run info = %+v", info)
	}
	var events []audit.Event
	deadline := time.Now().Add(time.Second)
	for len(events) == 0 && time.Now().Before(deadline) {
		events, err = rec.Tail(0, audit.Filter{Type: audit.EventRunUsage})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if len(events) != 1 {
		t.Fatalf("run_usage events=%v", events)
	}
	if events[0].Fields["output_tokens"] != int64(128000) || events[0].Fields["steps"] != 1 {
		t.Fatalf("run_usage = %+v", events[0])
	}
}
