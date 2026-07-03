package cmd

import (
	"fmt"
	"os"
	"path/filepath"
)

// workspaceFlag is bound to the persistent --workspace flag (see cmd/root.go).
var workspaceFlag string

// resolveWorkspace returns the workspace root: the directory the agent is acting on. It is
// the shell tool's working directory today and the anchor for the project prompt tier later
// (workspace.md §4). Precedence: the --workspace flag (validated to be an existing directory,
// made absolute) > the process cwd.
//
// This names and generalizes today's bare workDir (workspace.md §1): the same cwd default,
// now a first-class, overridable concept. The parent-directory walk that collects project
// context files up the tree is a later stage (workspace.md §2), and its stop bound is still an
// open question (§6), so this resolver does not walk — it returns the cwd (or the override).
func resolveWorkspace() (string, error) {
	if workspaceFlag != "" {
		abs, err := filepath.Abs(workspaceFlag)
		if err != nil {
			return "", fmt.Errorf("--workspace %q: %w", workspaceFlag, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return "", fmt.Errorf("--workspace %q: %w", workspaceFlag, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("--workspace %q: not a directory", workspaceFlag)
		}
		return abs, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}
	return wd, nil
}
