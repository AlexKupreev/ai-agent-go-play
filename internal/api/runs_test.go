package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"ai-agent-go-play/internal/agent"
)

// TestCancelStopsRun is the kill switch: a blocked run is cancelled mid-flight and
// ends in the error state via context cancellation.
func TestCancelStopsRun(t *testing.T) {
	runner := RunnerFunc(func(ctx context.Context, runID, task string, obs agent.Observer) (string, error) {
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

// TestFinishedRunEviction verifies the retention cap: finished runs beyond maxFinished
// are evicted oldest-first (so a long-lived serve doesn't accumulate every run's event
// history), while running runs are never evicted regardless of age.
func TestFinishedRunEviction(t *testing.T) {
	release := make(chan struct{})
	runner := RunnerFunc(func(ctx context.Context, runID, task string, obs agent.Observer) (string, error) {
		if task == "block" {
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return "done", nil
	})
	e := NewEngine(runner)
	e.maxFinished = 2

	blocked := e.StartRun("block") // oldest run of all, but still running

	ids := make([]string, 4)
	for i := range ids {
		ids[i] = e.StartRun("quick")
		waitRunState(t, e, ids[i], StateDone)
	}

	// The two newest finished runs are retained; the older finished ones are evicted.
	waitEvicted(t, e, ids[0])
	waitEvicted(t, e, ids[1])
	for _, id := range ids[2:] {
		if _, err := e.RunStatus(id); err != nil {
			t.Errorf("run %s was evicted, want retained: %v", id, err)
		}
	}
	// The running run survives eviction even though it started first.
	info, err := e.RunStatus(blocked)
	if err != nil || info.State != StateRunning {
		t.Errorf("blocked run: state=%q err=%v, want still running", info.State, err)
	}
	close(release)
	waitRunState(t, e, blocked, StateDone)
}

// memRunStore is an in-memory RunStore for tests: it records saved RunInfos and serves them
// back, standing in for the cmd layer's info.json store.
type memRunStore struct {
	mu    sync.Mutex
	saved map[string]RunInfo
}

func newMemRunStore() *memRunStore { return &memRunStore{saved: map[string]RunInfo{}} }

func (m *memRunStore) Save(info RunInfo) {
	m.mu.Lock()
	m.saved[info.ID] = info
	m.mu.Unlock()
}

func (m *memRunStore) Load(id string) (RunInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	info, ok := m.saved[id]
	return info, ok
}

// TestRunStoreSurvivesEviction proves §3.2: a run's metadata is persisted on completion, so
// RunStatus still returns it (task, state, result) after the engine has evicted the run.
func TestRunStoreSurvivesEviction(t *testing.T) {
	e := NewEngine(RunnerFunc(func(_ context.Context, _, task string, _ agent.Observer) (string, error) {
		return "answer for " + task, nil
	}))
	e.maxFinished = 1
	store := newMemRunStore()
	e.SetRunStore(store)

	first := e.StartRun("first")
	waitRunState(t, e, first, StateDone)
	// A second finished run pushes the first out of the in-memory map (cap = 1). Poll the
	// live map directly (lookup, not RunStatus — the latter now falls back to the store).
	second := e.StartRun("second")
	waitRunState(t, e, second, StateDone)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := e.lookup(first); errors.Is(err, ErrUnknownRun) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first run was not evicted from the live map")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The store saved it on completion, so RunStatus falls back to disk and returns it.
	if _, ok := store.Load(first); !ok {
		t.Fatal("run was not persisted to the store on completion")
	}
	info, err := e.RunStatus(first)
	if err != nil {
		t.Fatalf("RunStatus after eviction: %v", err)
	}
	if info.State != StateDone || info.Result != "answer for first" || info.Task != "first" {
		t.Fatalf("recovered run info wrong: %+v", info)
	}

	// A run neither live nor stored is still unknown.
	if _, err := e.RunStatus("deadbeef"); !errors.Is(err, ErrUnknownRun) {
		t.Fatalf("RunStatus(unknown) = %v, want ErrUnknownRun", err)
	}
}

// TestSetMaxFinishedRuns: a positive value tunes the retention cap; a non-positive value is
// ignored so the default stands.
func TestSetMaxFinishedRuns(t *testing.T) {
	e := NewEngine(RunnerFunc(fakeRunner))
	if e.maxFinished != maxFinishedRuns {
		t.Fatalf("default maxFinished = %d, want %d", e.maxFinished, maxFinishedRuns)
	}
	e.SetMaxFinishedRuns(5)
	if e.maxFinished != 5 {
		t.Fatalf("after Set(5), maxFinished = %d, want 5", e.maxFinished)
	}
	e.SetMaxFinishedRuns(0)
	if e.maxFinished != 5 {
		t.Fatalf("Set(0) should be ignored; maxFinished = %d, want 5", e.maxFinished)
	}
}

// waitEvicted polls until runID is no longer known to the engine (eviction happens on
// the finishing run's goroutine, so it is eventually consistent with run completion).
func waitEvicted(t *testing.T, e *Engine, runID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := e.RunStatus(runID); errors.Is(err, ErrUnknownRun) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run %s still present, want evicted", runID)
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
