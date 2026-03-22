package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/logger"

	"github.com/spf13/cobra"
)

var runCmd = &cobra.Command{
	Use:   "run <task>",
	Short: "Run the agent with a task",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		log, err := logger.New()
		if err != nil {
			return fmt.Errorf("failed to create logger: %w", err)
		}
		defer log.Close()

		task := strings.Join(args, " ")
		fmt.Fprintf(os.Stderr, "Run ID: %s\n", log.RunID)
		fmt.Fprintf(os.Stderr, "Log:    %s\n", log.Path)
		fmt.Fprintf(os.Stderr, "Task:   %s\n\n", task)

		workDir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get working directory: %w", err)
		}

		a := agent.New(cfg.OpenAIKey, workDir, log)
		return a.Run(context.Background(), task)
	},
}
