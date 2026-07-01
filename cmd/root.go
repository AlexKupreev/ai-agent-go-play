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
			"(default ~/.local/share/ai-agent/sessions; env AI_AGENT_SESSIONS_DIR)")

	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(chatCmd)
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(clientCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(toolCmd)
	rootCmd.AddCommand(auditCmd)
}
