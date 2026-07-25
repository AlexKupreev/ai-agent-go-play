package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/capability"

	"github.com/spf13/cobra"
)

// TestParseBool checks the friendly on/off spellings plus the empty/unknown miss.
func TestParseBool(t *testing.T) {
	for _, tc := range []struct {
		in      string
		wantVal bool
		wantOk  bool
	}{
		{"on", true, true}, {"ON", true, true}, {"true", true, true}, {"yes", true, true}, {"1", true, true},
		{"off", false, true}, {"false", false, true}, {"no", false, true}, {"0", false, true},
		{" on ", true, true}, {"", false, false}, {"maybe", false, false},
	} {
		val, ok := parseBool(tc.in)
		if val != tc.wantVal || ok != tc.wantOk {
			t.Errorf("parseBool(%q) = (%v, %v), want (%v, %v)", tc.in, val, ok, tc.wantVal, tc.wantOk)
		}
	}
}

// TestResolveVerbose checks the precedence: --quiet/--verbose flags > AI_AGENT_VERBOSE
// env > config default > false.
func TestResolveVerbose(t *testing.T) {
	// newCmd returns a command with verbose/quiet registered, optionally marking each as
	// explicitly set (as cobra does when the flag appears on the command line).
	newCmd := func(setVerbose, setQuiet bool) *cobra.Command {
		c := &cobra.Command{Use: "x", RunE: func(*cobra.Command, []string) error { return nil }}
		var v, q bool
		c.Flags().BoolVar(&v, "verbose", false, "")
		c.Flags().BoolVar(&q, "quiet", false, "")
		if setVerbose {
			_ = c.Flags().Set("verbose", "true")
		}
		if setQuiet {
			_ = c.Flags().Set("quiet", "true")
		}
		return c
	}

	t.Run("quiet flag wins over everything", func(t *testing.T) {
		t.Setenv(envVerbose, "on")
		if resolveVerbose(newCmd(true, true), Config{Verbose: true}) {
			t.Fatal("expected --quiet to force false")
		}
	})
	t.Run("verbose flag beats env and config", func(t *testing.T) {
		t.Setenv(envVerbose, "off")
		if !resolveVerbose(newCmd(true, false), Config{Verbose: false}) {
			t.Fatal("expected --verbose to force true")
		}
	})
	t.Run("env beats config", func(t *testing.T) {
		t.Setenv(envVerbose, "on")
		if !resolveVerbose(newCmd(false, false), Config{Verbose: false}) {
			t.Fatal("expected env to force true")
		}
	})
	t.Run("config used when no flag or env", func(t *testing.T) {
		t.Setenv(envVerbose, "")
		if !resolveVerbose(newCmd(false, false), Config{Verbose: true}) {
			t.Fatal("expected config default true")
		}
	})
	t.Run("false when nothing set", func(t *testing.T) {
		t.Setenv(envVerbose, "")
		if resolveVerbose(newCmd(false, false), Config{}) {
			t.Fatal("expected default false")
		}
	})
}

// TestResolveAddr checks that --addr resolves a configured engine alias to its address
// and passes any other value (a literal host:port) through unchanged.
func TestResolveAddr(t *testing.T) {
	orig := configDirFlag
	t.Cleanup(func() { configDirFlag = orig })
	configDirFlag = t.TempDir()
	t.Setenv(envConfigDir, "")

	if err := saveConfig(Config{Engines: map[string]string{"alex": "127.0.0.1:9001"}}); err != nil {
		t.Fatal(err)
	}

	if got := resolveAddr("alex"); got != "127.0.0.1:9001" {
		t.Fatalf("resolveAddr(alias) = %q, want 127.0.0.1:9001", got)
	}
	if got := resolveAddr("127.0.0.1:8080"); got != "127.0.0.1:8080" {
		t.Fatalf("resolveAddr(literal) = %q, want it passed through unchanged", got)
	}
	if got := resolveAddr("unknown"); got != "unknown" {
		t.Fatalf("resolveAddr(unknown alias) = %q, want it passed through unchanged", got)
	}
}

// TestResolveModel checks the precedence: --model flag > AI_AGENT_MODEL env > config value.
func TestResolveContextLimit(t *testing.T) {
	// Config override wins over the built-in table.
	if got := resolveContextLimit("gpt-4o", Config{ContextLimits: map[string]int{"gpt-4o": 999}}); got != 999 {
		t.Errorf("config override = %d, want 999", got)
	}
	// Override for a private/renamed model the table doesn't know.
	if got := resolveContextLimit("my-local-model", Config{ContextLimits: map[string]int{"my-local-model": 32000}}); got != 32000 {
		t.Errorf("private-model override = %d, want 32000", got)
	}
	// No override → built-in table.
	if got := resolveContextLimit("gpt-4o", Config{}); got != 128000 {
		t.Errorf("table fallback = %d, want 128000", got)
	}
	// Unknown model, no override → 0.
	if got := resolveContextLimit("unknown-xyz", Config{}); got != 0 {
		t.Errorf("unknown = %d, want 0", got)
	}
}

func TestResolveModel(t *testing.T) {
	t.Run("flag beats env and config", func(t *testing.T) {
		t.Setenv(envModel, "env-model")
		if got := resolveModel("flag-model", Config{Model: "cfg-model"}); got != "flag-model" {
			t.Fatalf("resolveModel = %q, want flag-model", got)
		}
	})
	t.Run("env beats config", func(t *testing.T) {
		t.Setenv(envModel, "env-model")
		if got := resolveModel("", Config{Model: "cfg-model"}); got != "env-model" {
			t.Fatalf("resolveModel = %q, want env-model", got)
		}
	})
	t.Run("config when neither flag nor env", func(t *testing.T) {
		t.Setenv(envModel, "")
		if got := resolveModel("", Config{Model: "cfg-model"}); got != "cfg-model" {
			t.Fatalf("resolveModel = %q, want cfg-model", got)
		}
	})
}

// TestResolveTier checks the precedence: --tier flag > AI_AGENT_TIER env > config > balanced,
// and that an invalid value from any source errors.
func TestResolveTier(t *testing.T) {
	t.Run("env beats config", func(t *testing.T) {
		t.Setenv(envTier, "safe")
		got, err := resolveTier("", Config{Tier: "permissive"})
		if err != nil || got != capability.TierSafe {
			t.Fatalf("resolveTier = (%q, %v), want (safe, nil)", got, err)
		}
	})
	t.Run("flag beats env", func(t *testing.T) {
		t.Setenv(envTier, "safe")
		got, err := resolveTier("permissive", Config{})
		if err != nil || got != capability.TierPermissive {
			t.Fatalf("resolveTier = (%q, %v), want (permissive, nil)", got, err)
		}
	})
	t.Run("default balanced when unset", func(t *testing.T) {
		t.Setenv(envTier, "")
		got, err := resolveTier("", Config{})
		if err != nil || got != capability.TierBalanced {
			t.Fatalf("resolveTier = (%q, %v), want (balanced, nil)", got, err)
		}
	})
	t.Run("invalid env errors", func(t *testing.T) {
		t.Setenv(envTier, "bogus")
		if _, err := resolveTier("", Config{}); err == nil {
			t.Fatal("expected an error for an invalid tier from env")
		}
	})
}

// TestResolveOpenAIBaseURL checks the precedence: AI_AGENT_OPENAI_BASE_URL env > config > "".
func TestResolveOpenAIBaseURL(t *testing.T) {
	t.Run("env beats config", func(t *testing.T) {
		t.Setenv(envOpenAIBaseURL, "http://env:1234/v1")
		if got := resolveOpenAIBaseURL(Config{OpenAIBaseURL: "http://cfg:1/v1"}); got != "http://env:1234/v1" {
			t.Fatalf("resolveOpenAIBaseURL = %q, want the env value", got)
		}
	})
	t.Run("config when env unset", func(t *testing.T) {
		t.Setenv(envOpenAIBaseURL, "")
		if got := resolveOpenAIBaseURL(Config{OpenAIBaseURL: "http://cfg:1/v1"}); got != "http://cfg:1/v1" {
			t.Fatalf("resolveOpenAIBaseURL = %q, want the config value", got)
		}
	})
	t.Run("empty when neither set", func(t *testing.T) {
		t.Setenv(envOpenAIBaseURL, "")
		if got := resolveOpenAIBaseURL(Config{}); got != "" {
			t.Fatalf("resolveOpenAIBaseURL = %q, want empty (SDK default)", got)
		}
	})
}

// TestResolveAgentLimits checks the ConfigLimits → agent.Limits mapping: seconds become a
// Duration, other fields pass through, and an unset field stays zero (so the agent defaults it).
func TestResolveAgentLimits(t *testing.T) {
	got := resolveAgentLimits(Config{Limits: ConfigLimits{
		MaxIterations: 40, ScriptTimeoutS: 8, MaxInlineTools: 20, MaxHTTPBytes: 5 << 20,
	}})
	if got.MaxIterations != 40 || got.ScriptTimeout != 8*time.Second || got.MaxInlineTools != 20 || got.MaxHTTPBytes != 5<<20 {
		t.Fatalf("resolveAgentLimits mapped wrong: %+v", got)
	}
	// An empty ConfigLimits maps to a zero agent.Limits (the agent then applies its defaults).
	if z := resolveAgentLimits(Config{}); z != (agent.Limits{}) {
		t.Fatalf("empty config should map to zero Limits, got %+v", z)
	}
}

// TestResolveSpawnDepth: configured value wins; unset falls back to defaultSpawnDepth.
func TestResolveSpawnDepth(t *testing.T) {
	if got := resolveSpawnDepth(Config{Limits: ConfigLimits{SpawnDepth: 3}}); got != 3 {
		t.Fatalf("resolveSpawnDepth(configured) = %d, want 3", got)
	}
	if got := resolveSpawnDepth(Config{}); got != defaultSpawnDepth {
		t.Fatalf("resolveSpawnDepth(unset) = %d, want %d", got, defaultSpawnDepth)
	}
}

// TestConfigLimitsOmitzero: an all-default Limits is dropped from the persisted config, so
// `config set-*` doesn't write a noisy empty "limits" block.
func TestConfigLimitsOmitzero(t *testing.T) {
	orig := configDirFlag
	t.Cleanup(func() { configDirFlag = orig })
	configDirFlag = t.TempDir()
	t.Setenv(envConfigDir, "")
	if err := saveConfig(Config{OpenAIKey: "sk-x"}); err != nil {
		t.Fatal(err)
	}
	path, _ := configPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "limits") {
		t.Fatalf("empty limits should be omitted from config.json; got:\n%s", data)
	}
}

// TestConfigDirPrecedence checks the resolution order: --config-dir flag beats the
// AI_AGENT_CONFIG_DIR env, which beats the ~/.config/ai-agent default.
func TestConfigDirPrecedence(t *testing.T) {
	// Reset the flag var around the test (it is package-global, set by cobra).
	orig := configDirFlag
	t.Cleanup(func() { configDirFlag = orig })

	t.Run("flag wins over env", func(t *testing.T) {
		t.Setenv(envConfigDir, "/from/env")
		configDirFlag = "/from/flag"
		got, err := configDir()
		if err != nil {
			t.Fatal(err)
		}
		if got != "/from/flag" {
			t.Fatalf("configDir() = %q, want /from/flag", got)
		}
	})

	t.Run("env wins over default", func(t *testing.T) {
		configDirFlag = ""
		t.Setenv(envConfigDir, "/from/env")
		got, err := configDir()
		if err != nil {
			t.Fatal(err)
		}
		if got != "/from/env" {
			t.Fatalf("configDir() = %q, want /from/env", got)
		}
	})

	t.Run("default when neither set", func(t *testing.T) {
		configDirFlag = ""
		t.Setenv(envConfigDir, "")
		got, err := configDir()
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(got) != "ai-agent" {
			t.Fatalf("configDir() = %q, want a path ending in ai-agent", got)
		}
	})

	t.Run("runs dir: flag > env > <config-dir>/runs default", func(t *testing.T) {
		origS := sessionsDirFlag
		t.Cleanup(func() { sessionsDirFlag = origS })

		t.Setenv(envSessionsDir, "/from/env")
		sessionsDirFlag = "/from/flag"
		if got, _ := runsDir(); got != "/from/flag" {
			t.Fatalf("runsDir() = %q, want /from/flag", got)
		}
		sessionsDirFlag = ""
		if got, _ := runsDir(); got != "/from/env" {
			t.Fatalf("runsDir() = %q, want /from/env", got)
		}
		// No flag/env: transcripts default under the config dir (share-nothing), in a
		// distinct "runs" subfolder — not the shared ~/.local/share default.
		t.Setenv(envSessionsDir, "")
		configDirFlag = "/agents/work"
		t.Setenv(envConfigDir, "")
		if got, _ := runsDir(); got != "/agents/work/runs" {
			t.Fatalf("runsDir() = %q, want /agents/work/runs", got)
		}
	})

	t.Run("store paths live under the config dir", func(t *testing.T) {
		configDirFlag = "/agents/work"
		t.Setenv(envConfigDir, "")
		for _, tc := range []struct {
			get  func() (string, error)
			want string
		}{
			{configPath, "/agents/work/config.json"},
			{catalogPath, "/agents/work/tools.json"},
			{auditPath, "/agents/work/audit.jsonl"},
			{sessionStorePath, "/agents/work/sessions"},
			{runsDir, "/agents/work/runs"},
		} {
			got, err := tc.get()
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("path = %q, want %q", got, tc.want)
			}
		}
	})

	t.Run("memory and spaces live under the workspace", func(t *testing.T) {
		// The agent's own data is workspace-local (spaces.md governing decision):
		// per-workspace by construction, committable/ignorable as plain files.
		if got := memoryPath("/repo"); got != "/repo/.agent/memory.json" {
			t.Fatalf("memoryPath = %q, want /repo/.agent/memory.json", got)
		}
		if got := spacesDir("/repo"); got != "/repo/.agent/spaces" {
			t.Fatalf("spacesDir = %q, want /repo/.agent/spaces", got)
		}
	})
}

func TestSecretsResolver(t *testing.T) {
	// No config, no env → nil (fail closed: a cap naming a secret is denied).
	if r := secretsResolver(Config{}); r != nil {
		t.Fatal("empty config + no env should yield a nil resolver")
	}

	// Config-only secret resolves.
	r := secretsResolver(Config{Secrets: map[string]string{"scrapingant": "cfgval"}})
	if r == nil {
		t.Fatal("config secret should yield a resolver")
	}
	if v, ok := r("scrapingant"); !ok || v != "cfgval" {
		t.Fatalf("config secret = (%q,%v), want (cfgval,true)", v, ok)
	}
	if _, ok := r("nope"); ok {
		t.Error("unknown secret should not resolve")
	}

	// AI_AGENT_SECRET_* env supplies a secret (lowercased name) and overrides config —
	// the deployment path (e.g. `fly secrets set`).
	t.Setenv("AI_AGENT_SECRET_SCRAPINGANT", "envval")
	t.Setenv("AI_AGENT_SECRET_OTHER", "o")
	r = secretsResolver(Config{Secrets: map[string]string{"scrapingant": "cfgval"}})
	if v, ok := r("scrapingant"); !ok || v != "envval" {
		t.Fatalf("env should override config: got (%q,%v), want (envval,true)", v, ok)
	}
	if v, ok := r("other"); !ok || v != "o" {
		t.Fatalf("env-only secret = (%q,%v), want (o,true)", v, ok)
	}
}

// TestSecretNames covers what `config secrets` and author_tool's description report: an
// env-supplied secret must be listed even though it is nowhere in config.json — the
// deployment case (`fly secrets set AI_AGENT_SECRET_…`) where the listing previously said
// "no secrets" while the broker was resolving one fine.
func TestSecretNames(t *testing.T) {
	if got := secretNames(Config{}); len(got) != 0 {
		t.Fatalf("no config + no env: names = %v, want none", got)
	}

	t.Setenv("AI_AGENT_SECRET_SCRAPINGANT", "envval")
	cfg := Config{Secrets: map[string]string{"other": "o"}}

	got := secretNames(cfg)
	want := []string{"other", "scrapingant"} // sorted, both sources
	if len(got) != len(want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("names = %v, want %v", got, want)
		}
	}

	// The source tag is what tells the operator where a secret came from.
	_, sources := mergedSecrets(cfg)
	if sources["scrapingant"] != "env" {
		t.Errorf("scrapingant source = %q, want env", sources["scrapingant"])
	}
	if sources["other"] != "config" {
		t.Errorf("other source = %q, want config", sources["other"])
	}

	// An env secret shadowing a config one is reported as coming from env, since that is
	// the value the broker will inject.
	_, sources = mergedSecrets(Config{Secrets: map[string]string{"scrapingant": "cfgval"}})
	if sources["scrapingant"] != "env" {
		t.Errorf("shadowed secret source = %q, want env", sources["scrapingant"])
	}
}
