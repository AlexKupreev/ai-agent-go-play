package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/logger"
	"ai-agent-go-play/internal/memory"
	"ai-agent-go-play/internal/tools"

	"github.com/spf13/cobra"
)

var modelFlag string
var tierFlag string
var verboseFlag bool
var quietFlag bool

var runCmd = &cobra.Command{
	Use:   "run <task>",
	Short: "Run the agent with a task",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		verbose := resolveVerbose(cmd, cfg)

		// Ctrl+C cancels the run gracefully (the loop honors ctx at the next
		// model/tool boundary); a second Ctrl+C is no longer caught and force-quits.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()

		runsBase, err := runsDir()
		if err != nil {
			return err
		}
		log, err := logger.New(runsBase)
		if err != nil {
			return fmt.Errorf("failed to create logger: %w", err)
		}
		defer log.Close()

		task := strings.Join(args, " ")
		fmt.Fprintf(os.Stderr, "Run ID: %s\n", log.RunID)
		fmt.Fprintf(os.Stderr, "Log:    %s\n", log.Path)
		fmt.Fprintf(os.Stderr, "Task:   %s\n\n", task)

		workDir, err := resolveWorkspace()
		if err != nil {
			return err
		}

		prov := newProvider(cfg)
		model := resolveModel(modelFlag, cfg)
		tier, err := resolveTier(tierFlag, cfg)
		if err != nil {
			return err
		}

		// Run events fan out to the disk log always, and to the CLI when verbose. A
		// usage accumulator sums tokens across the planner + executor for a summary.
		usage := agent.NewUsageObserver()
		obs := agent.Observers{agent.NewLoggerObserver(log), usage}
		if verbose {
			obs = append(obs, agent.NewCLIObserver(os.Stderr))
		}
		runStart := time.Now()

		// Prompt customization (SYSTEM.md/AGENTS.md/PLANNER.md) read once, shared by the
		// planner (PLANNER.md) and the executor (SYSTEM.md/AGENTS.md) below.
		prompts, err := loadPrompts(workDir, tier)
		if err != nil {
			return err
		}
		workspaceGuidance, err := workspaceGuidanceStore(workDir, nil).Get()
		if err != nil {
			return fmt.Errorf("load workspace guidance: %w", err)
		}

		// Live audit log (per run) + persistent tool catalog, threaded into the
		// executor so authored tools run brokered and audited.
		rec, err := audit.NewJSONLRecorder(filepath.Join(log.SessionDir, "audit.jsonl"))
		if err != nil {
			return fmt.Errorf("failed to open audit log: %w", err)
		}
		defer rec.Close()

		catPath, err := catalogPath()
		if err != nil {
			return err
		}
		registry, err := tools.NewPersistentRegistry(catPath)
		if err != nil {
			return fmt.Errorf("failed to load tool catalog: %w", err)
		}

		// Memory is workspace-local (<workspace>/.agent/memory.json, spaces.md). One-shot
		// runs have no session to carry an active space, so they use the global scope.
		mem, err := memory.NewPersistentStore(memoryPath(workDir))
		if err != nil {
			return fmt.Errorf("failed to load memory store: %w", err)
		}

		// Also append run_usage to the process-wide audit log so day-wide token totals
		// (the usage tool / `agent usage`) include one-shot CLI runs, not just serve.
		central, ledger, err := openCentralLedger()
		if err != nil {
			return err
		}
		if central != nil {
			defer central.Close()
		}

		catalog, err := loadAgentCatalog(workDir, tier)
		if err != nil {
			return err
		}

		// Build the executor before the planner so the planner can be handed the executor's
		// live environment (generated tools + tier + host) — the planner plans for it.
		executor := agent.NewExecutor(agent.ExecutorConfig{
			Provider: prov, WorkDir: workDir, Model: model, RunID: log.RunID,
			Observer: obs, Registry: registry, Memory: mem, Docs: selfDocs,
			Audit: rec, Tier: tier, Gate: tools.StdinGate{},
			Usage: tools.UsageContext{Ledger: ledger}, AuditReader: central,
			SystemPromptOverride: prompts.Override, PromptAppends: withGuidance(prompts.Appends, workspaceGuidance, "", ""),
			AgentCatalog: catalog, SpawnDepth: resolveSpawnDepth(cfg),
			StatusDirs: agentStateDirs(workDir), Limits: resolveAgentLimits(cfg),
			ContextLimit: resolveContextLimit(model, cfg),
			Secrets:      secretsResolver(cfg),
			SecretNames:  secretNames(cfg),
		})

		// run has no artifact manifest (no scratch cache today, chat-planner.md Q9c) ⇒ "".
		// CLI clarifications read from stdin (nil ⇒ StdinGate).
		planner := agent.NewPlannerWithLimits(prov, model, prompts.PlannerOverride, executor.EnvironmentSummary(), "", tools.StdinGate{}, log.RunID, obs, resolveAgentLimits(cfg))
		fmt.Fprintln(os.Stderr, "[planner] clarifying task...")
		planJSON, err := planner.Run(ctx, task)
		if err != nil {
			return fmt.Errorf("planner error: %w", err)
		}

		var plan agent.Plan
		if err := json.Unmarshal([]byte(planJSON), &plan); err != nil {
			return fmt.Errorf("failed to parse plan: %w", err)
		}

		if verbose {
			fmt.Fprintf(os.Stderr, "[planner] refined task: %s\n", plan.RefinedTask)
			for _, a := range plan.Assumptions {
				fmt.Fprintf(os.Stderr, "[planner] assumption: %s\n", a)
			}
			for _, c := range plan.Confirmed {
				fmt.Fprintf(os.Stderr, "[planner] confirmed: %s\n", c)
			}
			fmt.Fprintln(os.Stderr)
		}

		// Seed the executor with the flattened brief (refined task + context + artifact
		// refs + success criteria), not the bare refined task — chat-planner.md §4.
		result, err := executor.Run(ctx, plan.Brief())
		if err != nil {
			return err
		}
		fmt.Println(result)

		total, steps := usage.Total(), usage.Steps()
		if central != nil {
			recordRunUsage(central, log.RunID, "", total, steps)
		}
		fmt.Fprintln(os.Stderr, formatUsage(total, steps, time.Since(runStart)))
		if ledger != nil {
			if today, runs := ledger.Today(); runs > 0 {
				fmt.Fprintf(os.Stderr, "today: %s across %d run(s)\n", formatTokens(today), runs)
			}
		}
		return nil
	},
}

func init() {
	runCmd.Flags().StringVar(&modelFlag, "model", "", modelFlagUsage)
	runCmd.Flags().StringVar(&tierFlag, "tier", "", "trust tier: safe|balanced|permissive (overrides config; default: balanced)")
	runCmd.Flags().BoolVar(&verboseFlag, "verbose", false, "print the tool-call trace to stderr (overrides config/env)")
	runCmd.Flags().BoolVar(&quietFlag, "quiet", false, "suppress the tool-call trace (overrides config/env; the disk transcript is unaffected)")
}
