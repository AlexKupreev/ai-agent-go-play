package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ai-agent-go-play/internal/hoststat"
	"ai-agent-go-play/internal/memory"
)

// StateDir is one on-disk state location the status tool reports usage for. The cmd layer
// resolves and supplies these (config dir, runs dir, session store, …); internal/tools
// resolves no paths itself, so the package stays free of the config-dir layout.
type StateDir struct {
	Label string // human label, e.g. "transcripts (runs)"
	Path  string // absolute path to a file or directory
}

// StateUsage is the structured disk-usage view shared by the in-run status tool and
// the HTTP management status endpoint. Entries counts only immediate children while
// Bytes is recursive. Truncated marks the bounded walk's byte count as a lower bound.
type StateUsage struct {
	Label     string `json:"label"`
	Entries   int    `json:"entries"`
	Bytes     int64  `json:"bytes"`
	Truncated bool   `json:"truncated"`
}

// StatusSpace is the body-redacted identity of the active memory/guidance scope.
type StatusSpace struct {
	ID   string
	Name string
}

// StatusSource is one prompt or agent-type provenance entry. Status deliberately
// carries metadata only: prompt and guidance bodies never enter this structure.
type StatusSource struct {
	Name   string
	Path   string
	Layer  string
	Mode   string
	Active bool
}

type StatusLimits struct {
	MaxIterations           int
	ScriptTimeoutS          int
	MaxInlineTools          int
	MaxHTTPBytes            int64
	MaxFinishedRuns         int
	SpawnDepth              int
	MaxRevisions            int
	PlannerMaxOutputTokens  int64
	CriticMaxOutputTokens   int64
	ExecutorMaxOutputTokens int64
}

// StatusConfiguration describes the resolved configuration of this executor.
// It is separate from the API DTO to keep tools transport-neutral.
type StatusConfiguration struct {
	Workspace         string
	SessionID         string
	RequestedModel    string
	RequestedTier     string
	GuidanceChars     int
	ActiveSpace       *StatusSpace
	PromptComposition string
	PromptSources     []StatusSource
	AgentTypeCount    int
	AgentTypeSources  []StatusSource
	Plan              bool
	Critique          bool
	Limits            StatusLimits
}

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
	Config   StatusConfiguration

	// StateDirs are the agent's own on-disk state locations (sessions, transcripts,
	// scratch, catalog, memory, audit). Empty ⇒ the "State on disk" section is omitted.
	StateDirs []StateDir

	// Context, when set, reports the current context-window fill: used = the input tokens of
	// the most recent model response (0 before the first call), limit = the model's window in
	// tokens (0 ⇒ unknown). It is a func so the reading is live at call time. Nil ⇒ the
	// "Context" section is omitted (e.g. a bare status tool built without an agent).
	Context func() (used int64, limit int)
}

// maxStateWalkFiles bounds the disk-usage walk so a huge runs/ tree can't stall the status
// tool. Past it the reported size is a lower bound (marked with a leading '≥').
const maxStateWalkFiles = 200000

// NewStatusTool returns the `status` built-in: the agent's self-report — its identity
// (model, tier, run, build), how many authored tools and memory entries it has, and the
// host's live resources (CPU load, memory, disk, its own RSS, uptime). Read-only,
// trusted, and not exposed to the sandbox. It answers "how am I configured / how much
// headroom does this machine have" without the agent guessing or shelling out.
func NewStatusTool(deps StatusDeps) Tool {
	return Tool{
		Name: "status",
		Description: "Report your own status: your model, trust tier, run id, build version, resolved " +
			"configuration (workspace, active space, prompt and agent-type provenance, workflow, and limits); how many " +
			"authored tools and memory entries you have; how full your context window is (tokens used vs the " +
			"model's limit); the host machine's live resources (CPU count and load, memory, disk free, your " +
			"process RSS, and uptime); and how much disk your own state (sessions, run transcripts, scratch " +
			"cache, tool catalog, memory, audit log) is using. Use it to answer questions about your current " +
			"configuration, whether you are running low on context, how much headroom the machine has before " +
			"starting heavy work, or what is consuming your disk.",
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

			renderStatusConfiguration(&b, deps.Config)

			// Context-window fill: how full the conversation is, so the agent can decide to
			// summarize/wrap up before it runs out of room.
			if deps.Context != nil {
				used, limit := deps.Context()
				fmt.Fprintf(&b, "Context\n")
				switch {
				case limit > 0 && used > 0:
					fmt.Fprintf(&b, "  window: %s of %s tokens used (%d%%)\n",
						commaInt(used), commaInt(int64(limit)), int(used*100/int64(limit)))
				case limit > 0:
					fmt.Fprintf(&b, "  window: %s tokens (no model call yet this run)\n", commaInt(int64(limit)))
				case used > 0:
					fmt.Fprintf(&b, "  ~%s tokens in the last request (window size unknown for this model)\n", commaInt(used))
				default:
					fmt.Fprintf(&b, "  (no model call yet this run)\n")
				}
			}

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

			// Agent state on disk: what each store is consuming, so the agent can answer
			// "what's eating my disk?" and notice an unreaped runs/ tree.
			if line := stateOnDisk(deps.StateDirs); line != "" {
				b.WriteByte('\n')
				b.WriteString(line)
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	}
}

func renderStatusConfiguration(b *strings.Builder, cfg StatusConfiguration) {
	if cfg.Workspace == "" && cfg.SessionID == "" && cfg.ActiveSpace == nil &&
		cfg.PromptComposition == "" && cfg.AgentTypeCount == 0 && cfg.Limits == (StatusLimits{}) {
		return
	}
	fmt.Fprintln(b, "Configuration")
	if cfg.Workspace != "" {
		fmt.Fprintf(b, "  workspace: %s\n", cfg.Workspace)
	}
	if cfg.SessionID != "" {
		fmt.Fprintf(b, "  session: %s", shortID(cfg.SessionID))
		if cfg.GuidanceChars > 0 {
			fmt.Fprintf(b, "   guidance: %d chars", cfg.GuidanceChars)
		}
		b.WriteByte('\n')
	}
	if cfg.ActiveSpace == nil {
		fmt.Fprintln(b, "  active space: (workspace-global)")
	} else {
		fmt.Fprintf(b, "  active space: %s (id: %s)\n", cfg.ActiveSpace.Name, cfg.ActiveSpace.ID)
	}
	if cfg.RequestedModel != "" || cfg.RequestedTier != "" {
		fmt.Fprintf(b, "  requested: model %s, tier %s\n", emptyDefault(cfg.RequestedModel), emptyDefault(cfg.RequestedTier))
	}
	if cfg.PromptComposition != "" {
		fmt.Fprintf(b, "  prompts: %s\n", cfg.PromptComposition)
	}
	for _, source := range cfg.PromptSources {
		state := "active"
		if !source.Active {
			state = "inactive"
		}
		fmt.Fprintf(b, "    %s: %s [%s, %s, %s]\n", source.Name, source.Path, source.Layer, source.Mode, state)
	}
	if cfg.AgentTypeCount > 0 || cfg.AgentTypeSources != nil {
		fmt.Fprintf(b, "  agent types: %d\n", cfg.AgentTypeCount)
		for _, source := range cfg.AgentTypeSources {
			if source.Path == "" {
				fmt.Fprintf(b, "    %s [%s]\n", source.Name, source.Layer)
			} else {
				fmt.Fprintf(b, "    %s: %s [%s]\n", source.Name, source.Path, source.Layer)
			}
		}
	}
	fmt.Fprintf(b, "  workflow: plan %t, critique %t, max revisions %d\n", cfg.Plan, cfg.Critique, cfg.Limits.MaxRevisions)
	if cfg.Limits != (StatusLimits{}) {
		fmt.Fprintf(b, "  limits: iterations %d, script %ds, inline tools %d, HTTP bytes %d, finished runs %d, spawn depth %d\n",
			cfg.Limits.MaxIterations, cfg.Limits.ScriptTimeoutS, cfg.Limits.MaxInlineTools,
			cfg.Limits.MaxHTTPBytes, cfg.Limits.MaxFinishedRuns, cfg.Limits.SpawnDepth)
		fmt.Fprintf(b, "    output tokens/call: planner %d, critic %d, executor/sub-agent %d\n",
			cfg.Limits.PlannerMaxOutputTokens, cfg.Limits.CriticMaxOutputTokens, cfg.Limits.ExecutorMaxOutputTokens)
	}
}

func emptyDefault(value string) string {
	if value == "" {
		return "(default)"
	}
	return value
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

// stateOnDisk renders the "State on disk" section: one line per present StateDir with its
// entry count and total bytes. Absent paths are skipped; an all-absent set yields "" (no
// dangling header). Best-effort — an unreadable dir contributes what it could read.
func stateOnDisk(dirs []StateDir) string {
	var b strings.Builder
	b.WriteString("State on disk\n")
	usage := SnapshotState(dirs)
	for _, item := range usage {
		size := humanBytes(item.Bytes)
		if item.Truncated {
			size = "≥" + size
		}
		if item.Entries > 0 {
			fmt.Fprintf(&b, "  %s: %d %s, %s\n", item.Label, item.Entries, itemWord(item.Entries), size)
		} else {
			fmt.Fprintf(&b, "  %s: %s\n", item.Label, size)
		}
	}
	if len(usage) == 0 {
		return ""
	}
	return strings.TrimRight(b.String(), "\n")
}

// SnapshotState measures each configured state path in order. Missing paths are
// omitted and an all-missing input returns a non-nil empty slice for stable JSON.
func SnapshotState(dirs []StateDir) []StateUsage {
	usage := make([]StateUsage, 0, len(dirs))
	for _, sd := range dirs {
		entries, bytes, exists, truncated := diskUsage(sd.Path)
		if !exists {
			continue
		}
		usage = append(usage, StateUsage{
			Label: sd.Label, Entries: entries, Bytes: bytes, Truncated: truncated,
		})
	}
	return usage
}

// diskUsage returns the immediate entry count and total (recursive) bytes at path. exists is
// false when the path is absent (skip it). For a regular file, entries is 0 and bytes is its
// size. truncated is true when the walk hit maxStateWalkFiles (bytes is then a lower bound).
func diskUsage(path string) (entries int, bytes int64, exists, truncated bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, false, false
	}
	if !info.IsDir() {
		return 0, info.Size(), true, false
	}
	if des, err := os.ReadDir(path); err == nil {
		entries = len(des)
	}
	seen := 0
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip an unreadable entry, keep going
		}
		if d.IsDir() {
			return nil
		}
		seen++
		if seen > maxStateWalkFiles {
			truncated = true
			return filepath.SkipAll
		}
		if fi, err := d.Info(); err == nil {
			bytes += fi.Size()
		}
		return nil
	})
	return entries, bytes, true, truncated
}

// commaInt formats n with thousands separators (e.g. 128000 -> "128,000").
func commaInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := ""
	if n < 0 {
		neg, s = "-", s[1:]
	}
	var out []byte
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	return neg + string(out)
}

// itemWord is the singular/plural of "item" for an entry count.
func itemWord(n int) string {
	if n == 1 {
		return "item"
	}
	return "items"
}

// humanBytes renders a byte count as B/KB/MB/GB… with one decimal above 1 KiB.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
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
