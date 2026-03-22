package tools

import (
	"context"
	"fmt"
	"os/exec"
)

// NewShell creates a shell tool that runs commands with workDir as the working directory.
// Any files the agent creates will land in workDir automatically.
func NewShell(workDir string) Tool {
	return Tool{
		Name:        "shell",
		Description: "Run a shell command and return its combined stdout+stderr output. Use this for filesystem operations, running scripts, inspecting the environment, etc.",
		Parameters: map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The bash command to execute",
			},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			command, ok := args["command"].(string)
			if !ok {
				return "", fmt.Errorf("command must be a string")
			}

			cmd := exec.CommandContext(ctx, "bash", "-c", command)
			cmd.Dir = workDir // all relative paths resolve here
			out, err := cmd.CombinedOutput()

			if err != nil {
				return fmt.Sprintf("exit error: %v\noutput:\n%s", err, string(out)), nil
			}
			return string(out), nil
		},
	}
}
