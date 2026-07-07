package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/artifact"
	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/logger"
	"ai-agent-go-play/internal/memory"
	"ai-agent-go-play/internal/tools"

	"github.com/spf13/cobra"
)

var (
	chatNoPlanFlag       bool
	chatNoCritiqueFlag   bool
	chatMaxRevisionsFlag int
	chatAddrFlag         string
	chatSessionFlag      string
	chatListFlag         bool
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

		// Deliberate planning + the critique loop are ON by default; --no-plan / --no-critique
		// turn them off. Critique needs the deliberate pipeline, so --no-plan implies no critique.
		plan := !chatNoPlanFlag
		critique := plan && !chatNoCritiqueFlag

		prov := newProvider(cfg)
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

		// Deliberate chat (--plan) is disk-backed: a session-scoped scratch dir holds working
		// artifacts and a manifest indexes them (chat-planner.md §D3–D5). Both nil in bare chat,
		// so the executor built below is unchanged there. The manifest lives under the run's
		// session dir, so it is namespaced per session and reaped with the transcript.
		var manifest *artifact.Manifest
		var scratchDir string
		if plan {
			scratchDir = filepath.Join(log.SessionDir, "scratch")
			if err := os.MkdirAll(scratchDir, 0o755); err != nil {
				return fmt.Errorf("failed to create scratch dir: %w", err)
			}
			manifest, err = artifact.New(filepath.Join(scratchDir, "manifest.json"))
			if err != nil {
				return fmt.Errorf("failed to open artifact manifest: %w", err)
			}
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
				AgentCatalog: catalog, SpawnDepth: resolveSpawnDepth(cfg),
				StatusDirs: agentStateDirs(), Limits: resolveAgentLimits(cfg),
				// nil/"" in bare chat ⇒ no record_artifact and no scratch note (unchanged).
				Manifest: manifest, ScratchDir: scratchDir,
			}), nil
		}

		// buildPlanner constructs a fresh planner, re-reading PLANNER.md each time so an edit
		// takes effect on the next planned turn without a restart (mirrors buildExecutor for
		// the pre-execution clarify/refine pass). It is fed the executor's live environment and
		// the rendered artifact manifest so it plans in context (chat-planner.md §D4/§D6). Used
		// per turn only when --plan is set.
		buildPlanner := func(environment, manifestView string) (*agent.Agent, error) {
			prompts, err := loadPrompts(workDir, tier)
			if err != nil {
				return nil, err
			}
			// CLI clarifications read from stdin (nil ⇒ StdinGate); runID ties them to this run.
			return agent.NewPlanner(prov, model, prompts.PlannerOverride, environment, manifestView, tools.StdinGate{}, log.RunID, obs), nil
		}

		// buildCritic constructs the verdict-emitting critic for the --critique loop (§9),
		// re-reading CRITIC.md each turn so an edit takes effect without a restart (like
		// buildPlanner). Tools-light: it only judges the answer against the brief's success
		// criteria. The Verdict schema is enforced in code, so an override cannot break it.
		buildCritic := func() (*agent.Agent, error) {
			prompts, err := loadPrompts(workDir, tier)
			if err != nil {
				return nil, err
			}
			return agent.NewCritic(prov, model, prompts.CriticOverride, obs), nil
		}

		// One executor for the whole session: its conversation persists across turns.
		executor, err := buildExecutor()
		if err != nil {
			return err
		}

		critiqueStatus := "off"
		if critique {
			critiqueStatus = fmt.Sprintf("on (max %d revisions)", chatMaxRevisionsFlag)
		}
		fmt.Fprintf(os.Stderr, "agent chat — model %s, tier %s, planner %s, critique %s, verbose %s\n", executor.Model(), tier, onOff(plan), critiqueStatus, onOff(trace.Enabled()))
		fmt.Fprintf(os.Stderr, "session %s  (/new or /reset to clear, /reload to re-read prompts+agents, /verbose to toggle the trace, /exit or Ctrl-D to quit)\n", log.RunID)

		// SIGINT cancels the current turn rather than killing the session; drained at
		// each prompt so a stray Ctrl-C while idle doesn't cancel the next turn.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt)
		defer signal.Stop(sigCh)

		// In --plan mode the conversation is a loop-owned turn log (chat-planner.md §D6): the
		// executor is stateless and rebuilt per turn, the planner reads this log each turn. In
		// bare chat it stays nil (the persistent executor holds the history instead).
		var turnLog []chatTurn

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
			// /attach <path> registers a user-provided file in the artifact manifest with
			// origin:user (chat-planner.md §D4 — explicit attach only, no prose sniffing). The
			// executor then reads it by path like any other artifact. Requires --plan (the
			// manifest only exists there).
			if arg, ok := strings.CutPrefix(line, "/attach"); ok {
				path := strings.TrimSpace(arg)
				if manifest == nil {
					fmt.Fprintln(os.Stderr, "/attach requires --plan")
					continue
				}
				if path == "" {
					fmt.Fprintln(os.Stderr, "usage: /attach <path>")
					continue
				}
				abs, err := filepath.Abs(path)
				if err != nil {
					fmt.Fprintf(os.Stderr, "/attach: %v\n", err)
					continue
				}
				if _, err := os.Stat(abs); err != nil {
					fmt.Fprintf(os.Stderr, "/attach: %v\n", err)
					continue
				}
				if err := manifest.Append(artifact.Entry{Path: abs, Origin: artifact.OriginUser, Description: "user-attached file"}); err != nil {
					fmt.Fprintf(os.Stderr, "/attach: %v\n", err)
					continue
				}
				fmt.Fprintf(os.Stderr, "(attached %s)\n", abs)
				continue
			}
			switch line {
			case "":
				continue
			case "/exit", "/quit":
				return nil
			case "/reset", "/new":
				// /new + /reset are aliases (matching the remote REPL and Telegram bot):
				// "start fresh" means clearing the conversation. In --plan mode that is the
				// loop-owned turn log; in bare chat it is the persistent executor's history.
				// (The scratch artifacts persist for the session either way — /reset clears
				// the dialogue, not the materialized data.)
				if plan {
					turnLog = nil
				} else {
					executor.Reset()
				}
				fmt.Fprintln(os.Stderr, "(conversation cleared)")
				continue
			case "/reload":
				// In --plan mode prompts + agent types are re-read on every turn (the
				// executor and planner are rebuilt per turn), so /reload is a no-op there.
				if plan {
					fmt.Fprintln(os.Stderr, "(prompts and agent types are re-read each turn in --plan mode)")
					continue
				}
				// Bare chat: re-read prompt files + agent-type catalog and rebuild the
				// executor, carrying the conversation forward. On failure (e.g. a malformed
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
			if plan {
				// Deliberate turn: planner (context-aware) → executor (stateless) → optional
				// critique loop. The loop owns the conversation, so append this turn on success.
				answer, err := runPlannedTurn(sigCh, deliberateDeps{
					buildExecutor: buildExecutor,
					buildPlanner:  buildPlanner,
					buildCritic:   buildCritic,
					manifest:      manifest,
					critique:      critique,
					maxRevisions:  chatMaxRevisionsFlag,
				}, turnLog, line)
				if err != nil {
					fmt.Fprintf(os.Stderr, "error: %v\n", err)
				} else {
					turnLog = append(turnLog, chatTurn{User: line, Answer: answer})
				}
			} else if err := runTurn(sigCh, executor, line); err != nil {
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

// runTurn runs one bare-chat turn (no planner) under a cancellable context — Ctrl-C cancels
// just this turn. The persistent executor holds the conversation itself.
func runTurn(sigCh <-chan os.Signal, executor *agent.Agent, line string) error {
	return underTurnContext(sigCh, func(ctx context.Context) error {
		result, err := executor.Run(ctx, line)
		if err != nil {
			return err
		}
		fmt.Println(result)
		return nil
	})
}

// underTurnContext runs fn under a context cancelled by Ctrl-C, so a stray SIGINT ends the
// current turn rather than the session. It drains any Ctrl-C that arrived while idle first.
func underTurnContext(sigCh <-chan os.Signal, fn func(ctx context.Context) error) error {
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
	return fn(ctx)
}

// runPlannedTurn runs one deliberate CLI turn: it wraps the shared runDeliberateTurn
// (cmd/deliberate.go) in the Ctrl-C context and surfaces the brief + critique notes to
// stderr. It prints the answer and returns it for the loop to append to the turn log.
func runPlannedTurn(sigCh <-chan os.Signal, deps deliberateDeps, turnLog []chatTurn, line string) (string, error) {
	deps.onBrief = func(label, brief string) {
		if label == "" {
			fmt.Fprintf(os.Stderr, "[brief]\n%s\n\n", brief)
		} else {
			fmt.Fprintf(os.Stderr, "[brief · %s]\n%s\n\n", label, brief)
		}
	}
	deps.onNote = func(msg string) { fmt.Fprintf(os.Stderr, "[critic] %s\n", msg) }

	var answer string
	err := underTurnContext(sigCh, func(ctx context.Context) error {
		a, err := runDeliberateTurn(ctx, deps, turnLog, line)
		if err != nil {
			return err
		}
		answer = a
		fmt.Println(answer)
		return nil
	})
	return answer, err
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func init() {
	chatCmd.Flags().StringVar(&modelFlag, "model", "", modelFlagUsage)
	chatCmd.Flags().StringVar(&tierFlag, "tier", "", "trust tier: safe|balanced|permissive (overrides config; default: balanced)")
	chatCmd.Flags().BoolVar(&chatNoPlanFlag, "no-plan", false, "disable deliberate mode (planning is ON by default): run the bare executor with retained context instead of planner → stateless executor")
	chatCmd.Flags().BoolVar(&chatNoCritiqueFlag, "no-critique", false, "disable the critique loop (ON by default with planning): after each answer a critic judges it against the plan's success criteria and re-plans on a shortfall")
	chatCmd.Flags().IntVar(&chatMaxRevisionsFlag, "max-revisions", 1, "max planner re-plan cycles in the critique loop before delivering the best answer")
	chatCmd.Flags().BoolVar(&verboseFlag, "verbose", false, "start with the tool-call trace on (default off; toggle live with /verbose)")
	chatCmd.Flags().BoolVar(&quietFlag, "quiet", false, "start with the tool-call trace off (the default; overrides config/env)")
	chatCmd.Flags().StringVar(&chatAddrFlag, "addr", "", "drive a running engine's persistent session instead of an in-process executor (host:port or an alias from `agent config set-engine`)")
	chatCmd.Flags().StringVar(&chatSessionFlag, "session", "", "with --addr: resume this session id instead of starting a new one")
	chatCmd.Flags().BoolVar(&chatListFlag, "list", false, "with --addr: list resumable sessions on the engine and exit")
}
