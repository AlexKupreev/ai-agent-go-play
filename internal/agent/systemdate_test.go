package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/provider"
	"ai-agent-go-play/internal/tools"
)

// The current date is injected into the system message each request, so the agent knows
// "today" without a tool call (it can't get the clock from the compute-only sandbox).
func TestSystemPrompt_CarriesCurrentDate(t *testing.T) {
	prov := &recordingProvider{answers: []string{"ok"}}
	exec := NewExecutor(ExecutorConfig{
		Provider: prov, WorkDir: t.TempDir(), Registry: tools.NewMemoryRegistry(),
		Audit: &audit.MemoryRecorder{}, Tier: capability.TierBalanced,
	})

	if _, err := exec.Run(context.Background(), "what year is it?"); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(prov.lastMessages) == 0 || prov.lastMessages[0].Role != provider.RoleSystem {
		t.Fatal("first request message is not the system prompt")
	}
	sys := prov.lastMessages[0].Content[0].Text
	today := time.Now().Format("2006-01-02")
	if !strings.Contains(sys, today) {
		t.Fatalf("system prompt missing today's date %q; got:\n%s", today, sys)
	}
}
