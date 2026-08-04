package commands

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

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
		Use:   "assign <id|url>...",
		Short: "Assign someone to an item",
		Long: `Assign a person to one or more to-dos, cards, or card steps.

	By default assigns to a to-do. Use --card or --step for other types.

	Person can be:
	  - "me" for the current user
	  - A numeric person ID
	  - An email address (will be resolved to ID)

	Examples:
	  basecamp assign 123 --to me                     # Assign to-do
	  basecamp assign 123 456 --to me                  # Assign multiple to-dos
	  basecamp assign 456 --card --to me               # Assign card
	  basecamp assign 789 --step --to me               # Assign card step`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<id|url>...")
			}
			if isCard && isStep {
				return output.ErrUsage("Cannot use --card and --step together")
			}
			return assignItems(cmd, args, &assignee, project, isCard, isStep)
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
		Use:   "unassign <id|url>...",
		Short: "Remove assignment",
		Long: `Remove a person from one or more to-dos, cards, or card steps.

	By default unassigns from a to-do. Use --card or --step for other types.

	Person can be:
	  - "me" for the current user
	  - A numeric person ID
	  - An email address (will be resolved to ID)

	Examples:
	  basecamp unassign 123 --from me                     # Unassign from to-do
	  basecamp unassign 123 456 --from me                  # Unassign multiple to-dos
	  basecamp unassign 456 --card --from me               # Unassign from card
	  basecamp unassign 789 --step --from me               # Unassign from card step`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<id|url>...")
			}
			if isCard && isStep {
				return output.ErrUsage("Cannot use --card and --step together")
			}
			return unassignItems(cmd, args, &assignee, project, isCard, isStep)
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

// fatalAssignError wraps errors that should halt the batch loop (e.g. assignee
// resolution failure), distinguishing them from per-item validation errors.
type fatalAssignError struct{ err error }

func (e *fatalAssignError) Error() string { return e.err.Error() }
func (e *fatalAssignError) Unwrap() error { return e.err }

// assignResult holds the outcome of a single assign/unassign operation.
type assignResult struct {
	id          string
	item        any
	summary     string
	breadcrumbs []output.Breadcrumb
}

func assignItems(cmd *cobra.Command, args []string, assignee *string, project string, isCard, isStep bool) error {
	app := appctx.FromContext(cmd.Context())
	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	resolvedProjectID, err := resolveProjectID(cmd, app, project)
	if err != nil {
		return err
	}

	if *assignee == "" && !app.IsInteractive() {
		return output.ErrUsageHint("Person to assign is required", "Use --to <person>")
	}

	extractedIDs := extractIDs(args)
	if len(extractedIDs) == 0 {
		return missingArg(cmd, "<id|url>...")
	}

	var results []*assignResult
	var failed []string
	var firstErr error
	var assigneeResolved bool
	var assigneeID string
	var assigneeIDInt int64

	for _, itemID := range extractedIDs {
		res, err := assignOneItem(cmd, app, itemID, isCard, isStep, resolvedProjectID,
			assignee, &assigneeResolved, &assigneeID, &assigneeIDInt)
		if err != nil {
			var fatal *fatalAssignError
			if errors.As(err, &fatal) {
				return fatal.err
			}
			failed = append(failed, itemID)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		results = append(results, res)
	}

	if len(results) == 0 && len(failed) > 0 {
		return batchFailError("assign", failed, firstErr)
	}

	// Single item, no failures — return directly with per-item breadcrumbs
	if len(results) == 1 && len(failed) == 0 {
		return app.OK(results[0].item,
			output.WithSummary(results[0].summary),
			output.WithBreadcrumbs(results[0].breadcrumbs...),
		)
	}

	summary := fmt.Sprintf("Assigned %d item(s)", len(results))
	if len(failed) > 0 {
		summary = fmt.Sprintf("Assigned %d, failed %d", len(results), len(failed))
	}

	var typeFlag string
	if isCard {
		typeFlag = " --card"
	} else if isStep {
		typeFlag = " --step"
	}

	batchBreadcrumbs := []output.Breadcrumb{{
		Action:      "unassign",
		Cmd:         fmt.Sprintf("basecamp unassign %s%s --from %s --project %s", results[0].id, typeFlag, assigneeID, resolvedProjectID),
		Description: "Remove assignee",
	}}

	if len(results) == 1 {
		return app.OK(results[0].item,
			output.WithSummary(summary),
			output.WithBreadcrumbs(batchBreadcrumbs...),
		)
	}

	items := make([]any, len(results))
	for i, r := range results {
		items[i] = r.item
	}

	return app.OK(items,
		output.WithSummary(summary),
		output.WithBreadcrumbs(batchBreadcrumbs...),
	)
}

func unassignItems(cmd *cobra.Command, args []string, assignee *string, project string, isCard, isStep bool) error {
	app := appctx.FromContext(cmd.Context())
	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	resolvedProjectID, err := resolveProjectID(cmd, app, project)
	if err != nil {
		return err
	}

	if *assignee == "" && !app.IsInteractive() {
		return output.ErrUsageHint("Person to unassign is required", "Use --from <person>")
	}

	extractedIDs := extractIDs(args)
	if len(extractedIDs) == 0 {
		return missingArg(cmd, "<id|url>...")
	}

	var results []*assignResult
	var failed []string
	var firstErr error
	var assigneeResolved bool
	var assigneeIDInt int64

	for _, itemID := range extractedIDs {
		res, err := unassignOneItem(cmd, app, itemID, isCard, isStep, resolvedProjectID,
			assignee, &assigneeResolved, &assigneeIDInt)
		if err != nil {
			var fatal *fatalAssignError
			if errors.As(err, &fatal) {
				return fatal.err
			}
			failed = append(failed, itemID)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		results = append(results, res)
	}

	if len(results) == 0 && len(failed) > 0 {
		return batchFailError("unassign", failed, firstErr)
	}

	// Single item, no failures — return directly with per-item breadcrumbs
	if len(results) == 1 && len(failed) == 0 {
		return app.OK(results[0].item,
			output.WithSummary(results[0].summary),
			output.WithBreadcrumbs(results[0].breadcrumbs...),
		)
	}

	summary := fmt.Sprintf("Unassigned %d item(s)", len(results))
	if len(failed) > 0 {
		summary = fmt.Sprintf("Unassigned %d, failed %d", len(results), len(failed))
	}

	var typeFlag string
	if isCard {
		typeFlag = " --card"
	} else if isStep {
		typeFlag = " --step"
	}

	batchBreadcrumbs := []output.Breadcrumb{{
		Action:      "assign",
		Cmd:         fmt.Sprintf("basecamp assign %s%s --to <person> --project %s", results[0].id, typeFlag, resolvedProjectID),
		Description: "Add assignee",
	}}

	if len(results) == 1 {
		return app.OK(results[0].item,
			output.WithSummary(summary),
			output.WithBreadcrumbs(batchBreadcrumbs...),
		)
	}

	items := make([]any, len(results))
	for i, r := range results {
		items[i] = r.item
	}

	return app.OK(items,
		output.WithSummary(summary),
		output.WithBreadcrumbs(batchBreadcrumbs...),
	)
}

// batchFailError builds an error when all items in a batch operation failed.
// Preserves typed errors (not-found, API errors) from validation and mutation.
func batchFailError(action string, failed []string, firstErr error) error {
	if firstErr != nil {
		var outErr *output.Error
		if errors.As(firstErr, &outErr) {
			return &output.Error{
				Code:       outErr.Code,
				Message:    fmt.Sprintf("Failed to %s items %s: %s", action, strings.Join(failed, ", "), outErr.Message),
				Hint:       outErr.Hint,
				HTTPStatus: outErr.HTTPStatus,
				Retryable:  outErr.Retryable,
				Cause:      outErr,
			}
		}
		return fmt.Errorf("failed to %s items %s: %w", action, strings.Join(failed, ", "), firstErr)
	}
	return output.ErrUsage(fmt.Sprintf("Invalid item ID(s): %s", strings.Join(failed, ", ")))
}

// assignOneItem validates one item and assigns it. The assignee is resolved
// lazily on the first call where *assigneeResolved is false, preserving
// PR #279 ordering (validate before person picker).
func assignOneItem(cmd *cobra.Command, app *appctx.App, itemID string, isCard, isStep bool, resolvedProjectID string,
	assignee *string, assigneeResolved *bool, assigneeID *string, assigneeIDInt *int64) (*assignResult, error) {

	switch {
	case isCard:
		card, err := validateCard(cmd, app, itemID)
		if err != nil {
			return nil, err
		}
		if !*assigneeResolved {
			aID, aIDInt, err := resolveAssignee(cmd, app, assignee, resolvedProjectID,
				"Person to assign is required", "Use --to <person>")
			if err != nil {
				return nil, &fatalAssignError{err}
			}
			*assigneeID, *assigneeIDInt, *assigneeResolved = aID, aIDInt, true
		}
		return doAssignCard(cmd, app, itemID, *assigneeID, *assigneeIDInt, resolvedProjectID, card)
	case isStep:
		step, err := validateStep(cmd, app, itemID)
		if err != nil {
			return nil, err
		}
		if !*assigneeResolved {
			aID, aIDInt, err := resolveAssignee(cmd, app, assignee, resolvedProjectID,
				"Person to assign is required", "Use --to <person>")
			if err != nil {
				return nil, &fatalAssignError{err}
			}
			*assigneeID, *assigneeIDInt, *assigneeResolved = aID, aIDInt, true
		}
		return doAssignStep(cmd, app, itemID, *assigneeID, *assigneeIDInt, resolvedProjectID, step)
	default:
		todo, err := validateTodo(cmd, app, itemID)
		if err != nil {
			return nil, err
		}
		if !*assigneeResolved {
			aID, aIDInt, err := resolveAssignee(cmd, app, assignee, resolvedProjectID,
				"Person to assign is required", "Use --to <person>")
			if err != nil {
				return nil, &fatalAssignError{err}
			}
			*assigneeID, *assigneeIDInt, *assigneeResolved = aID, aIDInt, true
		}
		return doAssignTodo(cmd, app, itemID, *assigneeID, *assigneeIDInt, resolvedProjectID, todo)
	}
}

// unassignOneItem validates one item and unassigns from it. The assignee is
// resolved lazily on the first call where *assigneeResolved is false.
func unassignOneItem(cmd *cobra.Command, app *appctx.App, itemID string, isCard, isStep bool, resolvedProjectID string,
	assignee *string, assigneeResolved *bool, assigneeIDInt *int64) (*assignResult, error) {

	switch {
	case isCard:
		card, err := validateCard(cmd, app, itemID)
		if err != nil {
			return nil, err
		}
		if !*assigneeResolved {
			_, aIDInt, err := resolveAssignee(cmd, app, assignee, resolvedProjectID,
				"Person to unassign is required", "Use --from <person>")
			if err != nil {
				return nil, &fatalAssignError{err}
			}
			*assigneeIDInt, *assigneeResolved = aIDInt, true
		}
		return doUnassignCard(cmd, app, itemID, *assigneeIDInt, resolvedProjectID, card)
	case isStep:
		step, err := validateStep(cmd, app, itemID)
		if err != nil {
			return nil, err
		}
		if !*assigneeResolved {
			_, aIDInt, err := resolveAssignee(cmd, app, assignee, resolvedProjectID,
				"Person to unassign is required", "Use --from <person>")
			if err != nil {
				return nil, &fatalAssignError{err}
			}
			*assigneeIDInt, *assigneeResolved = aIDInt, true
		}
		return doUnassignStep(cmd, app, itemID, *assigneeIDInt, resolvedProjectID, step)
	default:
		todo, err := validateTodo(cmd, app, itemID)
		if err != nil {
			return nil, err
		}
		if !*assigneeResolved {
			_, aIDInt, err := resolveAssignee(cmd, app, assignee, resolvedProjectID,
				"Person to unassign is required", "Use --from <person>")
			if err != nil {
				return nil, &fatalAssignError{err}
			}
			*assigneeIDInt, *assigneeResolved = aIDInt, true
		}
		return doUnassignTodo(cmd, app, itemID, *assigneeIDInt, resolvedProjectID, todo)
	}
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
func validateStep(cmd *cobra.Command, app *appctx.App, stepIDStr string) (*basecamp.CardStep, error) {
	stepID, err := strconv.ParseInt(stepIDStr, 10, 64)
	if err != nil {
		return nil, output.ErrUsage("Invalid step ID")
	}
	step, err := app.Account().CardSteps().Get(cmd.Context(), stepID)
	if err != nil {
		return nil, notFoundOrConvert(err, "step", stepIDStr)
	}
	return step, nil
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

// doAssignTodo assigns a person to a to-do.
func doAssignTodo(cmd *cobra.Command, app *appctx.App, todoIDStr, assigneeID string, assigneeIDInt int64, resolvedProjectID string, todo *basecamp.Todo) (*assignResult, error) {
	todoID, err := strconv.ParseInt(todoIDStr, 10, 64)
	if err != nil {
		return nil, output.ErrUsage("Invalid to-do ID")
	}

	assigneeIDs := existingAssigneeIDs(todo.Assignees)
	if containsID(assigneeIDs, assigneeIDInt) {
		assigneeName := findAssigneeName(todo.Assignees, assigneeIDInt)
		return &assignResult{
			id:      todoIDStr,
			item:    todo,
			summary: fmt.Sprintf("%s is already assigned to to-do #%s", assigneeName, todoIDStr),
		}, nil
	}
	assigneeIDs = append(assigneeIDs, assigneeIDInt)

	updated, err := app.Account().Todos().Update(cmd.Context(), todoID, &basecamp.UpdateTodoRequest{
		AssigneeIDs: assigneeIDs,
	})
	if err != nil {
		return nil, convertSDKError(err)
	}

	assigneeName := findAssigneeName(updated.Assignees, assigneeIDInt)

	return &assignResult{
		id:      todoIDStr,
		item:    updated,
		summary: fmt.Sprintf("Assigned to-do #%s to %s", todoIDStr, assigneeName),
		breadcrumbs: []output.Breadcrumb{
			{
				Action:      "view",
				Cmd:         fmt.Sprintf("basecamp show todo %s --project %s", todoIDStr, resolvedProjectID),
				Description: "View to-do",
			},
			{
				Action:      "unassign",
				Cmd:         fmt.Sprintf("basecamp unassign %s --from %s --project %s", todoIDStr, assigneeID, resolvedProjectID),
				Description: "Remove assignee",
			},
		},
	}, nil
}

// doAssignCard assigns a person to a card.
func doAssignCard(cmd *cobra.Command, app *appctx.App, cardIDStr, assigneeID string, assigneeIDInt int64, resolvedProjectID string, card *basecamp.Card) (*assignResult, error) {
	cardID, err := strconv.ParseInt(cardIDStr, 10, 64)
	if err != nil {
		return nil, output.ErrUsage("Invalid card ID")
	}

	assigneeIDs := existingAssigneeIDs(card.Assignees)
	if containsID(assigneeIDs, assigneeIDInt) {
		assigneeName := findAssigneeName(card.Assignees, assigneeIDInt)
		return &assignResult{
			id:      cardIDStr,
			item:    card,
			summary: fmt.Sprintf("%s is already assigned to card #%s", assigneeName, cardIDStr),
		}, nil
	}
	assigneeIDs = append(assigneeIDs, assigneeIDInt)

	updated, err := app.Account().Cards().Update(cmd.Context(), cardID, &basecamp.UpdateCardRequest{
		AssigneeIDs: assigneeIDs,
	})
	if err != nil {
		return nil, convertSDKError(err)
	}

	assigneeName := findAssigneeName(updated.Assignees, assigneeIDInt)

	return &assignResult{
		id:      cardIDStr,
		item:    updated,
		summary: fmt.Sprintf("Assigned card #%s to %s", cardIDStr, assigneeName),
		breadcrumbs: []output.Breadcrumb{
			{
				Action:      "view",
				Cmd:         fmt.Sprintf("basecamp cards show %s", cardIDStr),
				Description: "View card",
			},
			{
				Action:      "unassign",
				Cmd:         fmt.Sprintf("basecamp unassign %s --card --from %s --project %s", cardIDStr, assigneeID, resolvedProjectID),
				Description: "Remove assignee",
			},
		},
	}, nil
}

// doAssignStep assigns a person to a card step.
func doAssignStep(cmd *cobra.Command, app *appctx.App, stepIDStr, assigneeID string, assigneeIDInt int64, resolvedProjectID string, step *basecamp.CardStep) (*assignResult, error) {
	stepID, err := strconv.ParseInt(stepIDStr, 10, 64)
	if err != nil {
		return nil, output.ErrUsage("Invalid step ID")
	}

	assigneeIDs := existingAssigneeIDs(step.Assignees)
	if containsID(assigneeIDs, assigneeIDInt) {
		assigneeName := findAssigneeName(step.Assignees, assigneeIDInt)
		return &assignResult{
			id:      stepIDStr,
			item:    step,
			summary: fmt.Sprintf("%s is already assigned to step #%s", assigneeName, stepIDStr),
		}, nil
	}
	assigneeIDs = append(assigneeIDs, assigneeIDInt)

	updated, err := app.Account().CardSteps().Update(cmd.Context(), stepID, &basecamp.UpdateStepRequest{
		AssigneeIDs: assigneeIDs,
	})
	if err != nil {
		return nil, convertSDKError(err)
	}

	assigneeName := findAssigneeName(updated.Assignees, assigneeIDInt)

	return &assignResult{
		id:      stepIDStr,
		item:    updated,
		summary: fmt.Sprintf("Assigned step #%s to %s", stepIDStr, assigneeName),
		breadcrumbs: []output.Breadcrumb{
			{
				Action:      "unassign",
				Cmd:         fmt.Sprintf("basecamp unassign %s --step --from %s --project %s", stepIDStr, assigneeID, resolvedProjectID),
				Description: "Remove assignee",
			},
		},
	}, nil
}

// doUnassignTodo removes a person from a to-do's assignees.
func doUnassignTodo(cmd *cobra.Command, app *appctx.App, todoIDStr string, assigneeIDInt int64, resolvedProjectID string, todo *basecamp.Todo) (*assignResult, error) {
	todoID, err := strconv.ParseInt(todoIDStr, 10, 64)
	if err != nil {
		return nil, output.ErrUsage("Invalid to-do ID")
	}

	assigneeIDs := removeID(existingAssigneeIDs(todo.Assignees), assigneeIDInt)

	updated, err := app.Account().Todos().Update(cmd.Context(), todoID, &basecamp.UpdateTodoRequest{
		AssigneeIDs: assigneeIDs,
	})
	if err != nil {
		return nil, convertSDKError(err)
	}

	return &assignResult{
		id:      todoIDStr,
		item:    updated,
		summary: fmt.Sprintf("Removed assignee from to-do #%s", todoIDStr),
		breadcrumbs: []output.Breadcrumb{
			{
				Action:      "view",
				Cmd:         fmt.Sprintf("basecamp show todo %s --project %s", todoIDStr, resolvedProjectID),
				Description: "View to-do",
			},
			{
				Action:      "assign",
				Cmd:         fmt.Sprintf("basecamp assign %s --to <person> --project %s", todoIDStr, resolvedProjectID),
				Description: "Add assignee",
			},
		},
	}, nil
}

// doUnassignCard removes a person from a card's assignees.
func doUnassignCard(cmd *cobra.Command, app *appctx.App, cardIDStr string, assigneeIDInt int64, resolvedProjectID string, card *basecamp.Card) (*assignResult, error) {
	cardID, err := strconv.ParseInt(cardIDStr, 10, 64)
	if err != nil {
		return nil, output.ErrUsage("Invalid card ID")
	}

	assigneeIDs := removeID(existingAssigneeIDs(card.Assignees), assigneeIDInt)

	updated, err := app.Account().Cards().Update(cmd.Context(), cardID, &basecamp.UpdateCardRequest{
		AssigneeIDs: assigneeIDs,
	})
	if err != nil {
		return nil, convertSDKError(err)
	}

	return &assignResult{
		id:      cardIDStr,
		item:    updated,
		summary: fmt.Sprintf("Removed assignee from card #%s", cardIDStr),
		breadcrumbs: []output.Breadcrumb{
			{
				Action:      "view",
				Cmd:         fmt.Sprintf("basecamp cards show %s", cardIDStr),
				Description: "View card",
			},
			{
				Action:      "assign",
				Cmd:         fmt.Sprintf("basecamp assign %s --card --to <person> --project %s", cardIDStr, resolvedProjectID),
				Description: "Add assignee",
			},
		},
	}, nil
}

// doUnassignStep removes a person from a card step's assignees.
func doUnassignStep(cmd *cobra.Command, app *appctx.App, stepIDStr string, assigneeIDInt int64, resolvedProjectID string, step *basecamp.CardStep) (*assignResult, error) {
	stepID, err := strconv.ParseInt(stepIDStr, 10, 64)
	if err != nil {
		return nil, output.ErrUsage("Invalid step ID")
	}

	assigneeIDs := removeID(existingAssigneeIDs(step.Assignees), assigneeIDInt)

	updated, err := app.Account().CardSteps().Update(cmd.Context(), stepID, &basecamp.UpdateStepRequest{
		AssigneeIDs: assigneeIDs,
	})
	if err != nil {
		return nil, convertSDKError(err)
	}

	return &assignResult{
		id:      stepIDStr,
		item:    updated,
		summary: fmt.Sprintf("Removed assignee from step #%s", stepIDStr),
		breadcrumbs: []output.Breadcrumb{
			{
				Action:      "assign",
				Cmd:         fmt.Sprintf("basecamp assign %s --step --to <person> --project %s", stepIDStr, resolvedProjectID),
				Description: "Add assignee",
			},
		},
	}, nil
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
