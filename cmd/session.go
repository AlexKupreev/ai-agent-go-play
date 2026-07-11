package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"ai-agent-go-play/internal/api"

	"github.com/spf13/cobra"
)

var (
	sessionAddrFlag string
	sessionYesFlag  bool
)

// sessionCmd groups the session management verbs that drive a running engine: list the
// resumable conversations, purge one for good, or restore a closed one. Closing (archive)
// stays where it belongs — the chat REPL's /end and Telegram's /end — since it is part of a
// live conversation; this command is the out-of-band management surface for the rest.
var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage a running engine's persistent conversations (list, purge, restore)",
	Long: "List, purge, or restore the persistent conversations of an engine started with " +
		"`agent serve`. Closing a session (archive, recoverable) is done from the chat REPL " +
		"(/end) or Telegram (/end); this command adds the management operations around it:\n\n" +
		"  agent session list                 the resumable (live) sessions\n" +
		"  agent session purge <id>           irreversibly delete a session (live or archived)\n" +
		"  agent session restore <id>         un-archive a closed session so it can be resumed\n\n" +
		"--addr accepts a host:port or an alias from `agent config set-engine`.",
}

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the engine's resumable (live) sessions, newest first",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c := api.NewClient("http://" + resolveAddr(sessionAddrFlag))
		infos, err := c.ListSessions(context.Background())
		if err != nil {
			return err
		}
		if len(infos) == 0 {
			fmt.Println("no sessions (a message starts one)")
			return nil
		}
		fmt.Printf("%-18s %6s  %-19s  %s\n", "SESSION", "TURNS", "LAST ACTIVE", "TITLE")
		for _, in := range infos {
			fmt.Printf("%-18s %6d  %-19s  %s\n", in.ID, in.Turns, in.UpdatedAt.Local().Format("2006-01-02 15:04:05"), in.Title)
		}
		return nil
	},
}

var sessionPurgeCmd = &cobra.Command{
	Use:   "purge <id>",
	Short: "Irreversibly delete a session (live or archived) and its scratch cache",
	Long: "Permanently remove a session's conversation and scratch cache from the engine. " +
		"Unlike closing a session (which archives it, recoverably), a purge cannot be undone. " +
		"Prompts for confirmation unless --yes is given.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		if !sessionYesFlag && !confirm(fmt.Sprintf("Permanently delete session %s? This cannot be undone.", id)) {
			fmt.Println("aborted")
			return nil
		}
		c := api.NewClient("http://" + resolveAddr(sessionAddrFlag))
		if err := c.PurgeSession(context.Background(), id); err != nil {
			return err
		}
		fmt.Printf("purged session %s\n", id)
		return nil
	},
}

var sessionRestoreCmd = &cobra.Command{
	Use:   "restore <id>",
	Short: "Un-archive a closed session so it can be resumed",
	Long: "Move a closed (archived) session back to the live set so it can be resumed with " +
		"`agent chat --addr --session <id>`. The id is the one shown when the session was " +
		"closed (there is no archive listing yet).",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		c := api.NewClient("http://" + resolveAddr(sessionAddrFlag))
		if err := c.RestoreSession(context.Background(), id); err != nil {
			return err
		}
		fmt.Printf("restored session %s — resume with: agent chat --addr %s --session %s\n", id, resolveAddr(sessionAddrFlag), id)
		return nil
	},
}

// confirm reads a y/N answer from stdin, defaulting to no. Used to gate irreversible
// operations (session purge) on an interactive terminal.
func confirm(prompt string) bool {
	fmt.Printf("%s [y/N] ", prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

func init() {
	sessionCmd.PersistentFlags().StringVar(&sessionAddrFlag, "addr", "127.0.0.1:8080", "engine address (host:port or an alias from `agent config set-engine`)")
	sessionPurgeCmd.Flags().BoolVarP(&sessionYesFlag, "yes", "y", false, "skip the confirmation prompt")
	sessionCmd.AddCommand(sessionListCmd, sessionPurgeCmd, sessionRestoreCmd)
}
