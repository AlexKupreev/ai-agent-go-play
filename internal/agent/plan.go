package agent

import "ai-agent-go-play/internal/provider"

// Plan is the structured output produced by the planner agent.
type Plan struct {
	// RefinedTask is the clear, unambiguous task description passed to the executor.
	RefinedTask string `json:"refined_task"`
	// Assumptions lists things the planner inferred without explicit user confirmation.
	Assumptions []string `json:"assumptions"`
	// Confirmed lists things the user explicitly confirmed during clarification.
	Confirmed []string `json:"confirmed"`
}

// planResponseFormat is the structured output schema enforced on the planner's final response.
var planResponseFormat = provider.ResponseFormat{
	Name:        "plan",
	Description: "Refined task and clarification notes produced by the planner",
	Strict:      true,
	Schema: map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"refined_task", "assumptions", "confirmed"},
		"properties": map[string]any{
			"refined_task": map[string]any{
				"type":        "string",
				"description": "Clear, unambiguous task description for the executor",
			},
			"assumptions": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Things the planner inferred without explicit user confirmation",
			},
			"confirmed": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Things the user explicitly confirmed during clarification",
			},
		},
	},
}
