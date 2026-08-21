package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "agent",
	Short: "An AI agent CLI",
	Long:  "A simple AI agent that plans and executes tasks step by step using OpenAI.",
}

// Execute is called from main.go. It runs the appropriate subcommand based on CLI args.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&configDirFlag, "config-dir", "",
		"directory for this agent's config, tool catalog, audit log, sessions, and global "+
			"prompt files (default ~/.config/ai-agent; env AI_AGENT_CONFIG_DIR). Its memory "+
			"and spaces do NOT live here — they are workspace-local (see --workspace), so two "+
			"independent agents need a different --config-dir AND a different --workspace.")
	rootCmd.PersistentFlags().StringVar(&sessionsDirFlag, "sessions-dir", "",
		"directory for per-run transcripts, one subdir per run "+
			"(default <config-dir>/runs, so separate --config-dir agents share nothing; "+
			"env AI_AGENT_SESSIONS_DIR)")
	rootCmd.PersistentFlags().BoolVar(&noContextFilesFlag, "no-context-files", false,
		"ignore ALL prompt customization — SYSTEM.md / AGENTS.md / PLANNER.md / CRITIC.md, "+
			"--context-file, and agents/*.md, in both the config dir and the workspace "+
			"(reproducible runs on the built-in base prompts + built-in agent types)")
	rootCmd.PersistentFlags().StringVar(&workspaceFlag, "workspace", "",
		"directory the agent acts on — the shell tool's working directory, the workspace "+
			"prompt tier, and the home of its memory + spaces under <workspace>/.agent "+
			"(default: the current directory). Point it at a persistent directory for a "+
			"long-running `agent serve`, or memory does not survive a restart.")
	rootCmd.PersistentFlags().StringArrayVar(&contextFileFlag, "context-file", nil,
		"extra prompt file(s) to append as operator instructions "+
			"(repeatable). Named explicitly, so always loaded regardless of tier.")

	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(chatCmd)
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(clientCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(toolCmd)
	rootCmd.AddCommand(auditCmd)
	rootCmd.AddCommand(usageCmd)
	rootCmd.AddCommand(reloadCmd)
	rootCmd.AddCommand(sessionCmd)
	rootCmd.AddCommand(guidanceCmd)
}
