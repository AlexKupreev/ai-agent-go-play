// Package api exposes the headless engine over a network transport. The package
// is split into a transport-neutral core (Engine + Hub, this file and engine.go)
// and transport adapters (http.go is the SSE adapter). A future JSON-RPC adapter
// attaches to the same core — see docs/api-transport.md.
package api

import "ai-agent-go-play/internal/agent"

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
