package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"ai-agent-go-play/internal/capability"

	"github.com/spf13/cobra"
)

// Config holds persistent settings stored on disk.
type Config struct {
	OpenAIKey string `json:"openai_key"`
	Model     string `json:"model,omitempty"` // default model; overridden by --model
	Tier      string `json:"tier,omitempty"`  // default trust tier; overridden by --tier
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage agent configuration",
}

var setKeyCmd = &cobra.Command{
	Use:   "set-key <api-key>",
	Short: "Save your OpenAI API key",
	Args:  cobra.ExactArgs(1), // enforces exactly one argument
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := loadConfigOrEmpty()
		cfg.OpenAIKey = args[0]
		return saveConfig(cfg)
	},
}

var setModelCmd = &cobra.Command{
	Use:   "set-model <model>",
	Short: "Save the default model (e.g. gpt-4o); --model overrides it per run",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := loadConfigOrEmpty()
		cfg.Model = args[0]
		return saveConfig(cfg)
	},
}

var setTierCmd = &cobra.Command{
	Use:   "set-tier <safe|balanced|permissive>",
	Short: "Save the default trust tier (autonomy dial); --tier overrides it per run",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := capability.ParseTier(args[0]); err != nil {
			return err
		}
		cfg := loadConfigOrEmpty()
		cfg.Tier = args[0]
		return saveConfig(cfg)
	},
}

func init() {
	configCmd.AddCommand(setKeyCmd)
	configCmd.AddCommand(setModelCmd)
	configCmd.AddCommand(setTierCmd)
}

// configPath returns the path to the config file, e.g. ~/.config/ai-agent/config.json
func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "ai-agent", "config.json"), nil
}

// catalogPath returns the path to the persistent tool catalog, e.g.
// ~/.config/ai-agent/tools.json (created on first authored tool).
func catalogPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "ai-agent", "tools.json"), nil
}

// memoryPath returns the path to the long-term memory store, e.g.
// ~/.config/ai-agent/memory.json (created on first remembered fact).
func memoryPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "ai-agent", "memory.json"), nil
}

// auditPath returns the path to the process-wide audit log used by the serve
// management plane, e.g. ~/.config/ai-agent/audit.jsonl. (Per-run transcripts keep
// their own audit file under the session dir; this one records management-plane
// effects such as tool revocation. A run-spanning central reader is Phase 4e-4.)
func auditPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "ai-agent", "audit.jsonl"), nil
}

func saveConfig(cfg Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	// Create the directory if it doesn't exist. 0700 = only owner can read/write/enter.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// 0600 = only owner can read/write. Important: this file contains your API key.
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	fmt.Println("config saved to", path)
	return nil
}

func loadConfig() (Config, error) {
	path, err := configPath()
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("no config found — run: agent config set-key <your-key>")
	}
	var cfg Config
	return cfg, json.Unmarshal(data, &cfg)
}

// loadConfigOrEmpty loads the config, returning a zero Config if none exists yet.
// Used by the setters so updating one field preserves the others.
func loadConfigOrEmpty() Config {
	cfg, err := loadConfig()
	if err != nil {
		return Config{}
	}
	return cfg
}

// resolveModel applies model precedence: the --model flag wins, then the saved
// config default, then "" (the agent falls back to its built-in default).
func resolveModel(flag string, cfg Config) string {
	if flag != "" {
		return flag
	}
	return cfg.Model
}

// resolveTier applies tier precedence: the --tier flag wins, then the saved config
// default, then TierBalanced. An invalid value (from either source) is an error.
func resolveTier(flag string, cfg Config) (capability.Tier, error) {
	raw := flag
	if raw == "" {
		raw = cfg.Tier
	}
	if raw == "" {
		return capability.TierBalanced, nil
	}
	return capability.ParseTier(raw)
}
