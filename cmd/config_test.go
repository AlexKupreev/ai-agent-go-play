package cmd

import (
	"path/filepath"
	"testing"
)

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
