package commands

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/completion"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewAssignCmd creates the assign command.
func NewAssignCmd() *cobra.Command {
	var assignee string
	var project string
	var isCard bool
	var isStep bool

	cmd := &cobra.Command{
		Use:   "assign <id>",
		Short: "Assign someone to an item",
		Long: `Assign a person to a to-do, card, or card step.

By default assigns to a to-do. Use --card or --step for other types.

Person can be:
  - "me" for the current user
  - A numeric person ID
  - An email address (will be resolved to ID)

Examples:
  basecamp assign 123 --to me                   # Assign to-do
  basecamp assign 456 --card --to me             # Assign card
  basecamp assign 789 --step --to me             # Assign card step`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if isCard && isStep {
				return output.ErrUsage("Cannot use --card and --step together")
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			itemID := args[0]

			resolvedProjectID, err := resolveProjectID(cmd, app, project)
			if err != nil {
				return err
			}

			if assignee == "" && !app.IsInteractive() {
				return output.ErrUsageHint("Person to assign is required", "Use --to <person>")
			}

			switch {
			case isCard:
				card, err := validateCard(cmd, app, itemID)
				if err != nil {
					return err
				}
				assigneeID, assigneeIDInt, err := resolveAssignee(cmd, app, &assignee, resolvedProjectID, "Person to assign is required", "Use --to <person>")
				if err != nil {
					return err
				}
				return assignCard(cmd, app, itemID, assigneeID, assigneeIDInt, resolvedProjectID, card)
			case isStep:
				step, err := validateStep(cmd, app, itemID, resolvedProjectID)
				if err != nil {
					return err
				}
				assigneeID, assigneeIDInt, err := resolveAssignee(cmd, app, &assignee, resolvedProjectID, "Person to assign is required", "Use --to <person>")
				if err != nil {
					return err
				}
				return assignStep(cmd, app, itemID, assigneeID, assigneeIDInt, resolvedProjectID, step)
			default:
				todo, err := validateTodo(cmd, app, itemID)
				if err != nil {
					return err
				}
				assigneeID, assigneeIDInt, err := resolveAssignee(cmd, app, &assignee, resolvedProjectID, "Person to assign is required", "Use --to <person>")
				if err != nil {
					return err
				}
				return assignTodo(cmd, app, itemID, assigneeID, assigneeIDInt, resolvedProjectID, todo)
			}
		},
	}

	cmd.Flags().StringVar(&assignee, "to", "", "Person to assign (ID, email, or 'me'); prompts interactively if omitted")
	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.Flags().BoolVar(&isCard, "card", false, "Assign to a card instead of a to-do")
	cmd.Flags().BoolVar(&isStep, "step", false, "Assign to a card step instead of a to-do")

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
	var isCard bool
	var isStep bool

	cmd := &cobra.Command{
		Use:   "unassign <id>",
		Short: "Remove assignment",
		Long: `Remove a person from a to-do, card, or card step.

By default unassigns from a to-do. Use --card or --step for other types.

Person can be:
  - "me" for the current user
  - A numeric person ID
  - An email address (will be resolved to ID)

Examples:
  basecamp unassign 123 --from me                   # Unassign from to-do
  basecamp unassign 456 --card --from me             # Unassign from card
  basecamp unassign 789 --step --from me             # Unassign from card step`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if isCard && isStep {
				return output.ErrUsage("Cannot use --card and --step together")
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			itemID := args[0]

			resolvedProjectID, err := resolveProjectID(cmd, app, project)
			if err != nil {
				return err
			}

			if assignee == "" && !app.IsInteractive() {
				return output.ErrUsageHint("Person to unassign is required", "Use --from <person>")
			}

			switch {
			case isCard:
				card, err := validateCard(cmd, app, itemID)
				if err != nil {
					return err
				}
				_, assigneeIDInt, err := resolveAssignee(cmd, app, &assignee, resolvedProjectID, "Person to unassign is required", "Use --from <person>")
				if err != nil {
					return err
				}
				return unassignCard(cmd, app, itemID, assigneeIDInt, resolvedProjectID, card)
			case isStep:
				step, err := validateStep(cmd, app, itemID, resolvedProjectID)
				if err != nil {
					return err
				}
				_, assigneeIDInt, err := resolveAssignee(cmd, app, &assignee, resolvedProjectID, "Person to unassign is required", "Use --from <person>")
				if err != nil {
					return err
				}
				return unassignStep(cmd, app, itemID, assigneeIDInt, resolvedProjectID, step)
			default:
				todo, err := validateTodo(cmd, app, itemID)
				if err != nil {
					return err
				}
				_, assigneeIDInt, err := resolveAssignee(cmd, app, &assignee, resolvedProjectID, "Person to unassign is required", "Use --from <person>")
				if err != nil {
					return err
				}
				return unassignTodo(cmd, app, itemID, assigneeIDInt, resolvedProjectID, todo)
			}
		},
	}

	cmd.Flags().StringVar(&assignee, "from", "", "Person to remove (ID, email, or 'me'); prompts interactively if omitted")
	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.Flags().BoolVar(&isCard, "card", false, "Unassign from a card instead of a to-do")
	cmd.Flags().BoolVar(&isStep, "step", false, "Unassign from a card step instead of a to-do")

	completer := completion.NewCompleter(nil)
	_ = cmd.RegisterFlagCompletionFunc("from", completer.PeopleNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("project", completer.ProjectNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("in", completer.ProjectNameCompletion())

	return cmd
}

// resolveAssignee resolves the assignee for assign/unassign commands.
func resolveAssignee(cmd *cobra.Command, app *appctx.App, assignee *string, resolvedProjectID, missingMsg, missingHint string) (string, int64, error) {
	if *assignee == "" {
		if !app.IsInteractive() {
			return "", 0, output.ErrUsageHint(missingMsg, missingHint)
		}
		selectedPerson, err := ensurePersonInProject(cmd, app, resolvedProjectID)
		if err != nil {
			return "", 0, err
		}
		*assignee = selectedPerson
	}

	assigneeID, _, err := app.Names.ResolvePerson(cmd.Context(), *assignee)
	if err != nil {
		return "", 0, err
	}

	assigneeIDInt, err := strconv.ParseInt(assigneeID, 10, 64)
	if err != nil {
		return "", 0, output.ErrUsage("Invalid assignee ID: " + assigneeID)
	}

	return assigneeID, assigneeIDInt, nil
}

// resolveProjectID resolves the project ID from flags, config, or interactive prompt.
func resolveProjectID(cmd *cobra.Command, app *appctx.App, project string) (string, error) {
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return "", err
		}
		projectID = app.Config.ProjectID
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return "", err
	}
	return resolvedProjectID, nil
}

// validateTodo fetches a to-do to verify it exists before showing the person picker.
func validateTodo(cmd *cobra.Command, app *appctx.App, todoIDStr string) (*basecamp.Todo, error) {
	todoID, err := strconv.ParseInt(todoIDStr, 10, 64)
	if err != nil {
		return nil, output.ErrUsage("Invalid to-do ID")
	}
	todo, err := app.Account().Todos().Get(cmd.Context(), todoID)
	if err != nil {
		return nil, notFoundOrConvert(err, "to-do", todoIDStr)
	}
	return todo, nil
}

// validateCard fetches a card to verify it exists before showing the person picker.
func validateCard(cmd *cobra.Command, app *appctx.App, cardIDStr string) (*basecamp.Card, error) {
	cardID, err := strconv.ParseInt(cardIDStr, 10, 64)
	if err != nil {
		return nil, output.ErrUsage("Invalid card ID")
	}
	card, err := app.Account().Cards().Get(cmd.Context(), cardID)
	if err != nil {
		return nil, notFoundOrConvert(err, "card", cardIDStr)
	}
	return card, nil
}

// validateStep fetches a card step to verify it exists before showing the person picker.
func validateStep(cmd *cobra.Command, app *appctx.App, stepIDStr, resolvedProjectID string) (*basecamp.CardStep, error) {
	stepID, err := strconv.ParseInt(stepIDStr, 10, 64)
	if err != nil {
		return nil, output.ErrUsage("Invalid step ID")
	}
	stepPath := fmt.Sprintf("/buckets/%s/card_steps/%d.json", resolvedProjectID, stepID)
	resp, err := app.Account().Get(cmd.Context(), stepPath)
	if err != nil {
		return nil, notFoundOrConvert(err, "step", stepIDStr)
	}
	var step basecamp.CardStep
	if err := resp.UnmarshalData(&step); err != nil {
		return nil, fmt.Errorf("failed to parse step: %w", err)
	}
	return &step, nil
}

// notFoundOrConvert returns a friendly not-found error for the item type,
// or converts the SDK error if it's not a 404.
func notFoundOrConvert(err error, typeName, itemIDStr string) error {
	var sdkErr *basecamp.Error
	if errors.As(err, &sdkErr) && sdkErr.Code == basecamp.CodeNotFound {
		return output.ErrNotFound(typeName, itemIDStr)
	}
	return convertSDKError(err)
}

// assignTodo assigns a person to a to-do using the SDK.
func assignTodo(cmd *cobra.Command, app *appctx.App, todoIDStr, assigneeID string, assigneeIDInt int64, resolvedProjectID string, todo *basecamp.Todo) error {
	todoID, err := strconv.ParseInt(todoIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid to-do ID")
	}

	assigneeIDs := existingAssigneeIDs(todo.Assignees)
	if containsID(assigneeIDs, assigneeIDInt) {
		assigneeName := findAssigneeName(todo.Assignees, assigneeIDInt)
		return app.OK(todo,
			output.WithSummary(fmt.Sprintf("%s is already assigned to to-do #%s", assigneeName, todoIDStr)),
		)
	}
	assigneeIDs = append(assigneeIDs, assigneeIDInt)

	updated, err := app.Account().Todos().Update(cmd.Context(), todoID, &basecamp.UpdateTodoRequest{
		AssigneeIDs: assigneeIDs,
	})
	if err != nil {
		return convertSDKError(err)
	}

	assigneeName := findAssigneeName(updated.Assignees, assigneeIDInt)

	return app.OK(updated,
		output.WithSummary(fmt.Sprintf("Assigned to-do #%s to %s", todoIDStr, assigneeName)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "view",
				Cmd:         fmt.Sprintf("basecamp show todo %s --project %s", todoIDStr, resolvedProjectID),
				Description: "View to-do",
			},
			output.Breadcrumb{
				Action:      "unassign",
				Cmd:         fmt.Sprintf("basecamp unassign %s --from %s --project %s", todoIDStr, assigneeID, resolvedProjectID),
				Description: "Remove assignee",
			},
		),
	)
}

// assignCard assigns a person to a card using the SDK.
func assignCard(cmd *cobra.Command, app *appctx.App, cardIDStr, assigneeID string, assigneeIDInt int64, resolvedProjectID string, card *basecamp.Card) error {
	cardID, err := strconv.ParseInt(cardIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid card ID")
	}

	assigneeIDs := existingAssigneeIDs(card.Assignees)
	if containsID(assigneeIDs, assigneeIDInt) {
		assigneeName := findAssigneeName(card.Assignees, assigneeIDInt)
		return app.OK(card,
			output.WithSummary(fmt.Sprintf("%s is already assigned to card #%s", assigneeName, cardIDStr)),
		)
	}
	assigneeIDs = append(assigneeIDs, assigneeIDInt)

	updated, err := app.Account().Cards().Update(cmd.Context(), cardID, &basecamp.UpdateCardRequest{
		AssigneeIDs: assigneeIDs,
	})
	if err != nil {
		return convertSDKError(err)
	}

	assigneeName := findAssigneeName(updated.Assignees, assigneeIDInt)

	return app.OK(updated,
		output.WithSummary(fmt.Sprintf("Assigned card #%s to %s", cardIDStr, assigneeName)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "view",
				Cmd:         fmt.Sprintf("basecamp cards show %s", cardIDStr),
				Description: "View card",
			},
			output.Breadcrumb{
				Action:      "unassign",
				Cmd:         fmt.Sprintf("basecamp unassign %s --card --from %s --project %s", cardIDStr, assigneeID, resolvedProjectID),
				Description: "Remove assignee",
			},
		),
	)
}

// assignStep assigns a person to a card step.
func assignStep(cmd *cobra.Command, app *appctx.App, stepIDStr, assigneeID string, assigneeIDInt int64, resolvedProjectID string, step *basecamp.CardStep) error {
	stepID, err := strconv.ParseInt(stepIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid step ID")
	}

	assigneeIDs := existingAssigneeIDs(step.Assignees)
	if containsID(assigneeIDs, assigneeIDInt) {
		assigneeName := findAssigneeName(step.Assignees, assigneeIDInt)
		return app.OK(step,
			output.WithSummary(fmt.Sprintf("%s is already assigned to step #%s", assigneeName, stepIDStr)),
		)
	}
	assigneeIDs = append(assigneeIDs, assigneeIDInt)

	updated, err := app.Account().CardSteps().Update(cmd.Context(), stepID, &basecamp.UpdateStepRequest{
		Assignees: assigneeIDs,
	})
	if err != nil {
		return convertSDKError(err)
	}

	assigneeName := findAssigneeName(updated.Assignees, assigneeIDInt)

	return app.OK(updated,
		output.WithSummary(fmt.Sprintf("Assigned step #%s to %s", stepIDStr, assigneeName)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "unassign",
				Cmd:         fmt.Sprintf("basecamp unassign %s --step --from %s --project %s", stepIDStr, assigneeID, resolvedProjectID),
				Description: "Remove assignee",
			},
		),
	)
}

// unassignTodo removes a person from a to-do's assignees using the SDK.
func unassignTodo(cmd *cobra.Command, app *appctx.App, todoIDStr string, assigneeIDInt int64, resolvedProjectID string, todo *basecamp.Todo) error {
	todoID, err := strconv.ParseInt(todoIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid to-do ID")
	}

	assigneeIDs := removeID(existingAssigneeIDs(todo.Assignees), assigneeIDInt)

	updated, err := app.Account().Todos().Update(cmd.Context(), todoID, &basecamp.UpdateTodoRequest{
		AssigneeIDs: assigneeIDs,
	})
	if err != nil {
		return convertSDKError(err)
	}

	return app.OK(updated,
		output.WithSummary(fmt.Sprintf("Removed assignee from to-do #%s", todoIDStr)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "view",
				Cmd:         fmt.Sprintf("basecamp show todo %s --project %s", todoIDStr, resolvedProjectID),
				Description: "View to-do",
			},
			output.Breadcrumb{
				Action:      "assign",
				Cmd:         fmt.Sprintf("basecamp assign %s --to <person> --project %s", todoIDStr, resolvedProjectID),
				Description: "Add assignee",
			},
		),
	)
}

// unassignCard removes a person from a card's assignees using the SDK.
func unassignCard(cmd *cobra.Command, app *appctx.App, cardIDStr string, assigneeIDInt int64, resolvedProjectID string, card *basecamp.Card) error {
	cardID, err := strconv.ParseInt(cardIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid card ID")
	}

	assigneeIDs := removeID(existingAssigneeIDs(card.Assignees), assigneeIDInt)

	updated, err := app.Account().Cards().Update(cmd.Context(), cardID, &basecamp.UpdateCardRequest{
		AssigneeIDs: assigneeIDs,
	})
	if err != nil {
		return convertSDKError(err)
	}

	return app.OK(updated,
		output.WithSummary(fmt.Sprintf("Removed assignee from card #%s", cardIDStr)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "view",
				Cmd:         fmt.Sprintf("basecamp cards show %s", cardIDStr),
				Description: "View card",
			},
			output.Breadcrumb{
				Action:      "assign",
				Cmd:         fmt.Sprintf("basecamp assign %s --card --to <person> --project %s", cardIDStr, resolvedProjectID),
				Description: "Add assignee",
			},
		),
	)
}

// unassignStep removes a person from a card step's assignees.
func unassignStep(cmd *cobra.Command, app *appctx.App, stepIDStr string, assigneeIDInt int64, resolvedProjectID string, step *basecamp.CardStep) error {
	stepID, err := strconv.ParseInt(stepIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid step ID")
	}

	assigneeIDs := removeID(existingAssigneeIDs(step.Assignees), assigneeIDInt)

	updated, err := app.Account().CardSteps().Update(cmd.Context(), stepID, &basecamp.UpdateStepRequest{
		Assignees: assigneeIDs,
	})
	if err != nil {
		return convertSDKError(err)
	}

	return app.OK(updated,
		output.WithSummary(fmt.Sprintf("Removed assignee from step #%s", stepIDStr)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "assign",
				Cmd:         fmt.Sprintf("basecamp assign %s --step --to <person> --project %s", stepIDStr, resolvedProjectID),
				Description: "Add assignee",
			},
		),
	)
}

// existingAssigneeIDs extracts IDs from a list of Person values.
func existingAssigneeIDs(people []basecamp.Person) []int64 {
	ids := make([]int64, 0, len(people))
	for _, p := range people {
		ids = append(ids, p.ID)
	}
	return ids
}

// containsID checks if a slice contains a given ID.
func containsID(ids []int64, target int64) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

// removeID returns a new slice with the target ID removed.
func removeID(ids []int64, target int64) []int64 {
	result := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id != target {
			result = append(result, id)
		}
	}
	return result
}

// findAssigneeName returns the name for a person ID from a list of assignees.
func findAssigneeName(people []basecamp.Person, id int64) string {
	for _, p := range people {
		if p.ID == id {
			return p.Name
		}
	}
	return "Unknown"
}
