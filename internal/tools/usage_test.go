package tools

import (
	"context"
	"strings"
	"testing"

	"ai-agent-go-play/internal/provider"
)

type fakeLedger struct {
	sess, day   provider.Usage
	sessN, dayN int
}

func (f fakeLedger) Session(string) (provider.Usage, int) { return f.sess, f.sessN }
func (f fakeLedger) Today() (provider.Usage, int)         { return f.day, f.dayN }

func TestUsageTool(t *testing.T) {
	led := fakeLedger{
		sess:  provider.Usage{InputTokens: 22180, OutputTokens: 5340},
		sessN: 6,
		day:   provider.Usage{InputTokens: 118900, OutputTokens: 27400},
		dayN:  14,
	}

	// In a session: reports both this session and today.
	tool := NewUsageTool(UsageContext{SessionID: "s1", Ledger: led})
	out, _ := tool.Run(context.Background(), map[string]any{})
	for _, want := range []string{"this session:", "22180 in / 5340 out", "6 turns", "today:", "118900 in", "14 runs"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage output missing %q; got:\n%s", want, out)
		}
	}

	// Outside a session: today only, no session line.
	tool = NewUsageTool(UsageContext{SessionID: "", Ledger: led})
	out, _ = tool.Run(context.Background(), map[string]any{})
	if strings.Contains(out, "this session") {
		t.Errorf("no session id should omit the session line; got:\n%s", out)
	}
	if !strings.Contains(out, "today:") {
		t.Errorf("today line missing; got:\n%s", out)
	}
}

func TestPlural(t *testing.T) {
	if plural(1, "turn") != "turn" || plural(2, "turn") != "turns" || plural(0, "run") != "runs" {
		t.Fatal("plural mishandled")
	}
}
