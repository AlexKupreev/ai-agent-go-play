package cmd

import (
	"fmt"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/memory"
	"ai-agent-go-play/internal/session"
	"ai-agent-go-play/internal/tools"

	"github.com/spf13/cobra"
)

var (
	promptsPlannerFlag bool
	promptsCriticFlag  bool
	promptsAllFlag     bool
)

var promptsCmd = &cobra.Command{
	Use:   "prompts",
	Short: "Inspect the agent's composed prompts",
}

var promptsShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the composed system prompt the next run would use (executor by default)",
	Long: "Assemble and print the effective system prompt(s) — base + tier policy + tool roster " +
		"+ your SYSTEM.md/AGENTS.md layers — exactly as a run would compose them, without calling " +
		"the model. Honors the global --workspace / --tier / --context-file / --no-context-files " +
		"flags, so you can see what a given configuration actually produces.\n\n" +
		"By default it shows the executor prompt; --planner and --critic show those instead, and " +
		"--all shows every prompt. No API key or network is needed.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := loadConfigOrEmpty() // no key needed — nothing is sent to the model
		workDir, err := resolveWorkspace()
		if err != nil {
			return err
		}
		tier, err := resolveTier(tierFlag, cfg)
		if err != nil {
			return err
		}
		model := resolveModel(modelFlag, cfg)

		prompts, err := loadPrompts(workDir, tier)
		if err != nil {
			return err
		}
		catalog, err := loadAgentCatalog(workDir, tier)
		if err != nil {
			return err
		}

		// Build the executor with the real catalog/memory so the tool roster and tier policy
		// in the prompt are the actual ones — no model call happens at construction.
		registry, err := loadRegistryForInspect()
		if err != nil {
			return err
		}
		mem, err := loadMemoryForInspect()
		if err != nil {
			return err
		}
		prov := newProvider(cfg)
		var sessReader tools.SessionReader
		if dir, err := sessionStorePath(); err == nil {
			sessReader = session.NewFileStore(dir)
		}
		executor := agent.NewExecutor(agent.ExecutorConfig{
			Provider: prov, WorkDir: workDir, Model: model, Tier: tier,
			Registry: registry, Memory: mem, Docs: selfDocs,
			AgentCatalog: catalog, SpawnDepth: defaultSpawnDepth,
			SystemPromptOverride: prompts.Override, PromptAppends: prompts.Appends,
			StatusDirs: agentStateDirs(), Sessions: sessReader,
		})

		showExecutor := promptsAllFlag || (!promptsPlannerFlag && !promptsCriticFlag)
		if showExecutor {
			section("EXECUTOR", executor.SystemPrompt())
		}
		if promptsAllFlag || promptsPlannerFlag {
			planner := agent.NewPlanner(prov, model, prompts.PlannerOverride, executor.EnvironmentSummary(), "", nil, "", nil)
			section("PLANNER", planner.SystemPrompt())
		}
		if promptsAllFlag || promptsCriticFlag {
			critic := agent.NewCritic(prov, model, prompts.CriticOverride, nil)
			section("CRITIC", critic.SystemPrompt())
		}
		return nil
	},
}

// section prints a labeled prompt block to stdout with a rule, so several prompts read
// clearly when --all is used.
func section(label, body string) {
	fmt.Printf("=== %s ===\n%s\n\n", label, body)
}

// loadRegistryForInspect loads the persistent tool catalog for prompt inspection.
func loadRegistryForInspect() (tools.Registry, error) {
	catPath, err := catalogPath()
	if err != nil {
		return nil, err
	}
	return tools.NewPersistentRegistry(catPath)
}

// loadMemoryForInspect loads the persistent memory store for prompt inspection.
func loadMemoryForInspect() (memory.Store, error) {
	memPath, err := memoryPath()
	if err != nil {
		return nil, err
	}
	return memory.NewPersistentStore(memPath)
}

func init() {
	promptsShowCmd.Flags().StringVar(&modelFlag, "model", "", modelFlagUsage)
	promptsShowCmd.Flags().StringVar(&tierFlag, "tier", "", "trust tier: safe|balanced|permissive (affects the tier-policy section)")
	promptsShowCmd.Flags().BoolVar(&promptsPlannerFlag, "planner", false, "show the planner prompt instead of the executor")
	promptsShowCmd.Flags().BoolVar(&promptsCriticFlag, "critic", false, "show the critic prompt instead of the executor")
	promptsShowCmd.Flags().BoolVar(&promptsAllFlag, "all", false, "show the executor, planner, and critic prompts")
	promptsCmd.AddCommand(promptsShowCmd)
	rootCmd.AddCommand(promptsCmd)
}
