package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
)

// AskUser is a tool that lets the agent ask the user a clarifying question.
// It blocks until the user types a response.
var AskUser = Tool{
	Name:        "ask_user",
	Description: "Ask the user a clarifying question when the task is ambiguous and cannot be resolved through research. Use this sparingly — only when you genuinely cannot proceed without human input.",
	Parameters: map[string]any{
		"question": map[string]any{
			"type":        "string",
			"description": "The question to ask the user",
		},
	},
	Run: func(_ context.Context, args map[string]any) (string, error) {
		question, ok := args["question"].(string)
		if !ok {
			return "", fmt.Errorf("question must be a string")
		}

		fmt.Printf("\n[agent asks] %s\n> ", question)

		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			return strings.TrimSpace(scanner.Text()), nil
		}
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("failed to read user input: %w", err)
		}
		return "", nil
	},
}
