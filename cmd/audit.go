package cmd

import (
	"context"
	"fmt"

	"ai-agent-go-play/internal/api"
	"ai-agent-go-play/internal/audit"

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
	Short: "Browse the audit log (capability use, tool authoring/revocation, memory writes)",
	Long: "Query the local process-wide audit log under --config-dir. When --addr is explicitly " +
		"supplied, query that running engine instead. The log is the single review surface for " +
		"everything effectful. Filter with --run and --type; --limit caps to the last N matches.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		events, err := loadAuditEvents(cmd, auditAddrFlag, auditRunFlag, auditTypeFlag, auditLimitFlag)
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

// loadAuditEvents selects local or remote audit history. Merely having the
// default address value is not enough to select remote mode: the caller must
// have explicitly supplied --addr.
func loadAuditEvents(cmd *cobra.Command, addr, run, typ string, limit int) ([]audit.Event, error) {
	if cmd.Flags().Changed("addr") {
		c := api.NewClient("http://" + resolveAddr(addr))
		return c.Audit(context.Background(), run, typ, limit)
	}
	reader, err := openCentralAuditReader()
	if err != nil {
		return nil, err
	}
	return reader.Tail(limit, audit.Filter{Run: run, Type: typ})
}

func init() {
	auditCmd.Flags().StringVar(&auditAddrFlag, "addr", "127.0.0.1:8080", "query this engine instead of the local log")
	auditCmd.Flags().StringVar(&auditRunFlag, "run", "", "filter by run id")
	auditCmd.Flags().StringVar(&auditTypeFlag, "type", "", "filter by event type (e.g. tool_revoked)")
	auditCmd.Flags().IntVar(&auditLimitFlag, "limit", 0, "return only the last N matching events (0 = all)")
}
