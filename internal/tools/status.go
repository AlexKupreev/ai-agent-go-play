package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ai-agent-go-play/internal/hoststat"
	"ai-agent-go-play/internal/memory"
)

// StatusDeps is what the status tool reports about the running agent. The host figures
// are read live at call time (see hoststat).
type StatusDeps struct {
	Model    string // resolved model ("" ⇒ provider default)
	Tier     string // trust tier
	RunID    string // current run/turn id
	Version  string // build version
	WorkDir  string // filesystem to report disk free for
	Registry Registry
	Memory   memory.Store // may be nil
}

// NewStatusTool returns the `status` built-in: the agent's self-report — its identity
// (model, tier, run, build), how many authored tools and memory entries it has, and the
// host's live resources (CPU load, memory, disk, its own RSS, uptime). Read-only,
// trusted, and not exposed to the sandbox. It answers "how am I configured / how much
// headroom does this machine have" without the agent guessing or shelling out.
func NewStatusTool(deps StatusDeps) Tool {
	return Tool{
		Name: "status",
		Description: "Report your own status: your model, trust tier, run id, and build version; how many " +
			"authored tools and memory entries you have; and the host machine's live resources (CPU count and " +
			"load, memory, disk free, your process RSS, and uptime). Use it to answer questions about your " +
			"current configuration or how much headroom the machine has before starting heavy work.",
		Parameters: map[string]any{},
		Required:   []string{},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			model := deps.Model
			if model == "" {
				model = "(provider default)"
			}
			nMem := 0
			if deps.Memory != nil {
				nMem = len(deps.Memory.List())
			}
			nTools := 0
			if deps.Registry != nil {
				nTools = len(deps.Registry.List(ScopeAny))
			}

			var b strings.Builder
			fmt.Fprintf(&b, "Agent\n")
			fmt.Fprintf(&b, "  model: %s   tier: %s   run: %s\n", model, deps.Tier, shortID(deps.RunID))
			fmt.Fprintf(&b, "  build: %s\n", deps.Version)
			fmt.Fprintf(&b, "  authored tools: %d   memory entries: %d\n", nTools, nMem)

			s := hoststat.Read(deps.WorkDir)
			fmt.Fprintf(&b, "Host\n")
			fmt.Fprintf(&b, "  cpu: %d cores", s.NumCPU)
			if s.Load1 > 0 || s.Load5 > 0 || s.Load15 > 0 {
				fmt.Fprintf(&b, ", load %.2f %.2f %.2f", s.Load1, s.Load5, s.Load15)
			}
			b.WriteByte('\n')
			if s.MemTotalMB > 0 {
				fmt.Fprintf(&b, "  mem: %d MB free of %d MB\n", s.MemAvailMB, s.MemTotalMB)
			}
			if s.DiskTotalMB > 0 {
				fmt.Fprintf(&b, "  disk: %d MB free of %d MB\n", s.DiskFreeMB, s.DiskTotalMB)
			}
			fmt.Fprintf(&b, "  process: RSS %d MB, %d goroutines, Go heap %d MB", s.ProcRSSMB, s.Goroutines, s.GoHeapMB)
			if s.HostUptime > 0 {
				fmt.Fprintf(&b, ", host up %s", humanDuration(s.HostUptime))
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}
}

// shortID trims a run id to its first 8 chars for a compact report; "(none)" if empty.
func shortID(id string) string {
	if id == "" {
		return "(none)"
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// humanDuration renders a coarse "3d 4h" / "5h 12m" / "8m" uptime.
func humanDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}
