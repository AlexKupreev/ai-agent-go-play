package cmd

import (
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = orig
	out, _ := io.ReadAll(r)
	return string(out)
}

// withPromptsShowIsolation sets the globals the command reads to a reproducible, file-free
// configuration (temp config dir, bare built-in prompts) and restores them after.
func withPromptsShowIsolation(t *testing.T) {
	t.Helper()
	cd, ws, tf, mf, ncf := configDirFlag, workspaceFlag, tierFlag, modelFlag, noContextFilesFlag
	pa, pp, pc := promptsAllFlag, promptsPlannerFlag, promptsCriticFlag
	t.Cleanup(func() {
		configDirFlag, workspaceFlag, tierFlag, modelFlag, noContextFilesFlag = cd, ws, tf, mf, ncf
		promptsAllFlag, promptsPlannerFlag, promptsCriticFlag = pa, pp, pc
	})
	configDirFlag = t.TempDir()
	noContextFilesFlag = true // bare built-in prompts — no repo/config files
	tierFlag = "safe"
	modelFlag = ""
	promptsAllFlag, promptsPlannerFlag, promptsCriticFlag = false, false, false
}

// TestPromptsShow_ExecutorDefault: with no selector flag, only the executor prompt prints.
func TestPromptsShow_ExecutorDefault(t *testing.T) {
	withPromptsShowIsolation(t)
	out := captureStdout(t, func() {
		if err := promptsShowCmd.RunE(promptsShowCmd, nil); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "=== EXECUTOR ===") || !strings.Contains(out, "helpful AI agent") {
		t.Fatalf("executor prompt not rendered:\n%s", out)
	}
	if strings.Contains(out, "=== PLANNER ===") || strings.Contains(out, "=== CRITIC ===") {
		t.Fatalf("default should show only the executor:\n%s", out)
	}
	// The tier-policy section reflects the selected tier.
	if !strings.Contains(out, "trust tier: safe") {
		t.Fatalf("executor prompt missing the safe tier policy:\n%s", out)
	}
}

// TestPromptsShow_All: --all prints executor, planner, and critic prompts.
func TestPromptsShow_All(t *testing.T) {
	withPromptsShowIsolation(t)
	promptsAllFlag = true
	out := captureStdout(t, func() {
		if err := promptsShowCmd.RunE(promptsShowCmd, nil); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})
	for _, h := range []string{"=== EXECUTOR ===", "=== PLANNER ===", "=== CRITIC ==="} {
		if !strings.Contains(out, h) {
			t.Fatalf("--all missing section %q:\n%s", h, out)
		}
	}
	// The planner prompt carries the live execution-environment section.
	if !strings.Contains(out, "planning agent") || !strings.Contains(out, "Trust tier: safe") {
		t.Fatalf("planner prompt not rendered with its environment:\n%s", out)
	}
}
