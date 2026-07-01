package api

import (
	"context"
	"fmt"
	"sync"

	"ai-agent-go-play/internal/tools"
)

// ApprovalQueue is a queue-backed tools.Approver: when the engine asks to approve a
// risky action, Approve parks the request and blocks until a frontend resolves it
// (POST /approvals/{id}) or the run's context is cancelled. It is the async approver
// the Approver seam (Phase 4a) was built for — the same contract StdinApprover
// satisfies for the CLI.
type ApprovalQueue struct {
	mu      sync.Mutex
	pending map[string]*pendingApproval
	seq     int
	emit    func(runID string, ev Event) // optional; pushes escalations onto a run's stream
}

type pendingApproval struct {
	id      string
	req     tools.ApprovalRequest
	resolve chan bool
}

// PendingApproval is the wire form of a parked request (GET /approvals).
type PendingApproval struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	RunID  string `json:"run_id"`
}

func NewApprovalQueue() *ApprovalQueue {
	return &ApprovalQueue{pending: make(map[string]*pendingApproval)}
}

// SetEmitter installs a hook that pushes approval escalations onto the owning run's
// event stream (typically Engine.PublishToRun), so streaming frontends see a parked
// request — and its resolution — without polling. Optional; nil leaves the queue
// poll-only via GET /approvals.
func (q *ApprovalQueue) SetEmitter(emit func(runID string, ev Event)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.emit = emit
}

// emitter returns the current emit hook under lock (nil if unset).
func (q *ApprovalQueue) emitter() func(runID string, ev Event) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.emit
}

// Approve implements tools.Approver. It registers the request, then blocks until
// Resolve delivers a decision or ctx is done (treated as not-approved, per the
// Approver contract). The pending entry is always removed before returning.
func (q *ApprovalQueue) Approve(ctx context.Context, req tools.ApprovalRequest) (bool, error) {
	q.mu.Lock()
	q.seq++
	p := &pendingApproval{
		id:      fmt.Sprintf("a%d", q.seq),
		req:     req,
		resolve: make(chan bool, 1),
	}
	q.pending[p.id] = p
	q.mu.Unlock()

	// Announce the parked escalation on the run's stream (best-effort; a nil emitter
	// or unknown run is a no-op). Reused fields: Tool=category, Text=title, Input=detail.
	if emit := q.emitter(); emit != nil {
		emit(req.RunID, Event{
			Kind:       KindApprovalRequested,
			ApprovalID: p.id,
			Tool:       req.Kind,
			Text:       req.Title,
			Input:      req.Detail,
		})
	}

	defer func() {
		q.mu.Lock()
		delete(q.pending, p.id)
		q.mu.Unlock()
	}()

	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case decision := <-p.resolve:
		// Emit the resolution here, on the run's own goroutine, before it proceeds —
		// so approval_resolved is ordered ahead of any later run events (and the
		// terminal done that closes the hub). Emitting from Resolve instead would race
		// the run completing and could be dropped after the hub closed.
		if emit := q.emitter(); emit != nil {
			decided := decision
			emit(req.RunID, Event{
				Kind:       KindApprovalResolved,
				ApprovalID: p.id,
				Approved:   &decided,
			})
		}
		return decision, nil
	}
}

// Pending returns a snapshot of the parked requests for a frontend to render.
func (q *ApprovalQueue) Pending() []PendingApproval {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]PendingApproval, 0, len(q.pending))
	for _, p := range q.pending {
		out = append(out, PendingApproval{
			ID:     p.id,
			Kind:   p.req.Kind,
			Title:  p.req.Title,
			Detail: p.req.Detail,
			RunID:  p.req.RunID,
		})
	}
	return out
}

// Resolve delivers a decision to a parked request. It errors if the id is unknown
// (already resolved, expired, or never existed).
func (q *ApprovalQueue) Resolve(id string, approved bool) error {
	q.mu.Lock()
	p, ok := q.pending[id]
	q.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown approval: %s", id)
	}
	// Buffered channel + single delivery: a second Resolve for the same id finds it
	// already removed by Approve's defer, so it returns the unknown-id error. The
	// approval_resolved stream event is emitted by Approve (on the run's goroutine)
	// once it receives this decision, so it stays ordered ahead of the run's later
	// events.
	select {
	case p.resolve <- approved:
		return nil
	default:
		return fmt.Errorf("approval already resolved: %s", id)
	}
}
