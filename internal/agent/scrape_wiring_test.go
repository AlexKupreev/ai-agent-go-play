package agent

import (
	"testing"

	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/capability"
)

func hasTool(a *Agent, name string) bool {
	for _, t := range a.tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

// The scrape tool is registered from the same secret store authored tools reference by
// name, and only when the token is actually resolvable — the executor must not offer a paid
// tool that is guaranteed to fail.
func TestExecutor_ScrapeRegisteredOnlyWithSecret(t *testing.T) {
	base := func(secrets func(string) (string, bool)) ExecutorConfig {
		return ExecutorConfig{
			WorkDir: t.TempDir(),
			Audit:   &audit.MemoryRecorder{},
			Tier:    capability.TierBalanced,
			Secrets: secrets,
		}
	}

	if exec := NewExecutor(base(nil)); hasTool(exec, "scrape") {
		t.Error("no secret store: scrape should not be registered")
	}

	missing := func(string) (string, bool) { return "", false }
	if exec := NewExecutor(base(missing)); hasTool(exec, "scrape") {
		t.Error("secret store without a scrapingant token: scrape should not be registered")
	}

	present := func(n string) (string, bool) { return "tok", n == "scrapingant" }
	exec := NewExecutor(base(present))
	if !hasTool(exec, "scrape") {
		t.Error("stored scrapingant token: scrape should be registered")
	}
	// web_fetch stays available either way — scrape is the deliberate paid fallback, not a
	// replacement for the free path.
	if !hasTool(exec, "web_fetch") {
		t.Error("web_fetch should remain registered alongside scrape")
	}
}
