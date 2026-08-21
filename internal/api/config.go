package api

import (
	"fmt"
	"net/http"
	"strings"
)

// EffectiveConfigService supplies a read-only snapshot of the configuration a running
// engine will apply to its next run. The cmd layer implements it because it owns flags,
// environment precedence, filesystem prompt discovery, and frontend wiring.
type EffectiveConfigService interface {
	EffectiveConfig() EffectiveConfig
}

// ConfigValue reports an effective scalar together with the layer that selected it.
type ConfigValue struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

// PromptSource identifies one live prompt input. Digest is a short SHA-256 fingerprint,
// useful for proving a reload changed bytes without returning the prompt body itself.
type PromptSource struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Layer  string `json:"layer"`
	Mode   string `json:"mode"`
	Bytes  int64  `json:"bytes"`
	Digest string `json:"digest"`
	Active bool   `json:"active"`
}

type PromptConfig struct {
	Composition string         `json:"composition"`
	Sources     []PromptSource `json:"sources"`
	Warnings    []string       `json:"warnings"`
}

// AgentTypeSource gives the winning provenance for one spawnable type. Built-in types use
// layer "built-in" and an empty path; file-defined types name their source file.
type AgentTypeSource struct {
	Name   string `json:"name"`
	Layer  string `json:"layer"`
	Path   string `json:"path,omitempty"`
	Digest string `json:"digest,omitempty"`
}

type AgentTypeConfig struct {
	Count   int               `json:"count"`
	Sources []AgentTypeSource `json:"sources"`
}

type EffectiveLimits struct {
	MaxIterations   int   `json:"max_iterations"`
	ScriptTimeoutS  int   `json:"script_timeout_seconds"`
	MaxInlineTools  int   `json:"max_inline_tools"`
	MaxHTTPBytes    int64 `json:"max_http_bytes"`
	MaxFinishedRuns int   `json:"max_finished_runs"`
	SpawnDepth      int   `json:"spawn_depth"`
	MaxRevisions    int   `json:"max_revisions"`
}

type FrontendConfig struct {
	TelegramConfigured bool `json:"telegram_configured"`
	Plan               bool `json:"plan"`
	Critique           bool `json:"critique"`
}

// GuidanceSource describes the engine-wide guidance layer. Space and session guidance are
// target-specific and remain inspectable through their own endpoints.
type GuidanceSource struct {
	Scope  string `json:"scope"`
	Path   string `json:"path"`
	Loaded bool   `json:"loaded"`
	Chars  int    `json:"chars"`
}

// EffectiveConfig is deliberately secret-safe: only configured secret names are exposed.
type EffectiveConfig struct {
	Model       ConfigValue      `json:"model"`
	TierCeiling ConfigValue      `json:"tier_ceiling"`
	Workspace   string           `json:"workspace"`
	Prompts     PromptConfig     `json:"prompts"`
	Guidance    []GuidanceSource `json:"guidance"`
	AgentTypes  AgentTypeConfig  `json:"agent_types"`
	Limits      EffectiveLimits  `json:"limits"`
	SecretNames []string         `json:"secret_names"`
	Frontends   FrontendConfig   `json:"frontends"`
}

// ReloadDiff is the structured POST /reload response shared by HTTP, CLI, and frontends.
type ReloadDiff struct {
	Changed    []string      `json:"changed"`
	Prompts    PromptConfig  `json:"prompts"`
	AgentTypes AgentTypeDiff `json:"agent_types"`
	Defaults   DefaultsDiff  `json:"defaults"`
}

type AgentTypeDiff struct {
	Count   int               `json:"count"`
	Added   []string          `json:"added"`
	Removed []string          `json:"removed"`
	Changed []string          `json:"changed"`
	Sources []AgentTypeSource `json:"sources"`
}

type ValueChange struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

type DefaultsDiff struct {
	Model ValueChange `json:"model"`
	Tier  ValueChange `json:"tier"`
}

// Summary renders a compact, deterministic confirmation suitable for a terminal or chat.
func (d ReloadDiff) Summary() string {
	if len(d.Changed) == 0 {
		return fmt.Sprintf("reload complete: no effective changes (%d agent types)", d.AgentTypes.Count)
	}
	return "reload complete: changed " + strings.Join(d.Changed, ", ")
}

func handleEffectiveConfig(service EffectiveConfigService) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, service.EffectiveConfig()) }
}
