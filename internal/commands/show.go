package commands

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
	"github.com/basecamp/bcq/internal/urlarg"
)

// NewShowCmd creates the show command for viewing any recording.
func NewShowCmd() *cobra.Command {
	var recordType string
	var project string

	cmd := &cobra.Command{
		Use:   "show [type] <id|url>",
		Short: "Show any recording by ID",
		Long: `Show details of any Basecamp recording by ID or URL.

Types: todo, todolist, message, comment, card, card-table, document

If no type specified, uses generic recording lookup.

You can also pass a Basecamp URL directly:
  bcq show https://3.basecamp.com/123/buckets/456/todos/789
  bcq show todo 789 --in my-project`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			// Parse positional args: [type] <id|url>
			var id string
			if len(args) == 1 {
				id = args[0]
			} else {
				// Two args: type and id
				if recordType == "" {
					recordType = args[0]
				}
				id = args[1]
			}

			// Check if the id is a URL and extract components
			if parsed := urlarg.Parse(id); parsed != nil {
				id = parsed.RecordingID
				if project == "" && parsed.ProjectID != "" {
					project = parsed.ProjectID
				}
				// Auto-detect type from URL if not specified
				if recordType == "" && parsed.Type != "" {
					recordType = parsed.Type
				}
			}

			// Validate type early (before account check) for better error messages
			if !isValidRecordType(recordType) {
				return output.ErrUsageHint(
					fmt.Sprintf("Unknown type: %s", recordType),
					"Supported: todo, todolist, message, comment, card, card-table, document",
				)
			}

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

			// Resolve project name to ID if needed
			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Determine endpoint based on type
			var endpoint string
			switch recordType {
			case "todo", "todos":
				endpoint = fmt.Sprintf("/buckets/%s/todos/%s.json", resolvedProjectID, id)
			case "todolist", "todolists":
				endpoint = fmt.Sprintf("/buckets/%s/todolists/%s.json", resolvedProjectID, id)
			case "message", "messages":
				endpoint = fmt.Sprintf("/buckets/%s/messages/%s.json", resolvedProjectID, id)
			case "comment", "comments":
				endpoint = fmt.Sprintf("/buckets/%s/comments/%s.json", resolvedProjectID, id)
			case "card", "cards":
				endpoint = fmt.Sprintf("/buckets/%s/card_tables/cards/%s.json", resolvedProjectID, id)
			case "card-table", "card_table", "cardtable":
				endpoint = fmt.Sprintf("/buckets/%s/card_tables/%s.json", resolvedProjectID, id)
			case "document", "documents":
				endpoint = fmt.Sprintf("/buckets/%s/documents/%s.json", resolvedProjectID, id)
			case "", "recording", "recordings":
				// Generic recording lookup
				endpoint = fmt.Sprintf("/buckets/%s/recordings/%s.json", resolvedProjectID, id)
			default:
				return output.ErrUsageHint(
					fmt.Sprintf("Unknown type: %s", recordType),
					"Supported: todo, todolist, message, comment, card, card-table, document",
				)
			}

			resp, err := app.Account().Get(cmd.Context(), endpoint)
			if err != nil {
				return convertSDKError(err)
			}

			// Check for empty response (204 No Content)
			if resp.StatusCode == http.StatusNoContent {
				if recordType == "" || recordType == "recording" || recordType == "recordings" {
					return output.ErrUsageHint(
						fmt.Sprintf("Recording %s not found or type required", id),
						"Specify a type: bcq show todo|todolist|message|comment|card|document <id>",
					)
				}
				return output.ErrNotFound("recording", id)
			}

			// Parse response for summary
			var data map[string]any
			if err := json.Unmarshal(resp.Data, &data); err != nil {
				return err
			}

			// Extract title from various fields
			title := ""
			for _, key := range []string{"title", "name", "content", "subject"} {
				if v, ok := data[key].(string); ok && v != "" {
					title = v
					break
				}
			}
			if len(title) > 60 {
				title = title[:57] + "..."
			}

			itemType := "Recording"
			if t, ok := data["type"].(string); ok && t != "" {
				itemType = t
			}

			summary := fmt.Sprintf("%s #%s: %s", itemType, id, title)
			breadcrumbs := []output.Breadcrumb{
				{
					Action:      "comment",
					Cmd:         fmt.Sprintf("bcq comment --on %s --content \"text\"", id),
					Description: "Add comment",
				},
			}

			return app.OK(resp.Data,
				output.WithSummary(summary),
				output.WithBreadcrumbs(breadcrumbs...),
			)
		},
	}

	cmd.Flags().StringVarP(&recordType, "type", "t", "", "Recording type (todo, todolist, message, comment, card, card-table, document)")
	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	return cmd
}

// isValidRecordType checks if the given type is a valid recording type.
func isValidRecordType(t string) bool {
	switch t {
	case "", "todo", "todos", "todolist", "todolists", "message", "messages",
		"comment", "comments", "card", "cards", "card-table", "card_table",
		"cardtable", "document", "documents", "recording", "recordings":
		return true
	default:
		return false
	}
}
