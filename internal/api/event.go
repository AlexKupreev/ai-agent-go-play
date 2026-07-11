// Package api exposes the headless engine over a network transport. The package
// is split into a transport-neutral core (Engine + Hub, this file and engine.go)
// and transport adapters (http.go is the SSE adapter). A future JSON-RPC adapter
// attaches to the same core — see docs/api-transport.md.
package api

import (
	"strings"

	"ai-agent-go-play/internal/agent"
)

// Event is the wire form of a run event: a stable, transport-neutral schema that
// every adapter (SSE today, JSON-RPC later) serializes identically. It is decoupled
// from agent.Event on purpose, so the on-the-wire contract does not move when the
// engine's internal event struct gains fields.
type Event struct {
	Kind      string `json:"kind"`
	Iteration int    `json:"iteration,omitempty"`
	Task      string `json:"task,omitempty"`
	Text      string `json:"text,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Input     string `json:"input,omitempty"`
	Result    string `json:"result,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`

	// Human-gate fields. On an approval (KindApprovalRequested / KindApprovalResolved): a
	// requested event carries the id a frontend POSTs back to resolve, Tool = the action
	// category, Text = the human title, Input = the full detail; a resolved event carries
	// the decision in Approved. On a question (KindQuestionRequested / KindQuestionAnswered):
	// ApprovalID is the id to answer and Text = the prompt (requested) or the answer (answered).
	ApprovalID string `json:"approval_id,omitempty"`
	Approved   *bool  `json:"approved,omitempty"`
}

// fromAgentEvent projects an internal agent.Event onto the wire schema.
func fromAgentEvent(e agent.Event) Event {
	out := Event{
		Kind:      string(e.Kind),
		Iteration: e.Iteration,
		Task:      e.Task,
		Text:      e.Text,
		Result:    e.Result,
		IsError:   e.IsError,
	}
	if e.Call != nil {
		out.Tool = e.Call.Name
		out.Input = string(e.Call.Input)
	}
	return out
}

// Terminal event kinds emitted by the engine itself (not the loop). They mark the
// end of a run's stream so a client knows to stop reading.
const (
	KindDone  = "done"  // run finished; Text holds the final answer
	KindError = "error" // run failed; Text holds the error message
)

// KindBrief is a deliberate turn's surfaced brief (chat-planner.md §0): the clean, rendered
// plan the executor was seeded with, plus any critique-loop progress notes. It is published
// out-of-band (like approvals) via Engine.PublishToRun, so a frontend can render the
// deliberation distinctly instead of receiving the planner's raw structured output. Text
// holds the brief/note; a frontend may show or ignore it.
const KindBrief = "brief"

// SummarizeBrief reduces a KindBrief text to its one-line display form: the first content
// line (the refined task), with a leading "(label)" line — a critique revision marker —
// kept as a prefix. The full brief is deliberately chatty (context, success criteria,
// assumptions), so clients show it only on request (e.g. chat's /verbose); the run
// transcript always keeps the whole thing. Shared here so every frontend renders briefs
// the same way.
func SummarizeBrief(text string) string {
	label := ""
	rest := text
	for {
		line, tail, more := strings.Cut(rest, "\n")
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			// skip blank lines
		case label == "" && strings.HasPrefix(line, "(") && strings.HasSuffix(line, ")"):
			label = line + " "
		default:
			return label + line
		}
		if !more {
			return strings.TrimSpace(label)
		}
		rest = tail
	}
}

// Approval escalation event kinds. Emitted into a run's stream by the shared
// ApprovalQueue so a streaming frontend learns of a parked escalation (and its
// resolution) in the event stream it is already reading, rather than by polling.
const (
	KindApprovalRequested = "approval_requested"
	KindApprovalResolved  = "approval_resolved"
)

// Question event kinds — the ask_user half of the human-gate seam. A run pauses on a
// free-text question (requested), resolved when a frontend POSTs an answer (answered).
const (
	KindQuestionRequested = "question_requested"
	KindQuestionAnswered  = "question_answered"
)
