package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/audit"
	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/logger"
	"ai-agent-go-play/internal/memory"
	"ai-agent-go-play/internal/provider"
	openaiprovider "ai-agent-go-play/internal/provider/openai"
	"ai-agent-go-play/internal/tools"

	"github.com/spf13/cobra"
	yaml "go.yaml.in/yaml/v3"
)

var (
	evalVariantsFlag string
	evalModelsFlag   []string
)

// evalVariant is one configuration to run the task under. The zero value means "the ambient
// defaults" (config model/tier, cwd workspace, config-dir + workspace prompt files); each set
// field overrides that. It mirrors the CLI's own knobs so a variant is a named, file-backed
// equivalent of a set of run flags. YAML field names match the CLI flags (snake_case).
type evalVariant struct {
	Name           string   `yaml:"name"`
	Model          string   `yaml:"model"`
	Tier           string   `yaml:"tier"`
	Workspace      string   `yaml:"workspace"`
	ContextFiles   []string `yaml:"context_files"`
	NoContextFiles bool     `yaml:"no_context_files"`

	// Inline prompt overrides, so a variant needn't ship a file: SystemPrompt replaces the
	// base executor prompt (like a SYSTEM.md), AgentsMD is appended (like an AGENTS.md). They
	// layer over any file-based prompts resolved for the variant's workspace.
	SystemPrompt string `yaml:"system_prompt"`
	AgentsMD     string `yaml:"agents_md"`

	// Per-variant limit + delegation overrides, layered over the ambient config limits (a
	// zero field inherits). Lets a sweep vary max_iterations, script_timeout_seconds, etc.
	Limits     ConfigLimits `yaml:"limits"`
	SpawnDepth int          `yaml:"spawn_depth"`
}

// evalResult is the outcome of running the task under one variant.
type evalResult struct {
	Variant evalVariant
	Model   string // the effective model id (after variant/config/default resolution)
	Output  string
	Usage   provider.Usage
	Steps   int
	Elapsed time.Duration
	Err     error
}

var evalCmd = &cobra.Command{
	Use:   "eval <task>",
	Short: "Run one task under N config variants and compare outputs + token usage",
	Long: "The experimentation harness: run the same task under several configurations " +
		"and print their outputs and token usage side by side, so prompt and organization " +
		"experiments can be measured, not guessed.\n\n" +
		"Variants come from a YAML file (--variants) and/or a quick model sweep (--models, one " +
		"variant per model). A variant overrides the ambient defaults with any of: model, tier, " +
		"workspace, context_files, no_context_files, inline system_prompt / agents_md, per-variant " +
		"limits (max_iterations, script_timeout_seconds, …), and spawn_depth.\n\n" +
		"Each variant runs the executor directly (no planner) with a fresh context, sharing this " +
		"agent's tool catalog and memory. Ctrl+C stops after the current variant.\n\n" +
		"Example variants.yaml:\n" +
		"  - name: baseline\n" +
		"  - name: inline-prompt\n" +
		"    system_prompt: |\n" +
		"      You are a terse assistant. Answer in one sentence.\n" +
		"  - name: deeper-loops\n" +
		"    model: gpt-4o\n" +
		"    limits: { max_iterations: 40 }",
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		variants, err := collectEvalVariants()
		if err != nil {
			return err
		}
		task := strings.Join(args, " ")

		// Ctrl+C cancels the in-flight variant; the loop then stops before the next one.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()

		prov := newProvider(cfg)

		// One shared tool catalog across variants (same as `run`): the comparison is about
		// prompt/model, not tool state, and sharing keeps the run realistic. Memory is
		// workspace-local now, so it is opened per variant (a variant can override the
		// workspace) — still the real store, same rationale.
		catPath, err := catalogPath()
		if err != nil {
			return err
		}
		registry, err := tools.NewPersistentRegistry(catPath)
		if err != nil {
			return fmt.Errorf("failed to load tool catalog: %w", err)
		}

		fmt.Fprintf(os.Stderr, "eval: %d variant(s) on task: %s\n", len(variants), task)
		results := make([]evalResult, 0, len(variants))
		for _, v := range variants {
			if ctx.Err() != nil {
				fmt.Fprintln(os.Stderr, "eval: interrupted — reporting completed variants")
				break
			}
			fmt.Fprintf(os.Stderr, "\n=== variant %q ===\n", v.Name)
			res := runEvalVariant(ctx, v, task, cfg, prov, registry)
			results = append(results, res)
			if res.Err != nil {
				fmt.Fprintf(os.Stderr, "  error: %v\n", res.Err)
			} else {
				fmt.Fprintf(os.Stderr, "  %s\n", formatUsage(res.Usage, res.Steps, res.Elapsed))
			}
		}

		fmt.Print(formatEvalReport(results))
		return nil
	},
}

// collectEvalVariants gathers the run configurations from --variants (a YAML list) and
// --models (one variant per model id). At least one source must yield a variant.
func collectEvalVariants() ([]evalVariant, error) {
	var variants []evalVariant
	if evalVariantsFlag != "" {
		loaded, err := loadEvalVariants(evalVariantsFlag)
		if err != nil {
			return nil, err
		}
		variants = append(variants, loaded...)
	}
	for _, m := range evalModelsFlag {
		if m = strings.TrimSpace(m); m != "" {
			variants = append(variants, evalVariant{Name: m, Model: m})
		}
	}
	if len(variants) == 0 {
		return nil, fmt.Errorf("no variants: pass --variants <file.yaml> and/or --models <a,b>")
	}
	// Default any unnamed variant to its 1-based position so the report is unambiguous.
	for i := range variants {
		if strings.TrimSpace(variants[i].Name) == "" {
			variants[i].Name = fmt.Sprintf("variant-%d", i+1)
		}
	}
	return variants, nil
}

// loadEvalVariants reads a YAML file holding a list of variants.
func loadEvalVariants(path string) ([]evalVariant, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("--variants %q: %w", path, err)
	}
	var variants []evalVariant
	if err := yaml.Unmarshal(b, &variants); err != nil {
		return nil, fmt.Errorf("--variants %q: %w", path, err)
	}
	if len(variants) == 0 {
		return nil, fmt.Errorf("--variants %q: no variants in file", path)
	}
	return variants, nil
}

// runEvalVariant runs the task once under one variant, returning its result (never
// panicking — a build/run error is captured in Err so the other variants still report).
func runEvalVariant(ctx context.Context, v evalVariant, task string, cfg Config, prov *openaiprovider.Client, registry tools.Registry) evalResult {
	res := evalResult{Variant: v}

	model := resolveModel(v.Model, cfg)
	tier, err := resolveTier(v.Tier, cfg)
	if err != nil {
		res.Err = err
		return res
	}

	workDir, prompts, catalog, err := resolveVariantContext(v, tier)
	if err != nil {
		res.Err = err
		return res
	}
	// The variant's workspace-local memory store (global scope; eval runs carry no session,
	// so no space is active) — the real store, matching `run`.
	mem, err := memory.NewPersistentStore(memoryPath(workDir))
	if err != nil {
		res.Err = fmt.Errorf("memory store: %w", err)
		return res
	}
	// Inline prompt overrides layer over the file-based ones: system_prompt replaces the base
	// (like SYSTEM.md), agents_md appends (like AGENTS.md).
	if v.SystemPrompt != "" {
		prompts.Override = v.SystemPrompt
	}
	if v.AgentsMD != "" {
		prompts.Appends = append(prompts.Appends, v.AgentsMD)
	}
	workspaceGuidance, err := workspaceGuidanceStore(workDir, nil).Get()
	if err != nil {
		res.Err = fmt.Errorf("load workspace guidance: %w", err)
		return res
	}

	// Per-variant limits/spawn-depth layered over the ambient config.
	limits := resolveAgentLimits(Config{Limits: cfg.Limits.merge(v.Limits)})
	spawnDepth := resolveSpawnDepth(cfg)
	if v.SpawnDepth > 0 {
		spawnDepth = v.SpawnDepth
	}

	runsBase, err := runsDir()
	if err != nil {
		res.Err = err
		return res
	}
	log, err := logger.New(runsBase)
	if err != nil {
		res.Err = fmt.Errorf("logger: %w", err)
		return res
	}
	defer log.Close()
	rec, err := audit.NewJSONLRecorder(filepath.Join(log.SessionDir, "audit.jsonl"))
	if err != nil {
		res.Err = fmt.Errorf("audit log: %w", err)
		return res
	}
	defer rec.Close()

	usageObs := agent.NewUsageObserver()
	obs := agent.Observers{agent.NewLoggerObserver(log), usageObs}

	executor := agent.NewExecutor(agent.ExecutorConfig{
		Provider: prov, WorkDir: workDir, Model: model, RunID: log.RunID,
		Observer: obs, Registry: registry, Memory: mem, Docs: selfDocs,
		Audit: rec, Tier: tier, Gate: tools.StdinGate{},
		// Keep eval history variant-local so comparisons are reproducible and do
		// not depend on ambient process-wide activity.
		Usage: tools.UsageContext{}, AuditReader: rec,
		SystemPromptOverride: prompts.Override, PromptAppends: withGuidance(prompts.Appends, workspaceGuidance, "", ""),
		AgentCatalog: catalog, SpawnDepth: spawnDepth,
		StatusDirs: agentStateDirs(workDir), Limits: limits,
		ContextLimit: resolveContextLimit(model, cfg),
		// Variants must run with the same tools a real run gets, or the comparison measures
		// a roster the agent never actually has.
		Secrets: secretsResolver(cfg), SecretNames: secretNames(cfg),
	})
	res.Model = executor.Model() // the effective id after the built-in default is applied

	start := time.Now()
	out, err := executor.Run(ctx, task)
	res.Elapsed = time.Since(start)
	res.Usage, res.Steps = usageObs.Total(), usageObs.Steps()
	res.Output, res.Err = out, err
	return res
}

// resolveVariantContext resolves the variant's workspace and loads its prompt files + agent
// catalog. loadPrompts/loadAgentCatalog/resolveWorkspace read package-level flag variables
// (--workspace, --context-file, --no-context-files); a variant carries per-run equivalents,
// so we install them for the duration of the load and restore them after. Variants run
// sequentially, so there is no concurrent reader of these globals.
func resolveVariantContext(v evalVariant, tier capability.Tier) (workDir string, prompts promptFiles, catalog *agent.AgentCatalog, err error) {
	savedWs, savedCtx, savedNo := workspaceFlag, contextFileFlag, noContextFilesFlag
	defer func() { workspaceFlag, contextFileFlag, noContextFilesFlag = savedWs, savedCtx, savedNo }()
	if v.Workspace != "" {
		workspaceFlag = v.Workspace // an explicit workspace also authorizes its project tier
	}
	contextFileFlag = v.ContextFiles
	noContextFilesFlag = v.NoContextFiles

	workDir, err = resolveWorkspace()
	if err != nil {
		return "", promptFiles{}, nil, err
	}
	prompts, err = loadPrompts(workDir, tier)
	if err != nil {
		return "", promptFiles{}, nil, err
	}
	catalog, err = loadAgentCatalog(workDir, tier)
	if err != nil {
		return "", promptFiles{}, nil, err
	}
	return workDir, prompts, catalog, nil
}

// formatEvalReport renders the side-by-side comparison: a summary table (one row per variant)
// followed by each variant's full output. Pure so it is unit-testable without a provider.
func formatEvalReport(results []evalResult) string {
	var b strings.Builder
	b.WriteString("\n=== eval comparison ===\n")

	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "VARIANT\tMODEL\tSTEPS\tIN\tOUT\tDURATION\tSTATUS")
	for _, r := range results {
		status := "ok"
		if r.Err != nil {
			status = "ERROR"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
			r.Variant.Name, r.Model, r.Steps,
			humanInt(r.Usage.InputTokens), humanInt(r.Usage.OutputTokens),
			r.Elapsed.Round(100*time.Millisecond), status)
	}
	tw.Flush()

	for _, r := range results {
		b.WriteString("\n--- " + r.Variant.Name + " ---\n")
		if r.Err != nil {
			b.WriteString("error: " + r.Err.Error() + "\n")
			continue
		}
		b.WriteString(strings.TrimRight(r.Output, "\n") + "\n")
	}
	return b.String()
}

func init() {
	evalCmd.Flags().StringVar(&evalVariantsFlag, "variants", "", "YAML file listing the config variants to compare")
	evalCmd.Flags().StringSliceVar(&evalModelsFlag, "models", nil, "quick model sweep: one variant per model id (comma-separated or repeated)")
	rootCmd.AddCommand(evalCmd)
}
