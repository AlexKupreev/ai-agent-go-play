package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"ai-agent-go-play/internal/agent"
	"ai-agent-go-play/internal/api"

	"github.com/spf13/cobra"
)

var clientAddrFlag string

var clientCmd = &cobra.Command{
	Use:   "client <task>",
	Short: "Drive a task on a running engine (HTTP+SSE client)",
	Long: "Connect to an engine started with `agent serve`, start a run, stream its " +
		"events, and answer any approval prompts over the API. The CLI is just one " +
		"client of the headless engine.",
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		task := strings.Join(args, " ")
		c := api.NewClient("http://" + clientAddrFlag)

		ctx, cancel := context.WithCancel(cmd.Context())
		defer cancel()

		runID, err := c.StartRun(ctx, task)
		if err != nil {
			return fmt.Errorf("start run: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Run ID: %s\n\n", runID)

		// Approvals have no server push over SSE, so poll for parked requests and
		// prompt the operator until the run ends (ctx cancelled when the stream closes).
		go watchApprovals(ctx, c)

		printErr := c.StreamEvents(ctx, runID, printEvent)
		cancel() // stop the approval watcher
		return printErr
	},
}

// printEvent renders a streamed event to the terminal, mirroring the in-process
// CLI trace (the CLIObserver).
func printEvent(e api.Event) {
	switch e.Kind {
	case string(agent.EvResponse):
		if e.Text != "" {
			fmt.Println(e.Text)
		}
	case string(agent.EvToolStart):
		fmt.Printf("\n[tool: %s] %s\n", e.Tool, e.Input)
	case string(agent.EvToolResult):
		fmt.Printf("[result] %s\n", e.Result)
	case api.KindDone:
		if e.Text != "" {
			fmt.Println(e.Text)
		}
	case api.KindError:
		fmt.Fprintf(os.Stderr, "run error: %s\n", e.Text)
	}
}

// watchApprovals polls the engine for parked approvals and prompts the operator to
// resolve each one, until ctx is cancelled. Handled ids are remembered so a request
// is not prompted twice while a poll overlaps its resolution.
func watchApprovals(ctx context.Context, c *api.Client) {
	handled := map[string]bool{}
	stdin := bufio.NewScanner(os.Stdin)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		pending, err := c.Pending(ctx)
		if err != nil {
			return // engine gone or ctx cancelled
		}
		for _, p := range pending {
			if handled[p.ID] {
				continue
			}
			handled[p.ID] = true
			fmt.Printf("\n[approve] %s\n  %s\n  proceed? [y/N] > ", p.Title, p.Detail)
			approved := false
			if stdin.Scan() {
				ans := strings.ToLower(strings.TrimSpace(stdin.Text()))
				approved = ans == "y" || ans == "yes"
			}
			if err := c.Resolve(ctx, p.ID, approved); err != nil {
				fmt.Fprintf(os.Stderr, "resolve approval: %v\n", err)
			}
		}
	}
}

func init() {
	clientCmd.Flags().StringVar(&clientAddrFlag, "addr", "127.0.0.1:8080", "engine address to connect to")
}
