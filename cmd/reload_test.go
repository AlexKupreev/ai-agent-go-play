package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"ai-agent-go-play/internal/capability"
)

// promptState.reload re-reads the prompt files + agent catalog from disk, so a `serve` picks
// up edits without a restart. A fresh snapshot reflects the new files.
func TestPromptState_ReloadPicksUpEdits(t *testing.T) {
	resetPromptFlags(t)
	cfg := t.TempDir()
	writeFile(t, cfg, agentsFile, "v1 operator note")
	t.Setenv(envConfigDir, cfg)

	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, agentsDir), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(ws, agentsDir), "helper.md", "---\ndescription: a helper\n---\nhelp body")
	workspaceFlag = ws // explicit workspace so the project tier loads on any tier

	s, err := newPromptState(ws, capability.TierBalanced)
	if err != nil {
		t.Fatal(err)
	}
	pf, cat := s.snapshot()
	if len(pf.Appends) != 1 || pf.Appends[0] != "v1 operator note" {
		t.Fatalf("initial Appends = %v, want [v1 operator note]", pf.Appends)
	}
	if _, ok := cat.Get("helper"); !ok {
		t.Fatalf("initial catalog missing project agent type 'helper'")
	}

	// Edit the operator note and add a second project agent type, then reload.
	writeFile(t, cfg, agentsFile, "v2 operator note")
	writeFile(t, filepath.Join(ws, agentsDir), "critic.md", "---\ndescription: a critic\n---\ncritique body")
	if err := s.reload(); err != nil {
		t.Fatal(err)
	}
	pf, cat = s.snapshot()
	if len(pf.Appends) != 1 || pf.Appends[0] != "v2 operator note" {
		t.Errorf("after reload Appends = %v, want [v2 operator note]", pf.Appends)
	}
	if _, ok := cat.Get("critic"); !ok {
		t.Errorf("after reload catalog missing newly added agent type 'critic'")
	}
}

// A malformed agent file makes reload fail; the previously loaded config must survive intact.
func TestPromptState_ReloadErrorKeepsPrevious(t *testing.T) {
	resetPromptFlags(t)
	cfg := t.TempDir()
	writeFile(t, cfg, agentsFile, "good note")
	t.Setenv(envConfigDir, cfg)
	ws := t.TempDir()
	workspaceFlag = ws

	s, err := newPromptState(ws, capability.TierBalanced)
	if err != nil {
		t.Fatal(err)
	}

	// A parallel agent that inherits all tools is rejected at load (parallel ⇒ read-only).
	if err := os.MkdirAll(filepath.Join(ws, agentsDir), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(ws, agentsDir), "bad.md", "---\ndescription: bad\nparallel: true\ntools: \"*\"\n---\nbody")
	if err := s.reload(); err == nil {
		t.Fatal("reload with a malformed agent file should error")
	}

	// The previously loaded config is unchanged.
	pf, _ := s.snapshot()
	if len(pf.Appends) != 1 || pf.Appends[0] != "good note" {
		t.Errorf("after failed reload Appends = %v, want [good note] (previous config preserved)", pf.Appends)
	}
}
