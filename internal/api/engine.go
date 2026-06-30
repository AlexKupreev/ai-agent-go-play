package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"ai-agent-go-play/internal/agent"
)

// ErrUnknownRun is returned when a run id is not found.
var ErrUnknownRun = errors.New("unknown run")

// Run states.
const (
	StateRunning = "running"
	StateDone    = "done"
	StateError   = "error"
)

// Runner executes a task to completion, emitting run events to obs. It abstracts
// the agent wiring (provider, registry, broker, executor) so the API core stays
// independent of how a run is assembled — cmd injects the real wiring, tests inject
// a fake.
type Runner interface {
	Run(ctx context.Context, task string, obs agent.Observer) (string, error)
}

// RunnerFunc adapts a plain function to Runner.
type RunnerFunc func(ctx context.Context, task string, obs agent.Observer) (string, error)

func (f RunnerFunc) Run(ctx context.Context, task string, obs agent.Observer) (string, error) {
	return f(ctx, task, obs)
}

// RunInfo is the metadata + status of a run, serializable for the management
// endpoints (GET /runs, GET /runs/{id}).
type RunInfo struct {
	ID        string     `json:"id"`
	Task      string     `json:"task"`
	State     string     `json:"state"` // running | done | error
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Result    string     `json:"result,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// run is the engine's live record of one run: its event hub, cancel handle (the
// kill switch), and mutable status.
type run struct {
	hub    *Hub
	cancel context.CancelFunc

	mu   sync.Mutex
	info RunInfo
}

func (r *run) snapshot() RunInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.info
}

// Engine is the transport-neutral core: it starts runs and lets adapters subscribe
// to a run's event stream. Both the SSE adapter (http.go) and any future JSON-RPC
// adapter drive the engine through this same surface.
type Engine struct {
	runner Runner

	mu   sync.Mutex
	runs map[string]*run
}

// NewEngine builds an engine over the given Runner.
func NewEngine(r Runner) *Engine {
	return &Engine{runner: r, runs: make(map[string]*run)}
}

// StartRun begins a run in the background and returns its id. Events flow to the
// run's Hub, which callers reach via Subscribe; the hub closes when the run ends.
//
// The run gets its own cancellable context, not the caller's: it outlives the
// request that started it (an HTTP handler's context is cancelled when the handler
// returns, which would abort the run mid-flight — e.g. while it waits for an
// approval). The stored cancel is the per-run kill switch (StopRun).
func (e *Engine) StartRun(task string) string {
	id := newRunID()
	hub := newHub()
	ctx, cancel := context.WithCancel(context.Background())

	r := &run{
		hub:    hub,
		cancel: cancel,
		info: RunInfo{
			ID:        id,
			Task:      task,
			State:     StateRunning,
			StartedAt: time.Now().UTC(),
		},
	}

	e.mu.Lock()
	e.runs[id] = r
	e.mu.Unlock()

	go func() {
		defer cancel() // release the run context once the run finishes
		result, err := e.runner.Run(ctx, task, hub)

		ended := time.Now().UTC()
		r.mu.Lock()
		r.info.EndedAt = &ended
		if err != nil {
			r.info.State = StateError
			r.info.Error = err.Error()
		} else {
			r.info.State = StateDone
			r.info.Result = result
		}
		r.mu.Unlock()

		if err != nil {
			hub.publish(Event{Kind: KindError, Text: err.Error()})
		} else {
			hub.publish(Event{Kind: KindDone, Text: result})
		}
		hub.Close()
	}()

	return id
}

// lookup returns a run by id, or ErrUnknownRun if it does not exist.
func (e *Engine) lookup(id string) (*run, error) {
	e.mu.Lock()
	r, ok := e.runs[id]
	e.mu.Unlock()
	if !ok {
		return nil, ErrUnknownRun
	}
	return r, nil
}

// Subscribe returns the event stream for a run (history replayed, then live) plus a
// cancel func to detach. ErrUnknownRun if the id is unknown.
func (e *Engine) Subscribe(id string) (<-chan Event, func(), error) {
	r, err := e.lookup(id)
	if err != nil {
		return nil, nil, err
	}
	ch, cancel := r.hub.Subscribe()
	return ch, cancel, nil
}

// StopRun cancels a run (the kill switch). Cancellation propagates through the run
// context to provider.Step/tools, so the run stops at the next model/tool boundary.
// ErrUnknownRun if the id is unknown. Idempotent.
func (e *Engine) StopRun(id string) error {
	r, err := e.lookup(id)
	if err != nil {
		return err
	}
	r.cancel()
	return nil
}

// ListRuns returns all runs, newest first.
func (e *Engine) ListRuns() []RunInfo {
	e.mu.Lock()
	out := make([]RunInfo, 0, len(e.runs))
	for _, r := range e.runs {
		out = append(out, r.snapshot())
	}
	e.mu.Unlock()
	// Newest first by start time.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].StartedAt.After(out[j-1].StartedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// RunStatus returns the metadata for a run. ErrUnknownRun otherwise.
func (e *Engine) RunStatus(id string) (RunInfo, error) {
	r, err := e.lookup(id)
	if err != nil {
		return RunInfo{}, err
	}
	return r.snapshot(), nil
}

func newRunID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
