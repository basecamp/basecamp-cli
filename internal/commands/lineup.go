package commands

import (
	"fmt"
	"strconv"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/dateparse"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewLineupCmd creates the lineup command for managing lineup markers.
func NewLineupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lineup",
		Short: "Manage Lineup markers",
		Long: `Manage Lineup markers (account-wide date markers).

Lineup markers are account-wide date markers that appear in the Lineup
view across all projects. They're useful for marking milestones, deadlines,
or other important dates visible to the entire team.

Unlike most basecamp commands, lineup markers are not scoped to a project.
They apply to the entire Basecamp account.`,
		Annotations: map[string]string{"agent_notes": "Lineup markers are account-wide, not project-scoped"},
	}

	cmd.AddCommand(
		newLineupListCmd(),
		newLineupCreateCmd(),
		newLineupUpdateCmd(),
		newLineupDeleteCmd(),
	)

	return cmd
}

func newLineupListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List lineup markers",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			result, err := app.Account().Lineup().ListMarkers(cmd.Context())
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(result.Markers,
				output.WithSummary(fmt.Sprintf("%d lineup markers", len(result.Markers))),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "create",
						Cmd:         "basecamp lineup create <name> <date>",
						Description: "Create new marker",
					},
				),
			)
		},
	}
}

func newLineupCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <name> <date>",
		Short: "Create a new lineup marker",
		Long: `Create a new lineup marker with a name and date.

The date accepts natural language dates:
- Relative: today, tomorrow, +3, in 5 days
- Weekdays: monday, next friday
- Explicit: 2024-03-15 (YYYY-MM-DD)`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			date := ""
			if len(args) > 0 {
				name = args[0]
			}
			if len(args) > 1 {
				date = args[1]
			}

			// Show help when invoked with no arguments
			if name == "" {
				return missingArg(cmd, "<name>")
			}
			if date == "" {
				return missingArg(cmd, "<date>")
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Parse natural date if needed
			parsedDate := dateparse.Parse(date)
			if parsedDate == "" {
				parsedDate = date // fallback to raw value
			}

			req := &basecamp.CreateMarkerRequest{
				Name: name,
				Date: parsedDate,
			}

			if err := app.Account().Lineup().CreateMarker(cmd.Context(), req); err != nil {
				return convertSDKError(err)
			}

			result := map[string]any{
				"name": name,
				"date": parsedDate,
			}

			return app.OK(result,
				output.WithSummary(fmt.Sprintf("Created lineup marker: %s on %s", name, parsedDate)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "list",
						Cmd:         "basecamp lineup",
						Description: "View all markers",
					},
				),
			)
		},
	}

	return cmd
}

func newLineupUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update <id|url> [name] [date]",
		Short: "Update a lineup marker",
		Long: `Update an existing lineup marker's name or date.

You can pass either a marker ID or a Basecamp URL:
  basecamp lineup update 789 "new name"
  basecamp lineup update 789 "new name" 2024-03-15`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked with no arguments
			if len(args) == 0 {
				return missingArg(cmd, "<id|url>")
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			markerIDStr := extractID(args[0])
			markerID, err := strconv.ParseInt(markerIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid marker ID")
			}

			name := ""
			date := ""
			if len(args) > 1 {
				name = args[1]
			}
			if len(args) > 2 {
				date = args[2]
			}

			// Show help when no update fields provided
			if name == "" && date == "" {
				return noChanges(cmd)
			}

			req := &basecamp.UpdateMarkerRequest{}
			if name != "" {
				req.Name = name
			}
			if date != "" {
				// Parse natural date if needed
				parsedDate := dateparse.Parse(date)
				if parsedDate == "" {
					parsedDate = date // fallback to raw value
				}
				req.Date = parsedDate
			}

			if err := app.Account().Lineup().UpdateMarker(cmd.Context(), markerID, req); err != nil {
				return convertSDKError(err)
			}

			result := map[string]any{"id": markerID, "updated": true}
			if req.Name != "" {
				result["name"] = req.Name
			}
			if req.Date != "" {
				result["date"] = req.Date
			}

			summary := fmt.Sprintf("Updated lineup marker #%d", markerID)
			if name != "" {
				summary = fmt.Sprintf("Updated lineup marker #%d: %s", markerID, name)
			}

			return app.OK(result,
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "delete",
						Cmd:         fmt.Sprintf("basecamp lineup delete %d", markerID),
						Description: "Delete marker",
					},
				),
			)
		},
	}

	return cmd
}

func newLineupDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id|url>",
		Short: "Delete a lineup marker",
		Long: `Delete an existing lineup marker.

You can pass either a marker ID or a Basecamp URL:
  basecamp lineup delete 789
  basecamp lineup delete https://3.basecamp.com/123/my/lineup/markers/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			markerIDStr := extractID(args[0])
			markerID, err := strconv.ParseInt(markerIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid marker ID")
			}

			if err := app.Account().Lineup().DeleteMarker(cmd.Context(), markerID); err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"id": markerID, "deleted": true},
				output.WithSummary(fmt.Sprintf("Deleted lineup marker #%d", markerID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "create",
						Cmd:         "basecamp lineup create <name> <date>",
						Description: "Create new marker",
					},
				),
			)
		},
	}
}
