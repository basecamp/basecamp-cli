package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/output"
	internalversion "github.com/basecamp/basecamp-cli/internal/version"
)

// NewVersionCmd creates the version command.
func NewVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show version",
		Long:  "Show the installed Basecamp CLI version.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if jq, _ := cmd.Root().PersistentFlags().GetString("jq"); jq != "" {
				return output.ErrJQNotSupported("the version command")
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), internalversion.Full())
			return err
		},
	}
}
