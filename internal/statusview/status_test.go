package statusview

import (
	"strings"
	"testing"

	"ai-agent-go-play/internal/api"
	"ai-agent-go-play/internal/tools"
)

func TestRenderSessionStatus(t *testing.T) {
	got := Render(api.StatusResponse{
		Version: "v1.2.3",
		Config: api.EffectiveConfig{
			Model: api.ConfigValue{Value: "default-model"}, TierCeiling: api.ConfigValue{Value: "balanced"},
			Workspace: "/workspace", Frontends: api.FrontendConfig{Plan: true, Critique: true},
		},
		Session: &api.StatusSession{
			ID: "sess-1", Model: api.StatusValue{Effective: "custom-model"},
			Tier: api.StatusValue{Requested: "permissive", Effective: "balanced"}, GuidanceChars: 13,
			ActiveSpace: &api.StatusSpace{ID: "polish", Name: "Polish lessons"},
		},
		Host:  api.StatusHost{CPUCount: 4, Load1: .5, Load5: .25, Load15: .1, MemoryTotalMB: 8192, MemoryAvailableMB: 4096, DiskTotalMB: 102400, DiskFreeMB: 51200, ProcessRSSMB: 128, Goroutines: 12},
		State: []tools.StateUsage{{Label: "sessions", Entries: 2, Bytes: 1024}, {Label: "runs", Entries: 3, Bytes: 2048, Truncated: true}},
	})
	for _, want := range []string{
		"version: v1.2.3", "workspace: /workspace", "workflow: plan on, critique on",
		"id: sess-1", "model: custom-model", "tier: balanced (requested permissive)",
		`space: polish ("Polish lessons")`, "guidance: 13 chars", "memory: 4 GiB available / 8 GiB",
		"5 entries, at least 3 KiB across 2 stores",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status missing %q:\n%s", want, got)
		}
	}
}

func TestRenderEngineOnlyStatus(t *testing.T) {
	got := Render(api.StatusResponse{Config: api.EffectiveConfig{Model: api.ConfigValue{Value: "m"}}})
	for _, want := range []string{"version: dev", "Session\n  none selected", "Host\n  unavailable"} {
		if !strings.Contains(got, want) {
			t.Fatalf("engine status missing %q:\n%s", want, got)
		}
	}
}
