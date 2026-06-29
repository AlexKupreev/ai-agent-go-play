package api

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/tools"
)

// TestClient_DrivesRunWithApproval exercises the Client end-to-end against a real
// server: start a run that parks an approval, resolve it via the client while
// streaming, and confirm the streamed terminal event reflects the decision.
func TestClient_DrivesRunWithApproval(t *testing.T) {
	q := NewApprovalQueue()
	runner := RunnerFunc(func(ctx context.Context, task string, obs agent.Observer) (string, error) {
		obs.Emit(agent.Event{Kind: agent.EvStart, Task: task})
		ok, err := q.Approve(ctx, tools.ApprovalRequest{Kind: "shell.destructive", Title: "rm", Detail: "rm -rf x", RunID: task})
		if err != nil {
			return "", err
		}
		if !ok {
			return "declined", nil
		}
		return "did it", nil
	})

	srv := httptest.NewServer(NewServer(NewEngine(runner), q, nil))
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()

	runID, err := c.StartRun(ctx, "clean")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// Background: watch for the parked approval and approve it.
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			pending, err := c.Pending(ctx)
			if err == nil && len(pending) > 0 {
				_ = c.Resolve(ctx, pending[0].ID, true)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	var mu sync.Mutex
	var terminal string
	err = c.StreamEvents(ctx, runID, func(e Event) {
		if e.Kind == KindDone || e.Kind == KindError {
			mu.Lock()
			terminal = e.Text
			mu.Unlock()
		}
	})
	if err != nil {
		t.Fatalf("StreamEvents: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if terminal != "did it" {
		t.Fatalf("terminal event text = %q, want %q", terminal, "did it")
	}
}

func TestClient_StartRunErrorsOnBadStatus(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewEngine(RunnerFunc(fakeRunner)), nil, nil))
	defer srv.Close()
	c := NewClient(srv.URL)

	// Empty task is rejected with 400 by the server.
	if _, err := c.StartRun(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty task, got nil")
	}
}
