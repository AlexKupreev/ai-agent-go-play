package cmd

import (
	"os"
	"path/filepath"
	"testing"
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

func TestLoadConfigDirPrompts_NoContextFilesFlag(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, systemPromptFile, "should be ignored")
	writeFile(t, dir, agentsFile, "should be ignored")
	t.Setenv(envConfigDir, dir)

	noContextFilesFlag = true
	defer func() { noContextFilesFlag = false }()

	pf, err := loadConfigDirPrompts()
	if err != nil {
		t.Fatal(err)
	}
	if pf.Override != "" || len(pf.Appends) != 0 {
		t.Errorf("--no-context-files should yield empty promptFiles, got %+v", pf)
	}
}

func TestLoadConfigDirPrompts_ReadsConfigDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, agentsFile, "operator note")
	t.Setenv(envConfigDir, dir)

	pf, err := loadConfigDirPrompts()
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.Appends) != 1 || pf.Appends[0] != "operator note" {
		t.Errorf("Appends = %v, want [operator note]", pf.Appends)
	}
}
