package cmd

import (
	"path/filepath"
	"testing"
)

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

	t.Run("sessions dir: flag > env > empty default", func(t *testing.T) {
		origS := sessionsDirFlag
		t.Cleanup(func() { sessionsDirFlag = origS })

		t.Setenv(envSessionsDir, "/from/env")
		sessionsDirFlag = "/from/flag"
		if got := sessionsDir(); got != "/from/flag" {
			t.Fatalf("sessionsDir() = %q, want /from/flag", got)
		}
		sessionsDirFlag = ""
		if got := sessionsDir(); got != "/from/env" {
			t.Fatalf("sessionsDir() = %q, want /from/env", got)
		}
		t.Setenv(envSessionsDir, "")
		if got := sessionsDir(); got != "" {
			t.Fatalf("sessionsDir() = %q, want empty (logger default)", got)
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
			{memoryPath, "/agents/work/memory.json"},
			{auditPath, "/agents/work/audit.jsonl"},
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
}
