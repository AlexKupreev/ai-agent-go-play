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
	d := serveDeps{defaults: newServeDefaults("gpt-4o-mini", capability.TierBalanced)}

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

// TestServeDefaultsReload proves resolveOpts reflects a live retune of the serve defaults (the
// POST /reload path): after set(), a new default model is inherited and the tier ceiling that
// clamps a per-request override moves with it.
func TestServeDefaultsReload(t *testing.T) {
	d := serveDeps{defaults: newServeDefaults("gpt-4o-mini", capability.TierBalanced)}

	// Before reload: a permissive request clamps to the balanced ceiling.
	if _, tier, _ := d.resolveOpts(api.RunOptions{Tier: "permissive"}); tier != capability.TierBalanced {
		t.Fatalf("pre-reload tier = %q, want clamped to balanced", tier)
	}

	// Retune the defaults, as POST /reload does after re-reading config.json.
	d.defaults.set("gpt-4o", capability.TierPermissive)

	model, tier, err := d.resolveOpts(api.RunOptions{})
	if err != nil || model != "gpt-4o" || tier != capability.TierPermissive {
		t.Fatalf("post-reload defaults = (%q, %q, %v), want gpt-4o / permissive", model, tier, err)
	}
	// The ceiling moved: a permissive request is now honored, not clamped.
	if _, tier, _ := d.resolveOpts(api.RunOptions{Tier: "permissive"}); tier != capability.TierPermissive {
		t.Fatalf("post-reload tier = %q, want permissive (ceiling raised)", tier)
	}
}
