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
	"ai-agent-go-play/internal/provider"
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
		"Commands (local): /reset clears the conversation, /exit (or Ctrl-D) quits.\n" +
		"Commands (--addr): /reset starts a fresh session, /end closes the session, /exit (or " +
		"Ctrl-D) detaches and leaves it resumable. Ctrl-C cancels the current turn.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if chatAddrFlag != "" {
			return runRemoteChat(resolveAddr(chatAddrFlag))
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		workDir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get working directory: %w", err)
		}

		prov := openaiprovider.New(cfg.OpenAIKey)
		model := resolveModel(modelFlag, cfg)
		tier, err := resolveTier(tierFlag, cfg)
		if err != nil {
			return err
		}

		log, err := logger.New(sessionsDir())
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

		// Interactive: stream the agent's activity (tool calls/results) to stderr, and
		// keep the transcript on disk. The final answer of each turn goes to stdout. A
		// usage accumulator runs for the whole session; per-turn cost is its delta.
		usage := agent.NewUsageObserver()
		obs := agent.Observers{agent.NewLoggerObserver(log), agent.NewCLIObserver(os.Stderr), usage}

		prompts, err := loadConfigDirPrompts()
		if err != nil {
			return err
		}

		// One executor for the whole session: its conversation persists across turns.
		// Local chat shows per-turn + session token totals itself (below); it doesn't
		// wire the audit-log ledger, so the model-facing usage tool is omitted here.
		executor := agent.NewExecutor(agent.ExecutorConfig{
			Provider: prov, WorkDir: workDir, Model: model, RunID: log.RunID,
			Observer: obs, Registry: registry, Memory: mem, Docs: selfDocs,
			Audit: rec, Tier: tier, Approver: tools.StdinApprover{},
			Usage: tools.UsageContext{}, AuditReader: rec,
			SystemPromptOverride: prompts.Override, PromptAppends: prompts.Appends,
		})

		fmt.Fprintf(os.Stderr, "agent chat — model %s, tier %s, planner %s\n", executor.Model(), tier, onOff(chatPlanFlag))
		fmt.Fprintf(os.Stderr, "session %s  (/reset to clear, /exit or Ctrl-D to quit)\n", log.RunID)

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
			switch line {
			case "":
				continue
			case "/exit", "/quit":
				return nil
			case "/reset":
				executor.Reset()
				fmt.Fprintln(os.Stderr, "(conversation cleared)")
				continue
			}

			before, beforeSteps := usage.Total(), usage.Steps()
			turnStart := time.Now()
			if err := runTurn(sigCh, executor, prov, model, obs, line); err != nil {
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
func runTurn(sigCh <-chan os.Signal, executor *agent.Agent, prov provider.Provider, model string, obs agent.Observer, line string) error {
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
		// A fresh planner per turn: planning is independent of the running dialogue.
		planner := agent.NewPlanner(prov, model, obs)
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
	chatCmd.Flags().StringVar(&chatAddrFlag, "addr", "", "drive a running engine's persistent session instead of an in-process executor (host:port or an alias from `agent config set-engine`)")
	chatCmd.Flags().StringVar(&chatSessionFlag, "session", "", "with --addr: resume this session id instead of starting a new one")
	chatCmd.Flags().BoolVar(&chatListFlag, "list", false, "with --addr: list resumable sessions on the engine and exit")
}
