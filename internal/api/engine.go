package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"

	"ai-agent-go-play/internal/agent"
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

// Engine is the transport-neutral core: it starts runs and lets adapters subscribe
// to a run's event stream. Both the SSE adapter (http.go) and any future JSON-RPC
// adapter drive the engine through this same surface.
type Engine struct {
	runner Runner

	mu   sync.Mutex
	runs map[string]*Hub
}

// NewEngine builds an engine over the given Runner.
func NewEngine(r Runner) *Engine {
	return &Engine{runner: r, runs: make(map[string]*Hub)}
}

// StartRun begins a run in the background and returns its id. Events flow to the
// run's Hub, which callers reach via Subscribe; the hub closes when the run ends.
//
// The run gets its own context, not the caller's: it outlives the request that
// started it (an HTTP handler's context is cancelled when the handler returns,
// which would abort the run mid-flight — e.g. while it waits for an approval).
func (e *Engine) StartRun(task string) string {
	id := newRunID()
	hub := newHub()

	e.mu.Lock()
	e.runs[id] = hub
	e.mu.Unlock()

	go func() {
		result, err := e.runner.Run(context.Background(), task, hub)
		if err != nil {
			hub.publish(Event{Kind: KindError, Text: err.Error()})
		} else {
			hub.publish(Event{Kind: KindDone, Text: result})
		}
		hub.Close()
	}()

	return id
}

// Subscribe returns the event stream for a run (history replayed, then live) plus a
// cancel func to detach. It errors if the run id is unknown.
func (e *Engine) Subscribe(id string) (<-chan Event, func(), error) {
	e.mu.Lock()
	hub, ok := e.runs[id]
	e.mu.Unlock()
	if !ok {
		return nil, nil, fmt.Errorf("unknown run: %s", id)
	}
	ch, cancel := hub.Subscribe()
	return ch, cancel, nil
}

func newRunID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
