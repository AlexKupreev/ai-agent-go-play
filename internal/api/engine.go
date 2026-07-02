package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/provider"
	"ai-agent-go-play/internal/session"
)

// ErrUnknownRun is returned when a run id is not found.
var ErrUnknownRun = errors.New("unknown run")

// ErrSessionsDisabled is returned by the session methods when no session store is
// wired (e.g. tests, or a serve without persistence).
var ErrSessionsDisabled = errors.New("sessions are not enabled")

// Run states.
const (
	StateRunning = "running"
	StateDone    = "done"
	StateError   = "error"
)

// Runner executes a task to completion, emitting run events to obs. It abstracts
// the agent wiring (provider, registry, broker, executor) so the API core stays
// independent of how a run is assembled — cmd injects the real wiring, tests inject
// a fake. runID is the engine's id for the run: the runner threads it into the
// executor so the session dir, event stream, audit records, and parked approvals all
// key off one id (which is what routes approval escalations back to this run's hub).
type Runner interface {
	Run(ctx context.Context, runID, task string, obs agent.Observer) (string, error)
}

// RunnerFunc adapts a plain function to Runner.
type RunnerFunc func(ctx context.Context, runID, task string, obs agent.Observer) (string, error)

func (f RunnerFunc) Run(ctx context.Context, runID, task string, obs agent.Observer) (string, error) {
	return f(ctx, runID, task, obs)
}

// TurnRunner runs one conversation turn: it builds an executor seeded with prior
// messages, runs text to a final answer while emitting events to obs, and returns the
// answer plus the updated history for the session layer to persist. It is the session
// analogue of Runner (a run whose executor carries prior context).
type TurnRunner interface {
	RunTurn(ctx context.Context, runID string, prior []provider.Message, text string, obs agent.Observer) (answer string, updated []provider.Message, err error)
}

// TurnRunnerFunc adapts a plain function to TurnRunner.
type TurnRunnerFunc func(ctx context.Context, runID string, prior []provider.Message, text string, obs agent.Observer) (string, []provider.Message, error)

func (f TurnRunnerFunc) RunTurn(ctx context.Context, runID string, prior []provider.Message, text string, obs agent.Observer) (string, []provider.Message, error) {
	return f(ctx, runID, prior, text, obs)
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

	// Token usage accumulated by the run's model calls, and the number of model
	// steps. Populated when the run ends (zero while running).
	Usage provider.Usage `json:"usage"`
	Steps int            `json:"steps,omitempty"`
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

	// Session support (optional; nil unless EnableSessions is called). turns runs a
	// turn seeded with prior history; sessions persists the conversation; sessLocks
	// serializes turns within one session so its history can't interleave.
	sessions    session.Store
	turns       TurnRunner
	sessLocksMu sync.Mutex
	sessLocks   map[string]*sync.Mutex

	// auditRec, when set, receives a run_usage event per completed run/turn so token
	// spend is reviewable through the same log as every other effect. Optional.
	auditRec audit.Recorder
}

// NewEngine builds an engine over the given Runner.
func NewEngine(r Runner) *Engine {
	return &Engine{runner: r, runs: make(map[string]*run), sessLocks: make(map[string]*sync.Mutex)}
}

// EnableSessions wires the persistent conversation layer: store holds histories and
// turns runs a turn seeded with prior context. Until called, the session methods
// return ErrSessionsDisabled and the /sessions endpoints are not served.
func (e *Engine) EnableSessions(store session.Store, turns TurnRunner) {
	e.sessions = store
	e.turns = turns
}

// SessionsEnabled reports whether the conversation layer is wired.
func (e *Engine) SessionsEnabled() bool { return e.sessions != nil && e.turns != nil }

// SetAuditRecorder wires a recorder that receives a run_usage event when each run (or
// session turn) completes. Optional — nil leaves usage in RunInfo only.
func (e *Engine) SetAuditRecorder(rec audit.Recorder) { e.auditRec = rec }

// StartRun begins a run in the background and returns its id. Events flow to the
// run's Hub, which callers reach via Subscribe; the hub closes when the run ends.
//
// The run gets its own cancellable context, not the caller's: it outlives the
// request that started it (an HTTP handler's context is cancelled when the handler
// returns, which would abort the run mid-flight — e.g. while it waits for an
// approval). The stored cancel is the per-run kill switch (StopRun).
func (e *Engine) StartRun(task string) string {
	return e.launch(task, func(ctx context.Context, id string, obs agent.Observer) (string, error) {
		return e.runner.Run(ctx, id, task, obs)
	})
}

// launch registers a run, executes work in the background, records the terminal state,
// and emits the terminal done/error event before closing the hub. It is the shared
// spine of both a plain run (StartRun) and a session turn (PostTurn).
func (e *Engine) launch(task string, work func(ctx context.Context, runID string, obs agent.Observer) (string, error)) string {
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

		// A per-run usage accumulator fanned alongside the hub, so token totals are
		// captured no matter which Runner/TurnRunner does the work.
		usageObs := agent.NewUsageObserver()
		result, err := work(ctx, id, agent.Observers{hub, usageObs})

		usage := usageObs.Total()
		steps := usageObs.Steps()
		ended := time.Now().UTC()
		r.mu.Lock()
		r.info.EndedAt = &ended
		r.info.Usage = usage
		r.info.Steps = steps
		if err != nil {
			r.info.State = StateError
			r.info.Error = err.Error()
		} else {
			r.info.State = StateDone
			r.info.Result = result
		}
		r.mu.Unlock()

		if e.auditRec != nil {
			e.auditRec.Record(audit.Event{
				Type: audit.EventRunUsage,
				Run:  id,
				Fields: map[string]any{
					"input_tokens":  usage.InputTokens,
					"output_tokens": usage.OutputTokens,
					"cached_tokens": usage.CachedTokens,
					"steps":         steps,
				},
			})
		}

		if err != nil {
			hub.publish(Event{Kind: KindError, Text: err.Error()})
		} else {
			hub.publish(Event{Kind: KindDone, Text: result})
		}
		hub.Close()
	}()

	return id
}

// StartSession creates a new persistent conversation and returns its id.
func (e *Engine) StartSession() (string, error) {
	if !e.SessionsEnabled() {
		return "", ErrSessionsDisabled
	}
	s, err := e.sessions.Create()
	if err != nil {
		return "", err
	}
	return s.ID, nil
}

// ListSessions returns session metadata, newest-updated first.
func (e *Engine) ListSessions() ([]session.Info, error) {
	if !e.SessionsEnabled() {
		return nil, ErrSessionsDisabled
	}
	return e.sessions.List()
}

// CloseSession terminates (deletes) a session. session.ErrNotFound if unknown.
func (e *Engine) CloseSession(id string) error {
	if !e.SessionsEnabled() {
		return ErrSessionsDisabled
	}
	return e.sessions.Delete(id)
}

// PostTurn runs one turn against a session: it starts a run (streamable via the usual
// run endpoints) that loads the session's history, runs text seeded with it, and
// persists the updated history. Turns within a session are serialized so the history
// can't interleave. Returns the run id, or session.ErrNotFound if the session is gone.
func (e *Engine) PostTurn(sessionID, text string) (string, error) {
	if !e.SessionsEnabled() {
		return "", ErrSessionsDisabled
	}
	// Fail fast if the session doesn't exist, before spinning up a run.
	if _, err := e.sessions.Get(sessionID); err != nil {
		return "", err
	}
	lock := e.sessionLock(sessionID)
	id := e.launch(text, func(ctx context.Context, runID string, obs agent.Observer) (string, error) {
		lock.Lock()
		defer lock.Unlock()

		sess, err := e.sessions.Get(sessionID)
		if err != nil {
			return "", err // closed between accept and execution
		}
		answer, updated, err := e.turns.RunTurn(ctx, runID, sess.Messages, text, obs)
		if err != nil {
			return "", err
		}
		sess.Messages = updated
		if err := e.sessions.Save(sess); err != nil {
			return "", fmt.Errorf("persist session: %w", err)
		}
		return answer, nil
	})
	return id, nil
}

// sessionLock returns the per-session turn lock, creating it on first use.
func (e *Engine) sessionLock(id string) *sync.Mutex {
	e.sessLocksMu.Lock()
	defer e.sessLocksMu.Unlock()
	lock := e.sessLocks[id]
	if lock == nil {
		lock = &sync.Mutex{}
		e.sessLocks[id] = lock
	}
	return lock
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

// PublishToRun broadcasts ev into a run's event stream (and its replay history).
// It is a no-op if the run is unknown or its hub has already closed. This lets
// out-of-band producers — notably the shared ApprovalQueue — surface events on the
// stream a frontend is already reading, instead of requiring a side poll.
func (e *Engine) PublishToRun(runID string, ev Event) {
	r, err := e.lookup(runID)
	if err != nil {
		return
	}
	r.hub.publish(ev)
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
