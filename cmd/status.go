package cmd

import (
	"ai-agent-go-play/internal/api"
	"ai-agent-go-play/internal/buildinfo"
	"ai-agent-go-play/internal/guidance"
	"ai-agent-go-play/internal/hoststat"
	"ai-agent-go-play/internal/space"
	"ai-agent-go-play/internal/statusview"
	"ai-agent-go-play/internal/tools"
)

// renderLocalStatus adapts the in-process chat state to the same structured
// snapshot used by GET /status. Local chat has no server-side session record, but
// its live model/tier/space and run id are equivalent session-scoped facts.
func renderLocalStatus(workDir, sessionID, defaultModel, defaultTier, model, tier, activeSpace, sessionGuidance string, plan, critique bool, spaces *space.Store) string {
	host := hoststat.Read(workDir)
	response := api.StatusResponse{
		Version: buildinfo.Version,
		Config: api.EffectiveConfig{
			Model:       api.ConfigValue{Value: defaultModel},
			TierCeiling: api.ConfigValue{Value: defaultTier},
			Workspace:   workDir,
			Frontends:   api.FrontendConfig{Plan: plan, Critique: critique},
		},
		Session: &api.StatusSession{
			ID: sessionID,
			Model: api.StatusValue{
				Requested: model,
				Effective: model,
			},
			Tier: api.StatusValue{
				Requested: tier,
				Effective: tier,
			},
			GuidanceChars: guidance.CharCount(sessionGuidance),
		},
		Host: api.StatusHost{
			CPUCount: host.NumCPU, Load1: host.Load1, Load5: host.Load5, Load15: host.Load15,
			MemoryTotalMB: host.MemTotalMB, MemoryAvailableMB: host.MemAvailMB,
			DiskTotalMB: host.DiskTotalMB, DiskFreeMB: host.DiskFreeMB,
			ProcessRSSMB: host.ProcRSSMB, Goroutines: host.Goroutines, GoHeapMB: host.GoHeapMB,
			HostUptimeSeconds: int64(host.HostUptime.Seconds()),
		},
		State: tools.SnapshotState(agentStateDirs(workDir)),
	}
	if activeSpace != "" {
		active := api.StatusSpace{ID: activeSpace, Name: activeSpace}
		if spaces != nil {
			if resolved, err := spaces.Get(activeSpace); err == nil {
				active.Name = resolved.Name
			}
		}
		response.Session.ActiveSpace = &active
	}
	return statusview.Render(response)
}
