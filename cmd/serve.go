package cmd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/api"
	"ai-agent-go-play/internal/artifact"
	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/frontend/telegram"
	"ai-agent-go-play/internal/logger"
	"ai-agent-go-play/internal/memory"
	"ai-agent-go-play/internal/provider"
	openaiprovider "ai-agent-go-play/internal/provider/openai"
	"ai-agent-go-play/internal/session"
	"ai-agent-go-play/internal/space"
	"ai-agent-go-play/internal/tools"
	"ai-agent-go-play/internal/usage"

	"github.com/spf13/cobra"
)

var (
	addrFlag              string
	serveNoPlanFlag       bool
	serveNoCritiqueFlag   bool
	serveMaxRevisionsFlag int
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the headless engine over HTTP+SSE",
	Long: "Expose the agent as a headless API: POST /runs starts a run, " +
		"GET /runs/{id}/events streams its events over SSE. Risky actions park on " +
		"GET /approvals and are resolved with POST /approvals/{id}.\n\n" +
		"Session turns are deliberate by default (chat-planner.md): each turn runs a " +
		"context-aware planner → stateless executor with a session-scoped, disk-backed artifact " +
		"cache; the conversation persists as the session's turn log, and a bounded critic→re-plan " +
		"loop revises a shortfall. Turn this off with --no-plan (bare executor) or --no-critique " +
		"(planner without the critique loop). These affect session turns (/sessions), not " +
		"one-shot /runs.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		// One human-gate queue, shared: the executor parks risky actions and ask_user
		// questions on it (as its HumanGate), and the HTTP endpoints resolve/answer them.
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

		workDir, err := resolveWorkspace()
		if err != nil {
			return err
		}

		// One global memory store, shared across runs: a fact remembered in one run is
		// recallable by later runs (the cross-run guarantee 4d is about). It is
		// workspace-local now (<workspace>/.agent/memory.json, spaces.md) — point
		// --workspace at a persistent dir so it survives restarts. Space shards layer
		// over it per turn via the session's active space.
		mem, err := memory.NewPersistentStore(memoryPath(workDir))
		if err != nil {
			return fmt.Errorf("failed to load memory store: %w", err)
		}
		spaces := space.NewStore(spacesDir(workDir))

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

		// Read the operator's prompt customization + agent-type catalog into a reloadable
		// holder: every run's executor reads the current snapshot (so the cached
		// system-prompt prefix stays stable within a run, prompts.md §0), and POST /reload
		// re-reads the files so edits take effect on the next run without a restart.
		promptSrc, err := newPromptState(workDir, tier)
		if err != nil {
			return err
		}
		deps := serveDeps{
			prov:       newProvider(cfg),
			workDir:    workDir,
			defaults:   newServeDefaults(resolveModel(modelFlag, cfg), tier),
			gate:       approvals,
			registry:   registry,
			mem:        mem,
			central:    rec,
			ledger:     usage.NewLedger(rec), // rec is the process-wide log (a Reader)
			reader:     rec,                  // same log, read side, for recent_activity
			prompts:    promptSrc,
			limits:        resolveAgentLimits(cfg),
			spawnDepth:    resolveSpawnDepth(cfg),
			contextLimits: cfg.ContextLimits,
			sessions:      sessions,
			secrets:       secretsResolver(cfg),
			spaces:        spaces,
			spaceMems:     newSpaceMemCache(spaces),
		}

		// Session turns are deliberate (planner + critique) by default; --no-plan / --no-critique
		// turn them off. Critique needs the deliberate pipeline, so --no-plan implies no critique.
		plan := !serveNoPlanFlag
		critique := plan && !serveNoCritiqueFlag

		engine := api.NewEngine(deps.runner())
		// Session turns run the deliberate planner→executor pipeline (chat-planner.md) unless
		// --no-plan drops back to the bare executor; one-shot /runs are unaffected either way.
		turns := deps.turnRunner()
		if plan {
			// PublishToRun lets the deliberate turn runner surface the brief on the run's
			// event stream out-of-band, the same seam the approval queue uses.
			turns = deps.deliberateTurnRunner(critique, serveMaxRevisionsFlag, engine.PublishToRun)
		}
		engine.EnableSessions(sessions, turns)
		// Tune the in-memory finished-run retention cap if configured (0 ⇒ keep the default).
		engine.SetMaxFinishedRuns(cfg.Limits.MaxFinishedRuns)
		// Record a run_usage event per completed run/turn into the process-wide log,
		// so token spend is browsable over GET /audit alongside every other effect.
		engine.SetAuditRecorder(rec)
		// Persist each finished run's metadata as info.json next to its transcript, so a run's
		// status survives eviction (maxFinishedRuns) and a restart (GET /runs/{id} then reads
		// it back). Best-effort — skipped if the runs dir can't be resolved.
		if runsBase, err := runsDir(); err == nil {
			engine.SetRunStore(fileRunStore{base: runsBase})
		}
		// Closing a session archives its conversation (recoverable), so its scratch cache is
		// reaped keeping any user-provided files (re-derivable agent artifacts go); a purge is
		// an explicit whole-session deletion, so it takes everything. This is the reaper for
		// session-scratch/.
		engine.SetSessionCloseHook(func(sessionID string, purge bool) {
			dir, err := sessionScratchDir(sessionID)
			if err != nil {
				return
			}
			if purge {
				_ = os.RemoveAll(dir)
			} else {
				_ = artifact.ReapScratch(dir)
			}
		})
		// Push parked escalations (and their resolutions) onto the owning run's event
		// stream, so a streaming frontend learns of them without polling /approvals.
		approvals.SetEmitter(engine.PublishToRun)

		srv := api.NewServer(engine, approvals, registry, rec, rec)

		// Mount the engine's HTTP surface under a thin outer mux that also serves
		// POST /reload — a management-plane trigger to re-read the prompt files +
		// agent-type catalog from disk. The handler lives here (not in the api package)
		// because reloading is a cmd concern: it knows the config-dir/workspace paths and
		// the tier gate. A method+path pattern outranks the "/" catch-all, so /reload wins.
		mux := http.NewServeMux()
		mux.HandleFunc("POST /reload", func(w http.ResponseWriter, r *http.Request) {
			// Re-read config defaults first (cheap, validated) so a malformed config.json or a
			// bad tier aborts the whole reload before anything is applied — no partial reload.
			cfg2, err := loadConfig()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			tier2, err := resolveTier(tierFlag, cfg2)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := promptSrc.reload(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			// Retune the engine's default model + tier ceiling. Flag/env precedence is
			// re-applied, so a --model/--tier launched engine keeps that choice; only a
			// config-sourced default moves. Per-session/per-turn overrides are unaffected and
			// still clamp to this (possibly new) ceiling. The prompt tier gate stays at the
			// startup tier (which workspace-tier prompt files loaded is fixed for the process).
			deps.defaults.set(resolveModel(modelFlag, cfg2), tier2)
			fmt.Fprintln(os.Stderr, "reloaded prompts, agent types, and config defaults (model, tier)")
			w.WriteHeader(http.StatusNoContent)
		})
		mux.Handle("/", srv)

		// Optional Telegram frontend: a peer client of this same engine over localhost.
		// Active only when a token is configured; the engine runs unchanged otherwise.
		// Started in the background so the Bot API handshake never delays serving.
		go startTelegramIfConfigured(cfg)

		if plan {
			fmt.Fprintf(os.Stderr, "deliberate session turns: planner on, critique %s\n", onOff(critique))
		} else {
			fmt.Fprintln(os.Stderr, "session turns: bare executor (--no-plan)")
		}
		fmt.Fprintf(os.Stderr, "engine listening on %s\n", addrFlag)
		fmt.Fprintf(os.Stderr, "  start:  curl -XPOST %s/runs -d '{\"task\":\"...\"}'\n", "http://"+addrFlag)
		fmt.Fprintf(os.Stderr, "  stream: curl -N %s/runs/<id>/events\n", "http://"+addrFlag)
		fmt.Fprintf(os.Stderr, "  reload: curl -XPOST %s/reload\n", "http://"+addrFlag)
		return http.ListenAndServe(addrFlag, mux)
	},
}

// serveDeps holds everything needed to build a per-run executor. The shared
// process-wide state (catalog, gate, memory, central audit) is consistent across
// runs and with the API; per-run state (transcript, session audit file) is fresh.
type serveDeps struct {
	prov       *openaiprovider.Client
	workDir    string
	defaults   *serveDefaults // engine-wide default model + tier ceiling (reloadable)
	gate       tools.HumanGate
	registry   tools.Registry
	mem        memory.Store
	central    audit.Recorder
	ledger     tools.UsageLedger // durable session/day token totals for the usage tool
	reader     audit.Reader      // process-wide log, for the recent_activity tool
	prompts    *promptState      // reloadable prompt customization + agent-type catalog
	limits     agent.Limits      // per-run bounds (from config)
	spawnDepth int               // sub-agent delegation budget (from config)
	sessions   session.Store     // durable session store, for the read-only session tools
	// contextLimits overrides the context-window size per model id for the usage gauge (from
	// config.json context_limits). Fixed at startup; the per-turn model resolves against it.
	contextLimits map[string]int
	// secrets resolves a named secret for the broker to inject into an authored tool's
	// brokered HTTP request, host-side (config `secrets`). Nil ⇒ no secret store.
	secrets func(name string) (string, bool)
	// spaces + spaceMems wire switchable data contexts (spaces.md): the store manages
	// <workspace>/.agent/spaces/, the cache shares one memory shard per space across
	// concurrent sessions so writes serialize instead of racing whole-file rewrites.
	spaces    *space.Store
	spaceMems *spaceMemCache
}

// turnIO is the per-run transcript + audit wiring opened once for a run/turn. It is kept
// separate from executor construction so the deliberate pipeline (serve --plan) can build
// several agents — planner, executor, critique re-runs — that all log to the same transcript
// under one runID, instead of each rebuild truncating run.jsonl (logger.NewWithID → os.Create).
type turnIO struct {
	rec     audit.Recorder
	obsAll  agent.Observer
	cleanup func()
}

// openTurnIO opens the per-run transcript + session audit for runID and fans effects to both
// the transcript and the process-wide log (GET /audit). obsAll layers the logger observer
// under the caller's obs (the engine hub + usage accumulator).
func (d serveDeps) openTurnIO(runID string, obs agent.Observer) (turnIO, error) {
	runsBase, err := runsDir()
	if err != nil {
		return turnIO{}, err
	}
	log, err := logger.NewWithID(runsBase, runID)
	if err != nil {
		return turnIO{}, fmt.Errorf("failed to create logger: %w", err)
	}
	sessionRec, err := audit.NewJSONLRecorder(filepath.Join(log.SessionDir, "audit.jsonl"))
	if err != nil {
		log.Close()
		return turnIO{}, fmt.Errorf("failed to open audit log: %w", err)
	}
	return turnIO{
		rec:     audit.Recorders{sessionRec, d.central},
		obsAll:  agent.Observers{agent.NewLoggerObserver(log), obs},
		cleanup: func() { sessionRec.Close(); log.Close() },
	}, nil
}

// serveDefaults holds the engine-wide default model and tier ceiling, re-readable at runtime
// so POST /reload can pick up config.json edits without a restart. resolveOpts reads a
// snapshot; the reload handler swaps in freshly-resolved values. Flag/env precedence is
// re-applied on each reload (via resolveModel/resolveTier), so an engine launched with an
// explicit --model/--tier keeps that choice — only a config-sourced default moves.
type serveDefaults struct {
	mu    sync.RWMutex
	model string
	tier  capability.Tier
}

func newServeDefaults(model string, tier capability.Tier) *serveDefaults {
	return &serveDefaults{model: model, tier: tier}
}

func (s *serveDefaults) snapshot() (string, capability.Tier) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.model, s.tier
}

func (s *serveDefaults) set(model string, tier capability.Tier) {
	s.mu.Lock()
	s.model, s.tier = model, tier
	s.mu.Unlock()
}

// resolveOpts applies a request's per-run overrides over the serve defaults: model falls back
// to the default model, and an explicit tier is validated and CLAMPED to no looser than the
// default tier (the serve-configured ceiling). An empty tier inherits the ceiling; an invalid
// one is an error. The defaults are snapshotted (they may have been retuned by POST /reload).
// This is where the trust policy lives — the engine passes RunOptions through untouched.
func (d serveDeps) resolveOpts(opts api.RunOptions) (model string, tier capability.Tier, err error) {
	model, tier = d.defaults.snapshot()
	if opts.Model != "" {
		model = opts.Model
	}
	if opts.Tier != "" {
		req, perr := capability.ParseTier(opts.Tier)
		if perr != nil {
			return "", "", perr
		}
		tier = capability.ClampTier(req, tier)
	}
	return model, tier, nil
}

// newExecutor builds a fresh executor over already-open per-turn IO (rec + obs), using the
// resolved model + tier for this run. spaceID is the active space (from the session sticky /
// per-request override): it scopes memory to that space's shard over the global store and
// loads the space's notes into the prompt; "" runs in the global scope. manifest + scratchDir
// wire the artifact cache (nil/"" for the plain, non-deliberate path). It opens at most the
// space's memory shard (cached process-wide), so it is safe to call repeatedly within one
// turn (the deliberate pipeline does). An unknown spaceID is an error — failing the turn
// loudly beats silently writing memory into the wrong scope.
func (d serveDeps) newExecutor(runID, sessionID, model string, tier capability.Tier, spaceID string, manifest *artifact.Manifest, scratchDir string, rec audit.Recorder, obs agent.Observer) (*agent.Agent, error) {
	usageCtx := tools.UsageContext{SessionID: sessionID, Ledger: d.ledger}
	// Snapshot the current prompts + catalog once, so a concurrent /reload can't change the
	// executor's prompt mid-run (prompts.md §0).
	prompts, catalog := d.prompts.snapshot()
	mem, spaceNote, err := spaceScope(d.spaces, spaceID, d.mem, d.spaceMems)
	if err != nil {
		return nil, err
	}
	spaceCtx := tools.SpaceContext{Store: d.spaces, ActiveID: spaceID}
	if sessionID != "" {
		// switch_space persists the session's sticky space through the store; the engine
		// re-reads the session before saving the turn's history, so the change survives
		// (and applies from the next turn). One-shot runs have no session ⇒ no Switch.
		spaceCtx.Switch = func(id string) error {
			sess, err := d.sessions.Get(sessionID)
			if err != nil {
				return err
			}
			sess.Space = id
			return d.sessions.Save(sess)
		}
	}
	return agent.NewExecutor(agent.ExecutorConfig{
		Provider: d.prov, WorkDir: d.workDir, Model: model, RunID: runID,
		Observer: obs, Registry: d.registry, Memory: mem, Docs: selfDocs,
		Audit: rec, Tier: tier, Gate: d.gate,
		Usage: usageCtx, AuditReader: d.reader,
		SystemPromptOverride: prompts.Override, PromptAppends: withSpaceNote(prompts.Appends, spaceNote),
		AgentCatalog: catalog, SpawnDepth: d.spawnDepth,
		Space:      spaceCtx,
		StatusDirs: agentStateDirs(d.workDir), Limits: d.limits,
		ContextLimit: contextLimitFor(model, d.contextLimits),
		Sessions:     d.sessions,
		Manifest:     manifest, ScratchDir: scratchDir,
		Secrets: d.secrets,
	}), nil
}

// buildExecutor constructs a fresh executor for one run/turn, keyed by the engine's runID
// (so the transcript, audit Run field, and parked approvals share one id) and using the
// resolved model + tier + active space. It returns a cleanup to defer. Shared by the plain
// runner and the non-deliberate session turn runner.
func (d serveDeps) buildExecutor(runID, sessionID, model string, tier capability.Tier, spaceID string, obs agent.Observer) (*agent.Agent, func(), error) {
	io, err := d.openTurnIO(runID, obs)
	if err != nil {
		return nil, nil, err
	}
	ex, err := d.newExecutor(runID, sessionID, model, tier, spaceID, nil, "", io.rec, io.obsAll)
	if err != nil {
		io.cleanup()
		return nil, nil, err
	}
	return ex, io.cleanup, nil
}

// runner drives a single-shot run: a fresh executor with no prior context.
func (d serveDeps) runner() api.Runner {
	return api.RunnerFunc(func(ctx context.Context, runID, task string, opts api.RunOptions, obs agent.Observer) (string, error) {
		model, tier, err := d.resolveOpts(opts)
		if err != nil {
			return "", err
		}
		ex, cleanup, err := d.buildExecutor(runID, "", model, tier, opts.Space, obs)
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
	return api.TurnRunnerFunc(func(ctx context.Context, runID, sessionID string, prior []provider.Message, text string, opts api.RunOptions, obs agent.Observer) (string, []provider.Message, error) {
		model, tier, err := d.resolveOpts(opts)
		if err != nil {
			return "", nil, err
		}
		ex, cleanup, err := d.buildExecutor(runID, sessionID, model, tier, opts.Space, obs)
		if err != nil {
			return "", nil, err
		}
		defer cleanup()
		ex.Restore(prior)
		answer, err := ex.Run(ctx, text)
		return answer, ex.Messages(), err
	})
}

// deliberateTurnRunner drives one session turn through the chat-planner pipeline (serve
// --plan): a context-aware planner → stateless executor, with a session-scoped disk-backed
// artifact cache, and — when critique is on — a bounded critic→re-plan loop. This lifts the
// deliberation from CLI-only (chat-planner.md §7) to the engine: the planner's ask_user
// routes through the engine's queue gate (d.gate) like the executor's, so a clarification
// reaches whichever frontend owns the session.
//
// The conversation is the session's persisted turn log: prior user/answer message pairs are
// reconstructed into the turn log the planner reads, and the turn appends one clean
// user+answer pair back — the "turn log stored on the filesystem" (the session store), with
// no executor tool-call cruft. Working data lives in the session scratch dir + manifest,
// which persist across turns and restarts, keyed by session id.
func (d serveDeps) deliberateTurnRunner(critique bool, maxRevisions int, publish func(runID string, ev api.Event)) api.TurnRunner {
	return api.TurnRunnerFunc(func(ctx context.Context, runID, sessionID string, prior []provider.Message, text string, opts api.RunOptions, obs agent.Observer) (string, []provider.Message, error) {
		model, tier, err := d.resolveOpts(opts)
		if err != nil {
			return "", nil, err
		}
		io, err := d.openTurnIO(runID, obs)
		if err != nil {
			return "", nil, err
		}
		defer io.cleanup()

		// Session-scoped scratch + manifest, persistent across turns/restarts (keyed by
		// session id), so cached artifacts survive between turns (chat-planner.md §D5).
		scratchDir, err := sessionScratchDir(sessionID)
		if err != nil {
			return "", nil, err
		}
		if err := os.MkdirAll(scratchDir, 0o755); err != nil {
			return "", nil, fmt.Errorf("failed to create session scratch dir: %w", err)
		}
		manifest, err := artifact.New(filepath.Join(scratchDir, "manifest.json"))
		if err != nil {
			return "", nil, fmt.Errorf("failed to open artifact manifest: %w", err)
		}

		// The planner + critic are background deliberation: internalize their observer so their
		// raw plan/verdict steps stay in the transcript (+ usage) but never reach the client
		// stream (agent.Internalized → api.Hub drops them). The executor keeps the full obs, so
		// its answer streams. The clean brief is surfaced separately as a KindBrief event.
		internal := agent.Internalized(io.obsAll)
		deps := deliberateDeps{
			buildExecutor: func() (*agent.Agent, error) {
				return d.newExecutor(runID, sessionID, model, tier, opts.Space, manifest, scratchDir, io.rec, io.obsAll)
			},
			buildPlanner: func(environment, manifestView string) (*agent.Agent, error) {
				prompts, _ := d.prompts.snapshot()
				// The planner's ask_user shares the engine gate + runID, so a clarification
				// routes to the session's frontend (not server stdin). It runs on the resolved
				// model like the executor (tier is executor-only — the planner has no broker).
				return agent.NewPlanner(d.prov, model, prompts.PlannerOverride, environment, manifestView, d.gate, runID, internal), nil
			},
			buildCritic: func() (*agent.Agent, error) {
				prompts, _ := d.prompts.snapshot()
				return agent.NewCritic(d.prov, model, prompts.CriticOverride, internal), nil
			},
			manifest:     manifest,
			critique:     critique,
			maxRevisions: maxRevisions,
			// Surface the clean brief + critique notes as first-class KindBrief events on the
			// run's stream, so a frontend renders the deliberation distinctly.
			onBrief: func(label, brief string) {
				text := brief
				if label != "" {
					text = "(" + label + ")\n" + brief
				}
				publish(runID, api.Event{Kind: api.KindBrief, Text: text})
			},
			onNote: func(msg string) {
				publish(runID, api.Event{Kind: api.KindBrief, Text: "note: " + msg})
			},
		}

		turnLog := messagesToTurnLog(prior)
		answer, err := runDeliberateTurn(ctx, deps, turnLog, text)
		if err != nil {
			return "", nil, err
		}
		return answer, appendTurnMessages(prior, text, answer), nil
	})
}

// startTelegramIfConfigured launches the optional Telegram bot when a token is
// configured (config or env). The bot is a peer client of this engine over the local
// HTTP transport — it drives api.Client exactly like `agent client`, with no special
// access, and gates callers by the user-id allowlist. When no token is set, or the
// token/connection is rejected, the engine simply runs without it.
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
	serveCmd.Flags().StringVar(&modelFlag, "model", "", modelFlagUsage)
	serveCmd.Flags().StringVar(&tierFlag, "tier", "", "trust tier: safe|balanced|permissive (overrides config; default: balanced)")
	serveCmd.Flags().BoolVar(&serveNoPlanFlag, "no-plan", false, "disable deliberate session turns (planning is ON by default): run the bare executor seeded with prior history instead")
	serveCmd.Flags().BoolVar(&serveNoCritiqueFlag, "no-critique", false, "disable the critique loop (ON by default with planning): after each answer a critic judges it and re-plans on a shortfall")
	serveCmd.Flags().IntVar(&serveMaxRevisionsFlag, "max-revisions", 1, "max planner re-plan cycles in the critique loop before delivering the best answer")
}
