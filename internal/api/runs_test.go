package api

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/tools"
)

// TestSessionsAreOwnerIsolated is the Phase 4e-1 acceptance: two owners share one
// engine, and neither can see, cancel, or resolve the other's run or approval.
func TestSessionsAreOwnerIsolated(t *testing.T) {
	q := NewApprovalQueue()
	// The run parks an approval (so we can test approval scoping) and finishes with
	// the decision.
	runner := RunnerFunc(func(ctx context.Context, task, owner string, obs agent.Observer) (string, error) {
		ok, err := q.Approve(ctx, tools.ApprovalRequest{
			Kind: "shell.destructive", Title: "rm", Detail: "rm -rf x", RunID: task, Owner: owner,
		})
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

	alice := NewClient(srv.URL)
	alice.Owner = "alice"
	bob := NewClient(srv.URL)
	bob.Owner = "bob"
	ctx := context.Background()

	runID, err := alice.StartRun(ctx, "clean")
	if err != nil {
		t.Fatalf("alice StartRun: %v", err)
	}

	// Wait for alice's approval to park.
	var aliceApprovalID string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p, _ := alice.Pending(ctx); len(p) > 0 {
			aliceApprovalID = p[0].ID
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if aliceApprovalID == "" {
		t.Fatal("alice's approval never parked")
	}

	// Bob sees none of alice's session.
	if runs, _ := bob.ListRuns(ctx); len(runs) != 0 {
		t.Errorf("bob sees alice's runs: %+v", runs)
	}
	if _, err := bob.RunStatus(ctx, runID); err == nil {
		t.Error("bob could read alice's run status")
	}
	if err := bob.StopRun(ctx, runID); err == nil {
		t.Error("bob could cancel alice's run")
	}
	if p, _ := bob.Pending(ctx); len(p) != 0 {
		t.Errorf("bob sees alice's approvals: %+v", p)
	}
	if err := bob.Resolve(ctx, aliceApprovalID, true); err == nil {
		t.Error("bob could resolve alice's approval")
	}

	// Alice sees her own run and resolves her own approval.
	if runs, _ := alice.ListRuns(ctx); len(runs) != 1 || runs[0].ID != runID {
		t.Fatalf("alice's runs = %+v, want one run %s", runs, runID)
	}
	if err := alice.Resolve(ctx, aliceApprovalID, true); err != nil {
		t.Fatalf("alice Resolve: %v", err)
	}
	if final := waitForDoneAs(t, alice, runID); final != "did it" {
		t.Fatalf("final = %q, want did it", final)
	}
}

// TestCancelStopsRun is the kill switch: a blocked run is cancelled mid-flight and
// ends in the error state via context cancellation.
func TestCancelStopsRun(t *testing.T) {
	runner := RunnerFunc(func(ctx context.Context, task, owner string, obs agent.Observer) (string, error) {
		obs.Emit(agent.Event{Kind: agent.EvStart, Task: task})
		<-ctx.Done() // block until the kill switch cancels the run context
		return "", ctx.Err()
	})
	srv := httptest.NewServer(NewServer(NewEngine(runner), nil, nil))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx := context.Background()
	runID, err := c.StartRun(ctx, "loop forever")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// The run is running; cancel it.
	if err := c.StopRun(ctx, runID); err != nil {
		t.Fatalf("StopRun: %v", err)
	}

	// It terminates with an error event, and its status reflects the error state.
	if k := waitForTerminalKindAs(t, c, runID); k != KindError {
		t.Fatalf("terminal kind = %q, want %q", k, KindError)
	}
	info, err := c.RunStatus(ctx, runID)
	if err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if info.State != StateError {
		t.Errorf("state = %q, want %q", info.State, StateError)
	}
}

func TestUnknownRunStatusIs404(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewEngine(RunnerFunc(fakeRunner)), nil, nil))
	defer srv.Close()
	c := NewClient(srv.URL)
	if _, err := c.RunStatus(context.Background(), "deadbeef"); err == nil {
		t.Fatal("expected error for unknown run")
	}
}

// waitForDoneAs streams runID as client c and returns the terminal event's text.
func waitForDoneAs(t *testing.T, c *Client, runID string) string {
	t.Helper()
	var text string
	err := c.StreamEvents(context.Background(), runID, func(e Event) {
		if e.Kind == KindDone || e.Kind == KindError {
			text = e.Text
		}
	})
	if err != nil {
		t.Fatalf("StreamEvents: %v", err)
	}
	return text
}

// waitForTerminalKindAs streams runID and returns the terminal event's kind.
func waitForTerminalKindAs(t *testing.T, c *Client, runID string) string {
	t.Helper()
	var kind string
	err := c.StreamEvents(context.Background(), runID, func(e Event) {
		if e.Kind == KindDone || e.Kind == KindError {
			kind = e.Kind
		}
	})
	if err != nil {
		t.Fatalf("StreamEvents: %v", err)
	}
	return kind
}
