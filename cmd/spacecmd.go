package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"ai-agent-go-play/internal/api"

	"github.com/spf13/cobra"
)

var spaceAddrFlag string

// spaceCmd is the remote, body-redacted management surface for a running
// engine's workspace-local space registry. Removal is intentionally absent until
// its archive/purge and active-session semantics are defined.
var spaceCmd = &cobra.Command{
	Use:   "space",
	Short: "List, show, or create spaces on a running engine",
	Long: "Manage the workspace-local spaces of an engine started with `agent serve`. " +
		"Listings and details expose metadata only; use `agent guidance space` to read " +
		"or replace a space's guidance.",
}

var spaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List spaces, newest-updated first",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client := api.NewClient("http://" + resolveAddr(spaceAddrFlag))
		spaces, err := client.ListSpaces(context.Background())
		if err != nil {
			return err
		}
		printSpaceList(cmd.OutOrStdout(), spaces)
		return nil
	},
}

var spaceShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show one space's metadata (not its guidance text)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := api.NewClient("http://" + resolveAddr(spaceAddrFlag))
		sp, err := client.GetSpace(context.Background(), args[0])
		if err != nil {
			return err
		}
		printSpace(cmd.OutOrStdout(), sp)
		return nil
	},
}

var spaceCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a space from a human-readable name",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := api.NewClient("http://" + resolveAddr(spaceAddrFlag))
		sp, err := client.CreateSpace(context.Background(), strings.Join(args, " "))
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "created space %q (id %s)\n", sp.Name, sp.ID)
		return nil
	},
}

func printSpaceList(w io.Writer, spaces []api.SpaceView) {
	if len(spaces) == 0 {
		fmt.Fprintln(w, "no spaces (create one with: agent space create <name>)")
		return
	}
	fmt.Fprintf(w, "%-17s %-21s %-9s %s\n", "SPACE", "NAME", "GUIDANCE", "UPDATED")
	for _, sp := range spaces {
		fmt.Fprintf(w, "%-17s %-21s %-9d %s\n", sp.ID, sp.Name, sp.GuidanceChars, utcRFC3339(sp.UpdatedAt))
	}
}

func printSpace(w io.Writer, sp api.SpaceView) {
	fmt.Fprintf(w, "id: %s\nname: %s\nguidance: %d chars\ncreated: %s\nupdated: %s\n",
		sp.ID, sp.Name, sp.GuidanceChars, utcRFC3339(sp.CreatedAt), utcRFC3339(sp.UpdatedAt))
}

func utcRFC3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func init() {
	spaceCmd.PersistentFlags().StringVar(&spaceAddrFlag, "addr", "127.0.0.1:8080", "engine address (host:port or an alias from agent config set-engine)")
	spaceCmd.AddCommand(spaceListCmd, spaceShowCmd, spaceCreateCmd)
}
