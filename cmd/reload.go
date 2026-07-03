package cmd

import (
	"context"
	"fmt"

	"ai-agent-go-play/internal/api"

	"github.com/spf13/cobra"
)

var reloadAddrFlag string

var reloadCmd = &cobra.Command{
	Use:   "reload",
	Short: "Tell a running engine to re-read its prompt files and agent-type catalog",
	Long: "Ask a running `agent serve` engine to re-read its prompt customization " +
		"(SYSTEM.md / AGENTS.md) and sub-agent types (agents/*.md) from disk, so edits take " +
		"effect on subsequent runs without restarting the engine. This is the remote " +
		"counterpart to the local `chat` REPL's /reload command.\n\n" +
		"A malformed file is rejected and leaves the engine's current configuration intact. " +
		"--addr accepts a host:port or an alias from `agent config set-engine`.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c := api.NewClient("http://" + resolveAddr(reloadAddrFlag))
		if err := c.Reload(context.Background()); err != nil {
			return err
		}
		fmt.Println("reloaded prompts and agent types")
		return nil
	},
}

func init() {
	reloadCmd.Flags().StringVar(&reloadAddrFlag, "addr", "127.0.0.1:8080", "engine address (host:port or an alias from `agent config set-engine`)")
}
