package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/provider"
	"ai-agent-go-play/internal/session"
	"ai-agent-go-play/internal/usage"
)

// ErrUnknownRun is returned when a run id is not found.
var ErrUnknownRun = errors.New("unknown run")

// ErrSessionsDisabled is returned by the session methods when no session store is
// wired (e.g. tests, or a serve without persistence).
var ErrSessionsDisabled = errors.New("sessions are not enabled")

// ErrUnknownSpace classifies a rejected space on StartSession/UpdateSession so the transport
// can answer 400 rather than 500. Match it with errors.Is; the message itself comes from the
// SpaceResolver (it names the available spaces), so it is not this sentinel's text.
var ErrUnknownSpace = errors.New("unknown space")

// unknownSpace tags a resolver error as ErrUnknownSpace while keeping the resolver's wording
// as the whole message — wrapping with fmt.Errorf would prefix a redundant "unknown space:".
type unknownSpace struct{ err error }

func (u unknownSpace) Error() string        { return u.err.Error() }
func (u unknownSpace) Unwrap() error        { return u.err }
func (u unknownSpace) Is(target error) bool { return target == ErrUnknownSpace }

// checkSpace validates a requested space and returns the id to store. "" is the global scope
// and always valid; with no resolver wired the value is stored verbatim.
func (e *Engine) checkSpace(requested string) (string, error) {
	if requested == "" || e.resolveSpace == nil {
		return requested, nil
	}
	id, err := e.resolveSpace(requested)
	if err != nil {
		return "", unknownSpace{err}
	}
	return id, nil
}

// Run states.
const (
	StateRunning = "running"
	StateDone    = "done"
	StateError   = "error"
)

// maxFinishedRuns bounds how many finished runs the engine retains in memory (newest
// kept). A long-lived serve turns every session turn into a run; without eviction the
// runs map — and each run's replayable event history — grows for the life of the
// process. An evicted run disappears from GET /runs and its event stream can no longer
// be replayed; the per-run transcript on disk keeps the durable record. Running runs
// are never evicted.
const maxFinishedRuns = 100

// Runner executes a task to completion, emitting run events to obs. It abstracts
// the agent wiring (provider, registry, broker, executor) so the API core stays
// independent of how a run is assembled — cmd injects the real wiring, tests inject
// a fake. runID is the engine's id for the run: the runner threads it into the
// executor so the session dir, event stream, audit records, and parked approvals all
// key off one id (which is what routes approval escalations back to this run's hub).
type Runner interface {
	Run(ctx context.Context, runID, task string, opts RunOptions, obs agent.Observer) (string, error)
}

// RunnerFunc adapts a plain function to Runner.
type RunnerFunc func(ctx context.Context, runID, task string, opts RunOptions, obs agent.Observer) (string, error)

func (f RunnerFunc) Run(ctx context.Context, runID, task string, opts RunOptions, obs agent.Observer) (string, error) {
	return f(ctx, runID, task, opts, obs)
}

// TurnRunner runs one conversation turn: it builds an executor seeded with prior
// messages, runs text to a final answer while emitting events to obs, and returns the
// answer plus the updated history for the session layer to persist. It is the session
// analogue of Runner (a run whose executor carries prior context).
type TurnRunner interface {
	RunTurn(ctx context.Context, runID, sessionID string, prior []provider.Message, text string, opts RunOptions, obs agent.Observer) (answer string, updated []provider.Message, err error)
}

// TurnRunnerFunc adapts a plain function to TurnRunner.
type TurnRunnerFunc func(ctx context.Context, runID, sessionID string, prior []provider.Message, text string, opts RunOptions, obs agent.Observer) (string, []provider.Message, error)

func (f TurnRunnerFunc) RunTurn(ctx context.Context, runID, sessionID string, prior []provider.Message, text string, opts RunOptions, obs agent.Observer) (string, []provider.Message, error) {
	return f(ctx, runID, sessionID, prior, text, opts, obs)
}

// RunOptions carries optional per-request overrides from a caller. An empty field inherits
// the engine's configured default. The engine passes these straight through to the
// Runner/TurnRunner; resolving them — applying defaults and clamping the tier to the serve
// ceiling — is the cmd layer's job, so the engine core stays free of that policy. Grown by
// adding fields (e.g. per-role models later), never by widening the run/turn signatures.
type RunOptions struct {
	Model string `json:"model,omitempty"` // model for this run's agents; "" ⇒ engine default
	Tier  string `json:"tier,omitempty"`  // requested tier, clamped to ≤ the serve tier; "" ⇒ engine default
	Space string `json:"space,omitempty"` // active space id for this turn's memory scope + notes; "" ⇒ global
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
	// maxFinished is the finished-run retention cap (maxFinishedRuns; a field so
	// tests can tighten it).
	maxFinished int

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

	// onSessionClose, when set, fires (best-effort) after a session is archived or purged, so
	// the cmd layer can reap the session's scratch cache — the engine core knows nothing of
	// disk paths, so this is its seam to them. The purge flag distinguishes the two: a close
	// (archive) reap preserves user-provided files (the conversation is recoverable), while a
	// purge is an explicit whole-session deletion and takes everything. Optional.
	onSessionClose func(sessionID string, purge bool)

	// runStore, when set, persists a run's final RunInfo when it reaches a terminal state
	// (Save) and reloads it for a run the engine has since evicted (Load). It lets run
	// metadata survive eviction and restart without the core knowing where on disk it goes.
	// Optional.
	runStore RunStore

	// files, when set, stores user-uploaded files into a session's working area — the write
	// counterpart to onSessionClose's reap, and the core's other seam to disk. Optional; with
	// no store, POST /sessions/{id}/files is not served (see uploads.go).
	files FileStore

	// resolveSpace, when set, validates a session's requested space at create/update time
	// and returns the canonical id to store. The space registry is a directory on disk, which
	// the core knows nothing about, so this is a seam like onSessionClose/runStore fed from
	// cmd. Nil (tests, an embedder with no space store) skips validation and stores the
	// requested value verbatim, as before. Optional.
	resolveSpace SpaceResolver
}

// SpaceResolver maps a requested space — an id or a display name — to the canonical space
// id to store, or reports why it is unusable. The error is shown to whoever set the space
// (a /space command, an API caller), so it should name the spaces that would have worked.
// The empty string (the global scope) is always valid and never reaches a resolver.
type SpaceResolver func(nameOrID string) (string, error)

// RunStore persists finished-run metadata so it survives eviction (maxFinishedRuns) and a
// process restart. The cmd layer supplies one that writes info.json next to the run's
// transcript; the engine calls Save on completion and Load as a RunStatus fallback.
type RunStore interface {
	Save(RunInfo)
	Load(id string) (RunInfo, bool)
}

// NewEngine builds an engine over the given Runner.
func NewEngine(r Runner) *Engine {
	return &Engine{runner: r, runs: make(map[string]*run), sessLocks: make(map[string]*sync.Mutex), maxFinished: maxFinishedRuns}
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

// SetSessionCloseHook installs a best-effort callback fired after a session is archived
// (CloseSession, purge=false) or purged (PurgeSession, purge=true), so the cmd layer can reap
// that session's scratch cache — preserving user files on a close, taking everything on a
// purge. Optional.
func (e *Engine) SetSessionCloseHook(fn func(sessionID string, purge bool)) { e.onSessionClose = fn }

// SetRunStore installs a store that persists a run's final RunInfo on completion and serves
// it back as a RunStatus fallback once the run is evicted. Optional. See RunStore.
func (e *Engine) SetRunStore(rs RunStore) { e.runStore = rs }

// SetSpaceResolver installs the validator for a session's sticky space, so an unknown space
// is rejected by StartSession/UpdateSession instead of failing the next turn. Optional; see
// SpaceResolver.
func (e *Engine) SetSpaceResolver(fn SpaceResolver) { e.resolveSpace = fn }

// SetMaxFinishedRuns tunes how many finished runs the engine retains in memory (a positive n;
// non-positive is ignored, keeping the default). Lets a small box run a tighter cap without a
// rebuild. See maxFinishedRuns.
func (e *Engine) SetMaxFinishedRuns(n int) {
	if n > 0 {
		e.mu.Lock()
		e.maxFinished = n
		e.mu.Unlock()
	}
}

// StartRun begins a run in the background and returns its id. Events flow to the
// run's Hub, which callers reach via Subscribe; the hub closes when the run ends.
//
// The run gets its own cancellable context, not the caller's: it outlives the
// request that started it (an HTTP handler's context is cancelled when the handler
// returns, which would abort the run mid-flight — e.g. while it waits for an
// approval). The stored cancel is the per-run kill switch (StopRun).
func (e *Engine) StartRun(task string, opts RunOptions) string {
	return e.launch(task, "", func(ctx context.Context, id string, obs agent.Observer) (string, error) {
		return e.runner.Run(ctx, id, task, opts, obs)
	})
}

// launch registers a run, executes work in the background, records the terminal state,
// and emits the terminal done/error event before closing the hub. It is the shared
// spine of both a plain run (StartRun) and a session turn (PostTurn).
func (e *Engine) launch(task, sessionID string, work func(ctx context.Context, runID string, obs agent.Observer) (string, error)) string {
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

		total := usageObs.Total()
		steps := usageObs.Steps()
		ended := time.Now().UTC()
		r.mu.Lock()
		r.info.EndedAt = &ended
		r.info.Usage = total
		r.info.Steps = steps
		if err != nil {
			r.info.State = StateError
			r.info.Error = err.Error()
		} else {
			r.info.State = StateDone
			r.info.Result = result
		}
		r.mu.Unlock()

		usage.Record(e.auditRec, id, sessionID, total, steps)

		// Persist the final metadata before pruning — this very run may be the one evicted,
		// and the store is what lets its status survive eviction (and a restart).
		if e.runStore != nil {
			e.runStore.Save(r.snapshot())
		}

		if err != nil {
			hub.publish(Event{Kind: KindError, Text: err.Error()})
		} else {
			hub.publish(Event{Kind: KindDone, Text: result})
		}
		hub.Close()
		e.pruneFinished()
	}()

	return id
}

// StartSession creates a new persistent conversation and returns its id. opts carries an
// optional sticky model/tier/space for the session (empty fields inherit the engine default);
// a turn may still override them per-request. The stored tier is a request, clamped to the
// serve ceiling per turn (cmd resolveOpts), so validation of its syntax is the transport
// boundary's job — the engine core stores what it is given. The space is different: it is
// checked here against the wired SpaceResolver and stored canonicalized, so a typo is
// rejected at the door (ErrUnknownSpace) instead of failing the session's first turn.
func (e *Engine) StartSession(opts RunOptions) (string, error) {
	if !e.SessionsEnabled() {
		return "", ErrSessionsDisabled
	}
	spaceID, err := e.checkSpace(opts.Space)
	if err != nil {
		return "", err
	}
	s, err := e.sessions.Create()
	if err != nil {
		return "", err
	}
	if opts.Model != "" || opts.Tier != "" || spaceID != "" {
		s.Model = opts.Model
		s.Tier = opts.Tier
		s.Space = spaceID
		if err := e.sessions.Save(s); err != nil {
			return "", err
		}
	}
	return s.ID, nil
}

// UpdateSession changes a session's sticky model/tier/space. A nil pointer leaves that
// field unchanged; a non-nil pointer sets it (an empty string clears it back to the engine
// default). It returns the updated session Info. session.ErrNotFound if the id is unknown.
// Like StartSession, the engine stores the tier verbatim — clamping to the serve ceiling
// happens per turn — while the space is resolved through the wired SpaceResolver and stored
// as its canonical id, so `/space polsih` fails here (ErrUnknownSpace, naming the spaces that
// exist) rather than sticking and breaking every later turn.
func (e *Engine) UpdateSession(id string, model, tier, space *string) (session.Info, error) {
	if !e.SessionsEnabled() {
		return session.Info{}, ErrSessionsDisabled
	}
	spaceID := ""
	if space != nil {
		var err error
		if spaceID, err = e.checkSpace(*space); err != nil {
			return session.Info{}, err
		}
	}
	sess, err := e.sessions.Get(id)
	if err != nil {
		return session.Info{}, err
	}
	if model != nil {
		sess.Model = *model
	}
	if tier != nil {
		sess.Tier = *tier
	}
	if space != nil {
		sess.Space = spaceID
	}
	if err := e.sessions.Save(sess); err != nil {
		return session.Info{}, err
	}
	return sess.ToInfo(), nil
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
	if err := e.sessions.Delete(id); err != nil {
		return err
	}
	// Free the session's turn lock — ids are never reused, so without this the map
	// grows one mutex per session for the life of the process. An in-flight turn
	// holds its own pointer to the mutex and is unaffected; its re-Get of the
	// deleted session fails cleanly.
	e.sessLocksMu.Lock()
	delete(e.sessLocks, id)
	e.sessLocksMu.Unlock()
	// Reap the session's scratch cache (large, re-derivable artifacts we don't archive with
	// the conversation). Best-effort — the hook resolves the path and, on this close, keeps
	// any user-provided files while removing the re-derivable rest.
	if e.onSessionClose != nil {
		e.onSessionClose(id, false)
	}
	return nil
}

// PurgeSession irreversibly removes a session — live or archived — and reaps its scratch
// cache. It is the destructive counterpart to CloseSession (which only archives): there is
// no recovery, so nothing is preserved. session.ErrNotFound if neither a live nor an
// archived session with the id exists. The purge is audited (EventSessionPurged) when a
// recorder is wired, so destructive management shows up on the same review surface as every
// other effect.
func (e *Engine) PurgeSession(id string) error {
	if !e.SessionsEnabled() {
		return ErrSessionsDisabled
	}
	if err := e.sessions.Purge(id); err != nil {
		return err
	}
	// Free the turn lock (ids are never reused) and reap the scratch cache — the same
	// housekeeping CloseSession does, but with no archive to keep artifacts for.
	e.sessLocksMu.Lock()
	delete(e.sessLocks, id)
	e.sessLocksMu.Unlock()
	if e.onSessionClose != nil {
		e.onSessionClose(id, true)
	}
	if e.auditRec != nil {
		e.auditRec.Record(audit.Event{
			Type:   audit.EventSessionPurged,
			At:     time.Now().UTC(),
			Fields: map[string]any{"session": id},
		})
	}
	return nil
}

// RestoreSession moves an archived (closed) session back to the live set so it can be
// resumed, closing the loop CloseSession opens. session.ErrNotFound if no archived session
// with the id exists.
func (e *Engine) RestoreSession(id string) error {
	if !e.SessionsEnabled() {
		return ErrSessionsDisabled
	}
	return e.sessions.Restore(id)
}

// PostTurn runs one turn against a session: it starts a run (streamable via the usual
// run endpoints) that loads the session's history, runs text seeded with it, and
// persists the updated history. Turns within a session are serialized so the history
// can't interleave. Returns the run id, or session.ErrNotFound if the session is gone.
func (e *Engine) PostTurn(sessionID, text string, opts RunOptions) (string, error) {
	if !e.SessionsEnabled() {
		return "", ErrSessionsDisabled
	}
	// Fail fast if the session doesn't exist, before spinning up a run.
	if _, err := e.sessions.Get(sessionID); err != nil {
		return "", err
	}
	lock := e.sessionLock(sessionID)
	id := e.launch(text, sessionID, func(ctx context.Context, runID string, obs agent.Observer) (string, error) {
		lock.Lock()
		defer lock.Unlock()

		sess, err := e.sessions.Get(sessionID)
		if err != nil {
			return "", err // closed between accept and execution
		}
		// Merge the session's sticky model/tier under the empty turn fields: a per-turn
		// override wins, else the session-stored value, else (still empty) the engine
		// default applied downstream. The still-empty tier is clamped to the serve ceiling
		// by the runner (cmd resolveOpts), so a session tier is bounded like any override.
		turnOpts := opts
		if turnOpts.Model == "" {
			turnOpts.Model = sess.Model
		}
		if turnOpts.Tier == "" {
			turnOpts.Tier = sess.Tier
		}
		if turnOpts.Space == "" {
			turnOpts.Space = sess.Space
		}
		answer, updated, err := e.turns.RunTurn(ctx, runID, sessionID, sess.Messages, text, turnOpts, obs)
		if err != nil {
			return "", err
		}
		// Re-read the session before persisting the history: a tool may have changed the
		// sticky fields mid-turn (switch_space writes Space through the store), and saving
		// the pre-turn snapshot would silently revert that. The per-session lock is held
		// for the whole turn, so this read-modify-write cannot interleave with another turn.
		sess, err = e.sessions.Get(sessionID)
		if err != nil {
			return "", err // closed mid-turn
		}
		sess.Messages = updated
		if err := e.sessions.Save(sess); err != nil {
			return "", fmt.Errorf("persist session: %w", err)
		}
		return answer, nil
	})
	return id, nil
}

// pruneFinished evicts the oldest finished runs beyond the retention cap, called after
// each run completes. Only finished runs (EndedAt set) are candidates, so a run cannot
// be evicted mid-flight.
func (e *Engine) pruneFinished() {
	type ended struct {
		id string
		at time.Time
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var finished []ended
	for id, r := range e.runs {
		if info := r.snapshot(); info.EndedAt != nil {
			finished = append(finished, ended{id, *info.EndedAt})
		}
	}
	if len(finished) <= e.maxFinished {
		return
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].at.Before(finished[j].at) })
	for _, f := range finished[:len(finished)-e.maxFinished] {
		delete(e.runs, f.id)
	}
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

// RunStatus returns the metadata for a run. For a run the engine has evicted (or that
// predates this process), it falls back to the persisted RunInfo if a RunStore is wired, so
// a status query survives eviction and restart. Live SSE replay is not reconstructable this
// way (the event history is gone), so Subscribe still 404s for an evicted run. ErrUnknownRun
// if neither the live map nor the store has it.
func (e *Engine) RunStatus(id string) (RunInfo, error) {
	r, err := e.lookup(id)
	if err != nil {
		if e.runStore != nil {
			if info, ok := e.runStore.Load(id); ok {
				return info, nil
			}
		}
		return RunInfo{}, err
	}
	return r.snapshot(), nil
}

func newRunID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
