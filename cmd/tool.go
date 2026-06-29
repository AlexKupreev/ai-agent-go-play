package cmd

import (
	"fmt"

	"ai-agent-go-play/internal/tools"

	"github.com/spf13/cobra"
)

var toolCmd = &cobra.Command{
	Use:   "tool",
	Short: "Inspect and manage agent-authored tools",
}

var toolListCmd = &cobra.Command{
	Use:   "list",
	Short: "List persisted authored tools",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		reg, err := openCatalog()
		if err != nil {
			return err
		}
		list := reg.List(tools.ScopeAny)
		if len(list) == 0 {
			fmt.Println("no authored tools yet")
			return nil
		}
		for _, t := range list {
			caps := len(t.RequiredCaps)
			fmt.Printf("%-24s v%d  %-9s  caps:%d  %s\n", t.Name, t.Version, t.Scope, caps, t.Description)
		}
		return nil
	},
}

var toolRevokeCmd = &cobra.Command{
	Use:   "revoke <name>",
	Short: "Remove an authored tool from the catalog",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		reg, err := openCatalog()
		if err != nil {
			return err
		}
		if !reg.Revoke(args[0]) {
			return fmt.Errorf("no tool named %q", args[0])
		}
		fmt.Printf("revoked %q\n", args[0])
		return nil
	},
}

// openCatalog loads the persistent tool registry from its on-disk catalog.
func openCatalog() (*tools.MemoryRegistry, error) {
	path, err := catalogPath()
	if err != nil {
		return nil, err
	}
	return tools.NewPersistentRegistry(path)
}

func init() {
	toolCmd.AddCommand(toolListCmd)
	toolCmd.AddCommand(toolRevokeCmd)
}
