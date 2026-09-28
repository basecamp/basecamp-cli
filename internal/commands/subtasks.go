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

// NewSubtasksCmd creates the subtasks command group: checklist items under a
// to-do or a card.
//
// Subtasks are the canonical, flat successor to card steps. bc3 routes them by
// account alone — /recordings/:id/subtasks.json for a parent's list and
// /subtasks/:id for one subtask — so no verb needs a project, and none takes
// --in. The wire still calls a subtask "Kanban::Step", and the records are the
// same ones `cards steps` reads through its card-scoped aliases.
func NewSubtasksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "subtasks",
		Short: "Manage subtasks on to-dos and cards",
		Long: `Manage subtasks: the checklist items under a to-do or a card.

A subtask belongs to one parent, addressed by the parent's id or pasted
Basecamp URL. Every other verb addresses the subtask itself, by id or by its
URL (a parent URL ending in #__recording_<id>).

  basecamp subtasks list 123
  basecamp subtasks create 123 "Book the venue" --due friday --assignees me
  basecamp subtasks complete 456
  basecamp subtasks move 456 --position 1`,
		Annotations: map[string]string{
			"agent_notes": "Parents are to-dos and cards only; any other recording answers 403.\n" +
				"Account-scoped — no --in <project> needed.\n" +
				"Same records as `cards steps`/`cards step` (the wire type is Kanban::Step); subtasks also cover to-dos.\n" +
				"A parent embeds at most 100 subtasks under steps; `subtasks list --all` reads every one.\n" +
				"move --position is 1-based (1 = top).",
		},
	}

	cmd.AddCommand(
		newSubtasksListCmd(),
		newSubtasksShowCmd(),
		newSubtasksCreateCmd(),
		newSubtasksUpdateCmd(),
		newSubtasksCompleteCmd(),
		newSubtasksUncompleteCmd(),
		newSubtasksMoveCmd(),
		newSubtasksDeleteCmd(),
	)

	return cmd
}

func newSubtasksListCmd() *cobra.Command {
	var limit, page int
	var all bool

	cmd := &cobra.Command{
		Use:   "list <todo-or-card-id|url>",
		Short: "List a to-do's or card's subtasks",
		Long: `List the subtasks of a to-do or a card, in position order.

  basecamp subtasks list 123
  basecamp subtasks list https://3.basecamp.com/123/buckets/456/todos/789 --all`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<todo-or-card-id|url>")
			}
			if len(args) > 1 {
				return output.ErrUsage("subtasks list takes one parent id or URL")
			}
			if all && limit > 0 {
				return output.ErrUsage("--all and --limit are mutually exclusive")
			}
			if page > 0 && (all || limit > 0) {
				return output.ErrUsage("--page cannot be combined with --all or --limit")
			}
			if cmd.Flags().Changed("page") && page < 1 {
				return output.ErrUsage("--page must be 1 or greater")
			}

			parentID, err := subtaskParentID(args[0])
			if err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			opts := &basecamp.SubtaskListOptions{}
			switch {
			case all:
				opts.Limit = -1 // SDK treats -1 as unlimited
			case limit > 0:
				opts.Limit = limit
			}
			if page > 0 {
				opts.Page = page
			}

			result, err := app.Account().Subtasks().List(cmd.Context(), parentID, opts)
			if err != nil {
				return convertSDKError(err)
			}
			subtasks := result.Subtasks
			if subtasks == nil {
				subtasks = []basecamp.CardStep{}
			}

			respOpts := []output.ResponseOption{
				output.WithSummary(fmt.Sprintf("%d subtasks on #%d", len(subtasks), parentID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "create",
						Cmd:         fmt.Sprintf("basecamp subtasks create %d <title>", parentID),
						Description: "Add a subtask",
					},
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp subtasks show <id>",
						Description: "Show a subtask",
					},
				),
			}
			if notice := output.TruncationNoticeWithTotal(len(subtasks), result.Meta.TotalCount); notice != "" {
				respOpts = append(respOpts, output.WithNotice(notice))
			}

			return app.OK(subtasks, respOpts...)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of subtasks to fetch (0 = default 100)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all subtasks (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")

	return cmd
}

func newSubtasksShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show a subtask",
		Long: `Show one subtask: its title, completion, due date, assignees, and parent.

  basecamp subtasks show 456
  basecamp subtasks show https://3.basecamp.com/123/buckets/456/todos/789#__recording_1011`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			subtaskID, err := subtaskIDArg(args[0])
			if err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			subtask, err := app.Account().Subtasks().Get(cmd.Context(), subtaskID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(subtask,
				output.WithSummary(fmt.Sprintf("Subtask #%d: %s", subtask.ID, subtask.Title)),
				output.WithBreadcrumbs(subtaskBreadcrumbs(subtask)...),
			)
		},
	}
}

func newSubtasksCreateCmd() *cobra.Command {
	var dueOn string
	var assignees string

	cmd := &cobra.Command{
		Use:   "create <todo-or-card-id|url> <title>",
		Short: "Add a subtask to a to-do or card",
		Long: `Add a subtask to a to-do or a card. It lands at the bottom of the list.

  basecamp subtasks create 123 "Book the venue"
  basecamp subtasks create 123 "Send invites" --due "next friday" --assignees me,annie@example.com`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 2 {
				if len(args) == 0 {
					return missingArg(cmd, "<todo-or-card-id|url>")
				}
				return missingArg(cmd, "<title>")
			}
			if len(args) > 2 {
				return output.ErrUsageHint("subtasks create takes one parent and one title",
					`Quote a title with spaces: basecamp subtasks create 123 "Book the venue"`)
			}

			parentID, err := subtaskParentID(args[0])
			if err != nil {
				return err
			}
			title := args[1]
			if title == "" {
				return output.ErrUsage("subtask title cannot be empty")
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			req := &basecamp.CreateSubtaskRequest{Title: title}
			if dueOn != "" {
				req.DueOn = dateparse.Parse(dueOn)
			}
			if assignees != "" {
				ids, err := resolveAssigneeIDs(cmd.Context(), app, assignees)
				if err != nil {
					return err
				}
				req.AssigneeIDs = ids
			}

			subtask, err := app.Account().Subtasks().Create(cmd.Context(), parentID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(subtask,
				output.WithSummary(fmt.Sprintf("Created subtask #%d: %s", subtask.ID, subtask.Title)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "complete",
						Cmd:         fmt.Sprintf("basecamp subtasks complete %d", subtask.ID),
						Description: "Complete subtask",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("basecamp subtasks list %d", parentID),
						Description: "List subtasks",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&dueOn, "due", "d", "", "Due date (natural language or YYYY-MM-DD)")
	cmd.Flags().StringVar(&assignees, "assignees", "", "Assignees (IDs, names, or emails, comma-separated)")

	return cmd
}

func newSubtasksUpdateCmd() *cobra.Command {
	var title, dueOn, assignees string
	var noDue, noAssignees bool

	cmd := &cobra.Command{
		Use:   "update <id|url> [title]",
		Short: "Update a subtask",
		Long: `Update a subtask's title, due date, or assignees. Anything not given is
left as it is.

  basecamp subtasks update 456 "Book the bigger venue"
  basecamp subtasks update 456 --due tomorrow
  basecamp subtasks update 456 --no-due --no-assignees`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<id|url>")
			}
			if len(args) > 2 {
				return output.ErrUsageHint("subtasks update takes one subtask and at most one title",
					`Quote a title with spaces: basecamp subtasks update 456 "Book the venue"`)
			}

			subtaskID, err := subtaskIDArg(args[0])
			if err != nil {
				return err
			}
			if len(args) == 2 {
				if cmd.Flags().Changed("title") {
					return output.ErrUsage("pass the title as an argument or with --title, not both")
				}
				title = args[1]
			}
			if (cmd.Flags().Changed("title") || len(args) == 2) && title == "" {
				return output.ErrUsage("subtask title cannot be empty")
			}
			if noDue && dueOn != "" {
				return output.ErrUsage("--due and --no-due are mutually exclusive")
			}
			if noAssignees && assignees != "" {
				return output.ErrUsage("--assignees and --no-assignees are mutually exclusive")
			}
			if title == "" && dueOn == "" && !noDue && assignees == "" && !noAssignees {
				return noChanges(cmd)
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			req := &basecamp.UpdateSubtaskRequest{Title: title}
			switch {
			case noDue:
				req.DueOn = basecamp.Ptr("")
			case dueOn != "":
				req.DueOn = basecamp.Ptr(dateparse.Parse(dueOn))
			}
			switch {
			case noAssignees:
				req.AssigneeIDs = []int64{}
			case assignees != "":
				ids, err := resolveAssigneeIDs(cmd.Context(), app, assignees)
				if err != nil {
					return err
				}
				req.AssigneeIDs = ids
			}

			subtask, err := app.Account().Subtasks().Update(cmd.Context(), subtaskID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(subtask,
				output.WithSummary(fmt.Sprintf("Updated subtask #%d", subtask.ID)),
				output.WithBreadcrumbs(subtaskBreadcrumbs(subtask)...),
			)
		},
	}

	cmd.Flags().StringVarP(&title, "title", "t", "", "New title")
	cmd.Flags().StringVarP(&dueOn, "due", "d", "", "Due date (natural language or YYYY-MM-DD)")
	cmd.Flags().BoolVar(&noDue, "no-due", false, "Clear the due date")
	cmd.Flags().StringVar(&assignees, "assignees", "", "Replace assignees (IDs, names, or emails, comma-separated)")
	cmd.Flags().BoolVar(&noAssignees, "no-assignees", false, "Remove every assignee")

	return cmd
}

func newSubtasksCompleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "complete <id|url>",
		Short: "Complete a subtask",
		Long: `Mark a subtask as completed.

  basecamp subtasks complete 456`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSubtaskCompletion(cmd, args[0], true)
		},
	}
}

func newSubtasksUncompleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uncomplete <id|url>",
		Short: "Uncomplete a subtask",
		Long: `Mark a subtask as not completed.

  basecamp subtasks uncomplete 456`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSubtaskCompletion(cmd, args[0], false)
		},
	}
}

// runSubtaskCompletion drives complete and uncomplete. bc3 answers both with a
// bare 204, so the payload reports the state that was asked for.
func runSubtaskCompletion(cmd *cobra.Command, arg string, complete bool) error {
	subtaskID, err := subtaskIDArg(arg)
	if err != nil {
		return err
	}

	app := appctx.FromContext(cmd.Context())
	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	summary := fmt.Sprintf("Completed subtask #%d", subtaskID)
	undo := output.Breadcrumb{
		Action:      "uncomplete",
		Cmd:         fmt.Sprintf("basecamp subtasks uncomplete %d", subtaskID),
		Description: "Uncomplete subtask",
	}
	if complete {
		err = app.Account().Subtasks().Complete(cmd.Context(), subtaskID)
	} else {
		err = app.Account().Subtasks().Uncomplete(cmd.Context(), subtaskID)
		summary = fmt.Sprintf("Uncompleted subtask #%d", subtaskID)
		undo = output.Breadcrumb{
			Action:      "complete",
			Cmd:         fmt.Sprintf("basecamp subtasks complete %d", subtaskID),
			Description: "Complete subtask",
		}
	}
	if err != nil {
		return convertSDKError(err)
	}

	return app.OK(map[string]any{"id": subtaskID, "completed": complete},
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			undo,
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("basecamp subtasks show %d", subtaskID),
				Description: "Show subtask",
			},
		),
	)
}

func newSubtasksMoveCmd() *cobra.Command {
	var position int

	cmd := &cobra.Command{
		Use:   "move <id|url>",
		Short: "Move a subtask",
		Long: `Move a subtask to a new position among its siblings. Positions are
1-based: 1 is the top.

  basecamp subtasks move 456 --position 1`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			subtaskID, err := subtaskIDArg(args[0])
			if err != nil {
				return err
			}
			if position < 1 {
				return output.ErrUsage("--position is required (1-based)")
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if err := app.Account().Subtasks().Reposition(cmd.Context(), subtaskID, position); err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"id": subtaskID, "position": position, "moved": true},
				output.WithSummary(fmt.Sprintf("Moved subtask #%d to position %d", subtaskID, position)),
				output.WithBreadcrumbs(output.Breadcrumb{
					Action:      "show",
					Cmd:         fmt.Sprintf("basecamp subtasks show %d", subtaskID),
					Description: "Show subtask",
				}),
			)
		},
	}

	cmd.Flags().IntVar(&position, "position", 0, "Target position (1-based)")
	cmd.Flags().IntVar(&position, "pos", 0, "Target position (alias for --position)")

	return cmd
}

func newSubtasksDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id|url>",
		Short: "Delete a subtask",
		Long: `Permanently delete a subtask. On accounts that limit deleting to admins and
the creator, everyone else is refused.

  basecamp subtasks delete 456`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			subtaskID, err := subtaskIDArg(args[0])
			if err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if err := app.Account().Subtasks().Delete(cmd.Context(), subtaskID); err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"id": subtaskID, "deleted": true},
				output.WithSummary(fmt.Sprintf("Deleted subtask #%d", subtaskID)),
			)
		},
	}
}

// subtaskBreadcrumbs are the follow-ups for a single subtask: toggle its
// completion, and go back to its parent's list.
func subtaskBreadcrumbs(subtask *basecamp.CardStep) []output.Breadcrumb {
	crumbs := []output.Breadcrumb{}
	if subtask.Completed {
		crumbs = append(crumbs, output.Breadcrumb{
			Action:      "uncomplete",
			Cmd:         fmt.Sprintf("basecamp subtasks uncomplete %d", subtask.ID),
			Description: "Uncomplete subtask",
		})
	} else {
		crumbs = append(crumbs, output.Breadcrumb{
			Action:      "complete",
			Cmd:         fmt.Sprintf("basecamp subtasks complete %d", subtask.ID),
			Description: "Complete subtask",
		})
	}
	if subtask.Parent != nil && subtask.Parent.ID != 0 {
		crumbs = append(crumbs, output.Breadcrumb{
			Action:      "list",
			Cmd:         fmt.Sprintf("basecamp subtasks list %d", subtask.Parent.ID),
			Description: "List sibling subtasks",
		})
	}
	return crumbs
}

// subtaskParentID resolves the <todo-or-card-id|url> positional: the id of the
// to-do or card itself, never a fragment inside its URL.
func subtaskParentID(arg string) (int64, error) {
	id, err := strconv.ParseInt(extractID(arg), 10, 64)
	if err != nil || id <= 0 {
		return 0, output.ErrUsageHint(
			fmt.Sprintf("%q is not a to-do or card id or Basecamp URL", arg),
			"Pass the numeric id of the to-do or card, or paste its Basecamp URL",
		)
	}
	return id, nil
}

// subtaskIDArg resolves the <id|url> positional the per-subtask verbs take. A
// subtask's app URL is its parent's URL with a #__recording_<id> fragment, so
// the fragment wins over the parent id in the path.
func subtaskIDArg(arg string) (int64, error) {
	raw, _ := extractCommentWithProject(arg)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, output.ErrUsageHint(
			fmt.Sprintf("%q is not a subtask id or Basecamp URL", arg),
			"Pass the numeric subtask id, or paste its URL (the parent's URL ending in #__recording_<id>)",
		)
	}
	return id, nil
}
