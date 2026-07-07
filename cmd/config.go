package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/capability"
	openaiprovider "ai-agent-go-play/internal/provider/openai"
	"ai-agent-go-play/internal/tools"

	"github.com/spf13/cobra"
)

// modelFlagUsage is the shared `--model` help string, derived from the agent's built-in
// default so the literal lives in exactly one place (agent.DefaultModel).
var modelFlagUsage = fmt.Sprintf("model to use (overrides config; default: %s)", agent.DefaultModel)

// Config holds persistent settings stored on disk.
type Config struct {
	OpenAIKey string `json:"openai_key"`
	// OpenAIBaseURL points the OpenAI-compatible client at a non-default endpoint (a local
	// llama.cpp/Ollama/vLLM server, OpenRouter, a proxy). Empty ⇒ the real OpenAI API.
	// Overridden by AI_AGENT_OPENAI_BASE_URL.
	OpenAIBaseURL string `json:"openai_base_url,omitempty"`
	Model         string `json:"model,omitempty"`   // default model; overridden by --model
	Tier          string `json:"tier,omitempty"`    // default trust tier; overridden by --tier
	Verbose       bool   `json:"verbose,omitempty"` // default trace verbosity; overridden by --verbose/--quiet

	// Optional Telegram frontend. Empty token ⇒ the bot is disabled and the engine
	// runs unchanged. Both may be supplied via env vars (see resolveTelegram*).
	TelegramToken        string  `json:"telegram_token,omitempty"`
	TelegramAllowedUsers []int64 `json:"telegram_allowed_users,omitempty"`

	// Engines maps a short alias to an engine address (host:port), so remote
	// commands can say `--addr alex` instead of `--addr 127.0.0.1:8081`. An --addr
	// value that is not a known alias is used verbatim (see resolveAddr).
	Engines map[string]string `json:"engines,omitempty"`

	// Limits tunes the built-in bounds without a rebuild (0/unset ⇒ the built-in default).
	// omitzero (not omitempty) so an all-default Limits is dropped from config.json entirely.
	Limits ConfigLimits `json:"limits,omitzero"`
}

// ConfigLimits are the on-disk, tunable bounds (config.json "limits"). A zero/absent field
// keeps the built-in default. These are experiment knobs — deeper ReAct loops, longer sandbox
// runs, a tighter run-retention cap on a small box — that otherwise needed a rebuild.
type ConfigLimits struct {
	MaxIterations   int   `json:"max_iterations,omitempty"`         // ReAct model-call iterations (default 20)
	ScriptTimeoutS  int   `json:"script_timeout_seconds,omitempty"` // per sandboxed script (default 5)
	MaxInlineTools  int   `json:"max_inline_tools,omitempty"`       // catalog size before search-gating (default 12)
	MaxHTTPBytes    int64 `json:"max_http_bytes,omitempty"`         // brokered HTTP response cap (default 1 MiB)
	MaxFinishedRuns int   `json:"max_finished_runs,omitempty"`      // engine in-memory finished-run retention (default 100)
	SpawnDepth      int   `json:"spawn_depth,omitempty"`            // sub-agent delegation budget (default 1)
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

var setBaseURLCmd = &cobra.Command{
	Use:   "set-base-url <url>",
	Short: "Point the OpenAI-compatible client at a non-default endpoint (local server / proxy); AI_AGENT_OPENAI_BASE_URL overrides it",
	Long: "Set the base URL for the OpenAI-compatible API so the agent can talk to a local " +
		"llama.cpp / Ollama / vLLM server, OpenRouter, or a proxy instead of the OpenAI API. " +
		"Pass an empty string to clear it and go back to the default. AI_AGENT_OPENAI_BASE_URL " +
		"overrides this per run.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := loadConfigOrEmpty()
		cfg.OpenAIBaseURL = strings.TrimSpace(args[0])
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
	configCmd.AddCommand(setBaseURLCmd)
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

// sessionScratchRoot returns the parent of every session's scratch dir, e.g.
// ~/.config/ai-agent/session-scratch. Reaped per session on close; reported as one store by
// the status tool's disk-usage section.
func sessionScratchRoot() (string, error) { return underConfigDir("session-scratch") }

// sessionScratchDir returns the per-session scratch directory for the deliberate engine
// path (serve --plan): the artifact cache + manifest for one session, e.g.
// ~/.config/ai-agent/session-scratch/<id>. Keyed by session id so it is namespaced per
// conversation and persists across turns and restarts (chat-planner.md §D5). Reaped when
// the session is closed (serve's session-close hook); cache-with-fallback keeps a
// stale/absent file correct in the meantime.
func sessionScratchDir(sessionID string) (string, error) {
	return underConfigDir(filepath.Join("session-scratch", sessionID))
}

// agentStateDirs resolves the agent's on-disk state locations for the status tool's
// disk-usage section, best-effort (a path that can't be resolved is skipped). internal/tools
// reports usage but resolves no paths itself; this is where the config-dir layout lives.
func agentStateDirs() []tools.StateDir {
	specs := []struct {
		label string
		fn    func() (string, error)
	}{
		{"transcripts (runs)", runsDir},
		{"sessions (+ archived)", sessionStorePath},
		{"scratch cache", sessionScratchRoot},
		{"tool catalog", catalogPath},
		{"memory", memoryPath},
		{"audit log", auditPath},
	}
	var dirs []tools.StateDir
	for _, s := range specs {
		if p, err := s.fn(); err == nil && p != "" {
			dirs = append(dirs, tools.StateDir{Label: s.label, Path: p})
		}
	}
	return dirs
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

// Env vars overriding the model/tier/base-URL defaults (a flag still wins over env).
const (
	envModel         = "AI_AGENT_MODEL"
	envTier          = "AI_AGENT_TIER"
	envOpenAIBaseURL = "AI_AGENT_OPENAI_BASE_URL"
)

// resolveOpenAIBaseURL applies base-URL precedence: AI_AGENT_OPENAI_BASE_URL env > config
// value > "" (the SDK default, the real OpenAI API).
func resolveOpenAIBaseURL(cfg Config) string {
	if v := strings.TrimSpace(os.Getenv(envOpenAIBaseURL)); v != "" {
		return v
	}
	return strings.TrimSpace(cfg.OpenAIBaseURL)
}

// newProvider builds the model provider from config + env, the single construction point so
// base-URL resolution (and a future adapter switch) lives in one place rather than at each
// call site.
func newProvider(cfg Config) *openaiprovider.Client {
	return openaiprovider.New(cfg.OpenAIKey, resolveOpenAIBaseURL(cfg))
}

// resolveAgentLimits maps the on-disk ConfigLimits to agent.Limits (seconds → Duration). A
// zero field stays zero, so the agent applies its own built-in default — this never forces a
// value, it only carries an operator override through.
func resolveAgentLimits(cfg Config) agent.Limits {
	l := cfg.Limits
	return agent.Limits{
		MaxIterations:  l.MaxIterations,
		ScriptTimeout:  time.Duration(l.ScriptTimeoutS) * time.Second,
		MaxInlineTools: l.MaxInlineTools,
		MaxHTTPBytes:   l.MaxHTTPBytes,
	}
}

// resolveSpawnDepth returns the configured sub-agent delegation budget, or defaultSpawnDepth
// when unset.
func resolveSpawnDepth(cfg Config) int {
	if cfg.Limits.SpawnDepth > 0 {
		return cfg.Limits.SpawnDepth
	}
	return defaultSpawnDepth
}

// resolveModel applies model precedence: the --model flag wins, then AI_AGENT_MODEL, then
// the saved config default, then "" (the agent falls back to its built-in default).
func resolveModel(flag string, cfg Config) string {
	if flag != "" {
		return flag
	}
	if v := strings.TrimSpace(os.Getenv(envModel)); v != "" {
		return v
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

// resolveTier applies tier precedence: the --tier flag wins, then AI_AGENT_TIER, then the
// saved config default, then TierBalanced. An invalid value (from any source) is an error.
func resolveTier(flag string, cfg Config) (capability.Tier, error) {
	raw := flag
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv(envTier))
	}
	if raw == "" {
		raw = cfg.Tier
	}
	if raw == "" {
		return capability.TierBalanced, nil
	}
	return capability.ParseTier(raw)
}
