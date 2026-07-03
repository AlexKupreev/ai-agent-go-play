package cmd

import (
	"os"
	"path/filepath"
	"strings"
)

// noContextFilesFlag is bound to the persistent --no-context-files flag (see cmd/root.go).
// When set, no SYSTEM.md/AGENTS.md is loaded and the agent runs on the built-in base prompt
// (parity with pi's -nc; useful for reproducible runs and debugging).
var noContextFilesFlag bool

// Context-file names loaded from a prompt tier (prompts.md §2). SYSTEM.md replaces the base
// prompt; AGENTS.md (alias CLAUDE.md) is appended as operator instructions.
const (
	systemPromptFile = "SYSTEM.md"
	agentsFile       = "AGENTS.md"
	agentsAliasFile  = "CLAUDE.md"
)

// promptFiles carries the operator's prompt customization read from disk, ready to hand to
// agent.ExecutorConfig. Override (from SYSTEM.md) replaces the base prompt; Appends (from
// AGENTS.md/CLAUDE.md bodies) are concatenated after it.
type promptFiles struct {
	Override string
	Appends  []string
}

// loadConfigDirPrompts reads the config-dir prompt tier (prompts.md §2): SYSTEM.md as the
// base-prompt override and AGENTS.md (or its alias CLAUDE.md) as an append. This is the
// global/operator tier, always trusted — it is the agent's own state, not an untrusted
// workspace. Missing files are a no-op. --no-context-files short-circuits to empty. The
// workspace tier (project files, tier-gated) layers on top in a later stage.
func loadConfigDirPrompts() (promptFiles, error) {
	if noContextFilesFlag {
		return promptFiles{}, nil
	}
	dir, err := configDir()
	if err != nil {
		return promptFiles{}, err
	}
	return loadPromptTier(dir)
}

// loadPromptTier reads SYSTEM.md and AGENTS.md/CLAUDE.md from a single directory. Absent
// files yield empty results (no error). When both AGENTS.md and CLAUDE.md exist in the same
// directory only one is loaded (AGENTS.md preferred), rather than silently concatenating two
// files that likely duplicate.
func loadPromptTier(dir string) (promptFiles, error) {
	var pf promptFiles

	override, err := readOptionalFile(filepath.Join(dir, systemPromptFile))
	if err != nil {
		return promptFiles{}, err
	}
	pf.Override = override

	agents, err := readOptionalFile(filepath.Join(dir, agentsFile))
	if err != nil {
		return promptFiles{}, err
	}
	if agents == "" {
		if agents, err = readOptionalFile(filepath.Join(dir, agentsAliasFile)); err != nil {
			return promptFiles{}, err
		}
	}
	if agents != "" {
		pf.Appends = append(pf.Appends, agents)
	}
	return pf, nil
}

// readOptionalFile returns the trimmed contents of path, or "" if it does not exist. A
// non-not-exist error (e.g. a permission problem) is returned so it is not silently ignored.
func readOptionalFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
