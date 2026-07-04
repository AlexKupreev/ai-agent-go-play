package tools

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
)

// Workspace is a mutable working-directory anchor. The shell tool reads the current
// directory from it at execution time, so a mid-run project switch (projects.md P3) can
// re-point subsequent commands at the new project without rebuilding the executor — the
// per-command `cmd.Dir` is the only thing that actually moves the shell, since each command
// is a fresh process and `cd` never persists (projects.md §7). Safe for concurrent use.
type Workspace struct {
	mu  sync.RWMutex
	dir string
}

// NewWorkspace returns a workspace anchored at dir.
func NewWorkspace(dir string) *Workspace { return &Workspace{dir: dir} }

// Dir returns the current working directory.
func (w *Workspace) Dir() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.dir
}

// Set re-anchors the workspace to dir; subsequent shell commands run there.
func (w *Workspace) Set(dir string) {
	w.mu.Lock()
	w.dir = dir
	w.mu.Unlock()
}

// NewShell creates a shell tool that runs commands with workDir as the working
// directory. Any files the agent creates will land in workDir automatically. This is the
// fixed-directory convenience form; NewShellIn takes a mutable Workspace for a re-anchorable
// executor.
//
// If gate is non-nil, commands that look destructive (see isDestructive) must
// be approved before they run. Pass nil to disable the gate (e.g. in tests).
func NewShell(workDir string, gate HumanGate) Tool {
	return NewShellIn(NewWorkspace(workDir), gate)
}

// NewShellIn is like NewShell but reads its working directory from a mutable Workspace at
// each invocation, so a project switch can re-point it mid-run (projects.md P3).
func NewShellIn(ws *Workspace, gate HumanGate) Tool {
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

			if gate != nil && isDestructive(command) {
				ok, err := gate.Approve(ctx, ApprovalRequest{
					Kind:   "shell.destructive",
					Title:  "This command looks destructive",
					Detail: command,
				})
				if err != nil {
					return fmt.Sprintf("command not run: approval failed: %v", err), nil
				}
				if !ok {
					return "command not run: declined by user", nil
				}
			}

			cmd := exec.CommandContext(ctx, "bash", "-c", command)
			cmd.Dir = ws.Dir() // all relative paths resolve here (re-anchorable, projects.md §7)
			out, err := cmd.CombinedOutput()

			if err != nil {
				return fmt.Sprintf("exit error: %v\noutput:\n%s", err, string(out)), nil
			}
			return string(out), nil
		},
	}
}
