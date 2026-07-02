package cmd

import (
	"fmt"
	"strconv"
	"time"

	"ai-agent-go-play/internal/provider"
)

// formatUsage renders a one-line token-usage summary for the end of a run/turn, e.g.
//
//	· 12,431 in / 3,210 out (1,024 cached) · 4 steps · 6.2s
//
// The cached segment is shown only when non-zero. Tokens only — no cost (Phase 6a).
func formatUsage(u provider.Usage, steps int, elapsed time.Duration) string {
	cached := ""
	if u.CachedTokens > 0 {
		cached = fmt.Sprintf(" (%s cached)", humanInt(u.CachedTokens))
	}
	stepWord := "steps"
	if steps == 1 {
		stepWord = "step"
	}
	return fmt.Sprintf("· %s in / %s out%s · %d %s · %s",
		humanInt(u.InputTokens), humanInt(u.OutputTokens), cached,
		steps, stepWord, elapsed.Round(100*time.Millisecond))
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
