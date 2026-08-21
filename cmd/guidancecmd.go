package cmd

import (
	"context"
	"fmt"
	"strings"

	"ai-agent-go-play/internal/api"
	"ai-agent-go-play/internal/guidance"

	"github.com/spf13/cobra"
)

var guidanceAddrFlag string

// guidanceCmd is the deterministic out-of-band management client for a running engine.
// Chat frontends offer the same grammar with their current space/session as the target;
// here those targets are explicit positional arguments.
var guidanceCmd = &cobra.Command{
	Use:   "guidance global <show|set|add|clear> [text]\n  agent guidance space <id-or-name> <show|set|add|clear> [text]\n  agent guidance session <id> <show|set|add|clear> [text]",
	Short: "Show or change global, space, or session guidance",
	Long: "Manage standing user guidance through a running engine. Global guidance applies " +
		"across the workspace; space and session commands require their target id (a space " +
		"name is also accepted). Set replaces, add appends a new line, and clear is idempotent.",
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		scope, err := guidance.ParseScope(args[0])
		if err != nil {
			return err
		}
		target := ""
		opAt := 1
		if scope != guidance.ScopeGlobal {
			if len(args) < 3 {
				return fmt.Errorf("%s guidance requires a target id or name", scope)
			}
			target = args[1]
			opAt = 2
		}
		parsed, err := guidance.ParseCommand(string(scope) + " " + strings.Join(args[opAt:], " "))
		if err != nil {
			return err
		}
		client := api.NewClient("http://" + resolveAddr(guidanceAddrFlag))
		ctx := context.Background()
		result, err := guidance.ApplyCommand(parsed,
			func(scope guidance.Scope) (string, error) {
				doc, err := client.GetGuidance(ctx, scope, target)
				return doc.Guidance, err
			},
			func(scope guidance.Scope, text string) error {
				_, err := client.SetGuidance(ctx, scope, target, text)
				return err
			},
		)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), guidance.FormatResult(result))
		return nil
	},
}

func init() {
	guidanceCmd.Flags().StringVar(&guidanceAddrFlag, "addr", "127.0.0.1:8080", "engine address (host:port or an alias from agent config set-engine)")
}
