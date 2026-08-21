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
	"ai-agent-go-play/internal/api"
	"ai-agent-go-play/internal/artifact"
	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/logger"
	"ai-agent-go-play/internal/memory"
	"ai-agent-go-play/internal/provider"
	"ai-agent-go-play/internal/session"
	"ai-agent-go-play/internal/space"
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
	chatSpaceFlag        string
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
		"Commands (local): /new (alias /reset) clears the conversation, /model [id] and /tier " +
		"[safe|balanced|permissive] show or switch the model/tier for the session, /space " +
		"[name|list|-] shows or switches the active space (a named memory context with its own " +
		"always-loaded guidance), /compact " +
		"summarizes the conversation so far to free context, /verbose [on|off] toggles the " +
		"trace and the full planner brief (off shows a one-line brief summary), /exit (or " +
		"Ctrl-D) quits.\n" +
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

		// recent_activity reads durable, process-wide history. The per-run recorder
		// above remains the write sink for this local chat's brokered effects.
		centralReader, err := openCentralAuditReader()
		if err != nil {
			return fmt.Errorf("failed to open central audit reader: %w", err)
		}

		catPath, err := catalogPath()
		if err != nil {
			return err
		}
		registry, err := tools.NewPersistentRegistry(catPath)
		if err != nil {
			return fmt.Errorf("failed to load tool catalog: %w", err)
		}

		// Memory is workspace-local (<workspace>/.agent/, spaces.md): a global store plus
		// per-space shards. activeSpace is this REPL's active space id ("" ⇒ global scope),
		// seeded by --space and switched live by /space or the switch_space tool. A mid-turn
		// tool switch sets spaceDirty; the loop applies it after the turn (bare chat rebuilds
		// the executor; deliberate mode rebuilds per turn anyway).
		globalMem, err := memory.NewPersistentStore(memoryPath(workDir))
		if err != nil {
			return fmt.Errorf("failed to load memory store: %w", err)
		}
		spaces := space.NewStore(spacesDir(workDir))
		activeSpace := ""
		if chatSpaceFlag != "" {
			sp, err := spaces.Resolve(chatSpaceFlag)
			if err != nil {
				return fmt.Errorf("--space: %w", err)
			}
			activeSpace = sp.ID
		}
		spaceDirty := false

		// Read-only access to past conversations (sessions written by serve / Telegram / a
		// remote chat), so the agent can revisit "what did we discuss last time". Local chat's
		// own conversation is in-process — not in this store — so there's no "current" id. Kept
		// as a nil interface when the path can't resolve, so the tools are simply omitted.
		var sessReader tools.SessionReader
		if dir, err := sessionStorePath(); err == nil {
			sessReader = session.NewFileStore(dir)
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
			workspaceGuidance, err := workspaceGuidanceStore(workDir, nil).Get()
			if err != nil {
				return nil, fmt.Errorf("load workspace guidance: %w", err)
			}
			catalog, err := loadAgentCatalog(workDir, tier)
			if err != nil {
				return nil, err
			}
			// Scope memory to the active space and load its guidance into the prompt; the
			// switch_space tool records a change that takes effect at the next (re)build.
			mem, spaceGuidance, err := spaceScope(spaces, activeSpace, globalMem, nil)
			if err != nil {
				return nil, err
			}
			return agent.NewExecutor(agent.ExecutorConfig{
				Provider: prov, WorkDir: workDir, Model: model, RunID: log.RunID,
				Observer: obs, Registry: registry, Memory: mem, Docs: selfDocs,
				Audit: rec, Tier: tier, Gate: tools.StdinGate{},
				Usage: tools.UsageContext{}, AuditReader: centralReader,
				SystemPromptOverride: prompts.Override, PromptAppends: withGuidance(prompts.Appends, workspaceGuidance, spaceGuidance, ""),
				AgentCatalog: catalog, SpawnDepth: resolveSpawnDepth(cfg),
				Space: tools.SpaceContext{Store: spaces, ActiveID: activeSpace, Switch: func(id string) error {
					activeSpace, spaceDirty = id, true
					return nil
				}},
				StatusDirs: agentStateDirs(workDir), Limits: resolveAgentLimits(cfg),
				ContextLimit: resolveContextLimit(model, cfg),
				Sessions:     sessReader,
				Secrets:      secretsResolver(cfg),
				SecretNames:  secretNames(cfg),
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

		// applyConfigChange makes a live /model or /tier change take effect. In bare chat the
		// persistent executor is rebuilt (carrying the conversation forward, like /reload); in
		// --plan mode the per-turn rebuild reads the updated model/tier on the next turn, so
		// there is nothing to rebuild now. buildExecutor/buildPlanner/buildCritic close over the
		// model/tier variables, so reassigning them is what the rebuild picks up.
		applyConfigChange := func(what string) {
			if plan {
				fmt.Fprintf(os.Stderr, "(%s; effective next turn)\n", what)
				return
			}
			next, err := buildExecutor()
			if err != nil {
				fmt.Fprintf(os.Stderr, "(%s, but rebuild failed — keeping current setup: %v)\n", what, err)
				return
			}
			next.Restore(executor.Messages())
			executor = next
			fmt.Fprintf(os.Stderr, "(%s)\n", what)
		}

		critiqueStatus := "off"
		if critique {
			critiqueStatus = fmt.Sprintf("on (max %d revisions)", chatMaxRevisionsFlag)
		}
		fmt.Fprintf(os.Stderr, "agent chat — model %s, tier %s, planner %s, critique %s, verbose %s\n", executor.Model(), tier, onOff(plan), critiqueStatus, onOff(trace.Enabled()))
		if activeSpace != "" {
			fmt.Fprintf(os.Stderr, "active space: %s\n", activeSpace)
		}
		fmt.Fprintf(os.Stderr, "session %s  (/new or /reset to clear, /model /tier /space to switch, /reload to re-read prompts+agents, /verbose to toggle the trace, /exit or Ctrl-D to quit)\n", log.RunID)

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
			// /model [id] shows or sets the model for the session (local mode). No arg prints
			// the current one; an id switches to it, taking effect immediately in bare chat and
			// on the next turn in --plan mode.
			if arg, ok := strings.CutPrefix(line, "/model"); ok {
				arg = strings.TrimSpace(arg)
				if arg == "" {
					fmt.Fprintf(os.Stderr, "(model: %s; usage: /model <id>)\n", modelLabel(model))
					continue
				}
				model = arg
				applyConfigChange("model set to " + arg)
				continue
			}
			// /tier [safe|balanced|permissive] shows or sets the trust tier for the session.
			if arg, ok := strings.CutPrefix(line, "/tier"); ok {
				arg = strings.TrimSpace(arg)
				if arg == "" {
					fmt.Fprintf(os.Stderr, "(tier: %s; usage: /tier safe|balanced|permissive)\n", tier)
					continue
				}
				t, err := capability.ParseTier(arg)
				if err != nil {
					fmt.Fprintf(os.Stderr, "%v\n", err)
					continue
				}
				tier = t
				applyConfigChange("tier set to " + string(t))
				continue
			}
			// /space manages the active space (spaces.md §5): no arg shows the current one,
			// `list` lists them all, `-` returns to the global scope, anything else switches
			// to that space (by id or name). Switching rebuilds the executor in bare chat
			// (scoped memory + guidance apply immediately); deliberate mode picks it up next turn.
			if arg, ok := strings.CutPrefix(line, "/space"); ok {
				arg = strings.TrimSpace(arg)
				switch arg {
				case "":
					if activeSpace == "" {
						fmt.Fprintln(os.Stderr, "(no space active — global scope; usage: /space <name>, /space list, /space -)")
					} else {
						fmt.Fprintf(os.Stderr, "(space: %s; usage: /space <name>, /space list, /space - for global)\n", activeSpace)
					}
				case "list":
					all, err := spaces.List()
					if err != nil {
						fmt.Fprintf(os.Stderr, "/space list: %v\n", err)
						continue
					}
					if len(all) == 0 {
						fmt.Fprintln(os.Stderr, "(no spaces yet — ask the agent to create one, e.g. \"create a space for my Polish lessons\")")
						continue
					}
					for _, sp := range all {
						marker := "  "
						if sp.ID == activeSpace {
							marker = "* "
						}
						fmt.Fprintf(os.Stderr, "%s%s (%q)\n", marker, sp.ID, sp.Name)
					}
				case "-":
					activeSpace = ""
					applyConfigChange("space cleared (global scope)")
				default:
					sp, err := spaces.Resolve(arg)
					if err != nil {
						fmt.Fprintf(os.Stderr, "/space: %v\n", err)
						continue
					}
					activeSpace = sp.ID
					applyConfigChange("space set to " + sp.ID)
				}
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
			case "/compact":
				// Summarize the conversation so far into a compact briefing and replace the
				// working history with it, freeing context. Manual for now (auto-compaction on a
				// threshold is deferred); the on-disk transcript is append-only and untouched, so
				// this only shrinks the *live* context, not the durable record.
				if plan {
					if len(turnLog) <= 1 {
						fmt.Fprintln(os.Stderr, "(nothing to compact yet)")
						continue
					}
					summary, err := compactSummarize(sigCh, prov, model, renderTurnLog(turnLog[:len(turnLog)-1]))
					if err != nil {
						fmt.Fprintf(os.Stderr, "compact failed (conversation unchanged): %v\n", err)
						continue
					}
					kept := turnLog[len(turnLog)-1]
					n := len(turnLog) - 1
					turnLog = []chatTurn{{User: "[earlier conversation]", Answer: summary}, kept}
					fmt.Fprintf(os.Stderr, "(compacted %d earlier %s into a summary; the last turn is kept verbatim)\n", n, plural(n, "turn"))
					continue
				}
				msgs := executor.Messages()
				if len(msgs) == 0 {
					fmt.Fprintln(os.Stderr, "(nothing to compact yet)")
					continue
				}
				summary, err := compactSummarize(sigCh, prov, model, renderMessages(msgs))
				if err != nil {
					fmt.Fprintf(os.Stderr, "compact failed (conversation unchanged): %v\n", err)
					continue
				}
				executor.Restore([]provider.Message{provider.UserText("[Summary of the earlier conversation]\n" + summary)})
				fmt.Fprintln(os.Stderr, "(conversation compacted into a summary)")
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
				}, turnLog, line, trace.Enabled)
				if err != nil {
					fmt.Fprintf(os.Stderr, "error: %v\n", err)
				} else {
					turnLog = append(turnLog, chatTurn{User: line, Answer: answer})
				}
			} else if err := runTurn(sigCh, executor, line); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
			}
			// A switch_space tool call mid-turn re-pointed the active space; apply it now so
			// the next turn runs with the new scope (bare chat rebuilds the executor here,
			// deliberate mode rebuilds per turn anyway).
			if spaceDirty {
				spaceDirty = false
				label := "space set to " + activeSpace
				if activeSpace == "" {
					label = "space cleared (global scope)"
				}
				applyConfigChange(label)
			}
			// Per-turn usage is the accumulator's delta; also show the session total.
			turn := subUsage(before, usage.Total())
			fmt.Fprintf(os.Stderr, "%s   (session: %s in / %s out)\n",
				formatUsage(turn, usage.Steps()-beforeSteps, time.Since(turnStart)),
				humanInt(usage.Total().InputTokens), humanInt(usage.Total().OutputTokens))
			// Context-window fill after this turn (last request's input tokens vs the model's
			// window), so a long conversation's pressure is visible. Skipped when unknown.
			if line := formatContext(usage.LastInput(), resolveContextLimit(model, cfg)); line != "" {
				fmt.Fprintln(os.Stderr, line)
			}
		}
		return nil
	},
}

// compactSummarize runs the /compact model call under a cancellable context so Ctrl-C aborts
// it (leaving the conversation unchanged). It returns the compact briefing that replaces the
// earlier turns.
func compactSummarize(sigCh <-chan os.Signal, prov provider.Provider, model, transcript string) (string, error) {
	fmt.Fprintln(os.Stderr, "(compacting…)")
	var summary string
	err := underTurnContext(sigCh, func(ctx context.Context) error {
		s, err := agent.Summarize(ctx, prov, model, transcript)
		summary = s
		return err
	})
	return summary, err
}

// renderMessages renders a bare-chat conversation (provider messages) to a plain transcript
// for the summarizer: role-labelled text, including tool-result output, skipping empty turns.
func renderMessages(msgs []provider.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		var text strings.Builder
		for _, c := range m.Content {
			if c.Text != "" {
				text.WriteString(c.Text)
			}
			if c.ToolCall != nil {
				fmt.Fprintf(&text, "[calls %s]", c.ToolCall.Name)
			}
			if c.ToolResult != nil {
				text.WriteString(c.ToolResult.Output)
			}
		}
		if s := strings.TrimSpace(text.String()); s != "" {
			fmt.Fprintf(&b, "%s: %s\n", m.Role, s)
		}
	}
	return b.String()
}

// plural returns "<word>" or "<word>s" for n.
func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
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
// stderr. The brief is a one-line summary by default (the full block — context, success
// criteria, assumptions — is planner work product, noise in a chat); fullBrief (the
// /verbose gate) switches to the whole thing. The transcript keeps the full brief either
// way. It prints the answer and returns it for the loop to append to the turn log.
func runPlannedTurn(sigCh <-chan os.Signal, deps deliberateDeps, turnLog []chatTurn, line string, fullBrief func() bool) (string, error) {
	deps.onBrief = func(label, brief string) {
		switch {
		case fullBrief != nil && fullBrief():
			if label == "" {
				fmt.Fprintf(os.Stderr, "[brief]\n%s\n\n", brief)
			} else {
				fmt.Fprintf(os.Stderr, "[brief · %s]\n%s\n\n", label, brief)
			}
		case label == "":
			fmt.Fprintf(os.Stderr, "[brief] %s\n", api.SummarizeBrief(brief))
		default:
			fmt.Fprintf(os.Stderr, "[brief · %s] %s\n", label, api.SummarizeBrief(brief))
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

// modelLabel renders a model id for display, naming the built-in default when unset.
func modelLabel(model string) string {
	if model == "" {
		return agent.DefaultModel + " (built-in default)"
	}
	return model
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
	chatCmd.Flags().StringVar(&chatSpaceFlag, "space", "", "start with this space active (id or name; see /space). Local mode: scopes memory + loads the space's guidance")
}
