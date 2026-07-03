package api

import (
	"context"
	"fmt"
	"sync"

	"ai-agent-go-play/internal/tools"
)

// ApprovalQueue is a queue-backed tools.HumanGate: when the engine needs a human, it
// parks the request and blocks until a frontend resolves it (POST /approvals/{id}) or the
// run's context is cancelled. It serves both halves of the seam — Approve (a yes/no
// escalation) and Ask (a free-text question) — so a run can pause for either over any
// client. It is the async gate the HumanGate seam was built for.
type ApprovalQueue struct {
	mu      sync.Mutex
	pending map[string]*pendingItem
	seq     int
	emit    func(runID string, ev Event) // optional; pushes parked items onto a run's stream
}

// pendingItem is one parked human interaction. mode selects which resolution channel is
// live: an "approval" delivers a bool via approve, a "question" delivers text via answer.
type pendingItem struct {
	id      string
	mode    string // "approval" | "question"
	kind    string // category ("shell.destructive", "tool.capability", or "ask_user")
	title   string // approval title, or the question prompt
	detail  string // approval detail (empty for a question)
	runID   string
	approve chan bool   // set for mode == "approval"
	answer  chan string // set for mode == "question"
}

// PendingApproval is the wire form of a parked item (GET /approvals). Mode distinguishes a
// yes/no approval from a free-text question so a client renders the right prompt; for a
// question, Title holds the prompt and Detail is empty.
type PendingApproval struct {
	ID     string `json:"id"`
	Mode   string `json:"mode"` // "approval" | "question"
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	RunID  string `json:"run_id"`
}

func NewApprovalQueue() *ApprovalQueue {
	return &ApprovalQueue{pending: make(map[string]*pendingItem)}
}

// SetEmitter installs a hook that pushes parked items onto the owning run's event stream
// (typically Engine.PublishToRun), so streaming frontends see a parked request — and its
// resolution — without polling. Optional; nil leaves the queue poll-only via GET /approvals.
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

// park registers an item and returns it. Caller holds no lock.
func (q *ApprovalQueue) park(p *pendingItem) {
	q.mu.Lock()
	q.pending[p.id] = p
	q.mu.Unlock()
}

func (q *ApprovalQueue) unpark(id string) {
	q.mu.Lock()
	delete(q.pending, id)
	q.mu.Unlock()
}

// Approve implements tools.HumanGate. It registers the request, then blocks until Resolve
// delivers a decision or ctx is done (treated as not-approved, per the contract). The
// pending entry is always removed before returning.
func (q *ApprovalQueue) Approve(ctx context.Context, req tools.ApprovalRequest) (bool, error) {
	q.mu.Lock()
	q.seq++
	p := &pendingItem{
		id:      fmt.Sprintf("a%d", q.seq),
		mode:    "approval",
		kind:    req.Kind,
		title:   req.Title,
		detail:  req.Detail,
		runID:   req.RunID,
		approve: make(chan bool, 1),
	}
	q.mu.Unlock()
	q.park(p)
	defer q.unpark(p.id)

	// Announce the parked escalation on the run's stream (best-effort; a nil emitter or
	// unknown run is a no-op). Reused fields: Tool=category, Text=title, Input=detail.
	if emit := q.emitter(); emit != nil {
		emit(req.RunID, Event{
			Kind:       KindApprovalRequested,
			ApprovalID: p.id,
			Tool:       req.Kind,
			Text:       req.Title,
			Input:      req.Detail,
		})
	}

	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case decision := <-p.approve:
		// Emit the resolution here, on the run's own goroutine, before it proceeds — so
		// approval_resolved is ordered ahead of any later run events (and the terminal
		// done that closes the hub). Emitting from Resolve instead would race the run
		// completing and could be dropped after the hub closed.
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

// Ask implements tools.HumanGate. It parks a free-text question, then blocks until Answer
// delivers the typed response or ctx is done (surfaced as an error to the model). The
// pending entry is always removed before returning.
func (q *ApprovalQueue) Ask(ctx context.Context, question tools.Question) (string, error) {
	q.mu.Lock()
	q.seq++
	p := &pendingItem{
		id:     fmt.Sprintf("q%d", q.seq),
		mode:   "question",
		kind:   "ask_user",
		title:  question.Prompt,
		runID:  question.RunID,
		answer: make(chan string, 1),
	}
	q.mu.Unlock()
	q.park(p)
	defer q.unpark(p.id)

	// Announce the parked question on the run's stream. Reused field: Text = the prompt.
	if emit := q.emitter(); emit != nil {
		emit(question.RunID, Event{
			Kind:       KindQuestionRequested,
			ApprovalID: p.id,
			Text:       question.Prompt,
		})
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case ans := <-p.answer:
		if emit := q.emitter(); emit != nil {
			emit(question.RunID, Event{
				Kind:       KindQuestionAnswered,
				ApprovalID: p.id,
				Text:       ans,
			})
		}
		return ans, nil
	}
}

// Pending returns a snapshot of the parked items for a frontend to render.
func (q *ApprovalQueue) Pending() []PendingApproval {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]PendingApproval, 0, len(q.pending))
	for _, p := range q.pending {
		out = append(out, PendingApproval{
			ID:     p.id,
			Mode:   p.mode,
			Kind:   p.kind,
			Title:  p.title,
			Detail: p.detail,
			RunID:  p.runID,
		})
	}
	return out
}

// Resolve delivers a yes/no decision to a parked approval. It errors if the id is unknown
// (already resolved, expired, or never existed) or names a question rather than an approval.
func (q *ApprovalQueue) Resolve(id string, approved bool) error {
	q.mu.Lock()
	p, ok := q.pending[id]
	q.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown approval: %s", id)
	}
	if p.mode != "approval" {
		return fmt.Errorf("%s is a question, not an approval — use an answer", id)
	}
	// Buffered channel + single delivery: a second Resolve for the same id finds it
	// already removed by Approve's defer, so it returns the unknown-id error.
	select {
	case p.approve <- approved:
		return nil
	default:
		return fmt.Errorf("approval already resolved: %s", id)
	}
}

// Answer delivers a free-text response to a parked question. It errors if the id is unknown
// or names an approval rather than a question.
func (q *ApprovalQueue) Answer(id, text string) error {
	q.mu.Lock()
	p, ok := q.pending[id]
	q.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown question: %s", id)
	}
	if p.mode != "question" {
		return fmt.Errorf("%s is an approval, not a question — use approved", id)
	}
	select {
	case p.answer <- text:
		return nil
	default:
		return fmt.Errorf("question already answered: %s", id)
	}
}
