package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var usageSessionFlag string

var usageCmd = &cobra.Command{
	Use:   "usage",
	Short: "Show token usage totals from the audit log (today, or a session)",
	Long: "Summarize token spend recorded in this agent's process-wide audit log " +
		"(run_usage events). With no flags it reports today's total across all runs; " +
		"--session <id> reports one conversation's total across its turns. Tokens only " +
		"(no cost). Reads the local audit log under --config-dir.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		rec, ledger, err := openCentralLedger()
		if err != nil {
			return err
		}
		defer rec.Close()

		if usageSessionFlag != "" {
			u, turns := ledger.Session(usageSessionFlag)
			if turns == 0 {
				fmt.Printf("no usage recorded for session %s\n", usageSessionFlag)
				return nil
			}
			fmt.Printf("session %s: %s across %d turn(s)\n", usageSessionFlag, formatTokens(u), turns)
			return nil
		}

		today, runs := ledger.Today()
		if runs == 0 {
			fmt.Println("no usage recorded today")
			return nil
		}
		fmt.Printf("today: %s across %d run(s)\n", formatTokens(today), runs)
		return nil
	},
}

func init() {
	usageCmd.Flags().StringVar(&usageSessionFlag, "session", "", "show one session's total instead of today's")
}
