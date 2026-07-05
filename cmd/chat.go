package cmd

import (
	"bufio"
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
	openaiprovider "ai-agent-go-play/internal/provider/openai"
	"ai-agent-go-play/internal/tools"

	"github.com/spf13/cobra"
)

var (
	chatPlanFlag    bool
	chatAddrFlag    string
	chatSessionFlag string
	chatListFlag    bool
)

var chatCmd = &cobra.Command{
	Use:   "chat",
	Short: "Interactive multi-turn chat (REPL) with retained context",
	Long: "Start an interactive session: type a message, get a reply, and keep going — " +
		"the conversation history is retained across turns (like a chat CLI).\n\n" +
		"By default the executor runs in THIS process (the tool catalog and memory are shared " +
		"with the rest of this agent — see --config-dir; the audit trail goes to this run's " +
		"transcript). With --addr the REPL instead drives a running `agent serve` engine as a " +
		"peer client: the conversation is a persistent, server-side session that survives quitting " +
		"and can be resumed here or from another client (e.g. Telegram). --addr accepts a host:port " +
		"or an alias from `agent config set-engine`. Use --list to see resumable sessions and " +
		"--session <id> to resume one.\n\n" +
		"The tool-call trace is off by default (like `run`); turn it on with --verbose or the " +
		"live /verbose toggle. The full transcript is always written to disk regardless.\n\n" +
		"Commands (local): /new (alias /reset) clears the conversation, /verbose [on|off] toggles " +
		"the trace, /exit (or Ctrl-D) quits.\n" +
		"Commands (--addr): /new (alias /reset) starts a fresh session, /end closes the session, " +
		"/exit (or Ctrl-D) detaches and leaves it resumable. Ctrl-C cancels the current turn.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if chatAddrFlag != "" {
			return runRemoteChat(resolveAddr(chatAddrFlag))
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		workDir, err := resolveWorkspace()
		if err != nil {
			return err
		}

		prov := openaiprovider.New(cfg.OpenAIKey)
		model := resolveModel(modelFlag, cfg)
		tier, err := resolveTier(tierFlag, cfg)
		if err != nil {
			return err
		}

		runsBase, err := runsDir()
		if err != nil {
			return err
		}
		log, err := logger.New(runsBase)
		if err != nil {
			return fmt.Errorf("failed to create logger: %w", err)
		}
		defer log.Close()

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

		memPath, err := memoryPath()
		if err != nil {
			return err
		}
		mem, err := memory.NewPersistentStore(memPath)
		if err != nil {
			return fmt.Errorf("failed to load memory store: %w", err)
		}

		// Interactive: optionally stream the agent's activity (tool calls/results) to
		// stderr, and keep the transcript on disk. The final answer of each turn goes to
		// stdout. A usage accumulator runs for the whole session; per-turn cost is its delta.
		// The CLI trace sits behind a gate so /verbose can toggle it live without rebuilding
		// the executor (buildExecutor captures obs once). Quiet by default, matching `run`.
		usage := agent.NewUsageObserver()
		trace := agent.NewGatedObserver(agent.NewCLIObserver(os.Stderr), resolveVerbose(cmd, cfg))
		obs := agent.Observers{agent.NewLoggerObserver(log), trace, usage}

		// buildExecutor reads the prompt files + agent-type catalog from disk and builds a
		// fresh executor over the shared, session-stable deps (provider, transcript, catalog,
		// memory, observers). It runs once at startup and again on /reload, so file edits to
		// SYSTEM.md/AGENTS.md/agents/*.md take effect without restarting the REPL.
		//
		// Local chat shows per-turn + session token totals itself (below); it doesn't wire the
		// audit-log ledger, so the model-facing usage tool is omitted here.
		buildExecutor := func() (*agent.Agent, error) {
			prompts, err := loadPrompts(workDir, tier)
			if err != nil {
				return nil, err
			}
			catalog, err := loadAgentCatalog(workDir, tier)
			if err != nil {
				return nil, err
			}
			return agent.NewExecutor(agent.ExecutorConfig{
				Provider: prov, WorkDir: workDir, Model: model, RunID: log.RunID,
				Observer: obs, Registry: registry, Memory: mem, Docs: selfDocs,
				Audit: rec, Tier: tier, Gate: tools.StdinGate{},
				Usage: tools.UsageContext{}, AuditReader: rec,
				SystemPromptOverride: prompts.Override, PromptAppends: prompts.Appends,
				AgentCatalog: catalog, SpawnDepth: defaultSpawnDepth,
			}), nil
		}

		// buildPlanner constructs a fresh planner, re-reading PLANNER.md each time so an edit
		// takes effect on the next planned turn without a restart (mirrors buildExecutor for
		// the pre-execution clarify/refine pass). Used per turn only when --plan is set.
		buildPlanner := func(environment string) (*agent.Agent, error) {
			prompts, err := loadPrompts(workDir, tier)
			if err != nil {
				return nil, err
			}
			return agent.NewPlanner(prov, model, prompts.PlannerOverride, environment, obs), nil
		}

		// One executor for the whole session: its conversation persists across turns.
		executor, err := buildExecutor()
		if err != nil {
			return err
		}

		fmt.Fprintf(os.Stderr, "agent chat — model %s, tier %s, planner %s, verbose %s\n", executor.Model(), tier, onOff(chatPlanFlag), onOff(trace.Enabled()))
		fmt.Fprintf(os.Stderr, "session %s  (/new or /reset to clear, /reload to re-read prompts+agents, /verbose to toggle the trace, /exit or Ctrl-D to quit)\n", log.RunID)

		// SIGINT cancels the current turn rather than killing the session; drained at
		// each prompt so a stray Ctrl-C while idle doesn't cancel the next turn.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt)
		defer signal.Stop(sigCh)

		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // allow long pasted input
		for {
			fmt.Fprint(os.Stderr, "\n> ")
			if !scanner.Scan() {
				fmt.Fprintln(os.Stderr) // newline after Ctrl-D
				break
			}
			line := strings.TrimSpace(scanner.Text())
			// /verbose [on|off] toggles the live CLI trace (no arg flips it). Handled
			// before the exact-match switch because it takes an optional argument.
			if arg, ok := strings.CutPrefix(line, "/verbose"); ok {
				arg = strings.TrimSpace(arg)
				if arg == "" {
					trace.SetEnabled(!trace.Enabled())
				} else if v, ok := parseBool(arg); ok {
					trace.SetEnabled(v)
				} else {
					fmt.Fprintln(os.Stderr, "usage: /verbose [on|off]")
					continue
				}
				fmt.Fprintf(os.Stderr, "(verbose %s)\n", onOff(trace.Enabled()))
				continue
			}
			switch line {
			case "":
				continue
			case "/exit", "/quit":
				return nil
			case "/reset", "/new":
				// /new + /reset are aliases (matching the remote REPL and Telegram bot):
				// in the local in-process chat, "start fresh" means clearing the history.
				executor.Reset()
				fmt.Fprintln(os.Stderr, "(conversation cleared)")
				continue
			case "/reload":
				// Re-read prompt files + agent-type catalog and rebuild the executor,
				// carrying the conversation forward. On failure (e.g. a malformed
				// agents/*.md) keep the current executor so the session survives a typo.
				next, err := buildExecutor()
				if err != nil {
					fmt.Fprintf(os.Stderr, "reload failed (keeping current prompts+agents): %v\n", err)
					continue
				}
				next.Restore(executor.Messages())
				executor = next
				fmt.Fprintln(os.Stderr, "(reloaded prompts and agent types)")
				continue
			}

			before, beforeSteps := usage.Total(), usage.Steps()
			turnStart := time.Now()
			if err := runTurn(sigCh, executor, buildPlanner, line); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
			}
			// Per-turn usage is the accumulator's delta; also show the session total.
			turn := subUsage(before, usage.Total())
			fmt.Fprintf(os.Stderr, "%s   (session: %s in / %s out)\n",
				formatUsage(turn, usage.Steps()-beforeSteps, time.Since(turnStart)),
				humanInt(usage.Total().InputTokens), humanInt(usage.Total().OutputTokens))
		}
		return nil
	},
}

// runTurn runs one chat turn under a cancellable context (Ctrl-C cancels just this
// turn). When the planner is enabled it refines the input first, mirroring `agent run`.
func runTurn(sigCh <-chan os.Signal, executor *agent.Agent, buildPlanner func(environment string) (*agent.Agent, error), line string) error {
	// Discard any Ctrl-C that arrived while idle at the prompt.
	select {
	case <-sigCh:
	default:
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-sigCh:
			fmt.Fprintln(os.Stderr, "\n(interrupted)")
			cancel()
		case <-done:
		}
	}()

	input := line
	if chatPlanFlag {
		// A fresh planner per turn: planning is independent of the running dialogue. Hand it
		// the executor's live environment (generated tools + tier + host) so it plans within
		// what the agent actually has — including tools authored in earlier turns.
		planner, err := buildPlanner(executor.EnvironmentSummary())
		if err != nil {
			return fmt.Errorf("planner: %w", err)
		}
		planJSON, err := planner.Run(ctx, line)
		if err != nil {
			return fmt.Errorf("planner: %w", err)
		}
		var plan agent.Plan
		if err := json.Unmarshal([]byte(planJSON), &plan); err != nil {
			return fmt.Errorf("parse plan: %w", err)
		}
		input = plan.RefinedTask
		fmt.Fprintf(os.Stderr, "[planner] %s\n", input)
	}

	result, err := executor.Run(ctx, input)
	if err != nil {
		return err
	}
	fmt.Println(result)
	return nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func init() {
	chatCmd.Flags().StringVar(&modelFlag, "model", "", "model to use (overrides config; default: gpt-4o-mini)")
	chatCmd.Flags().StringVar(&tierFlag, "tier", "", "trust tier: safe|balanced|permissive (overrides config; default: balanced)")
	chatCmd.Flags().BoolVar(&chatPlanFlag, "plan", false, "refine each message through the planner before executing (experimental)")
	chatCmd.Flags().BoolVar(&verboseFlag, "verbose", false, "start with the tool-call trace on (default off; toggle live with /verbose)")
	chatCmd.Flags().BoolVar(&quietFlag, "quiet", false, "start with the tool-call trace off (the default; overrides config/env)")
	chatCmd.Flags().StringVar(&chatAddrFlag, "addr", "", "drive a running engine's persistent session instead of an in-process executor (host:port or an alias from `agent config set-engine`)")
	chatCmd.Flags().StringVar(&chatSessionFlag, "session", "", "with --addr: resume this session id instead of starting a new one")
	chatCmd.Flags().BoolVar(&chatListFlag, "list", false, "with --addr: list resumable sessions on the engine and exit")
}
