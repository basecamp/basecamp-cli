package commands

import (
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewMCPCmd creates the mcp command group.
func NewMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "MCP server (Model Context Protocol)",
		Long: `MCP server for AI integration.

The MCP server allows AI assistants like Claude to interact with Basecamp.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(newMCPServeCmd())

	return cmd
}

func newMCPServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "serve",
		Short:  "Start MCP server",
		Long:   "Start the MCP server for AI assistant integration.",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return output.ErrUsageHint("MCP server not yet implemented", "This feature is coming soon")
		},
	}
}
