package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-agent-go-play/internal/logger"
	"ai-agent-go-play/internal/provider"
	"ai-agent-go-play/internal/tools"
)

// alwaysToolProvider returns a tool call on every step (never a final answer), so the ReAct
// loop runs until it hits its iteration bound. It counts how many times it was called.
type alwaysToolProvider struct{ calls int }

func (p *alwaysToolProvider) Step(_ context.Context, _ provider.StepRequest) (provider.StepResponse, error) {
	p.calls++
	return provider.StepResponse{
		Stop: provider.StopToolCalls,
		Content: []provider.ContentBlock{{
			Kind:     provider.BlockToolCall,
			ToolCall: &provider.ToolCall{ID: "c1", Name: "noop", Input: `{}`},
		}},
	}, nil
}

// TestLimits_MaxIterationsBoundsLoop proves ExecutorConfig.Limits.MaxIterations is honored:
// a run that never produces a final answer stops after exactly that many model calls.
func TestLimits_MaxIterationsBoundsLoop(t *testing.T) {
	prov := &alwaysToolProvider{}
	ex := NewExecutor(ExecutorConfig{
		Provider: prov, WorkDir: t.TempDir(), Registry: tools.NewMemoryRegistry(),
		Limits: Limits{MaxIterations: 3},
	})
	_, err := ex.Run(context.Background(), "loop forever")
	if err == nil || !strings.Contains(err.Error(), "max iterations (3)") {
		t.Fatalf("err = %v, want a max-iterations(3) error", err)
	}
	if prov.calls != 3 {
		t.Fatalf("provider called %d times, want 3 (the iteration cap)", prov.calls)
	}
}

// TestLimits_Defaults proves the zero Limits resolves to the built-in defaults, and that a
// partial override leaves the other fields at their defaults.
func TestLimits_Defaults(t *testing.T) {
	got := Limits{}.withDefaults()
	if got.MaxIterations != defaultMaxIterations || got.ScriptTimeout != defaultScriptTimeout || got.MaxInlineTools != defaultMaxInlineTools ||
		got.PlannerMaxOutputTokens != DefaultPlannerMaxOutputTokens || got.CriticMaxOutputTokens != DefaultCriticMaxOutputTokens ||
		got.ExecutorMaxOutputTokens != DefaultExecutorMaxOutputTokens {
		t.Fatalf("zero Limits.withDefaults() = %+v, want the built-in defaults", got)
	}
	partial := Limits{MaxIterations: 50, ScriptTimeout: 2 * time.Second}.withDefaults()
	if partial.MaxIterations != 50 || partial.ScriptTimeout != 2*time.Second {
		t.Fatalf("override lost: %+v", partial)
	}
	if partial.MaxInlineTools != defaultMaxInlineTools {
		t.Fatalf("unset MaxInlineTools should default, got %d", partial.MaxInlineTools)
	}
}

type captureRequestProvider struct {
	response provider.StepResponse
	requests []provider.StepRequest
}

func (p *captureRequestProvider) Step(_ context.Context, req provider.StepRequest) (provider.StepResponse, error) {
	p.requests = append(p.requests, req)
	return p.response, nil
}

func TestRunFailsClosedOnMaxOutputTokens(t *testing.T) {
	truncated := `{"query":"` + strings.Repeat("x", 128<<10)
	usage := provider.Usage{InputTokens: 99, OutputTokens: 128000, CachedTokens: 80}
	prov := &captureRequestProvider{response: provider.StepResponse{
		Stop: provider.StopMaxTokens, Usage: usage,
		Content: []provider.ContentBlock{{Kind: provider.BlockToolCall, ToolCall: &provider.ToolCall{
			ID: "call-runaway", Name: "web_search", Input: truncated,
		}}},
	}}
	logBase := t.TempDir()
	runLog, err := logger.NewWithID(logBase, "runaway")
	if err != nil {
		t.Fatal(err)
	}
	usageObs := NewUsageObserver()
	events := &captureObserver{}
	exec := NewExecutor(ExecutorConfig{
		Provider: prov, WorkDir: t.TempDir(),
		Observer: Observers{NewLoggerObserver(runLog), usageObs, events},
	})

	answer, err := exec.Run(context.Background(), "weather near Krakow")
	if closeErr := runLog.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	var limitErr *ModelOutputLimitError
	if answer != "" || !errors.As(err, &limitErr) || err.Error() != "model output limit reached" {
		t.Fatalf("Run = %q, %v; want typed output-limit error", answer, err)
	}
	if len(prov.requests) != 1 {
		t.Fatalf("provider calls = %d, want exactly one", len(prov.requests))
	}
	if prov.requests[0].MaxOutputTokens != DefaultExecutorMaxOutputTokens {
		t.Fatalf("request max output = %d, want %d", prov.requests[0].MaxOutputTokens, DefaultExecutorMaxOutputTokens)
	}
	if got := usageObs.Total(); got != usage || usageObs.Steps() != 1 {
		t.Fatalf("usage = %+v/%d steps, want %+v/1", got, usageObs.Steps(), usage)
	}
	for _, event := range events.events {
		if event.Kind == EvToolStart || event.Kind == EvToolResult {
			t.Fatalf("partial tool call executed/emitted: %+v", event)
		}
	}
	messages := exec.Messages()
	if len(messages) != 1 || messages[0].Role != provider.RoleUser {
		t.Fatalf("history retained malformed assistant content: %+v", messages)
	}

	data, err := os.ReadFile(filepath.Join(logBase, "runaway", "run.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var sawResponse, sawError bool
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("invalid transcript JSON: %v\n%s", err, line)
		}
		switch entry["type"] {
		case "response":
			sawResponse = true
			if entry["stop"] != string(provider.StopMaxTokens) {
				t.Fatalf("response stop = %v", entry["stop"])
			}
			calls := entry["tool_calls"].([]any)
			metadata := calls[0].(map[string]any)
			if metadata["argument_bytes"] != float64(len(truncated)) || metadata["valid_json"] != false {
				t.Fatalf("safe tool metadata = %v", metadata)
			}
			if _, retained := metadata["input"]; retained {
				t.Fatal("truncated arguments were retained in response metadata")
			}
		case "error":
			sawError = entry["error"] == "model output limit reached"
		}
	}
	if !sawResponse || !sawError {
		t.Fatalf("transcript missing safe response/error records:\n%s", data)
	}
}

func TestRunRejectsMalformedToolCallBeforeHistoryMutation(t *testing.T) {
	prov := &captureRequestProvider{response: provider.StepResponse{
		Stop: provider.StopToolCalls,
		Content: []provider.ContentBlock{{Kind: provider.BlockToolCall, ToolCall: &provider.ToolCall{
			ID: "c1", Name: "web_search", Input: `{"query":"unfinished`,
		}}},
	}}
	events := &captureObserver{}
	exec := NewExecutor(ExecutorConfig{Provider: prov, WorkDir: t.TempDir(), Observer: events})
	_, err := exec.Run(context.Background(), "search")
	if err == nil || !strings.Contains(err.Error(), "arguments must be a JSON object") {
		t.Fatalf("err = %v", err)
	}
	if len(prov.requests) != 1 || len(exec.Messages()) != 1 {
		t.Fatalf("calls=%d history=%+v", len(prov.requests), exec.Messages())
	}
	for _, event := range events.events {
		if event.Kind == EvToolStart || event.Kind == EvToolResult {
			t.Fatalf("malformed call reached tool dispatch: %+v", event)
		}
	}
}

func TestValidateToolCallsRejectsUnsafeShapesAndSizes(t *testing.T) {
	tests := []struct {
		name string
		call provider.ToolCall
		want string
	}{
		{name: "empty id", call: provider.ToolCall{Name: "web_search", Input: `{}`}, want: "empty call id"},
		{name: "empty name", call: provider.ToolCall{ID: "c1", Input: `{}`}, want: "empty tool name"},
		{name: "array", call: provider.ToolCall{ID: "c1", Name: "shell", Input: `[]`}, want: "JSON object"},
		{name: "null", call: provider.ToolCall{ID: "c1", Name: "shell", Input: `null`}, want: "JSON object"},
		{name: "oversized search", call: provider.ToolCall{ID: "c1", Name: "web_search", Input: `{"q":"` + strings.Repeat("x", maxWebSearchArgumentBytes) + `"}`}, want: "limit is 2048"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := validateToolCalls([]provider.ToolCall{tc.call}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRoleSpecificMaxOutputTokens(t *testing.T) {
	response := provider.StepResponse{Stop: provider.StopEndTurn, Content: []provider.ContentBlock{{Kind: provider.BlockText, Text: "ok"}}}
	tests := []struct {
		name  string
		want  int64
		build func(provider.Provider) *Agent
	}{
		{name: "executor", want: 7000, build: func(p provider.Provider) *Agent {
			return NewExecutor(ExecutorConfig{Provider: p, WorkDir: t.TempDir(), Limits: Limits{ExecutorMaxOutputTokens: 7000}})
		}},
		{name: "planner", want: 2000, build: func(p provider.Provider) *Agent {
			return NewPlannerWithLimits(p, "", "", "", "", nil, "", nil, Limits{PlannerMaxOutputTokens: 2000})
		}},
		{name: "critic", want: 1000, build: func(p provider.Provider) *Agent {
			return NewCriticWithLimits(p, "", "", nil, Limits{CriticMaxOutputTokens: 1000})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prov := &captureRequestProvider{response: response}
			if _, err := tc.build(prov).Run(context.Background(), "task"); err != nil {
				t.Fatal(err)
			}
			if len(prov.requests) != 1 || prov.requests[0].MaxOutputTokens != tc.want {
				t.Fatalf("requests=%+v, want max output %d", prov.requests, tc.want)
			}
		})
	}
}
