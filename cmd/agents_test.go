package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"ai-agent-go-play/internal/capability"
)

// writeAgentFile writes an agents/<name>.md file under dir/agents, creating the dir.
func writeAgentFile(t *testing.T, dir, name, content string) {
	t.Helper()
	adir := filepath.Join(dir, agentsDir)
	if err := os.MkdirAll(adir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(adir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSplitFrontmatter(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantFront string
		wantBody  string
	}{
		{"standard", "---\ndescription: x\n---\nbody here", "description: x\n", "body here"},
		{"crlf", "---\r\ndescription: x\r\n---\r\nbody", "description: x\r\n", "body"},
		{"no frontmatter", "just a body\nline two", "", "just a body\nline two"},
		{"empty frontmatter", "---\n---\nbody", "", "body"},
		{"no closing fence", "---\ndescription: x\nbody with no fence", "description: x\nbody with no fence", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			front, body := splitFrontmatter([]byte(tc.in))
			if string(front) != tc.wantFront {
				t.Errorf("front = %q, want %q", front, tc.wantFront)
			}
			if string(body) != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
		})
	}
}

func TestSplitTools(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"web_search, web_fetch", []string{"web_search", "web_fetch"}},
		{"web_search web_fetch", []string{"web_search", "web_fetch"}},
		{"  shell ,,  recall ", []string{"shell", "recall"}},
		{"*", []string{"*"}},
	}
	for _, tc := range cases {
		got := splitTools(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("splitTools(%q) = %v, want %v", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("splitTools(%q) = %v, want %v", tc.in, got, tc.want)
			}
		}
	}
}

func TestParseAgentFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "critic.md")
	body := "You are a critic. Find flaws.\n"
	if err := os.WriteFile(path, []byte("---\ndescription: A critic\ntools: web_search, web_fetch\nparallel: true\nprompt_mode: replace\n---\n"+body), 0o600); err != nil {
		t.Fatal(err)
	}
	at, err := parseAgentFile(path)
	if err != nil {
		t.Fatalf("parseAgentFile: %v", err)
	}
	if at.Name != "critic" {
		t.Errorf("Name = %q, want critic (the file stem)", at.Name)
	}
	if at.Description != "A critic" {
		t.Errorf("Description = %q", at.Description)
	}
	if len(at.Tools) != 2 || at.Tools[0] != "web_search" || at.Tools[1] != "web_fetch" {
		t.Errorf("Tools = %v", at.Tools)
	}
	if !at.Parallel || at.PromptMode != "replace" {
		t.Errorf("Parallel=%v PromptMode=%q", at.Parallel, at.PromptMode)
	}
	if at.Prompt != "You are a critic. Find flaws." {
		t.Errorf("Prompt = %q (want trimmed body)", at.Prompt)
	}
}

// The built-in catalog is always present; a global agents/*.md adds a new type and can
// override a built-in.
func TestLoadAgentCatalog_GlobalTierAndOverride(t *testing.T) {
	resetPromptFlags(t)
	cfg := t.TempDir()
	t.Setenv(envConfigDir, cfg)
	// A new type, and an override of the built-in researcher.
	writeAgentFile(t, cfg, "critic.md", "---\ndescription: Critic\ntools: web_search\n---\nBe critical.")
	writeAgentFile(t, cfg, "researcher.md", "---\ndescription: Custom researcher\ntools: web_search\n---\nCustom body.")

	cat, err := loadAgentCatalog(t.TempDir(), capability.TierBalanced)
	if err != nil {
		t.Fatalf("loadAgentCatalog: %v", err)
	}
	if _, ok := cat.Get("critic"); !ok {
		t.Error("critic not loaded from the global agents dir")
	}
	r, ok := cat.Get("researcher")
	if !ok || r.Prompt != "Custom body." {
		t.Errorf("researcher override not applied: %+v", r)
	}
	// general-purpose (built-in) still present.
	if _, ok := cat.Get("general-purpose"); !ok {
		t.Error("built-in general-purpose missing after loading files")
	}
}

// Project (workspace) agents override global ones; the tier gate blocks an untrusted
// workspace on the safe tier unless --workspace was given.
func TestLoadAgentCatalog_ProjectOverGlobalAndTierGate(t *testing.T) {
	resetPromptFlags(t)
	cfg := t.TempDir()
	t.Setenv(envConfigDir, cfg)
	writeAgentFile(t, cfg, "helper.md", "---\ndescription: Global helper\ntools: web_search\n---\nGlobal.")

	ws := t.TempDir()
	writeAgentFile(t, ws, "helper.md", "---\ndescription: Project helper\ntools: web_search\n---\nProject.")

	// Balanced tier: project overrides global.
	cat, err := loadAgentCatalog(ws, capability.TierBalanced)
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := cat.Get("helper"); h.Prompt != "Project." {
		t.Errorf("helper = %q, want project override", h.Prompt)
	}

	// Safe tier, no --workspace: the project tier must not auto-load, so the global wins.
	cat, err = loadAgentCatalog(ws, capability.TierSafe)
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := cat.Get("helper"); h.Prompt != "Global." {
		t.Errorf("safe-tier helper = %q, want global (project gated out)", h.Prompt)
	}

	// Safe tier + explicit --workspace: the project tier is authorized.
	workspaceFlag = ws
	cat, err = loadAgentCatalog(ws, capability.TierSafe)
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := cat.Get("helper"); h.Prompt != "Project." {
		t.Errorf("explicit-workspace helper = %q, want project", h.Prompt)
	}
}

// --no-context-files leaves only the built-in types (no file loading).
func TestLoadAgentCatalog_NoContextFiles(t *testing.T) {
	resetPromptFlags(t)
	cfg := t.TempDir()
	t.Setenv(envConfigDir, cfg)
	writeAgentFile(t, cfg, "critic.md", "---\ndescription: Critic\ntools: web_search\n---\nBe critical.")
	noContextFilesFlag = true

	cat, err := loadAgentCatalog(t.TempDir(), capability.TierBalanced)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cat.Get("critic"); ok {
		t.Error("critic loaded despite --no-context-files")
	}
	if _, ok := cat.Get("researcher"); !ok {
		t.Error("built-in researcher missing")
	}
}

// A malformed agent file (invalid type — e.g. a parallel type naming a write tool) is a
// hard error, not silently skipped.
func TestLoadAgentCatalog_InvalidFileErrors(t *testing.T) {
	resetPromptFlags(t)
	cfg := t.TempDir()
	t.Setenv(envConfigDir, cfg)
	// parallel + a non-read-only tool ⇒ AgentType.validate rejects it.
	writeAgentFile(t, cfg, "bad.md", "---\ndescription: bad\ntools: shell\nparallel: true\n---\nBody.")

	if _, err := loadAgentCatalog(t.TempDir(), capability.TierBalanced); err == nil {
		t.Fatal("expected an error for an invalid agent file, got nil")
	}
}
