// Package statusview renders the structured management status response for
// human-facing interactive frontends. The API remains the source of truth; this
// package only chooses a compact, body-redacted presentation suitable for a REPL
// or Telegram message.
package statusview

import (
	"fmt"
	"strconv"
	"strings"

	"ai-agent-go-play/internal/api"
)

// Render returns a compact, deterministic view of an engine snapshot. A response
// without a session is an engine-only view; importantly, frontends can render it
// without creating a conversation merely to answer /status.
func Render(status api.StatusResponse) string {
	var b strings.Builder
	b.WriteString("Engine\n")
	fmt.Fprintf(&b, "  version: %s\n", fallback(status.Version, "dev"))
	fmt.Fprintf(&b, "  workspace: %s\n", fallback(status.Config.Workspace, "(not reported)"))
	fmt.Fprintf(&b, "  defaults: model %s, tier %s\n",
		fallback(status.Config.Model.Value, "(provider default)"),
		fallback(status.Config.TierCeiling.Value, "(not reported)"))
	fmt.Fprintf(&b, "  workflow: plan %s, critique %s\n",
		onOff(status.Config.Frontends.Plan), onOff(status.Config.Frontends.Critique))

	b.WriteString("Session\n")
	if status.Session == nil {
		b.WriteString("  none selected\n")
	} else {
		session := status.Session
		fmt.Fprintf(&b, "  id: %s\n", session.ID)
		fmt.Fprintf(&b, "  model: %s\n", renderValue(session.Model, status.Config.Model.Value))
		fmt.Fprintf(&b, "  tier: %s\n", renderValue(session.Tier, status.Config.TierCeiling.Value))
		if session.ActiveSpace == nil {
			b.WriteString("  space: global scope\n")
		} else if session.ActiveSpace.Name == "" || session.ActiveSpace.Name == session.ActiveSpace.ID {
			fmt.Fprintf(&b, "  space: %s\n", session.ActiveSpace.ID)
		} else {
			fmt.Fprintf(&b, "  space: %s (%q)\n", session.ActiveSpace.ID, session.ActiveSpace.Name)
		}
		fmt.Fprintf(&b, "  guidance: %d chars\n", session.GuidanceChars)
	}

	b.WriteString("Host\n")
	hostLines := 0
	if status.Host.CPUCount > 0 {
		fmt.Fprintf(&b, "  cpu/load: %d CPUs, %.2f / %.2f / %.2f\n",
			status.Host.CPUCount, status.Host.Load1, status.Host.Load5, status.Host.Load15)
		hostLines++
	}
	if status.Host.MemoryTotalMB > 0 {
		fmt.Fprintf(&b, "  memory: %s available / %s\n",
			humanBytes(int64(status.Host.MemoryAvailableMB)<<20), humanBytes(int64(status.Host.MemoryTotalMB)<<20))
		hostLines++
	}
	if status.Host.DiskTotalMB > 0 {
		fmt.Fprintf(&b, "  disk: %s free / %s\n",
			humanBytes(int64(status.Host.DiskFreeMB)<<20), humanBytes(int64(status.Host.DiskTotalMB)<<20))
		hostLines++
	}
	if status.Host.ProcessRSSMB > 0 || status.Host.Goroutines > 0 {
		fmt.Fprintf(&b, "  process: %s RSS, %d goroutines\n",
			humanBytes(int64(status.Host.ProcessRSSMB)<<20), status.Host.Goroutines)
		hostLines++
	}
	if hostLines == 0 {
		b.WriteString("  unavailable\n")
	}

	if len(status.State) > 0 {
		var entries int
		var bytes int64
		truncated := false
		for _, item := range status.State {
			entries += item.Entries
			bytes += item.Bytes
			truncated = truncated || item.Truncated
		}
		size := humanBytes(bytes)
		if truncated {
			size = "at least " + size
		}
		fmt.Fprintf(&b, "State\n  %d entries, %s across %d stores\n", entries, size, len(status.State))
	}

	return strings.TrimRight(b.String(), "\n")
}

func renderValue(value api.StatusValue, inherited string) string {
	effective := fallback(value.Effective, fallback(inherited, "(not reported)"))
	if value.Requested != "" && value.Requested != effective {
		return fmt.Sprintf("%s (requested %s)", effective, value.Requested)
	}
	return effective
}

func fallback(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func onOff(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func humanBytes(bytes int64) string {
	const unit = int64(1024)
	if bytes < unit {
		return strconv.FormatInt(bytes, 10) + " B"
	}
	div, exp := unit, 0
	for n := bytes / unit; n >= unit && exp < 3; n /= unit {
		div *= unit
		exp++
	}
	value := float64(bytes) / float64(div)
	return strings.TrimSuffix(fmt.Sprintf("%.1f", value), ".0") + " " + []string{"KiB", "MiB", "GiB", "TiB"}[exp]
}
