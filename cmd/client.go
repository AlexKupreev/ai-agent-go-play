package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
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
		c := api.NewClient("http://" + resolveAddr(clientAddrFlag))

		// First Ctrl+C cancels the *remote* run and detaches; a second is no longer
		// caught and force-quits the client.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()

		runID, err := c.StartRun(ctx, task)
		if err != nil {
			return fmt.Errorf("start run: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Run ID: %s\n\n", runID)

		// Approvals have no server push over SSE, so poll for parked requests and
		// prompt the operator until the run ends (ctx cancelled when the stream closes).
		watchCtx, cancelWatch := context.WithCancel(ctx)
		defer cancelWatch()
		go watchApprovals(watchCtx, c)

		printErr := c.StreamEvents(ctx, runID, printEvent)
		cancelWatch() // stop the approval watcher
		// If we stopped because of Ctrl+C, cancel the remote run so it doesn't keep
		// running headless on the engine. Use a fresh context — ctx is already done.
		if ctx.Err() != nil {
			if err := c.StopRun(context.Background(), runID); err != nil {
				fmt.Fprintf(os.Stderr, "stop run: %v\n", err)
			}
			fmt.Fprintln(os.Stderr, "\ncancelled remote run")
			return nil
		}
		if printErr == nil {
			if info, err := c.RunStatus(context.Background(), runID); err == nil {
				printRunUsage(info)
			}
		}
		return printErr
	},
}

// printRunUsage prints a run's token-usage summary from its RunInfo (populated by the
// engine when the run ends). No-op when there's nothing to report.
func printRunUsage(info api.RunInfo) {
	if info.Steps == 0 && info.Usage.InputTokens == 0 && info.Usage.OutputTokens == 0 {
		return
	}
	var elapsed time.Duration
	if info.EndedAt != nil {
		elapsed = info.EndedAt.Sub(info.StartedAt)
	}
	fmt.Fprintln(os.Stderr, formatUsage(info.Usage, info.Steps, elapsed))
}

var stopCmd = &cobra.Command{
	Use:   "stop <run-id>",
	Short: "Cancel a run on a running engine",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := api.NewClient("http://" + resolveAddr(clientAddrFlag))
		if err := c.StopRun(cmd.Context(), args[0]); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "cancelled run %s\n", args[0])
		return nil
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
		// Terminal marker only: its Text duplicates the final EvResponse (already
		// printed above), so don't render it again.
	case api.KindApprovalRequested:
		fmt.Printf("\n[escalation %s] %s: %s\n", e.ApprovalID, e.Tool, e.Text)
	case api.KindApprovalResolved:
		decision := "denied"
		if e.Approved != nil && *e.Approved {
			decision = "approved"
		}
		fmt.Printf("[escalation %s] %s\n", e.ApprovalID, decision)
	case api.KindError:
		fmt.Fprintf(os.Stderr, "run error: %s\n", e.Text)
	}
}

// watchApprovals polls the engine for parked approvals and prompts the operator to
// resolve each one, until ctx is cancelled. Handled ids are remembered so a request
// is not prompted twice while a poll overlaps its resolution.
func watchApprovals(ctx context.Context, c *api.Client) {
	watchApprovalsScan(ctx, c, bufio.NewScanner(os.Stdin))
}

// watchApprovalsScan is watchApprovals reading operator answers from a caller-supplied
// scanner. The remote chat REPL passes its own stdin scanner so approval prompts and
// the "> " prompt share one buffer (they never read concurrently — a turn is either
// streaming or awaiting the next line), avoiding two bufio readers racing on stdin.
func watchApprovalsScan(ctx context.Context, c *api.Client, stdin *bufio.Scanner) {
	handled := map[string]bool{}
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
	stopCmd.Flags().StringVar(&clientAddrFlag, "addr", "127.0.0.1:8080", "engine address to connect to")
}
