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

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/logger"
	"ai-agent-go-play/internal/memory"
	"ai-agent-go-play/internal/provider"
	openaiprovider "ai-agent-go-play/internal/provider/openai"
	"ai-agent-go-play/internal/tools"

	"github.com/spf13/cobra"
)

var chatPlanFlag bool

var chatCmd = &cobra.Command{
	Use:   "chat",
	Short: "Interactive multi-turn chat (REPL) with retained context",
	Long: "Start an interactive session: type a message, get a reply, and keep going — " +
		"the conversation history is retained across turns (like a chat CLI). The tool catalog " +
		"and memory are shared with the rest of this agent (see --config-dir); the audit trail " +
		"goes to this run's transcript, not the process-wide log that `agent serve` exposes.\n\n" +
		"Commands: /reset clears the conversation, /exit (or Ctrl-D) quits. Ctrl-C cancels the " +
		"current turn.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
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
		// keep the transcript on disk. The final answer of each turn goes to stdout.
		obs := agent.Observers{agent.NewLoggerObserver(log), agent.NewCLIObserver(os.Stderr)}

		// One executor for the whole session: its conversation persists across turns.
		executor := agent.NewExecutor(prov, workDir, model, log.RunID, obs, registry, mem, rec, tier, tools.StdinApprover{})

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

			if err := runTurn(sigCh, executor, prov, model, obs, line); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
			}
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
}
