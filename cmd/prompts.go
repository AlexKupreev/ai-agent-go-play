package cmd

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/api"
	"ai-agent-go-play/internal/capability"
)

// promptState is a reloadable snapshot of the operator's prompt customization (SYSTEM.md /
// AGENTS.md) and the spawnable agent-type catalog (agents/*.md), keyed to a fixed workspace
// and tier. A long-lived `serve` reads the current snapshot when building each run's executor
// and re-reads the files on demand (POST /reload) so edits take effect without a restart. The
// lock lets a reload swap both values atomically while runs read them concurrently.
type promptState struct {
	workDir string
	tier    capability.Tier

	mu      sync.RWMutex
	prompts promptFiles
	catalog *agent.AgentCatalog
	agents  []api.AgentTypeSource
}

// newPromptState reads the files once; a load error (e.g. a malformed agents/*.md) fails
// startup rather than serving with a half-built catalog.
func newPromptState(workDir string, tier capability.Tier) (*promptState, error) {
	s := &promptState{workDir: workDir, tier: tier}
	if err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// reload re-reads both file tiers and swaps them in atomically. On error nothing changes,
// so a bad edit leaves the previously loaded prompts+catalog intact.
func (s *promptState) reload() error {
	prompts, err := loadPrompts(s.workDir, s.tier)
	if err != nil {
		return err
	}
	catalog, agents, err := loadAgentCatalogState(s.workDir, s.tier)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.prompts, s.catalog, s.agents = prompts, catalog, agents
	s.mu.Unlock()
	return nil
}

// snapshot returns the current prompts + catalog for one run to hold for its lifetime.
func (s *promptState) snapshot() (promptFiles, *agent.AgentCatalog) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.prompts, s.catalog
}

func (s *promptState) effectiveSnapshot() (promptFiles, []api.AgentTypeSource) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pf := s.prompts
	pf.Sources = append([]api.PromptSource(nil), pf.Sources...)
	pf.Warnings = append([]string(nil), pf.Warnings...)
	return pf, append([]api.AgentTypeSource(nil), s.agents...)
}

// noContextFilesFlag is bound to the persistent --no-context-files flag (see cmd/root.go).
// When set, no SYSTEM.md/AGENTS.md is loaded and the agent runs on the built-in base prompt
// (parity with pi's -nc; useful for reproducible runs and debugging).
var noContextFilesFlag bool

// contextFileFlag is bound to the persistent --context-file flag (repeatable). Each entry is
// an extra prompt file appended after both tiers. Because the operator names these explicitly,
// they are always loaded regardless of tier (workspace.md §5).
var contextFileFlag []string

// Context-file names loaded from a prompt tier (prompts.md §2). SYSTEM.md replaces the base
// prompt; AGENTS.md (alias CLAUDE.md) is appended as operator instructions.
const (
	systemPromptFile = "SYSTEM.md"
	agentsFile       = "AGENTS.md"
	agentsAliasFile  = "CLAUDE.md"
	// plannerPromptFile overrides the built-in planner prompt (the pre-execution
	// clarify/refine pass), so planner behavior is tunable without a rebuild and is
	// re-read on /reload like the other prompt files.
	plannerPromptFile = "PLANNER.md"
	// criticPromptFile overrides the built-in critic prompt (the chat --critique loop's
	// verdict pass), tunable without a rebuild like PLANNER.md. The Verdict schema is
	// enforced in code, so an override can retune the judging but not break the contract.
	criticPromptFile = "CRITIC.md"
)

// promptFiles carries the operator's prompt customization read from disk, ready to hand to
// agent.ExecutorConfig. Override (from SYSTEM.md) replaces the base prompt; Appends (from
// AGENTS.md/CLAUDE.md bodies) are concatenated after it.
type promptFiles struct {
	Override string
	Appends  []string
	// PlannerOverride is the operator's PLANNER.md (empty ⇒ built-in planner prompt). It
	// replaces the planner's base prompt, assembled with the same tier gate + project-wins
	// precedence as Override.
	PlannerOverride string
	// CriticOverride is the operator's CRITIC.md (empty ⇒ built-in critic prompt), for the
	// chat --critique loop. Same tier gate + project-wins precedence as PlannerOverride.
	CriticOverride string
	// Sources and Warnings are the secret-safe provenance view served by
	// GET /config/effective and returned after reload. Prompt bodies never leave cmd.
	Sources  []api.PromptSource
	Warnings []string
}

// loadPrompts assembles the two-tier prompt customization (prompts.md §2, workspace.md §3/§5):
// the config-dir tier (global, always trusted) and the workspace tier (project, tier-gated),
// project overriding global. Assembly:
//
//   - SYSTEM.md override: workspace wins outright over config-dir (replace ⇒ last writer).
//   - AGENTS.md appends: config-dir first, then workspace, concatenated (project has the last word).
//   - --context-file(s): appended last; named explicitly, so always loaded regardless of tier.
//
// The workspace tier is auto-loaded only above the safe tier — a `safe` agent must not let an
// untrusted checkout's AGENTS.md inject into its system prompt — unless the operator named the
// workspace explicitly (--workspace), which authorizes it. --no-context-files disables all file
// loading (both tiers and --context-file), for reproducible runs on the built-in base prompt.
func loadPrompts(workspace string, tier capability.Tier) (promptFiles, error) {
	if noContextFilesFlag {
		return promptFiles{}, nil
	}

	cfgDir, err := configDir()
	if err != nil {
		return promptFiles{}, err
	}
	pf, err := loadPromptTierFrom(cfgDir, "config") // config-dir (global) tier — always trusted
	if err != nil {
		return promptFiles{}, err
	}

	// Workspace (project) tier — tier-gated (workspace.md §5). Skipped when it would just
	// re-read the config dir (e.g. --workspace pointed at it).
	if loadWorkspaceTier(workspace, cfgDir, tier) {
		ws, err := loadPromptTierFrom(workspace, "workspace")
		if err != nil {
			return promptFiles{}, err
		}
		if ws.Override != "" {
			deactivatePromptMode(pf.Sources, "replace")
			pf.Override = ws.Override // project SYSTEM.md wins outright
		}
		if ws.PlannerOverride != "" {
			deactivatePromptName(pf.Sources, plannerPromptFile)
			pf.PlannerOverride = ws.PlannerOverride // project PLANNER.md wins outright
		}
		if ws.CriticOverride != "" {
			deactivatePromptName(pf.Sources, criticPromptFile)
			pf.CriticOverride = ws.CriticOverride // project CRITIC.md wins outright
		}
		pf.Appends = append(pf.Appends, ws.Appends...) // project appended after global
		pf.Sources = append(pf.Sources, ws.Sources...)
	}

	// Explicit --context-file(s): the user named them, so always honored (workspace.md §5).
	// Unlike tier files, a missing path is an error — it was requested by name.
	for _, path := range contextFileFlag {
		b, err := os.ReadFile(path)
		if err != nil {
			return promptFiles{}, fmt.Errorf("--context-file %q: %w", path, err)
		}
		if body := strings.TrimSpace(string(b)); body != "" {
			pf.Appends = append(pf.Appends, body)
			pf.Sources = append(pf.Sources, promptSource(path, "explicit", "append", b, true))
		}
	}
	pf.Warnings = promptWarnings(pf)

	return pf, nil
}

func deactivatePromptMode(sources []api.PromptSource, mode string) {
	for i := range sources {
		if sources[i].Mode == mode {
			sources[i].Active = false
		}
	}
}

func deactivatePromptName(sources []api.PromptSource, name string) {
	for i := range sources {
		if sources[i].Name == name {
			sources[i].Active = false
		}
	}
}

func promptWarnings(pf promptFiles) []string {
	if pf.Override != "" && !strings.Contains(pf.Override, "{{base}}") {
		return []string{"SYSTEM.md uses legacy replace semantics; add {{base}} to wrap the built-in base"}
	}
	return []string{}
}

// loadWorkspaceTier reports whether the workspace prompt tier should be auto-loaded. It is
// gated by trust (workspace.md §5): the safe tier does not auto-load an untrusted checkout's
// files, but an explicit --workspace authorizes them. It is also suppressed when the workspace
// resolves to the config dir, so those files are not loaded twice.
func loadWorkspaceTier(workspace, cfgDir string, tier capability.Tier) bool {
	if sameDir(workspace, cfgDir) {
		return false
	}
	if tier == capability.TierSafe && workspaceFlag == "" {
		return false
	}
	return true
}

// sameDir reports whether two paths refer to the same directory, comparing absolute, cleaned
// forms. Best-effort: if either path cannot be absolutized it is compared as given.
func sameDir(a, b string) bool {
	if abs, err := filepath.Abs(a); err == nil {
		a = abs
	}
	if abs, err := filepath.Abs(b); err == nil {
		b = abs
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// loadPromptTier reads SYSTEM.md and AGENTS.md/CLAUDE.md from a single directory. Absent
// files yield empty results (no error). When both AGENTS.md and CLAUDE.md exist in the same
// directory only one is loaded (AGENTS.md preferred), rather than silently concatenating two
// files that likely duplicate.
func loadPromptTier(dir string) (promptFiles, error) {
	return loadPromptTierFrom(dir, "unknown")
}

func loadPromptTierFrom(dir, layer string) (promptFiles, error) {
	var pf promptFiles

	override, source, err := readOptionalPrompt(filepath.Join(dir, systemPromptFile), layer, "replace")
	if err != nil {
		return promptFiles{}, err
	}
	pf.Override = override
	if source != nil {
		pf.Sources = append(pf.Sources, *source)
	}

	plannerOverride, source, err := readOptionalPrompt(filepath.Join(dir, plannerPromptFile), layer, "planner_override")
	if err != nil {
		return promptFiles{}, err
	}
	pf.PlannerOverride = plannerOverride
	if source != nil {
		pf.Sources = append(pf.Sources, *source)
	}

	criticOverride, source, err := readOptionalPrompt(filepath.Join(dir, criticPromptFile), layer, "critic_override")
	if err != nil {
		return promptFiles{}, err
	}
	pf.CriticOverride = criticOverride
	if source != nil {
		pf.Sources = append(pf.Sources, *source)
	}

	agents, source, err := readOptionalPrompt(filepath.Join(dir, agentsFile), layer, "append")
	if err != nil {
		return promptFiles{}, err
	}
	if agents == "" {
		if agents, source, err = readOptionalPrompt(filepath.Join(dir, agentsAliasFile), layer, "append"); err != nil {
			return promptFiles{}, err
		}
	}
	if agents != "" {
		pf.Appends = append(pf.Appends, agents)
		pf.Sources = append(pf.Sources, *source)
	}
	return pf, nil
}

func readOptionalPrompt(path, layer, mode string) (string, *api.PromptSource, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, nil
		}
		return "", nil, err
	}
	body := strings.TrimSpace(string(b))
	if body == "" {
		return "", nil, nil
	}
	source := promptSource(path, layer, mode, b, true)
	return body, &source, nil
}

func promptSource(path, layer, mode string, b []byte, active bool) api.PromptSource {
	digest := fmt.Sprintf("%x", sha256.Sum256(b))
	return api.PromptSource{
		Name: filepath.Base(path), Path: path, Layer: layer, Mode: mode,
		Bytes: int64(len(b)), Digest: digest[:12], Active: active,
	}
}
