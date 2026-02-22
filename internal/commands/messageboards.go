package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewMessageboardsCmd creates the messageboards command for viewing message board containers.
func NewMessageboardsCmd() *cobra.Command {
	var project string
	var boardID string

	cmd := &cobra.Command{
		Use:   "messageboards",
		Short: "View message board container",
		Long: `View message board container for a project.

A message board is the container that holds all messages in a project.
Each project has exactly one message board in its dock.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMessageboardShow(cmd, project, boardID)
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.Flags().StringVarP(&boardID, "board", "b", "", "Message board ID (auto-detected from project)")

	cmd.AddCommand(newMessageboardShowCmd(&project, &boardID))

	return cmd
}

// getMessageboardID gets the message board ID from the project dock, handling multi-dock projects.
func getMessageboardID(cmd *cobra.Command, app *appctx.App, projectID, boardID string) (string, error) {
	return getDockToolID(cmd.Context(), app, projectID, "message_board", boardID, "message board")
}

func newMessageboardShowCmd(project, boardID *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show [id]",
		Short: "Show message board details",
		Long:  "Display detailed information about a message board.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := *boardID
			if len(args) > 0 {
				id = args[0]
			}
			return runMessageboardShow(cmd, *project, id)
		},
	}
}

func runMessageboardShow(cmd *cobra.Command, project, boardIDStr string) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Resolve project, with interactive fallback
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return err
		}
		projectID = app.Config.ProjectID
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}

	// Get message board ID
	resolvedBoardIDStr, err := getMessageboardID(cmd, app, resolvedProjectID, boardIDStr)
	if err != nil {
		return err
	}

	boardID, err := strconv.ParseInt(resolvedBoardIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid message board ID")
	}

	board, err := app.Account().MessageBoards().Get(cmd.Context(), bucketID, boardID)
	if err != nil {
		return convertSDKError(err)
	}

	return app.OK(board,
		output.WithSummary(fmt.Sprintf("%d messages", board.MessagesCount)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "messages",
				Cmd:         fmt.Sprintf("basecamp messages --in %s", resolvedProjectID),
				Description: "List all messages",
			},
			output.Breadcrumb{
				Action:      "project",
				Cmd:         fmt.Sprintf("basecamp projects show %s", resolvedProjectID),
				Description: "View project details",
			},
		),
	)
}
