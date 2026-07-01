package agent

import (
	"fmt"
	"io"

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
			fmt.Fprintln(c.w, e.Text)
		}
	case EvToolStart:
		fmt.Fprintf(c.w, "\n[tool: %s] %s\n", e.Call.Name, string(e.Call.Input))
	case EvToolResult:
		fmt.Fprintf(c.w, "[result] %s\n", e.Result)
	}
}
