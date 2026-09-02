//go:build !dev

package commands

import (
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewImageProbeCmd returns a stub image-probe command for release builds.
func NewImageProbeCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "image-probe",
		Short:       "Ask the terminal whether it can draw pictures [dev]",
		Annotations: map[string]string{"dev_only": "true"},
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return output.ErrUsageHint(
				"image-probe is only available in development builds",
				"build with: make build (or go build -tags dev ./cmd/basecamp)",
			)
		},
	}
}
