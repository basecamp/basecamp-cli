package commands

import (
	"encoding/json"
	"fmt"

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
			if err := app.API.RequireAccount(); err != nil {
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

			body := map[string]string{
				"name": name,
				"date": parsedDate,
			}

			resp, err := app.API.Post(cmd.Context(), "/lineup/markers.json", body)
			if err != nil {
				return err
			}

			var marker struct {
				ID int64 `json:"id"`
			}
			if err := json.Unmarshal(resp.Data, &marker); err != nil {
				return fmt.Errorf("failed to parse response: %w", err)
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Created lineup marker #%d: %s on %s", marker.ID, name, parsedDate)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("bcq lineup update %d --name \"...\" --date \"...\"", marker.ID),
						Description: "Update marker",
					},
					output.Breadcrumb{
						Action:      "delete",
						Cmd:         fmt.Sprintf("bcq lineup delete %d", marker.ID),
						Description: "Delete marker",
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
		Use:   "update <id>",
		Short: "Update a lineup marker",
		Long:  "Update an existing lineup marker's name or date.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			markerID := args[0]

			if name == "" && date == "" {
				return output.ErrUsage("Provide --name and/or --date to update")
			}

			body := map[string]any{}
			if name != "" {
				body["name"] = name
			}
			if date != "" {
				// Parse natural date if needed
				parsedDate := dateparse.Parse(date)
				if parsedDate == "" {
					parsedDate = date // fallback to raw value
				}
				body["date"] = parsedDate
			}

			path := fmt.Sprintf("/lineup/markers/%s.json", markerID)
			resp, err := app.API.Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			var marker struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(resp.Data, &marker); err != nil {
				return fmt.Errorf("failed to parse response: %w", err)
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Updated lineup marker #%s: %s", markerID, marker.Name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "delete",
						Cmd:         fmt.Sprintf("bcq lineup delete %s", markerID),
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
		Use:   "delete <id>",
		Short: "Delete a lineup marker",
		Long:  "Delete an existing lineup marker.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			markerID := args[0]

			path := fmt.Sprintf("/lineup/markers/%s.json", markerID)
			_, err := app.API.Delete(cmd.Context(), path)
			if err != nil {
				return err
			}

			return app.Output.OK(map[string]any{"id": markerID, "deleted": true},
				output.WithSummary(fmt.Sprintf("Deleted lineup marker #%s", markerID)),
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
