package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/provider"
	"ai-agent-go-play/internal/usage"
)

// formatTokens renders "<in> in / <out> out (<cached> cached)" with thousands
// separators; the cached segment appears only when non-zero.
func formatTokens(u provider.Usage) string {
	s := fmt.Sprintf("%s in / %s out", humanInt(u.InputTokens), humanInt(u.OutputTokens))
	if u.CachedTokens > 0 {
		s += fmt.Sprintf(" (%s cached)", humanInt(u.CachedTokens))
	}
	return s
}

// formatUsage renders a one-line token-usage summary for the end of a run/turn, e.g.
//
//	· 12,431 in / 3,210 out (1,024 cached) · 4 steps · 6.2s
//
// Tokens only — no cost (Phase 6a).
func formatUsage(u provider.Usage, steps int, elapsed time.Duration) string {
	stepWord := "steps"
	if steps == 1 {
		stepWord = "step"
	}
	return fmt.Sprintf("· %s · %d %s · %s",
		formatTokens(u), steps, stepWord, elapsed.Round(100*time.Millisecond))
}

// formatContext renders the context-window fill for the end of a chat turn, e.g.
//
//	· context ~62,300 / 128,000 tokens (49%)
//
// used is the last request's input tokens (the current fill); limit is the model's window.
// Returns "" when there's nothing meaningful to show (no model call yet, or an unknown window
// with no usage), so the caller can skip the line.
func formatContext(used int64, limit int) string {
	switch {
	case limit > 0 && used > 0:
		return fmt.Sprintf("· context ~%s / %s tokens (%d%%)", humanInt(used), humanInt(int64(limit)), int(used*100/int64(limit)))
	case used > 0:
		return fmt.Sprintf("· context ~%s tokens (window size unknown)", humanInt(used))
	default:
		return ""
	}
}

// openCentralLedger opens the process-wide audit log for appending run_usage events and
// returns a ledger reading it, so day-wide totals include this process's runs.
func openCentralLedger() (*audit.JSONLRecorder, *usage.Ledger, error) {
	path, err := auditPath()
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, nil, err
	}
	rec, err := audit.NewJSONLRecorder(path)
	if err != nil {
		return nil, nil, err
	}
	return rec, usage.NewLedger(rec), nil
}

// openCentralAuditReader returns a read-only view of the process-wide audit log.
// It deliberately does not create the file: commands that only inspect history
// should treat a missing log as empty and leave the config directory untouched.
func openCentralAuditReader() (audit.Reader, error) {
	path, err := auditPath()
	if err != nil {
		return nil, err
	}
	return audit.NewJSONLReader(path), nil
}

// recordRunUsage appends a run_usage event (wraps usage.Record so run.go needn't import
// the usage package, whose name would clash with its local accumulator variable).
func recordRunUsage(rec audit.Recorder, runID, sessionID string, u provider.Usage, steps int) {
	usage.Record(rec, runID, sessionID, u, steps)
}

// subUsage returns b - a component-wise, for turning two cumulative snapshots into the
// delta a single turn spent (the chat REPL shares one accumulator across turns).
func subUsage(a, b provider.Usage) provider.Usage {
	return provider.Usage{
		InputTokens:  b.InputTokens - a.InputTokens,
		OutputTokens: b.OutputTokens - a.OutputTokens,
		CachedTokens: b.CachedTokens - a.CachedTokens,
	}
}

// humanInt formats n with thousands separators (e.g. 12431 -> "12,431").
func humanInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := ""
	if n < 0 {
		neg, s = "-", s[1:]
	}
	// Insert commas every three digits from the right.
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return neg + string(out)
}
