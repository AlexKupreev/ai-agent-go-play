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
	Owner  string `json:"owner"`
}

func NewApprovalQueue() *ApprovalQueue {
	return &ApprovalQueue{pending: make(map[string]*pendingApproval)}
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

	defer func() {
		q.mu.Lock()
		delete(q.pending, p.id)
		q.mu.Unlock()
	}()

	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case decision := <-p.resolve:
		return decision, nil
	}
}

// Pending returns a snapshot of owner's parked requests for a frontend to render.
// A request is owner-scoped so one user never sees another user's escalation
// (Phase 4e session isolation).
func (q *ApprovalQueue) Pending(owner string) []PendingApproval {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]PendingApproval, 0, len(q.pending))
	for _, p := range q.pending {
		if p.req.Owner != owner {
			continue
		}
		out = append(out, PendingApproval{
			ID:     p.id,
			Kind:   p.req.Kind,
			Title:  p.req.Title,
			Detail: p.req.Detail,
			RunID:  p.req.RunID,
			Owner:  p.req.Owner,
		})
	}
	return out
}

// Resolve delivers a decision to one of owner's parked requests. It errors if the id
// is unknown to owner (already resolved, expired, never existed, or owned by someone
// else — not-owner collapses to unknown so a user can't probe others' requests).
func (q *ApprovalQueue) Resolve(owner, id string, approved bool) error {
	q.mu.Lock()
	p, ok := q.pending[id]
	q.mu.Unlock()
	if !ok || p.req.Owner != owner {
		return fmt.Errorf("unknown approval: %s", id)
	}
	// Buffered channel + single delivery: a second Resolve for the same id finds it
	// already removed by Approve's defer, so it returns the unknown-id error.
	select {
	case p.resolve <- approved:
		return nil
	default:
		return fmt.Errorf("approval already resolved: %s", id)
	}
}
