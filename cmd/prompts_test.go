package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"ai-agent-go-play/internal/capability"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPromptTier_Empty(t *testing.T) {
	pf, err := loadPromptTier(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if pf.Override != "" || len(pf.Appends) != 0 {
		t.Errorf("expected empty promptFiles for a dir with no context files, got %+v", pf)
	}
}

func TestLoadPromptTier_SystemAndAgents(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, systemPromptFile, "  operator prompt  \n")
	writeFile(t, dir, agentsFile, "project rules")

	pf, err := loadPromptTier(dir)
	if err != nil {
		t.Fatal(err)
	}
	if pf.Override != "operator prompt" { // trimmed
		t.Errorf("Override = %q, want trimmed operator prompt", pf.Override)
	}
	if len(pf.Appends) != 1 || pf.Appends[0] != "project rules" {
		t.Errorf("Appends = %v, want [project rules]", pf.Appends)
	}
}

func TestLoadPromptTier_ClaudeAlias(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, agentsAliasFile, "from CLAUDE.md")

	pf, err := loadPromptTier(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.Appends) != 1 || pf.Appends[0] != "from CLAUDE.md" {
		t.Errorf("Appends = %v, want [from CLAUDE.md]", pf.Appends)
	}
}

// When both AGENTS.md and CLAUDE.md exist in the same dir, AGENTS.md is preferred and the
// alias is not also concatenated.
func TestLoadPromptTier_PrefersAgentsOverAlias(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, agentsFile, "canonical")
	writeFile(t, dir, agentsAliasFile, "alias")

	pf, err := loadPromptTier(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.Appends) != 1 || pf.Appends[0] != "canonical" {
		t.Errorf("Appends = %v, want [canonical] only", pf.Appends)
	}
}

// resetPromptFlags clears the package-level prompt/workspace flags so a test starts from a
// known state and restores them after.
func resetPromptFlags(t *testing.T) {
	t.Helper()
	noContextFilesFlag, contextFileFlag, workspaceFlag = false, nil, ""
	t.Cleanup(func() { noContextFilesFlag, contextFileFlag, workspaceFlag = false, nil, "" })
}

func TestLoadPrompts_NoContextFilesFlag(t *testing.T) {
	resetPromptFlags(t)
	cfg := t.TempDir()
	writeFile(t, cfg, systemPromptFile, "should be ignored")
	writeFile(t, cfg, agentsFile, "should be ignored")
	t.Setenv(envConfigDir, cfg)

	ws := t.TempDir()
	writeFile(t, ws, agentsFile, "should be ignored too")
	noContextFilesFlag = true

	pf, err := loadPrompts(ws, capability.TierPermissive)
	if err != nil {
		t.Fatal(err)
	}
	if pf.Override != "" || len(pf.Appends) != 0 {
		t.Errorf("--no-context-files should yield empty promptFiles, got %+v", pf)
	}
}

func TestLoadPrompts_ReadsConfigDir(t *testing.T) {
	resetPromptFlags(t)
	cfg := t.TempDir()
	writeFile(t, cfg, agentsFile, "operator note")
	t.Setenv(envConfigDir, cfg)

	// Safe tier + no --workspace: the workspace tier must not auto-load, so only the
	// config-dir append is present.
	ws := t.TempDir()
	writeFile(t, ws, agentsFile, "project note")
	pf, err := loadPrompts(ws, capability.TierSafe)
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.Appends) != 1 || pf.Appends[0] != "operator note" {
		t.Errorf("Appends = %v, want [operator note] only", pf.Appends)
	}
}

// Project over global: config-dir AGENTS.md then workspace AGENTS.md (project last), and a
// workspace SYSTEM.md wins outright over a config-dir one.
func TestLoadPrompts_ProjectOverGlobal(t *testing.T) {
	resetPromptFlags(t)
	cfg := t.TempDir()
	writeFile(t, cfg, systemPromptFile, "global system")
	writeFile(t, cfg, agentsFile, "global agents")
	t.Setenv(envConfigDir, cfg)

	ws := t.TempDir()
	writeFile(t, ws, systemPromptFile, "project system")
	writeFile(t, ws, agentsFile, "project agents")

	pf, err := loadPrompts(ws, capability.TierBalanced)
	if err != nil {
		t.Fatal(err)
	}
	if pf.Override != "project system" {
		t.Errorf("Override = %q, want project system (project wins outright)", pf.Override)
	}
	want := []string{"global agents", "project agents"}
	if len(pf.Appends) != 2 || pf.Appends[0] != want[0] || pf.Appends[1] != want[1] {
		t.Errorf("Appends = %v, want %v (global then project)", pf.Appends, want)
	}
}

// Safe tier does not auto-load workspace files, but an explicit --workspace authorizes them.
func TestLoadPrompts_SafeTierExplicitWorkspace(t *testing.T) {
	resetPromptFlags(t)
	cfg := t.TempDir()
	t.Setenv(envConfigDir, cfg)

	ws := t.TempDir()
	writeFile(t, ws, agentsFile, "project note")
	workspaceFlag = ws // explicit → honored even on safe

	pf, err := loadPrompts(ws, capability.TierSafe)
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.Appends) != 1 || pf.Appends[0] != "project note" {
		t.Errorf("Appends = %v, want [project note] (explicit --workspace on safe)", pf.Appends)
	}
}

// A workspace that resolves to the config dir must not be loaded twice.
func TestLoadPrompts_WorkspaceEqualsConfigDir(t *testing.T) {
	resetPromptFlags(t)
	dir := t.TempDir()
	writeFile(t, dir, agentsFile, "shared note")
	t.Setenv(envConfigDir, dir)
	workspaceFlag = dir

	pf, err := loadPrompts(dir, capability.TierPermissive)
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.Appends) != 1 || pf.Appends[0] != "shared note" {
		t.Errorf("Appends = %v, want [shared note] once, not duplicated", pf.Appends)
	}
}

// --context-file is appended last and honored regardless of tier; a missing one errors.
func TestLoadPrompts_ContextFile(t *testing.T) {
	resetPromptFlags(t)
	cfg := t.TempDir()
	writeFile(t, cfg, agentsFile, "global agents")
	t.Setenv(envConfigDir, cfg)

	extra := filepath.Join(t.TempDir(), "extra.md")
	if err := os.WriteFile(extra, []byte("  extra context  "), 0o600); err != nil {
		t.Fatal(err)
	}
	contextFileFlag = []string{extra}

	// Safe tier so no workspace tier interferes; --context-file still loads.
	pf, err := loadPrompts(t.TempDir(), capability.TierSafe)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"global agents", "extra context"}
	if len(pf.Appends) != 2 || pf.Appends[0] != want[0] || pf.Appends[1] != want[1] {
		t.Errorf("Appends = %v, want %v (context-file last, trimmed)", pf.Appends, want)
	}
}

func TestLoadPrompts_ContextFileMissingErrors(t *testing.T) {
	resetPromptFlags(t)
	t.Setenv(envConfigDir, t.TempDir())
	contextFileFlag = []string{filepath.Join(t.TempDir(), "nope.md")}

	if _, err := loadPrompts(t.TempDir(), capability.TierSafe); err == nil {
		t.Error("loadPrompts with a missing --context-file should error")
	}
}
