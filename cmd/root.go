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
		"directory for this agent's config/tools/memory/audit "+
			"(default ~/.config/ai-agent; env AI_AGENT_CONFIG_DIR). "+
			"Point two `agent serve` processes at different dirs for two independent agents.")
	rootCmd.PersistentFlags().StringVar(&sessionsDirFlag, "sessions-dir", "",
		"directory for per-run transcripts, one subdir per run "+
			"(default <config-dir>/runs, so separate --config-dir agents share nothing; "+
			"env AI_AGENT_SESSIONS_DIR)")
	rootCmd.PersistentFlags().BoolVar(&noContextFilesFlag, "no-context-files", false,
		"ignore SYSTEM.md / AGENTS.md prompt customization in the config dir "+
			"(reproducible runs with the built-in base prompt)")
	rootCmd.PersistentFlags().StringVar(&workspaceFlag, "workspace", "",
		"directory the agent acts on — the shell tool's working directory "+
			"(default: the current directory). The project the agent works in, "+
			"as distinct from --config-dir (the agent's own identity/state).")
	rootCmd.PersistentFlags().StringArrayVar(&contextFileFlag, "context-file", nil,
		"extra prompt file(s) to append as operator/project instructions "+
			"(repeatable). Named explicitly, so always loaded regardless of tier.")
	rootCmd.PersistentFlags().BoolVar(&noProjectFlag, "no-project", false,
		"flat-repo mode: act on the workspace directly, with no named-project "+
			"registry and no list/create/switch_project tools (projects.md §6)")
	rootCmd.PersistentFlags().StringVar(&projectFlag, "project", "",
		"activate a named project at launch (by uid, title, or path); the "+
			"workspace becomes that project's directory")

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
}
