package cmd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/api"
	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/frontend/telegram"
	"ai-agent-go-play/internal/logger"
	"ai-agent-go-play/internal/memory"
	"ai-agent-go-play/internal/provider"
	openaiprovider "ai-agent-go-play/internal/provider/openai"
	"ai-agent-go-play/internal/session"
	"ai-agent-go-play/internal/tools"

	"github.com/spf13/cobra"
)

var addrFlag string

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the headless engine over HTTP+SSE",
	Long: "Expose the agent as a headless API: POST /runs starts a run, " +
		"GET /runs/{id}/events streams its events over SSE. Risky actions park on " +
		"GET /approvals and are resolved with POST /approvals/{id}.\n\n" +
		"Note: this runs the executor directly (no interactive planner yet).",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		// One approval queue, shared: the executor parks risky actions on it (as its
		// Approver), and the HTTP endpoints resolve them.
		approvals := api.NewApprovalQueue()

		// One tool catalog, shared across runs and exposed at GET /tools: a tool
		// authored in one run is then visible to later runs and to the API.
		catPath, err := catalogPath()
		if err != nil {
			return err
		}
		registry, err := tools.NewPersistentRegistry(catPath)
		if err != nil {
			return fmt.Errorf("failed to load tool catalog: %w", err)
		}

		// One memory store, shared across runs: a fact remembered in one run is
		// recallable by later runs (the cross-run guarantee 4d is about).
		memPath, err := memoryPath()
		if err != nil {
			return err
		}
		mem, err := memory.NewPersistentStore(memPath)
		if err != nil {
			return fmt.Errorf("failed to load memory store: %w", err)
		}

		// One process-wide audit log, shared across runs and exposed at GET /audit:
		// every run's effects (plus management-plane effects like a tool revoked over
		// the API) land here so the log is a single queryable review surface. Per-run
		// transcripts keep their own audit file as well.
		auditFile, err := auditPath()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(auditFile), 0700); err != nil {
			return fmt.Errorf("failed to create audit dir: %w", err)
		}
		rec, err := audit.NewJSONLRecorder(auditFile)
		if err != nil {
			return fmt.Errorf("failed to open audit log: %w", err)
		}
		defer rec.Close()

		tier, err := resolveTier(tierFlag, cfg)
		if err != nil {
			return err
		}

		// One persistent conversation store, shared across runs and exposed at
		// /sessions: a chat can resume across restarts and across frontends.
		sessStoreDir, err := sessionStorePath()
		if err != nil {
			return err
		}
		sessions := session.NewFileStore(sessStoreDir)

		workDir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get working directory: %w", err)
		}
		deps := serveDeps{
			prov:     openaiprovider.New(cfg.OpenAIKey),
			workDir:  workDir,
			model:    resolveModel(modelFlag, cfg),
			tier:     tier,
			approver: approvals,
			registry: registry,
			mem:      mem,
			central:  rec,
		}

		engine := api.NewEngine(deps.runner())
		engine.EnableSessions(sessions, deps.turnRunner())
		// Push parked escalations (and their resolutions) onto the owning run's event
		// stream, so a streaming frontend learns of them without polling /approvals.
		approvals.SetEmitter(engine.PublishToRun)

		srv := api.NewServer(engine, approvals, registry, rec, rec)

		// Optional Telegram frontend: a peer client of this same engine over localhost.
		// Active only when a token is configured; the engine runs unchanged otherwise.
		startTelegramIfConfigured(cfg)

		fmt.Fprintf(os.Stderr, "engine listening on %s\n", addrFlag)
		fmt.Fprintf(os.Stderr, "  start:  curl -XPOST %s/runs -d '{\"task\":\"...\"}'\n", "http://"+addrFlag)
		fmt.Fprintf(os.Stderr, "  stream: curl -N %s/runs/<id>/events\n", "http://"+addrFlag)
		return http.ListenAndServe(addrFlag, srv)
	},
}

// serveDeps holds everything needed to build a per-run executor. The shared
// process-wide state (catalog, approver, memory, central audit) is consistent across
// runs and with the API; per-run state (transcript, session audit file) is fresh.
type serveDeps struct {
	prov     *openaiprovider.Client
	workDir  string
	model    string
	tier     capability.Tier
	approver tools.Approver
	registry tools.Registry
	mem      memory.Store
	central  audit.Recorder
}

// buildExecutor constructs a fresh executor for one run/turn, keyed by the engine's
// runID (so the transcript, audit Run field, and parked approvals share one id). It
// returns a cleanup to defer. Shared by the plain runner and the session turn runner.
func (d serveDeps) buildExecutor(runID string, obs agent.Observer) (*agent.Agent, func(), error) {
	log, err := logger.NewWithID(sessionsDir(), runID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create logger: %w", err)
	}
	sessionRec, err := audit.NewJSONLRecorder(filepath.Join(log.SessionDir, "audit.jsonl"))
	if err != nil {
		log.Close()
		return nil, nil, fmt.Errorf("failed to open audit log: %w", err)
	}
	// Effects fan out to the session transcript and the process-wide log (GET /audit).
	rec := audit.Recorders{sessionRec, d.central}
	obsAll := agent.Observers{agent.NewLoggerObserver(log), obs}
	executor := agent.NewExecutor(d.prov, d.workDir, d.model, runID, obsAll, d.registry, d.mem, rec, d.tier, d.approver)
	cleanup := func() { sessionRec.Close(); log.Close() }
	return executor, cleanup, nil
}

// runner drives a single-shot run: a fresh executor with no prior context.
func (d serveDeps) runner() api.Runner {
	return api.RunnerFunc(func(ctx context.Context, runID, task string, obs agent.Observer) (string, error) {
		ex, cleanup, err := d.buildExecutor(runID, obs)
		if err != nil {
			return "", err
		}
		defer cleanup()
		return ex.Run(ctx, task)
	})
}

// turnRunner drives one session turn: a fresh executor seeded with the session's
// prior history, returning the updated history for the engine to persist.
func (d serveDeps) turnRunner() api.TurnRunner {
	return api.TurnRunnerFunc(func(ctx context.Context, runID string, prior []provider.Message, text string, obs agent.Observer) (string, []provider.Message, error) {
		ex, cleanup, err := d.buildExecutor(runID, obs)
		if err != nil {
			return "", nil, err
		}
		defer cleanup()
		ex.Restore(prior)
		answer, err := ex.Run(ctx, text)
		return answer, ex.Messages(), err
	})
}

// startTelegramIfConfigured launches the optional Telegram bot when a token is
// configured (config or env). The bot is a peer client of this engine over the local
// HTTP transport — it drives api.Client exactly like `agent client`, with no special
// access, and gates callers by the user-id allowlist. When no token is set, or while
// the live Telegram transport is not yet built, the engine simply runs without it.
func startTelegramIfConfigured(cfg Config) {
	token := resolveTelegramToken(cfg)
	if token == "" {
		fmt.Fprintln(os.Stderr, "telegram: disabled (no token configured)")
		return
	}
	transport, err := telegram.NewHTTPTransport(token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "telegram: %v — running without the bot\n", err)
		return
	}
	allowed := resolveTelegramAllowed(cfg)
	if len(allowed) == 0 {
		fmt.Fprintln(os.Stderr, "telegram: warning — no allowed user ids; the bot will reject everyone")
	}
	// The bot reaches the engine over localhost, like any other client.
	bot := telegram.NewBot(transport, api.NewClient("http://"+addrFlag), allowed)
	go func() {
		if err := bot.Run(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "telegram: bot stopped: %v\n", err)
		}
	}()
	fmt.Fprintln(os.Stderr, "telegram: bot enabled")
}

func init() {
	serveCmd.Flags().StringVar(&addrFlag, "addr", "127.0.0.1:8080", "address to listen on")
	serveCmd.Flags().StringVar(&modelFlag, "model", "", "model to use (overrides config; default: gpt-4o-mini)")
	serveCmd.Flags().StringVar(&tierFlag, "tier", "", "trust tier: safe|balanced|permissive (overrides config; default: balanced)")
}
