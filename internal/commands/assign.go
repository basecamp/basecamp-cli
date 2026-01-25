package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// NewAssignCmd creates the assign command.
func NewAssignCmd() *cobra.Command {
	var assignee string
	var project string

	cmd := &cobra.Command{
		Use:   "assign <todo_id>",
		Short: "Assign a todo to a person",
		Long: `Assign a todo to a person.

Person can be:
  - "me" for the current user
  - A numeric person ID
  - An email address (will be resolved to ID)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			todoID := args[0]

			if assignee == "" {
				return output.ErrUsage("--to is required")
			}

			// Resolve project
			projectID := project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Resolve assignee to ID
			assigneeID, _, err := app.Names.ResolvePerson(cmd.Context(), assignee)
			if err != nil {
				return err
			}

			// Get current todo to preserve existing assignees
			todoPath := fmt.Sprintf("/buckets/%s/todos/%s.json", resolvedProjectID, todoID)
			todoResp, err := app.API.Get(cmd.Context(), todoPath)
			if err != nil {
				return err
			}

			var todo struct {
				Assignees []struct {
					ID   int64  `json:"id"`
					Name string `json:"name"`
				} `json:"assignees"`
			}
			if err := json.Unmarshal(todoResp.Data, &todo); err != nil {
				return fmt.Errorf("failed to parse todo: %w", err)
			}

			// Build new assignee list (add new, preserve existing)
			assigneeIDs := make([]int64, 0)
			var assigneeIDInt int64
			fmt.Sscanf(assigneeID, "%d", &assigneeIDInt)

			// Check if already assigned
			alreadyAssigned := false
			for _, a := range todo.Assignees {
				assigneeIDs = append(assigneeIDs, a.ID)
				if a.ID == assigneeIDInt {
					alreadyAssigned = true
				}
			}

			if !alreadyAssigned {
				assigneeIDs = append(assigneeIDs, assigneeIDInt)
			}

			// Update todo with new assignees
			body := map[string]any{
				"assignee_ids": assigneeIDs,
			}

			resp, err := app.API.Put(cmd.Context(), todoPath, body)
			if err != nil {
				return err
			}

			// Get assignee name from response
			var updated struct {
				Assignees []struct {
					ID   int64  `json:"id"`
					Name string `json:"name"`
				} `json:"assignees"`
			}
			if err := json.Unmarshal(resp.Data, &updated); err != nil {
				return fmt.Errorf("failed to parse response: %w", err)
			}

			assigneeName := "Unknown"
			for _, a := range updated.Assignees {
				if a.ID == assigneeIDInt {
					assigneeName = a.Name
					break
				}
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Assigned todo #%s to %s", todoID, assigneeName)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "view",
						Cmd:         fmt.Sprintf("bcq show todo %s --project %s", todoID, resolvedProjectID),
						Description: "View todo",
					},
					output.Breadcrumb{
						Action:      "unassign",
						Cmd:         fmt.Sprintf("bcq unassign %s --from %s --project %s", todoID, assigneeID, resolvedProjectID),
						Description: "Remove assignee",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&assignee, "to", "", "Person to assign (ID, email, or 'me')")
	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.MarkFlagRequired("to")

	return cmd
}

// NewUnassignCmd creates the unassign command.
func NewUnassignCmd() *cobra.Command {
	var assignee string
	var project string

	cmd := &cobra.Command{
		Use:   "unassign <todo_id>",
		Short: "Remove a person from a todo",
		Long: `Remove a person from a todo's assignees.

Person can be:
  - "me" for the current user
  - A numeric person ID
  - An email address (will be resolved to ID)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			todoID := args[0]

			if assignee == "" {
				return output.ErrUsage("--from is required")
			}

			// Resolve project
			projectID := project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Resolve assignee to ID
			assigneeID, _, err := app.Names.ResolvePerson(cmd.Context(), assignee)
			if err != nil {
				return err
			}

			var assigneeIDInt int64
			fmt.Sscanf(assigneeID, "%d", &assigneeIDInt)

			// Get current todo
			todoPath := fmt.Sprintf("/buckets/%s/todos/%s.json", resolvedProjectID, todoID)
			todoResp, err := app.API.Get(cmd.Context(), todoPath)
			if err != nil {
				return err
			}

			var todo struct {
				Assignees []struct {
					ID   int64  `json:"id"`
					Name string `json:"name"`
				} `json:"assignees"`
			}
			if err := json.Unmarshal(todoResp.Data, &todo); err != nil {
				return fmt.Errorf("failed to parse todo: %w", err)
			}

			// Build new assignee list (remove target)
			assigneeIDs := make([]int64, 0)
			for _, a := range todo.Assignees {
				if a.ID != assigneeIDInt {
					assigneeIDs = append(assigneeIDs, a.ID)
				}
			}

			// Update todo with new assignees
			body := map[string]any{
				"assignee_ids": assigneeIDs,
			}

			resp, err := app.API.Put(cmd.Context(), todoPath, body)
			if err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Removed assignee from todo #%s", todoID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "view",
						Cmd:         fmt.Sprintf("bcq show todo %s --project %s", todoID, resolvedProjectID),
						Description: "View todo",
					},
					output.Breadcrumb{
						Action:      "assign",
						Cmd:         fmt.Sprintf("bcq assign %s --to <person> --project %s", todoID, resolvedProjectID),
						Description: "Add assignee",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&assignee, "from", "", "Person to remove (ID, email, or 'me')")
	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.MarkFlagRequired("from")

	return cmd
}
