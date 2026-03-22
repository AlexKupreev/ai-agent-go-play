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
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(runCmd)
}
