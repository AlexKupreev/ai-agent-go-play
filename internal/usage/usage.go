// Package usage derives token-spend aggregates from the audit log. Every completed run
// or turn emits a run_usage event (see internal/audit), so session-wide and day-wide
// totals are just sums over those persisted events — restart-safe and cross-session by
// construction, with no live accumulators to keep in sync. Only the in-flight current
// run isn't represented (its run_usage is written when it ends).
package usage

import (
	"encoding/json"
	"time"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/provider"
)

// Record appends a run_usage event summarizing a completed run or turn. sessionID may be
// empty (a plain run). It is the single writer of the event shape Ledger reads, so the
// engine (serve) and the one-shot CLI stay consistent. A nil recorder is a no-op.
func Record(rec audit.Recorder, runID, sessionID string, u provider.Usage, steps int) {
	if rec == nil {
		return
	}
	fields := map[string]any{
		"input_tokens":  u.InputTokens,
		"output_tokens": u.OutputTokens,
		"cached_tokens": u.CachedTokens,
		"steps":         steps,
	}
	if sessionID != "" {
		fields["session"] = sessionID // links a turn's usage to its session
	}
	rec.Record(audit.Event{Type: audit.EventRunUsage, Run: runID, Fields: fields})
}

// Ledger sums run_usage events from an audit reader.
type Ledger struct{ r audit.Reader }

// NewLedger returns a ledger over r (typically the process-wide audit log). A nil reader
// yields zero totals.
func NewLedger(r audit.Reader) *Ledger { return &Ledger{r: r} }

// Session returns the total token usage and turn count recorded for a session id.
func (l *Ledger) Session(id string) (provider.Usage, int) {
	return l.sum(func(e audit.Event) bool {
		s, _ := e.Fields["session"].(string)
		return s == id
	})
}

// Today returns the total token usage and run count since local midnight.
func (l *Ledger) Today() (provider.Usage, int) {
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return l.sum(func(e audit.Event) bool { return !e.At.Before(midnight) })
}

// sum totals the run_usage events passing keep.
func (l *Ledger) sum(keep func(audit.Event) bool) (provider.Usage, int) {
	if l == nil || l.r == nil {
		return provider.Usage{}, 0
	}
	events, err := l.r.Tail(0, audit.Filter{Type: audit.EventRunUsage})
	if err != nil {
		return provider.Usage{}, 0
	}
	var total provider.Usage
	n := 0
	for _, e := range events {
		if !keep(e) {
			continue
		}
		total.InputTokens += num(e.Fields["input_tokens"])
		total.OutputTokens += num(e.Fields["output_tokens"])
		total.CachedTokens += num(e.Fields["cached_tokens"])
		n++
	}
	return total, n
}

// num coerces an audit field to int64. Values survive a JSONL round-trip as float64
// (json), stay int64 in the in-memory recorder — handle both.
func num(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	default:
		return 0
	}
}
