package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ConfirmFunc asks the user to approve an action, returning true to proceed.
type ConfirmFunc func(prompt string) bool

// StdinConfirm is the default CLI confirmation: it prints the prompt and reads a
// y/N answer from stdin. Anything other than y/yes is treated as decline.
func StdinConfirm(prompt string) bool {
	fmt.Printf("\n[confirm] %s\n  proceed? [y/N] > ", prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		ans := strings.ToLower(strings.TrimSpace(scanner.Text()))
		return ans == "y" || ans == "yes"
	}
	return false
}

// NewShell creates a shell tool that runs commands with workDir as the working
// directory. Any files the agent creates will land in workDir automatically.
//
// If confirm is non-nil, commands that look destructive (see isDestructive) must
// be approved before they run. Pass nil to disable the gate (e.g. in tests).
func NewShell(workDir string, confirm ConfirmFunc) Tool {
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

			if confirm != nil && isDestructive(command) {
				if !confirm(fmt.Sprintf("This command looks destructive:\n  %s", command)) {
					return "command not run: declined by user", nil
				}
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
