package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/completion"
	"github.com/basecamp/basecamp-cli/internal/output"
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
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			todoID := args[0]

			// Resolve project first (needed for person selection), with interactive fallback
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

			// If no assignee specified, try interactive selection
			if assignee == "" {
				if !app.IsInteractive() {
					return output.ErrUsageHint("Person to assign is required", "Use --to <person>")
				}
				selectedPerson, err := ensurePersonInProject(cmd, app, resolvedProjectID)
				if err != nil {
					return err
				}
				assignee = selectedPerson
			}

			// Resolve assignee to ID
			assigneeID, _, err := app.Names.ResolvePerson(cmd.Context(), assignee)
			if err != nil {
				return err
			}

			// Get current todo to preserve existing assignees
			todoPath := fmt.Sprintf("/buckets/%s/todos/%s.json", resolvedProjectID, todoID)
			todoResp, err := app.Account().Get(cmd.Context(), todoPath)
			if err != nil {
				return convertSDKError(err)
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
			_, _ = fmt.Sscanf(assigneeID, "%d", &assigneeIDInt) //nolint:gosec // G104: ID validated by ResolvePerson

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

			resp, err := app.Account().Put(cmd.Context(), todoPath, body)
			if err != nil {
				return convertSDKError(err)
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

			return app.OK(resp.Data,
				output.WithSummary(fmt.Sprintf("Assigned todo #%s to %s", todoID, assigneeName)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "view",
						Cmd:         fmt.Sprintf("basecamp show todo %s --project %s", todoID, resolvedProjectID),
						Description: "View todo",
					},
					output.Breadcrumb{
						Action:      "unassign",
						Cmd:         fmt.Sprintf("basecamp unassign %s --from %s --project %s", todoID, assigneeID, resolvedProjectID),
						Description: "Remove assignee",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&assignee, "to", "", "Person to assign (ID, email, or 'me'); prompts interactively if omitted")
	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	// Register tab completion for flags
	completer := completion.NewCompleter(nil)
	_ = cmd.RegisterFlagCompletionFunc("to", completer.PeopleNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("project", completer.ProjectNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("in", completer.ProjectNameCompletion())

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
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			todoID := args[0]

			// Resolve project first (needed for person selection), with interactive fallback
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

			// If no assignee specified, try interactive selection
			if assignee == "" {
				if !app.IsInteractive() {
					return output.ErrUsageHint("Person to unassign is required", "Use --from <person>")
				}
				selectedPerson, err := ensurePersonInProject(cmd, app, resolvedProjectID)
				if err != nil {
					return err
				}
				assignee = selectedPerson
			}

			// Resolve assignee to ID
			assigneeID, _, err := app.Names.ResolvePerson(cmd.Context(), assignee)
			if err != nil {
				return err
			}

			var assigneeIDInt int64
			_, _ = fmt.Sscanf(assigneeID, "%d", &assigneeIDInt) //nolint:gosec // G104: ID validated by ResolvePerson

			// Get current todo
			todoPath := fmt.Sprintf("/buckets/%s/todos/%s.json", resolvedProjectID, todoID)
			todoResp, err := app.Account().Get(cmd.Context(), todoPath)
			if err != nil {
				return convertSDKError(err)
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

			resp, err := app.Account().Put(cmd.Context(), todoPath, body)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(resp.Data,
				output.WithSummary(fmt.Sprintf("Removed assignee from todo #%s", todoID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "view",
						Cmd:         fmt.Sprintf("basecamp show todo %s --project %s", todoID, resolvedProjectID),
						Description: "View todo",
					},
					output.Breadcrumb{
						Action:      "assign",
						Cmd:         fmt.Sprintf("basecamp assign %s --to <person> --project %s", todoID, resolvedProjectID),
						Description: "Add assignee",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&assignee, "from", "", "Person to remove (ID, email, or 'me'); prompts interactively if omitted")
	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	// Register tab completion for flags
	completer := completion.NewCompleter(nil)
	_ = cmd.RegisterFlagCompletionFunc("from", completer.PeopleNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("project", completer.ProjectNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("in", completer.ProjectNameCompletion())

	return cmd
}
