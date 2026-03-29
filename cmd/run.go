package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/logger"

	"github.com/spf13/cobra"
)

var modelFlag string
var verboseFlag bool

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

		planner := agent.NewPlanner(cfg.OpenAIKey, modelFlag, verboseFlag, log)
		fmt.Fprintln(os.Stderr, "[planner] clarifying task...")
		planJSON, err := planner.Run(context.Background(), task)
		if err != nil {
			return fmt.Errorf("planner error: %w", err)
		}

		var plan agent.Plan
		if err := json.Unmarshal([]byte(planJSON), &plan); err != nil {
			return fmt.Errorf("failed to parse plan: %w", err)
		}

		if verboseFlag {
			fmt.Fprintf(os.Stderr, "[planner] refined task: %s\n", plan.RefinedTask)
			for _, a := range plan.Assumptions {
				fmt.Fprintf(os.Stderr, "[planner] assumption: %s\n", a)
			}
			for _, c := range plan.Confirmed {
				fmt.Fprintf(os.Stderr, "[planner] confirmed: %s\n", c)
			}
			fmt.Fprintln(os.Stderr)
		}

		executor := agent.NewExecutor(cfg.OpenAIKey, workDir, modelFlag, verboseFlag, log)
		result, err := executor.Run(context.Background(), plan.RefinedTask)
		if err != nil {
			return err
		}
		fmt.Println(result)
		return nil
	},
}

func init() {
	runCmd.Flags().StringVar(&modelFlag, "model", "", "model to use (default: gpt-4o-mini)")
	runCmd.Flags().BoolVar(&verboseFlag, "verbose", false, "print tool calls and results to stderr")
}
