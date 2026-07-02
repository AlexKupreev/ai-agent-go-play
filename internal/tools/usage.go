package tools

import (
	"context"
	"fmt"
	"strings"

	"ai-agent-go-play/internal/provider"
)

// UsageLedger returns durable token-spend aggregates (see internal/usage). Session and
// Today each return the summed usage and the number of turns/runs.
type UsageLedger interface {
	Session(id string) (provider.Usage, int)
	Today() (provider.Usage, int)
}

// UsageContext is what the usage tool needs: the current session id (empty outside a
// session) and a ledger over the audit log. A nil Ledger means the tool is omitted.
type UsageContext struct {
	SessionID string
	Ledger    UsageLedger
}

// NewUsageTool returns the `usage` built-in: the agent's own token-spend so far, this
// session (across turns) and today (across all runs), summed from the audit log. Lets
// the agent reason about how much it is spending. Read-only, trusted, not sandbox-exposed.
func NewUsageTool(uc UsageContext) Tool {
	return Tool{
		Name: "usage",
		Description: "Report your own token spend: totals for this session (across turns) and for today " +
			"(across all runs), so you can reason about how much you are spending. Figures come from the " +
			"audit log; the current in-flight run is included only once it finishes.",
		Parameters: map[string]any{},
		Required:   []string{},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			var b strings.Builder
			b.WriteString("Token usage\n")
			if uc.SessionID != "" {
				u, turns := uc.Ledger.Session(uc.SessionID)
				fmt.Fprintf(&b, "  this session: %s (%d %s)\n", fmtUsage(u), turns, plural(turns, "turn"))
			}
			u, runs := uc.Ledger.Today()
			fmt.Fprintf(&b, "  today:        %s (%d %s)", fmtUsage(u), runs, plural(runs, "run"))
			return b.String(), nil
		},
	}
}

// fmtUsage renders "<in> in / <out> out (<cached> cached)" for the model (plain ints).
func fmtUsage(u provider.Usage) string {
	s := fmt.Sprintf("%d in / %d out", u.InputTokens, u.OutputTokens)
	if u.CachedTokens > 0 {
		s += fmt.Sprintf(" (%d cached)", u.CachedTokens)
	}
	return s
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
