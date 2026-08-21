package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusTool(t *testing.T) {
	tool := NewStatusTool(StatusDeps{
		Model:    "gpt-4o-mini",
		Tier:     "balanced",
		RunID:    "adbc6b79365b6f99",
		Version:  "dev",
		WorkDir:  ".",
		Registry: NewMemoryRegistry(), // no authored tools
		Memory:   nil,                 // no memory store
	})

	out, err := tool.Run(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"model: gpt-4o-mini",
		"tier: balanced",
		"run: adbc6b79", // truncated to 8 chars
		"build: dev",
		"authored tools: 0",
		"memory entries: 0",
		"Host",
		"cpu:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q; got:\n%s", want, out)
		}
	}
}

// TestStatusTool_StateOnDisk checks the disk-usage section: a directory reports its entry
// count + bytes, a file reports its bytes, and an absent path is silently skipped.
func TestStatusTool_StateOnDisk(t *testing.T) {
	base := t.TempDir()
	runs := filepath.Join(base, "runs")
	if err := os.MkdirAll(filepath.Join(runs, "run1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runs, "run1", "run.jsonl"), []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := filepath.Join(base, "tools.json")
	if err := os.WriteFile(catalog, []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}

	tool := NewStatusTool(StatusDeps{
		Model: "gpt-4o-mini", Tier: "balanced", WorkDir: ".", Registry: NewMemoryRegistry(),
		StateDirs: []StateDir{
			{Label: "transcripts (runs)", Path: runs},
			{Label: "tool catalog", Path: catalog},
			{Label: "memory", Path: filepath.Join(base, "does-not-exist.json")},
		},
	})
	out, err := tool.Run(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"State on disk",
		"transcripts (runs): 1 item,", // one run dir
		"tool catalog: 2 B",           // the "[]" file
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q; got:\n%s", want, out)
		}
	}
	// An absent path is skipped, not rendered.
	if strings.Contains(out, "does-not-exist") || strings.Contains(out, "memory:") {
		t.Errorf("absent state path should be skipped; got:\n%s", out)
	}
}

// TestStatusTool_NoStateDirs omits the section entirely when no dirs are supplied (the
// default for runs/chat/eval executors that don't wire it — no dangling header).
func TestStatusTool_NoStateDirs(t *testing.T) {
	tool := NewStatusTool(StatusDeps{Model: "m", Tier: "safe", WorkDir: ".", Registry: NewMemoryRegistry()})
	out, _ := tool.Run(context.Background(), map[string]any{})
	if strings.Contains(out, "State on disk") {
		t.Errorf("no StateDirs should omit the section; got:\n%s", out)
	}
}

func TestStatusTool_ContextSection(t *testing.T) {
	// Known window with usage → percentage.
	tool := NewStatusTool(StatusDeps{Model: "m", Tier: "safe", WorkDir: ".", Registry: NewMemoryRegistry(),
		Context: func() (int64, int) { return 64000, 128000 }})
	out, _ := tool.Run(context.Background(), map[string]any{})
	if !strings.Contains(out, "Context") || !strings.Contains(out, "64,000 of 128,000 tokens used (50%)") {
		t.Errorf("expected a 50%% context line; got:\n%s", out)
	}

	// Unknown window (limit 0) but usage present → tokens without a percentage.
	tool = NewStatusTool(StatusDeps{Model: "m", Tier: "safe", WorkDir: ".", Registry: NewMemoryRegistry(),
		Context: func() (int64, int) { return 5000, 0 }})
	out, _ = tool.Run(context.Background(), map[string]any{})
	if !strings.Contains(out, "5,000 tokens in the last request") || !strings.Contains(out, "unknown") {
		t.Errorf("expected an unknown-window line; got:\n%s", out)
	}

	// No Context func → no section.
	tool = NewStatusTool(StatusDeps{Model: "m", Tier: "safe", WorkDir: ".", Registry: NewMemoryRegistry()})
	out, _ = tool.Run(context.Background(), map[string]any{})
	if strings.Contains(out, "Context\n") {
		t.Errorf("nil Context should omit the section; got:\n%s", out)
	}
}

func TestStatusTool_DefaultModelLabel(t *testing.T) {
	tool := NewStatusTool(StatusDeps{Model: "", Tier: "safe", WorkDir: ".", Registry: NewMemoryRegistry()})
	out, _ := tool.Run(context.Background(), map[string]any{})
	if !strings.Contains(out, "(provider default)") {
		t.Errorf("empty model should render as (provider default); got:\n%s", out)
	}
	if !strings.Contains(out, "run: (none)") {
		t.Errorf("empty run id should render as (none); got:\n%s", out)
	}
}

func TestStatusTool_ResolvedConfiguration(t *testing.T) {
	tool := NewStatusTool(StatusDeps{
		Model: "effective-model", Tier: "balanced", WorkDir: "/workspace", Registry: NewMemoryRegistry(),
		Config: StatusConfiguration{
			Workspace: "/workspace", SessionID: "0123abcd9999", RequestedModel: "requested-model",
			RequestedTier: "permissive", GuidanceChars: 7,
			ActiveSpace:       &StatusSpace{ID: "polish-lessons", Name: "Polish lessons"},
			PromptComposition: "SYSTEM.md wraps built-in base via {{base}}",
			PromptSources:     []StatusSource{{Name: "SYSTEM.md", Path: "/workspace/SYSTEM.md", Layer: "workspace", Mode: "replace", Active: true}},
			AgentTypeCount:    1, AgentTypeSources: []StatusSource{{Name: "worker", Path: "/workspace/agents/worker.md", Layer: "workspace"}},
			Plan: true, Critique: true,
			Limits: StatusLimits{MaxIterations: 20, ScriptTimeoutS: 30, MaxInlineTools: 5, MaxHTTPBytes: 4194304, MaxFinishedRuns: 100, SpawnDepth: 1, MaxRevisions: 1},
		},
	})
	out, err := tool.Run(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Configuration", "workspace: /workspace", "session: 0123abcd   guidance: 7 chars",
		"active space: Polish lessons (id: polish-lessons)",
		"requested: model requested-model, tier permissive",
		"prompts: SYSTEM.md wraps built-in base via {{base}}",
		"SYSTEM.md: /workspace/SYSTEM.md [workspace, replace, active]",
		"agent types: 1", "worker: /workspace/agents/worker.md [workspace]",
		"workflow: plan true, critique true, max revisions 1",
		"limits: iterations 20, script 30s, inline tools 5, HTTP bytes 4194304, finished runs 100, spawn depth 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q; got:\n%s", want, out)
		}
	}
}
