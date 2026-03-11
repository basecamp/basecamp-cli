//go:build !dev

package commands

import (
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewTUICmd returns a stub tui command for release builds.
func NewTUICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tui [url]",
		Short: "Launch the Basecamp workspace [dev]",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return output.ErrUsage("the tui workspace is only available in development builds")
		},
	}

	cmd.Flags().Bool("trace", false, "Enable trace logging to file")

	return cmd
}
