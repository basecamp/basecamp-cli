package commands

import (
	"fmt"
	"strconv"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/dateparse"
	"github.com/basecamp/bcq/internal/output"
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

Unlike most bcq commands, lineup markers are not scoped to a project.
They apply to the entire Basecamp account.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return output.ErrUsageHint("Action required", "Run: bcq lineup --help")
		},
	}

	cmd.AddCommand(
		newLineupCreateCmd(),
		newLineupUpdateCmd(),
		newLineupDeleteCmd(),
	)

	return cmd
}

func newLineupCreateCmd() *cobra.Command {
	var name string
	var date string

	cmd := &cobra.Command{
		Use:   "create [name] [date]",
		Short: "Create a new lineup marker",
		Long: `Create a new lineup marker with a name and date.

The --date flag accepts natural language dates:
- Relative: today, tomorrow, +3, in 5 days
- Weekdays: monday, next friday
- Explicit: 2024-03-15 (YYYY-MM-DD)`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Name and date from positional args or flags
			if len(args) > 0 && name == "" {
				name = args[0]
			}
			if len(args) > 1 && date == "" {
				date = args[1]
			}

			if name == "" {
				return output.ErrUsage("Marker name is required")
			}

			if date == "" {
				return output.ErrUsage("Marker date is required")
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
				"title":     name,
				"starts_on": parsedDate,
				"ends_on":   parsedDate,
			}

			return app.OK(result,
				output.WithSummary(fmt.Sprintf("Created lineup marker: %s on %s", name, parsedDate)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "list",
						Cmd:         "bcq lineup",
						Description: "View all markers",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "Marker name")
	cmd.Flags().StringVarP(&date, "date", "d", "", "Marker date (YYYY-MM-DD or natural language)")

	return cmd
}

func newLineupUpdateCmd() *cobra.Command {
	var name string
	var date string

	cmd := &cobra.Command{
		Use:   "update <id|url>",
		Short: "Update a lineup marker",
		Long: `Update an existing lineup marker's name or date.

You can pass either a marker ID or a Basecamp URL:
  bcq lineup update 789 --name "new name"
  bcq lineup update https://3.basecamp.com/123/my/lineup/markers/789`,
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

			if name == "" && date == "" {
				return output.ErrUsage("Provide --name and/or --date to update")
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
				result["title"] = req.Name
			}
			if req.Date != "" {
				result["starts_on"] = req.Date
				result["ends_on"] = req.Date
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
						Cmd:         fmt.Sprintf("bcq lineup delete %d", markerID),
						Description: "Delete marker",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "New name")
	cmd.Flags().StringVarP(&date, "date", "d", "", "New date (YYYY-MM-DD or natural language)")

	return cmd
}

func newLineupDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id|url>",
		Short: "Delete a lineup marker",
		Long: `Delete an existing lineup marker.

You can pass either a marker ID or a Basecamp URL:
  bcq lineup delete 789
  bcq lineup delete https://3.basecamp.com/123/my/lineup/markers/789`,
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
						Cmd:         "bcq lineup create <name> <date>",
						Description: "Create new marker",
					},
				),
			)
		},
	}
}
