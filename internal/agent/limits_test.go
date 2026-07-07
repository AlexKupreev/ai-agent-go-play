package agent

import (
	"context"
	"strings"
	"testing"
	"time"

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
			ToolCall: &provider.ToolCall{ID: "c1", Name: "noop", Input: []byte(`{}`)},
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
	if got.MaxIterations != defaultMaxIterations || got.ScriptTimeout != defaultScriptTimeout || got.MaxInlineTools != defaultMaxInlineTools {
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
