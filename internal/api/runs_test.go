package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"ai-agent-go-play/internal/agent"
)

// TestCancelStopsRun is the kill switch: a blocked run is cancelled mid-flight and
// ends in the error state via context cancellation.
func TestCancelStopsRun(t *testing.T) {
	runner := RunnerFunc(func(ctx context.Context, task string, obs agent.Observer) (string, error) {
		obs.Emit(agent.Event{Kind: agent.EvStart, Task: task})
		<-ctx.Done() // block until the kill switch cancels the run context
		return "", ctx.Err()
	})
	srv := httptest.NewServer(NewServer(NewEngine(runner), nil, nil, nil, nil))
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
	srv := httptest.NewServer(NewServer(NewEngine(RunnerFunc(fakeRunner)), nil, nil, nil, nil))
	defer srv.Close()
	c := NewClient(srv.URL)
	if _, err := c.RunStatus(context.Background(), "deadbeef"); err == nil {
		t.Fatal("expected error for unknown run")
	}
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
