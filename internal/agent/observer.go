package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"

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
	Stop       provider.StopReason // EvResponse
	DurationMs int64               // EvResponse
	SubAgent   string              // non-empty ⇒ emitted by a foreground sub-run of this type
	// Internal ⇒ background deliberation (the chat planner's planner/critic steps): kept in
	// the on-disk transcript and counted for usage, but NOT streamed to clients — a stream
	// sink (api.Hub) drops it so a frontend never sees the raw plan/verdict machinery, while
	// the transcript stays complete for debugging (chat-planner.md §0 surfacing vs. §8 audit).
	Internal bool
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

// internalizer stamps every forwarded event Internal (see Event.Internal), marking a
// background-deliberation agent's steps so a stream sink can drop them while the transcript
// and usage sinks keep them.
type internalizer struct{ inner Observer }

// Internalized wraps obs so every event it forwards is tagged Internal. Used for the chat
// planner's planner + critic, whose model steps belong in the transcript (and in usage) but
// not in the client-facing stream. Returns nil when there is no inner observer.
func Internalized(inner Observer) Observer {
	if inner == nil {
		return nil
	}
	return &internalizer{inner: inner}
}

func (o *internalizer) Emit(e Event) {
	e.Internal = true
	o.inner.Emit(e)
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
		calls := any(e.Calls)
		if e.Stop == provider.StopMaxTokens {
			// Do not retain a potentially huge/truncated argument string. Preserve enough
			// metadata to diagnose the capped call while keeping the transcript valid.
			metadata := make([]map[string]any, 0, len(e.Calls))
			for _, call := range e.Calls {
				metadata = append(metadata, map[string]any{
					"id": boundedMetadata(call.ID), "name": boundedMetadata(call.Name), "argument_bytes": len(call.Input),
					"valid_json": json.Valid([]byte(call.Input)),
				})
			}
			calls = metadata
		}
		l.log.LogResponse(e.Iteration, e.Text, calls, e.Stop, e.Usage, e.DurationMs)
		if e.Stop == provider.StopMaxTokens {
			l.log.LogError(e.Iteration, "model output limit reached")
		}
	case EvToolResult:
		l.log.LogToolResult(e.Call.Name, e.Call.ID, e.Call.Input, e.Result)
	}
}

func boundedMetadata(value string) string {
	runes := []rune(value)
	if len(runes) <= 256 {
		return value
	}
	return string(runes[:256]) + "…"
}

// UsageObserver accumulates token usage across a run's model responses so a caller
// can report a per-run (or, snapshotted, a per-turn) total. Every model reply carries
// its step Usage on an EvResponse event; this sums them. Safe for concurrent use.
type UsageObserver struct {
	mu        sync.Mutex
	total     provider.Usage
	steps     int
	lastInput int64
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
	if e.Usage.InputTokens > 0 {
		u.lastInput = e.Usage.InputTokens
	}
	u.mu.Unlock()
}

// LastInput returns the input-token count of the most recent model response — the number of
// tokens the model actually received that step (system prompt + full conversation + tool
// defs). It is the best proxy for current context fill: unlike Total (a sum across steps),
// it is the size of a single request, so `LastInput / context-window` is the fill fraction.
func (u *UsageObserver) LastInput() int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lastInput
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

// GatedObserver forwards events to an inner observer only while enabled. It backs
// chat's live /verbose toggle: the observer list is captured once (buildExecutor
// closes over it), so the CLI trace is switched on/off by flipping this gate rather
// than rebuilding the executor. Safe for concurrent use.
type GatedObserver struct {
	inner Observer
	on    atomic.Bool
}

// NewGatedObserver wraps inner, starting enabled iff on.
func NewGatedObserver(inner Observer, on bool) *GatedObserver {
	g := &GatedObserver{inner: inner}
	g.on.Store(on)
	return g
}

func (g *GatedObserver) Emit(e Event) {
	if g.on.Load() {
		g.inner.Emit(e)
	}
}

// SetEnabled turns forwarding on or off.
func (g *GatedObserver) SetEnabled(on bool) { g.on.Store(on) }

// Enabled reports whether events are currently forwarded.
func (g *GatedObserver) Enabled() bool { return g.on.Load() }

// ANSI styling for the trace. Grey (bright-black) so the whole intermediate trace
// visually recedes behind the final answer, which the command prints in the default
// colour. Emitted only when the sink is a real terminal (see colorEnabled).
const (
	ansiGrey  = "\x1b[90m"
	ansiReset = "\x1b[0m"
)

// CLIObserver prints the human-facing trace (the old verbose output) to a writer. The
// model's intermediate "thinking" (the preamble it writes before a tool call) is wrapped
// in a bounded, dimmed block so it is clearly separable from the final answer — the trace
// is the agent's work-in-progress, not its output.
type CLIObserver struct {
	w     io.Writer
	color bool
}

func NewCLIObserver(w io.Writer) *CLIObserver { return &CLIObserver{w: w, color: colorEnabled(w)} }

func (c *CLIObserver) Emit(e Event) {
	switch e.Kind {
	case EvResponse:
		// Print response text only when it precedes a tool call (the "explain what
		// I'm about to do" preamble). A response with no tool calls is the final
		// answer, which the command prints itself — printing it here too would
		// duplicate it.
		if e.Text != "" && len(e.Calls) > 0 {
			c.thinking(e)
		}
	case EvToolStart:
		c.line(fmt.Sprintf("%s[tool: %s] %s", subAgentPrefix(e), e.Call.Name, e.Call.Input))
	case EvToolResult:
		c.line(fmt.Sprintf("%s[result] %s", subAgentPrefix(e), e.Result))
	}
}

// thinking renders the model's preamble as a bounded, dimmed block so it reads as
// internal reasoning rather than the answer:
//
//	╭─ thinking ─
//	│ <text…>
//	╰─
func (c *CLIObserver) thinking(e Event) {
	prefix := subAgentPrefix(e)
	fmt.Fprintln(c.w)
	c.line(prefix + "╭─ thinking ─")
	for ln := range strings.SplitSeq(strings.TrimRight(e.Text, "\n"), "\n") {
		c.line(prefix + "│ " + ln)
	}
	c.line(prefix + "╰─")
}

// line writes one trace line, dimmed to grey when the sink supports colour.
func (c *CLIObserver) line(s string) {
	if c.color {
		fmt.Fprintf(c.w, "%s%s%s\n", ansiGrey, s, ansiReset)
		return
	}
	fmt.Fprintln(c.w, s)
}

// colorEnabled reports whether ANSI styling should be emitted to w: only when w is a
// real terminal (a char device) and the environment does not opt out (NO_COLOR, or a
// dumb TERM). Non-terminal sinks (a pipe, a file, a test buffer) get plain text, so
// captured/redirected output stays clean and tests are unaffected.
func colorEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := w.(interface{ Stat() (os.FileInfo, error) })
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// subAgentPrefix indents and labels a sub-run's line so a spawned agent's activity is
// visually distinct from the coordinator's. Empty for coordinator-level events.
func subAgentPrefix(e Event) string {
	if e.SubAgent == "" {
		return ""
	}
	return "  ↳ " + e.SubAgent + " "
}
