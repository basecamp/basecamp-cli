//go:build dev

package commands

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// NewImageProbeCmd asks the terminal whether it draws pictures and says what it
// answered. The TUI asks the same question at startup and says nothing about it, so
// this is how a reader finds out what their terminal told it — and how a terminal
// that answers wrongly gets found.
func NewImageProbeCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "image-probe",
		Short:       "Ask the terminal whether it can draw pictures [dev]",
		Annotations: map[string]string{"dev_only": "true"},
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			tui.DetectImageSupport(os.Stdin, os.Stdout)

			renderer := tui.NewImageRenderer()
			fmt.Fprintf(cmd.OutOrStdout(), "pictures: %s\n", renderer.Protocol())
			if override := os.Getenv(tui.ImageProtocolVar); override != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "%s=%s\n", tui.ImageProtocolVar, override)
			}
			return nil
		},
	}
}
