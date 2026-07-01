package agent

import (
	"context"
	"testing"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/provider"
	"ai-agent-go-play/internal/tools"
)

// recordingProvider returns scripted text answers and remembers the messages it was
// sent on the most recent Step, so a test can prove conversation history is retained.
type recordingProvider struct {
	answers      []string
	calls        int
	lastMessages []provider.Message
}

func (p *recordingProvider) Step(_ context.Context, req provider.StepRequest) (provider.StepResponse, error) {
	p.lastMessages = req.Messages
	r := textStep(p.answers[p.calls])
	p.calls++
	return r, nil
}

// TestExecutor_RetainsConversationAcrossTurns proves the REPL seam: calling Run
// repeatedly on one executor carries the conversation forward, and Reset clears it.
func TestExecutor_RetainsConversationAcrossTurns(t *testing.T) {
	prov := &recordingProvider{answers: []string{"first answer", "second answer"}}
	ex := NewExecutor(prov, t.TempDir(), "test-model", "run1", nil,
		tools.NewMemoryRegistry(), nil, &audit.MemoryRecorder{}, capability.TierSafe, nil)

	out1, err := ex.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if out1 != "first answer" {
		t.Fatalf("turn 1 = %q, want %q", out1, "first answer")
	}

	out2, err := ex.Run(context.Background(), "and now?")
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if out2 != "second answer" {
		t.Fatalf("turn 2 = %q, want %q", out2, "second answer")
	}

	// Turn 2 must have seen the full history: system, user(1), assistant(1), user(2).
	got := prov.lastMessages
	wantRoles := []provider.Role{provider.RoleSystem, provider.RoleUser, provider.RoleAssistant, provider.RoleUser}
	if len(got) != len(wantRoles) {
		t.Fatalf("turn 2 saw %d messages, want %d", len(got), len(wantRoles))
	}
	for i, want := range wantRoles {
		if got[i].Role != want {
			t.Fatalf("message %d role = %q, want %q", i, got[i].Role, want)
		}
	}

	// Reset clears history: the next turn starts fresh (system + user only).
	ex.Reset()
	prov.answers = append(prov.answers, "third answer")
	if _, err := ex.Run(context.Background(), "fresh start"); err != nil {
		t.Fatalf("post-reset turn: %v", err)
	}
	if len(prov.lastMessages) != 2 {
		t.Fatalf("after reset saw %d messages, want 2 (system+user)", len(prov.lastMessages))
	}
}
