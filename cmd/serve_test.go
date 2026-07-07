package cmd

import (
	"testing"

	"ai-agent-go-play/internal/api"
	"ai-agent-go-play/internal/capability"
)

// TestResolveOpts checks the serve-side per-request resolution: model falls back to the serve
// default, an explicit tier is clamped to no looser than the serve ceiling, and an invalid
// tier is rejected.
func TestResolveOpts(t *testing.T) {
	d := serveDeps{model: "gpt-4o-mini", tier: capability.TierBalanced}

	t.Run("empty opts inherit the serve defaults", func(t *testing.T) {
		model, tier, err := d.resolveOpts(api.RunOptions{})
		if err != nil || model != "gpt-4o-mini" || tier != capability.TierBalanced {
			t.Fatalf("got (%q, %q, %v), want the serve defaults", model, tier, err)
		}
	})
	t.Run("model override wins, tier inherits", func(t *testing.T) {
		model, tier, err := d.resolveOpts(api.RunOptions{Model: "gpt-4o"})
		if err != nil || model != "gpt-4o" || tier != capability.TierBalanced {
			t.Fatalf("got (%q, %q, %v), want gpt-4o / balanced", model, tier, err)
		}
	})
	t.Run("looser tier is clamped to the ceiling", func(t *testing.T) {
		_, tier, err := d.resolveOpts(api.RunOptions{Tier: "permissive"})
		if err != nil || tier != capability.TierBalanced {
			t.Fatalf("got (%q, %v), want it clamped to balanced", tier, err)
		}
	})
	t.Run("safer tier is honored", func(t *testing.T) {
		_, tier, err := d.resolveOpts(api.RunOptions{Tier: "safe"})
		if err != nil || tier != capability.TierSafe {
			t.Fatalf("got (%q, %v), want safe", tier, err)
		}
	})
	t.Run("invalid tier is rejected", func(t *testing.T) {
		if _, _, err := d.resolveOpts(api.RunOptions{Tier: "bogus"}); err == nil {
			t.Fatal("expected an error for an invalid requested tier")
		}
	})
}
