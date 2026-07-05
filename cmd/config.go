package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"ai-agent-go-play/internal/capability"

	"github.com/spf13/cobra"
)

// Config holds persistent settings stored on disk.
type Config struct {
	OpenAIKey string `json:"openai_key"`
	Model     string `json:"model,omitempty"`   // default model; overridden by --model
	Tier      string `json:"tier,omitempty"`    // default trust tier; overridden by --tier
	Verbose   bool   `json:"verbose,omitempty"` // default trace verbosity; overridden by --verbose/--quiet

	// Optional Telegram frontend. Empty token ⇒ the bot is disabled and the engine
	// runs unchanged. Both may be supplied via env vars (see resolveTelegram*).
	TelegramToken        string  `json:"telegram_token,omitempty"`
	TelegramAllowedUsers []int64 `json:"telegram_allowed_users,omitempty"`

	// Engines maps a short alias to an engine address (host:port), so remote
	// commands can say `--addr alex` instead of `--addr 127.0.0.1:8081`. An --addr
	// value that is not a known alias is used verbatim (see resolveAddr).
	Engines map[string]string `json:"engines,omitempty"`
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

var setVerboseCmd = &cobra.Command{
	Use:   "set-verbose <on|off>",
	Short: "Save the default trace verbosity; --verbose/--quiet override it per run",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		v, ok := parseBool(args[0])
		if !ok {
			return fmt.Errorf("expected on|off (or true|false), got %q", args[0])
		}
		cfg := loadConfigOrEmpty()
		cfg.Verbose = v
		return saveConfig(cfg)
	},
}

var setEngineCmd = &cobra.Command{
	Use:   "set-engine <alias> <host:port>",
	Short: "Name an engine address so `--addr <alias>` connects to it",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		alias, addr := args[0], args[1]
		if strings.TrimSpace(alias) == "" || strings.TrimSpace(addr) == "" {
			return fmt.Errorf("both an alias and a host:port are required")
		}
		cfg := loadConfigOrEmpty()
		if cfg.Engines == nil {
			cfg.Engines = map[string]string{}
		}
		cfg.Engines[alias] = addr
		return saveConfig(cfg)
	},
}

var rmEngineCmd = &cobra.Command{
	Use:   "rm-engine <alias>",
	Short: "Remove a named engine alias",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := loadConfigOrEmpty()
		if _, ok := cfg.Engines[args[0]]; !ok {
			return fmt.Errorf("no engine alias %q", args[0])
		}
		delete(cfg.Engines, args[0])
		return saveConfig(cfg)
	},
}

var enginesCmd = &cobra.Command{
	Use:   "engines",
	Short: "List named engine aliases",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := loadConfigOrEmpty()
		if len(cfg.Engines) == 0 {
			fmt.Println("no engine aliases (add one with: agent config set-engine <alias> <host:port>)")
			return nil
		}
		aliases := make([]string, 0, len(cfg.Engines))
		for a := range cfg.Engines {
			aliases = append(aliases, a)
		}
		sort.Strings(aliases)
		for _, a := range aliases {
			fmt.Printf("%-16s %s\n", a, cfg.Engines[a])
		}
		return nil
	},
}

func init() {
	configCmd.AddCommand(setKeyCmd)
	configCmd.AddCommand(setModelCmd)
	configCmd.AddCommand(setTierCmd)
	configCmd.AddCommand(setVerboseCmd)
	configCmd.AddCommand(setEngineCmd)
	configCmd.AddCommand(rmEngineCmd)
	configCmd.AddCommand(enginesCmd)
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

// runsDir returns the root under which per-run transcripts are written (each run gets
// its own <root>/<runID>/ subdirectory). Precedence: --sessions-dir flag >
// AI_AGENT_SESSIONS_DIR env > <config-dir>/runs. Defaulting under the config dir keeps
// the "separate --config-dir agents share nothing" guarantee: two agents no longer
// co-mingle transcripts in the shared ~/.local/share/ai-agent/sessions. The distinct
// "runs" subfolder avoids overloading <config-dir>/sessions, which holds the resumable
// session *store* (agent state), not these transcripts (logs).
func runsDir() (string, error) {
	if sessionsDirFlag != "" {
		return sessionsDirFlag, nil
	}
	if v := strings.TrimSpace(os.Getenv(envSessionsDir)); v != "" {
		return v, nil
	}
	return underConfigDir("runs")
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

// sessionScratchDir returns the per-session scratch directory for the deliberate engine
// path (serve --plan): the artifact cache + manifest for one session, e.g.
// ~/.config/ai-agent/session-scratch/<id>. Keyed by session id so it is namespaced per
// conversation and persists across turns and restarts (chat-planner.md §D5). v1 has no
// reaper — cache-with-fallback keeps a stale/absent file correct.
func sessionScratchDir(sessionID string) (string, error) {
	return underConfigDir(filepath.Join("session-scratch", sessionID))
}

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

// resolveAddr turns an --addr value into an engine host:port. If the value matches a
// configured engine alias (agent config set-engine <alias> <host:port>) it resolves to
// that alias's address; otherwise it is used verbatim, so a literal host:port always
// works and aliases are a pure convenience. A missing/unreadable config just means no
// aliases are known — the value passes through unchanged.
func resolveAddr(addr string) string {
	if a, ok := loadConfigOrEmpty().Engines[addr]; ok {
		return a
	}
	return addr
}

// resolveModel applies model precedence: the --model flag wins, then the saved
// config default, then "" (the agent falls back to its built-in default).
func resolveModel(flag string, cfg Config) string {
	if flag != "" {
		return flag
	}
	return cfg.Model
}

// envVerbose overrides the default trace verbosity when no flag is given.
const envVerbose = "AI_AGENT_VERBOSE"

// resolveVerbose applies verbosity precedence: an explicit --quiet or --verbose flag
// wins (quiet takes precedence if both are somehow set), then AI_AGENT_VERBOSE, then
// the saved config default, then false. The intermediate CLI trace is the only thing
// gated — the on-disk transcript is always written regardless.
func resolveVerbose(cmd *cobra.Command, cfg Config) bool {
	if cmd.Flags().Changed("quiet") {
		return false
	}
	if cmd.Flags().Changed("verbose") {
		return true
	}
	if v, ok := parseBool(os.Getenv(envVerbose)); ok {
		return v
	}
	return cfg.Verbose
}

// parseBool accepts the friendly on/off spellings alongside Go's true/false/1/0.
// The second return is false when the input is empty or unrecognized.
func parseBool(s string) (val bool, ok bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "yes", "1":
		return true, true
	case "off", "false", "no", "0":
		return false, true
	default:
		return false, false
	}
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
