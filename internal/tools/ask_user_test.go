package tools

import (
	"context"
	"testing"
)

// TestAskUserTool_RoutesThroughGate checks that ask_user forwards the question (and the
// run id) to the HumanGate and returns the gate's answer to the model.
func TestAskUserTool_RoutesThroughGate(t *testing.T) {
	var gotPrompt, gotRunID string
	gate := GateFuncs{
		AskFn: func(_ context.Context, q Question) (string, error) {
			gotPrompt, gotRunID = q.Prompt, q.RunID
			return "blue", nil
		},
	}
	tool := NewAskUserTool(gate, "run-7")

	out, err := tool.Run(context.Background(), map[string]any{"question": "which color?"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "blue" {
		t.Errorf("answer = %q, want blue", out)
	}
	if gotPrompt != "which color?" || gotRunID != "run-7" {
		t.Errorf("gate saw (%q, %q), want (which color?, run-7)", gotPrompt, gotRunID)
	}
}

// TestAskUserTool_RejectsNonStringQuestion checks the argument guard.
func TestAskUserTool_RejectsNonStringQuestion(t *testing.T) {
	tool := NewAskUserTool(GateFuncs{}, "")
	if _, err := tool.Run(context.Background(), map[string]any{"question": 42}); err == nil {
		t.Fatal("expected an error for a non-string question")
	}
}
