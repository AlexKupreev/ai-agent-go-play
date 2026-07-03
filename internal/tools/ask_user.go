package tools

import (
	"context"
	"fmt"
)

// NewAskUserTool builds the ask_user tool over a HumanGate, so a clarifying question is
// routed to whoever owns the run (stdin on the CLI, the approval queue on serve → the
// frontend that started the run) rather than always reading local stdin. runID tags the
// question for routing. It blocks until the human answers.
func NewAskUserTool(gate HumanGate, runID string) Tool {
	return Tool{
		Name:        "ask_user",
		Description: "Ask the user a clarifying question when the task is ambiguous and cannot be resolved through research. Use this sparingly — only when you genuinely cannot proceed without human input.",
		Parameters: map[string]any{
			"question": map[string]any{
				"type":        "string",
				"description": "The question to ask the user",
			},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			question, ok := args["question"].(string)
			if !ok {
				return "", fmt.Errorf("question must be a string")
			}
			return gate.Ask(ctx, Question{Prompt: question, RunID: runID})
		},
	}
}
