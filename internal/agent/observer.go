package agent

import (
	"fmt"
	"io"
	"sync"

	"ai-agent-go-play/internal/logger"
	"ai-agent-go-play/internal/provider"
)

// EventKind tags a run event emitted by the engine loop.
type EventKind string

const (
	EvStart      EventKind = "start"       // run began
	EvRequest    EventKind = "request"     // about to call the model
	EvResponse   EventKind = "response"    // model replied (text + tool calls)
	EvToolStart  EventKind = "tool_start"  // about to run a tool
	EvToolResult EventKind = "tool_result" // a tool finished
)

// Event is one observable moment in a run. Which fields are set depends on Kind.
// It is the single thing the engine emits, so any consumer — the disk logger,
// the CLI, or a future API stream — sees the same events.
type Event struct {
	Kind       EventKind
	Iteration  int
	Task       string              // EvStart
	Messages   []provider.Message  // EvRequest
	Text       string              // EvResponse
	Calls      []provider.ToolCall // EvResponse
	Call       *provider.ToolCall  // EvToolStart / EvToolResult
	Result     string              // EvToolResult
	IsError    bool                // EvToolResult
	Usage      provider.Usage      // EvResponse
	DurationMs int64               // EvResponse
	SubAgent   string              // non-empty ⇒ emitted by a foreground sub-run of this type
}

// subAgentLabeler forwards a sub-run's events to the parent's observer, stamping each
// with the sub-agent type name so a consumer (CLI/log) can attribute or indent the
// sub-run. Events already labelled are left as-is — the outermost spawn wins, which is
// all the v1 depth-1 topology (subagents.md §3) needs.
type subAgentLabeler struct {
	inner Observer
	name  string
}

// labelSubAgent wraps obs so every event it forwards is tagged with the sub-agent type.
// Returns nil when there is no inner observer (a nil sink stays nil).
func labelSubAgent(inner Observer, name string) Observer {
	if inner == nil {
		return nil
	}
	return &subAgentLabeler{inner: inner, name: name}
}

func (s *subAgentLabeler) Emit(e Event) {
	if e.SubAgent == "" {
		e.SubAgent = s.name
	}
	s.inner.Emit(e)
}

// Observer consumes run events. Implementations must tolerate being called from
// the loop goroutine and should not block.
type Observer interface {
	Emit(Event)
}

// Observers fans an event out to several observers (nil entries are skipped).
type Observers []Observer

func (os Observers) Emit(e Event) {
	for _, o := range os {
		if o != nil {
			o.Emit(e)
		}
	}
}

// LoggerObserver writes events to the structured run log on disk.
type LoggerObserver struct{ log *logger.Logger }

func NewLoggerObserver(log *logger.Logger) *LoggerObserver { return &LoggerObserver{log: log} }

func (l *LoggerObserver) Emit(e Event) {
	if l.log == nil {
		return
	}
	switch e.Kind {
	case EvStart:
		l.log.LogStart(e.Task)
	case EvRequest:
		l.log.LogRequest(e.Iteration, e.Messages)
	case EvResponse:
		l.log.LogResponse(e.Iteration, e.Text, e.Calls, e.Usage, e.DurationMs)
	case EvToolResult:
		l.log.LogToolResult(e.Call.Name, e.Call.ID, string(e.Call.Input), e.Result)
	}
}

// UsageObserver accumulates token usage across a run's model responses so a caller
// can report a per-run (or, snapshotted, a per-turn) total. Every model reply carries
// its step Usage on an EvResponse event; this sums them. Safe for concurrent use.
type UsageObserver struct {
	mu    sync.Mutex
	total provider.Usage
	steps int
}

// NewUsageObserver returns a zeroed accumulator.
func NewUsageObserver() *UsageObserver { return &UsageObserver{} }

func (u *UsageObserver) Emit(e Event) {
	if e.Kind != EvResponse {
		return
	}
	u.mu.Lock()
	u.total.InputTokens += e.Usage.InputTokens
	u.total.OutputTokens += e.Usage.OutputTokens
	u.total.CachedTokens += e.Usage.CachedTokens
	u.steps++
	u.mu.Unlock()
}

// Total returns the token usage accumulated so far.
func (u *UsageObserver) Total() provider.Usage {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.total
}

// Steps returns the number of model responses seen (one per loop iteration).
func (u *UsageObserver) Steps() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.steps
}

// CLIObserver prints the human-facing trace (the old verbose output) to a writer.
type CLIObserver struct{ w io.Writer }

func NewCLIObserver(w io.Writer) *CLIObserver { return &CLIObserver{w: w} }

func (c *CLIObserver) Emit(e Event) {
	switch e.Kind {
	case EvResponse:
		// Print response text only when it precedes a tool call (the "explain what
		// I'm about to do" preamble). A response with no tool calls is the final
		// answer, which the command prints itself — printing it here too would
		// duplicate it.
		if e.Text != "" && len(e.Calls) > 0 {
			fmt.Fprintf(c.w, "%s%s\n", subAgentPrefix(e), e.Text)
		}
	case EvToolStart:
		fmt.Fprintf(c.w, "\n%s[tool: %s] %s\n", subAgentPrefix(e), e.Call.Name, string(e.Call.Input))
	case EvToolResult:
		fmt.Fprintf(c.w, "%s[result] %s\n", subAgentPrefix(e), e.Result)
	}
}

// subAgentPrefix indents and labels a sub-run's line so a spawned agent's activity is
// visually distinct from the coordinator's. Empty for coordinator-level events.
func subAgentPrefix(e Event) string {
	if e.SubAgent == "" {
		return ""
	}
	return "  ↳ " + e.SubAgent + " "
}
