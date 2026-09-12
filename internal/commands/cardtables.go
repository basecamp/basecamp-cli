package commands

import (
	"github.com/spf13/cobra"
)

// NewCardTablesCmd creates the card-tables command for managing card tables themselves.
func NewCardTablesCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:   "card-tables",
		Short: "Manage card tables",
		Long: `Manage card tables in a project.

A card table is the board that contains columns and cards. Use this group for
actions on the table itself, such as saving it as a reusable template. Use
'basecamp cards' to manage cards, columns, steps, and wormholes inside a table.`,
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	cmd.AddCommand(
		newTemplatifyCmd(cardTableTemplatificationKind(), &project),
		newTemplatificationCmd(cardTableTemplatificationKind(), &project),
	)

	return cmd
}
