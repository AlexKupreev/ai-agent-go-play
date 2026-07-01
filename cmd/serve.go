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
	"ai-agent-go-play/internal/logger"
	"ai-agent-go-play/internal/memory"
	openaiprovider "ai-agent-go-play/internal/provider/openai"
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

		// One process-wide audit sink for management-plane effects (e.g. a tool
		// revoked over the API); per-run transcripts keep their own audit file.
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
		runner, err := newServeRunner(cfg, resolveModel(modelFlag, cfg), tier, approvals, registry, mem)
		if err != nil {
			return err
		}

		srv := api.NewServer(api.NewEngine(runner), approvals, registry, rec)
		fmt.Fprintf(os.Stderr, "engine listening on %s\n", addrFlag)
		fmt.Fprintf(os.Stderr, "  start:  curl -XPOST %s/runs -d '{\"task\":\"...\"}'\n", "http://"+addrFlag)
		fmt.Fprintf(os.Stderr, "  stream: curl -N %s/runs/<id>/events\n", "http://"+addrFlag)
		return http.ListenAndServe(addrFlag, srv)
	},
}

// newServeRunner builds the Runner the engine drives: each run gets its own disk
// log and audit recorder, but shares the process-wide tool catalog and approver, so
// authored tools and parked approvals are consistent across runs and with the API.
func newServeRunner(cfg Config, model string, tier capability.Tier, approver tools.Approver, registry tools.Registry, mem memory.Store) (api.Runner, error) {
	prov := openaiprovider.New(cfg.OpenAIKey)
	workDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get working directory: %w", err)
	}

	return api.RunnerFunc(func(ctx context.Context, task string, obs agent.Observer) (string, error) {
		log, err := logger.New()
		if err != nil {
			return "", fmt.Errorf("failed to create logger: %w", err)
		}
		defer log.Close()

		rec, err := audit.NewJSONLRecorder(filepath.Join(log.SessionDir, "audit.jsonl"))
		if err != nil {
			return "", fmt.Errorf("failed to open audit log: %w", err)
		}
		defer rec.Close()

		// Engine event stream + disk log see the same events.
		obsAll := agent.Observers{agent.NewLoggerObserver(log), obs}
		executor := agent.NewExecutor(prov, workDir, model, log.RunID, obsAll, registry, mem, rec, tier, approver)
		return executor.Run(ctx, task)
	}), nil
}

func init() {
	serveCmd.Flags().StringVar(&addrFlag, "addr", "127.0.0.1:8080", "address to listen on")
	serveCmd.Flags().StringVar(&modelFlag, "model", "", "model to use (overrides config; default: gpt-4o-mini)")
	serveCmd.Flags().StringVar(&tierFlag, "tier", "", "trust tier: safe|balanced|permissive (overrides config; default: balanced)")
}
