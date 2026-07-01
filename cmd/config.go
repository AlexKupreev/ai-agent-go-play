package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ai-agent-go-play/internal/capability"

	"github.com/spf13/cobra"
)

// Config holds persistent settings stored on disk.
type Config struct {
	OpenAIKey string `json:"openai_key"`
	Model     string `json:"model,omitempty"` // default model; overridden by --model
	Tier      string `json:"tier,omitempty"`  // default trust tier; overridden by --tier

	// Optional Telegram frontend. Empty token ⇒ the bot is disabled and the engine
	// runs unchanged. Both may be supplied via env vars (see resolveTelegram*).
	TelegramToken        string  `json:"telegram_token,omitempty"`
	TelegramAllowedUsers []int64 `json:"telegram_allowed_users,omitempty"`
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

// configDirFlag is bound to the persistent --config-dir flag (see cmd/root.go).
var configDirFlag string

// envConfigDir overrides the config directory when --config-dir is not given.
const envConfigDir = "AI_AGENT_CONFIG_DIR"

// configDir returns the base directory holding this agent's stored state — config,
// tool catalog, memory, and the process-wide audit log. Precedence: --config-dir flag
// > AI_AGENT_CONFIG_DIR env > the default ~/.config/ai-agent.
//
// Pointing two `agent serve` processes at different config dirs is how you run two
// fully independent agents (separate tools + memory + audit) on one box — no shared
// state between them.
func configDir() (string, error) {
	if configDirFlag != "" {
		return configDirFlag, nil
	}
	if v := strings.TrimSpace(os.Getenv(envConfigDir)); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "ai-agent"), nil
}

// sessionsDirFlag is bound to the persistent --sessions-dir flag (see cmd/root.go).
var sessionsDirFlag string

// envSessionsDir overrides the sessions directory when --sessions-dir is not given.
const envSessionsDir = "AI_AGENT_SESSIONS_DIR"

// sessionsDir returns the root under which per-run transcripts are written (each run
// gets its own <root>/<runID>/ subdirectory). Precedence: --sessions-dir flag >
// AI_AGENT_SESSIONS_DIR env > "" (the logger's default ~/.local/share/ai-agent/
// sessions). Give two agents distinct sessions roots so their transcripts stay
// separate, the same way --config-dir separates their config/tools/memory/audit.
func sessionsDir() string {
	if sessionsDirFlag != "" {
		return sessionsDirFlag
	}
	return strings.TrimSpace(os.Getenv(envSessionsDir))
}

// underConfigDir joins name onto the resolved config directory.
func underConfigDir(name string) (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// configPath returns the path to the config file, e.g. ~/.config/ai-agent/config.json
func configPath() (string, error) { return underConfigDir("config.json") }

// catalogPath returns the path to the persistent tool catalog, e.g.
// ~/.config/ai-agent/tools.json (created on first authored tool).
func catalogPath() (string, error) { return underConfigDir("tools.json") }

// memoryPath returns the path to the long-term memory store, e.g.
// ~/.config/ai-agent/memory.json (created on first remembered fact).
func memoryPath() (string, error) { return underConfigDir("memory.json") }

// auditPath returns the path to the process-wide audit log used by the serve
// management plane, e.g. ~/.config/ai-agent/audit.jsonl. (Per-run transcripts keep
// their own audit file under the session dir; this one records management-plane
// effects such as tool revocation.)
func auditPath() (string, error) { return underConfigDir("audit.jsonl") }

// sessionStorePath returns the directory holding persisted conversations (one JSON
// file per session), e.g. ~/.config/ai-agent/sessions. This is agent state (distinct
// from the per-run transcripts under --sessions-dir, which are logs).
func sessionStorePath() (string, error) { return underConfigDir("sessions") }

// Env vars overriding the Telegram config (env wins, so a token can be supplied
// without editing the config file).
const (
	envTelegramToken   = "AI_AGENT_TELEGRAM_TOKEN"
	envTelegramAllowed = "AI_AGENT_TELEGRAM_ALLOWED_USERS" // comma-separated user ids
)

// resolveTelegramToken returns the bot token, env taking precedence over config.
// Empty means the frontend stays disabled.
func resolveTelegramToken(cfg Config) string {
	if v := strings.TrimSpace(os.Getenv(envTelegramToken)); v != "" {
		return v
	}
	return strings.TrimSpace(cfg.TelegramToken)
}

// resolveTelegramAllowed returns the allowed Telegram user ids, env (comma-separated)
// taking precedence over config. Malformed env ids are skipped with a warning.
func resolveTelegramAllowed(cfg Config) []int64 {
	if v := strings.TrimSpace(os.Getenv(envTelegramAllowed)); v != "" {
		var ids []int64
		for part := range strings.SplitSeq(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			id, err := strconv.ParseInt(part, 10, 64)
			if err != nil {
				fmt.Fprintf(os.Stderr, "telegram: ignoring invalid user id %q in %s\n", part, envTelegramAllowed)
				continue
			}
			ids = append(ids, id)
		}
		return ids
	}
	return cfg.TelegramAllowedUsers
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
