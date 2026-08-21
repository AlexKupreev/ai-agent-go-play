package cmd

import (
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/api"
	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/tools"
)

type effectiveConfigService struct {
	workspace    string
	prompts      *promptState
	defaults     *serveDefaults
	limits       agent.Limits
	configLimits ConfigLimits
	spawnDepth   int
	secretNames  []string
	guidancePath string
	telegram     bool
	plan         bool
	critique     bool
	maxRevisions int
}

func (s *effectiveConfigService) EffectiveConfig() api.EffectiveConfig {
	pf, agents := s.prompts.effectiveSnapshot()
	if pf.Sources == nil {
		pf.Sources = []api.PromptSource{}
	}
	if pf.Warnings == nil {
		pf.Warnings = []string{}
	}
	if agents == nil {
		agents = []api.AgentTypeSource{}
	}
	model, tier, modelSource, tierSource := s.defaults.effectiveSnapshot()
	limits := s.limits.Effective()
	maxFinished := s.configLimits.MaxFinishedRuns
	if maxFinished <= 0 {
		maxFinished = api.DefaultMaxFinishedRuns
	}
	guidanceText, _ := os.ReadFile(s.guidancePath)
	secretNames := append([]string(nil), s.secretNames...)
	if secretNames == nil {
		secretNames = []string{}
	}
	return api.EffectiveConfig{
		Model:       api.ConfigValue{Value: model, Source: modelSource},
		TierCeiling: api.ConfigValue{Value: string(tier), Source: tierSource},
		Workspace:   s.workspace,
		Prompts: api.PromptConfig{
			Composition: promptComposition(pf), Sources: pf.Sources, Warnings: pf.Warnings,
		},
		Guidance: []api.GuidanceSource{{
			Scope: "global", Path: s.guidancePath, Loaded: len(strings.TrimSpace(string(guidanceText))) > 0,
			Chars: utf8.RuneCountInString(strings.TrimSpace(string(guidanceText))),
		}},
		AgentTypes: api.AgentTypeConfig{Count: len(agents), Sources: agents},
		Limits: api.EffectiveLimits{
			MaxIterations: limits.MaxIterations, ScriptTimeoutS: int(limits.ScriptTimeout.Seconds()),
			MaxInlineTools:  limits.MaxInlineTools,
			MaxHTTPBytes:    capability.EffectiveMaxHTTPBytes(limits.MaxHTTPBytes),
			MaxFinishedRuns: maxFinished, SpawnDepth: s.spawnDepth, MaxRevisions: s.maxRevisions,
		},
		SecretNames: secretNames,
		Frontends:   api.FrontendConfig{TelegramConfigured: s.telegram, Plan: s.plan, Critique: s.critique},
	}
}

// toolStatusConfiguration projects the shared secret-safe effective snapshot onto
// the transport-neutral status-tool DTO. Bodies and secret values are absent from
// the source representation, so this projection cannot widen the disclosure surface.
func toolStatusConfiguration(config api.EffectiveConfig) tools.StatusConfiguration {
	prompts := make([]tools.StatusSource, 0, len(config.Prompts.Sources))
	for _, source := range config.Prompts.Sources {
		prompts = append(prompts, tools.StatusSource{
			Name: source.Name, Path: source.Path, Layer: source.Layer,
			Mode: source.Mode, Active: source.Active,
		})
	}
	agents := make([]tools.StatusSource, 0, len(config.AgentTypes.Sources))
	for _, source := range config.AgentTypes.Sources {
		agents = append(agents, tools.StatusSource{Name: source.Name, Path: source.Path, Layer: source.Layer, Active: true})
	}
	return tools.StatusConfiguration{
		Workspace: config.Workspace, PromptComposition: config.Prompts.Composition,
		PromptSources: prompts, AgentTypeCount: config.AgentTypes.Count, AgentTypeSources: agents,
		Plan: config.Frontends.Plan, Critique: config.Frontends.Critique,
		Limits: tools.StatusLimits{
			MaxIterations: config.Limits.MaxIterations, ScriptTimeoutS: config.Limits.ScriptTimeoutS,
			MaxInlineTools: config.Limits.MaxInlineTools, MaxHTTPBytes: config.Limits.MaxHTTPBytes,
			MaxFinishedRuns: config.Limits.MaxFinishedRuns, SpawnDepth: config.Limits.SpawnDepth,
			MaxRevisions: config.Limits.MaxRevisions,
		},
	}
}

func promptComposition(pf promptFiles) string {
	if pf.Override == "" {
		return "built-in base"
	}
	if strings.Contains(pf.Override, "{{base}}") {
		return "SYSTEM.md wraps built-in base via {{base}}"
	}
	return "SYSTEM.md replaces built-in base (kernel blocks retained)"
}

func modelConfigSource(flag string, cfg Config) string {
	if flag != "" {
		return "flag"
	}
	if strings.TrimSpace(os.Getenv(envModel)) != "" {
		return "environment"
	}
	if cfg.Model != "" {
		return "config"
	}
	return "built-in"
}

func tierConfigSource(flag string, cfg Config) string {
	if flag != "" {
		return "flag"
	}
	if strings.TrimSpace(os.Getenv(envTier)) != "" {
		return "environment"
	}
	if cfg.Tier != "" {
		return "config"
	}
	return "built-in"
}

func reloadDiff(before, after api.EffectiveConfig) api.ReloadDiff {
	d := api.ReloadDiff{
		Changed:    []string{},
		Prompts:    after.Prompts,
		AgentTypes: api.AgentTypeDiff{Count: after.AgentTypes.Count, Added: []string{}, Removed: []string{}, Changed: []string{}, Sources: after.AgentTypes.Sources},
		Defaults: api.DefaultsDiff{
			Model: api.ValueChange{Before: before.Model.Value, After: after.Model.Value},
			Tier:  api.ValueChange{Before: before.TierCeiling.Value, After: after.TierCeiling.Value},
		},
	}
	if before.Model != after.Model {
		d.Changed = append(d.Changed, "model")
	}
	if before.TierCeiling != after.TierCeiling {
		d.Changed = append(d.Changed, "tier")
	}

	oldPrompts := promptSourceMap(before.Prompts.Sources)
	newPrompts := promptSourceMap(after.Prompts.Sources)
	keys := unionKeys(oldPrompts, newPrompts)
	for _, key := range keys {
		if oldPrompts[key] != newPrompts[key] {
			d.Changed = append(d.Changed, key)
		}
	}

	oldAgents := agentSourceMap(before.AgentTypes.Sources)
	newAgents := agentSourceMap(after.AgentTypes.Sources)
	for _, name := range unionKeys(oldAgents, newAgents) {
		old, hadOld := oldAgents[name]
		new, hasNew := newAgents[name]
		switch {
		case !hadOld:
			d.AgentTypes.Added = append(d.AgentTypes.Added, name)
		case !hasNew:
			d.AgentTypes.Removed = append(d.AgentTypes.Removed, name)
		case old != new:
			d.AgentTypes.Changed = append(d.AgentTypes.Changed, name)
		}
	}
	if len(d.AgentTypes.Added)+len(d.AgentTypes.Removed)+len(d.AgentTypes.Changed) > 0 {
		d.Changed = append(d.Changed, "agent_types")
	}
	sort.Strings(d.Changed)
	return d
}

func promptSourceMap(sources []api.PromptSource) map[string]string {
	out := make(map[string]string, len(sources))
	for _, source := range sources {
		key := source.Layer + "/" + source.Name
		out[key] = source.Digest + ":" + source.Mode + ":" + boolString(source.Active)
	}
	return out
}

func agentSourceMap(sources []api.AgentTypeSource) map[string]string {
	out := make(map[string]string, len(sources))
	for _, source := range sources {
		out[source.Name] = source.Layer + ":" + source.Path + ":" + source.Digest
	}
	return out
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func unionKeys[V comparable](a, b map[string]V) []string {
	seen := make(map[string]bool, len(a)+len(b))
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
