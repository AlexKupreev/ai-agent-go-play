package agent

import "strings"

// contextWindows maps known model ids (and id prefixes) to their context-window size in
// tokens. It is a best-effort convenience for the context-usage gauge; an unknown model
// returns 0 (fill is then reported without a percentage). Operators override or extend it
// via config (`context_limits`) for private, renamed, or newer endpoints — see the cmd
// layer's resolveContextLimit. Keep this list short and only for widely-used ids; the config
// map is the escape hatch, so this need not be exhaustive.
var contextWindows = map[string]int{
	"gpt-5.1":       400_000,
	"gpt-4.1":       1_000_000,
	"gpt-4o":        128_000,
	"gpt-4-turbo":   128_000,
	"gpt-4-32k":     32_768,
	"gpt-4":         8_192,
	"gpt-3.5-turbo": 16_385,
	"o1":            200_000,
	"o1-mini":       128_000,
	"o3":            200_000,
	"o3-mini":       200_000,
	"o4-mini":       200_000,
}

// ContextWindow returns the known context-window size in tokens for a model id, or 0 if
// unknown. It tries an exact match first, then the longest hyphen-delimited family prefix — so dated
// snapshots ("gpt-4o-2024-08-06") and sub-variants ("gpt-4.1-mini") resolve to their family
// ("gpt-4o" → 128k, "gpt-4.1" → 1M). Unknown ⇒ 0, which the callers render as "window
// unknown".
func ContextWindow(model string) int {
	if n, ok := contextWindows[model]; ok {
		return n
	}
	bestLen, best := 0, 0
	for k, n := range contextWindows {
		if len(k) > bestLen && strings.HasPrefix(model, k+"-") {
			bestLen, best = len(k), n
		}
	}
	return best
}
