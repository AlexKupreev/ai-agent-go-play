package cmd

import (
	"context"
	"fmt"

	"ai-agent-go-play/internal/api"

	"github.com/spf13/cobra"
)

var (
	auditAddrFlag  string
	auditRunFlag   string
	auditTypeFlag  string
	auditLimitFlag int
)

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Browse the engine's audit log (capability use, tool authoring/revocation, memory writes)",
	Long: "Query the process-wide audit log of a running engine started with `agent serve`. " +
		"The log is the single review surface for everything effectful. Filter with --run and " +
		"--type; --limit caps to the last N matches.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c := api.NewClient("http://" + resolveAddr(auditAddrFlag))
		events, err := c.Audit(context.Background(), auditRunFlag, auditTypeFlag, auditLimitFlag)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			fmt.Println("no matching audit events")
			return nil
		}
		for _, e := range events {
			run := e.Run
			if run == "" {
				run = "-"
			}
			fmt.Printf("%s  %-22s  run:%s  %v\n", e.At.Format("2006-01-02 15:04:05"), e.Type, run, e.Fields)
		}
		return nil
	},
}

func init() {
	auditCmd.Flags().StringVar(&auditAddrFlag, "addr", "127.0.0.1:8080", "engine address")
	auditCmd.Flags().StringVar(&auditRunFlag, "run", "", "filter by run id")
	auditCmd.Flags().StringVar(&auditTypeFlag, "type", "", "filter by event type (e.g. tool_revoked)")
	auditCmd.Flags().IntVar(&auditLimitFlag, "limit", 0, "return only the last N matching events (0 = all)")
}
