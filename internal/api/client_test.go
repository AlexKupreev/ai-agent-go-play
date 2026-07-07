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
	runner := RunnerFunc(func(ctx context.Context, runID, task string, _ RunOptions, obs agent.Observer) (string, error) {
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

	srv := httptest.NewServer(NewServer(NewEngine(runner), q, nil, nil, nil))
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()

	runID, err := c.StartRun(ctx, "clean", RunOptions{})
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

// TestClient_AnswersQuestion drives the ask_user half over the Client: the run parks a
// question, the client reads it from Pending (mode "question") and delivers a free-text
// answer via Answer, which unblocks the run.
func TestClient_AnswersQuestion(t *testing.T) {
	q := NewApprovalQueue()
	runner := RunnerFunc(func(ctx context.Context, runID, task string, _ RunOptions, obs agent.Observer) (string, error) {
		ans, err := q.Ask(ctx, tools.Question{Prompt: "which env?", RunID: task})
		if err != nil {
			return "", err
		}
		return "using " + ans, nil
	})

	srv := httptest.NewServer(NewServer(NewEngine(runner), q, nil, nil, nil))
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()

	runID, err := c.StartRun(ctx, "deploy", RunOptions{})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			pending, err := c.Pending(ctx)
			if err == nil && len(pending) > 0 && pending[0].Mode == "question" {
				_ = c.Answer(ctx, pending[0].ID, "prod")
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
	if terminal != "using prod" {
		t.Fatalf("terminal event text = %q, want %q", terminal, "using prod")
	}
}

// TestClient_RevokeTool exercises the client's tool-detail + revoke path against a
// real server sharing a catalog.
func TestClient_RevokeTool(t *testing.T) {
	cat := catalogWith(t, scriptSpec("reverse_string", "reverse the characters in a string"))
	srv := httptest.NewServer(NewServer(NewEngine(RunnerFunc(fakeRunner)), nil, cat, nil, nil))
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()

	detail, err := c.ToolDetail(ctx, "reverse_string")
	if err != nil {
		t.Fatalf("ToolDetail: %v", err)
	}
	if detail.Source == "" {
		t.Fatalf("detail missing source: %+v", detail)
	}

	if err := c.RevokeTool(ctx, "reverse_string"); err != nil {
		t.Fatalf("RevokeTool: %v", err)
	}
	if _, ok := cat.Get("reverse_string"); ok {
		t.Fatal("tool still present after client revoke")
	}

	// Revoking a missing tool surfaces an error.
	if err := c.RevokeTool(ctx, "reverse_string"); err == nil {
		t.Fatal("expected error revoking absent tool, got nil")
	}
}

func TestClient_StartRunErrorsOnBadStatus(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewEngine(RunnerFunc(fakeRunner)), nil, nil, nil, nil))
	defer srv.Close()
	c := NewClient(srv.URL)

	// Empty task is rejected with 400 by the server.
	if _, err := c.StartRun(context.Background(), "", RunOptions{}); err == nil {
		t.Fatal("expected error for empty task, got nil")
	}
}
