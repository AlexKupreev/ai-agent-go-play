package cmd

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/guidance"
	"ai-agent-go-play/internal/session"
	"ai-agent-go-play/internal/space"
)

func TestWithGuidancePrecedenceAndIsolation(t *testing.T) {
	operator := []string{"operator global", "operator workspace"}
	got := withGuidance(operator, "user global", "space guidance", "session rule")
	if len(got) != 5 {
		t.Fatalf("withGuidance returned %d layers, want 5: %#v", len(got), got)
	}
	want := []string{"operator global", "operator workspace", "Workspace guidance", "space guidance", "Session guidance"}
	for i, marker := range want {
		if !strings.Contains(got[i], marker) {
			t.Fatalf("layer %d = %q, want marker %q", i, got[i], marker)
		}
	}
	if len(operator) != 2 {
		t.Fatalf("input appends mutated: %#v", operator)
	}

	// The real executor composition preserves immutable kernel blocks ahead of all user
	// guidance while retaining the same specificity order.
	ex := agent.NewExecutor(agent.ExecutorConfig{Tier: capability.TierBalanced, PromptAppends: got})
	prompt := ex.SystemPrompt()
	markers := []string{
		"Do NOT run Python, Node.js, Ruby, or R via shell",
		"[END UNTRUSTED WEB CONTENT]",
		"operator global",
		"operator workspace",
		"Workspace guidance",
		"space guidance",
		"Session guidance",
	}
	last := -1
	for _, marker := range markers {
		at := strings.Index(prompt, marker)
		if at < 0 || at <= last {
			t.Fatalf("prompt marker %q missing or out of order; previous index %d, got %d", marker, last, at)
		}
		last = at
	}
}

func TestGuidanceServiceAllScopesAndRedactedAudit(t *testing.T) {
	root := t.TempDir()
	rec := &audit.MemoryRecorder{}
	global := guidance.NewFileStore(filepath.Join(root, "guidance.md"), "global", rec)
	spaces := space.NewStore(filepath.Join(root, "spaces"))
	sp, err := spaces.Create("Polish")
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewFileStore(filepath.Join(root, "sessions"))
	sess, err := sessions.Create()
	if err != nil {
		t.Fatal(err)
	}
	service := &guidanceService{workspace: global, spaces: spaces, sessions: sessions, rec: rec}

	secret := "private standing rule 🐻"
	for _, tc := range []struct {
		scope  guidance.Scope
		target string
	}{
		{guidance.ScopeGlobal, ""},
		{guidance.ScopeSpace, sp.ID},
		{guidance.ScopeSession, sess.ID},
	} {
		if err := service.SetGuidance(tc.scope, tc.target, secret); err != nil {
			t.Fatalf("SetGuidance(%s): %v", tc.scope, err)
		}
		got, err := service.GetGuidance(tc.scope, tc.target)
		if err != nil || got != secret {
			t.Fatalf("GetGuidance(%s) = %q, %v", tc.scope, got, err)
		}
		if err := service.SetGuidance(tc.scope, tc.target, secret); err != nil {
			t.Fatalf("idempotent SetGuidance(%s): %v", tc.scope, err)
		}
	}

	events := rec.Snapshot()
	if len(events) != 3 {
		t.Fatalf("guidance events = %d, want one per changed scope: %+v", len(events), events)
	}
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("guidance audit leaked body: %s", encoded)
	}
	for _, event := range events {
		if event.Type != audit.EventGuidanceUpdated || event.Fields["resulting_size"] != guidance.CharCount(secret) {
			t.Fatalf("bad guidance audit metadata: %+v", event)
		}
	}
}

func TestApplyLocalGuidanceRequiresActiveSpace(t *testing.T) {
	root := t.TempDir()
	sessionText := ""
	_, err := applyLocalGuidance("space show", guidance.NewFileStore(filepath.Join(root, "guidance.md"), "global", nil), space.NewStore(filepath.Join(root, "spaces")), "", &sessionText, nil, "run")
	if err == nil || !strings.Contains(err.Error(), "no space is active") {
		t.Fatalf("space guidance without active space = %v", err)
	}
}

func TestWithGuidanceSkipsEmptyScopes(t *testing.T) {
	got := withGuidance([]string{"operator"}, " \n", "", "")
	if len(got) != 1 || got[0] != "operator" {
		t.Fatalf("empty guidance added prompt layers: %#v", got)
	}
}
