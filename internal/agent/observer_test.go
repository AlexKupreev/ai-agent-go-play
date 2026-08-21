package agent

import (
	"bytes"
	"context"
	"strings"
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
	// LastInput tracks the most recent response's input tokens (the context fill), not the sum.
	if u.LastInput() != 5 {
		t.Fatalf("LastInput() = %d, want 5 (the last response's input tokens)", u.LastInput())
	}
}

// TestGatedObserver_ForwardsOnlyWhenEnabled checks that a gate suppresses events while
// off and passes them while on — the mechanism behind chat's live /verbose toggle.
func TestInternalized_TagsAndForwards(t *testing.T) {
	inner := &captureObserver{}
	obs := Internalized(inner)
	obs.Emit(Event{Kind: EvResponse, Text: "plan json"})

	if len(inner.events) != 1 {
		t.Fatalf("want 1 forwarded event, got %d", len(inner.events))
	}
	if !inner.events[0].Internal {
		t.Error("forwarded event should be tagged Internal")
	}
	if inner.events[0].Text != "plan json" {
		t.Errorf("payload should be preserved, got %q", inner.events[0].Text)
	}
}

func TestInternalized_NilInnerStaysNil(t *testing.T) {
	if Internalized(nil) != nil {
		t.Error("Internalized(nil) should be nil")
	}
}

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

// TestCLIObserver_ThinkingBlock checks that the model's preamble (a response that
// precedes a tool call) is wrapped in a bounded "thinking" block so it is separable
// from the final answer, while a response with no tool calls (the final answer) is NOT
// printed by the observer — the command prints that itself. A non-terminal sink (this
// buffer) gets plain text with no ANSI escapes.
func TestCLIObserver_ThinkingBlock(t *testing.T) {
	var buf bytes.Buffer
	obs := NewCLIObserver(&buf)

	// Preamble: text alongside a tool call → rendered as a bounded thinking block.
	obs.Emit(Event{Kind: EvResponse, Text: "let me search\nfor it", Calls: []provider.ToolCall{{Name: "web_search"}}})
	// Final answer: text with no tool calls → the observer must stay silent.
	obs.Emit(Event{Kind: EvResponse, Text: "the answer is 42"})

	got := buf.String()
	for _, want := range []string{"╭─ thinking ─", "│ let me search", "│ for it", "╰─"} {
		if !strings.Contains(got, want) {
			t.Errorf("trace missing %q\nfull:\n%s", want, got)
		}
	}
	if strings.Contains(got, "the answer is 42") {
		t.Errorf("observer printed the final answer (should be left to the command):\n%s", got)
	}
	if strings.Contains(got, "\x1b[") {
		t.Errorf("non-terminal sink got ANSI escapes:\n%q", got)
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

func TestRunRejectsWhitespaceOnlyFinalAnswer(t *testing.T) {
	prov := &scriptedProvider{steps: []provider.StepResponse{textStep(" \n\t ")}}
	exec := NewExecutor(ExecutorConfig{Provider: prov, WorkDir: t.TempDir(), Tier: capability.TierBalanced})
	if out, err := exec.Run(context.Background(), "answer"); err == nil || out != "" {
		t.Fatalf("Run = %q, %v; want empty-final error", out, err)
	}
}

func TestRunRejectsEmptyFinalAnswerAfterSuccessfulTool(t *testing.T) {
	prov := &scriptedProvider{steps: []provider.StepResponse{
		toolCallStep("c1", "run_code", map[string]any{"code": "return 1"}),
		textStep(""),
	}}
	exec := NewExecutor(ExecutorConfig{Provider: prov, WorkDir: t.TempDir(), Tier: capability.TierBalanced})
	if out, err := exec.Run(context.Background(), "compute"); err == nil || out != "" {
		t.Fatalf("Run = %q, %v; want empty-final error", out, err)
	}
}
