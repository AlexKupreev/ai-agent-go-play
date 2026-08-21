package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/capability"
)

func TestEffectiveConfigSnapshotIsResolvedAndSecretSafe(t *testing.T) {
	resetPromptFlags(t)
	cfgDir := t.TempDir()
	writeFile(t, cfgDir, systemPromptFile, "Ada {{base}}")
	t.Setenv(envConfigDir, cfgDir)
	workspace := t.TempDir()
	workspaceFlag = workspace
	prompts, err := newPromptState(workspace, capability.TierBalanced)
	if err != nil {
		t.Fatal(err)
	}
	guidanceFile := filepath.Join(workspace, ".agent", "guidance.md")
	if err := os.MkdirAll(filepath.Dir(guidanceFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(guidanceFile, []byte("answer in Polish"), 0o600); err != nil {
		t.Fatal(err)
	}
	defaults := newServeDefaults("", capability.TierBalanced)
	defaults.setWithSources("", capability.TierBalanced, "built-in", "built-in")
	service := effectiveConfigService{
		workspace: workspace, prompts: prompts, defaults: defaults,
		limits: agent.Limits{}, configLimits: ConfigLimits{}, spawnDepth: 1,
		secretNames: []string{"weather"}, guidancePath: guidanceFile,
		plan: true, critique: true, maxRevisions: 1,
	}

	got := service.EffectiveConfig()
	if got.Model.Value != agent.DefaultModel || got.Model.Source != "built-in" {
		t.Fatalf("model = %+v", got.Model)
	}
	if got.Prompts.Composition != "SYSTEM.md wraps built-in base via {{base}}" || len(got.Prompts.Sources) != 1 {
		t.Fatalf("prompts = %+v", got.Prompts)
	}
	if len(got.Guidance) != 1 || !got.Guidance[0].Loaded || got.Guidance[0].Chars != 16 {
		t.Fatalf("guidance = %+v", got.Guidance)
	}
	if len(got.SecretNames) != 1 || got.SecretNames[0] != "weather" {
		t.Fatalf("secret names = %v", got.SecretNames)
	}
	if got.Limits.MaxIterations != 20 || got.Limits.MaxFinishedRuns != 100 || got.Limits.MaxHTTPBytes != 1<<20 ||
		got.Limits.PlannerMaxOutputTokens != 6144 || got.Limits.CriticMaxOutputTokens != 3072 || got.Limits.ExecutorMaxOutputTokens != 12288 {
		t.Fatalf("limits = %+v", got.Limits)
	}
	status := toolStatusConfiguration(got)
	if status.Workspace != workspace || status.PromptComposition != got.Prompts.Composition ||
		status.Limits.MaxFinishedRuns != 100 || status.Limits.MaxRevisions != 1 || status.Limits.ExecutorMaxOutputTokens != 12288 || !status.Plan || !status.Critique {
		t.Fatalf("tool status config = %+v", status)
	}
}
