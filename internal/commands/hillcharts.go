package commands

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewHillchartsCmd creates the hillcharts command group.
func NewHillchartsCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:   "hillcharts",
		Short: "Manage hill charts",
		Long: `Manage hill charts for tracking todolist progress.

A hill chart shows the progress of todolists in a project's todoset,
with each tracked todolist represented as a dot on the hill.`,
		Annotations: map[string]string{
			"agent_notes": "Hill charts track todolist progress. Requires a todoset (auto-detected from project). Use track/untrack with todolist IDs.",
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	cmd.AddCommand(
		newHillchartsShowCmd(&project),
		newHillchartsTrackCmd(&project),
		newHillchartsUntrackCmd(&project),
	)

	return cmd
}

func newHillchartsShowCmd(project *string) *cobra.Command {
	var todosetID string

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show hill chart for a todoset",
		Long: `Show the hill chart state for a todoset, including all tracked todolists.

  basecamp hillcharts show --in MyProject
  basecamp hillcharts show --in MyProject --todoset 12345`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHillchartsShow(cmd, *project, todosetID)
		},
	}

	cmd.Flags().StringVarP(&todosetID, "todoset", "t", "", "Todoset ID (auto-detected from project)")

	return cmd
}

func runHillchartsShow(cmd *cobra.Command, project, todosetID string) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	resolvedProjectID, err := resolveProjectID(cmd, app, project)
	if err != nil {
		return err
	}

	resolvedTodosetID, err := ensureTodoset(cmd, app, resolvedProjectID, todosetID)
	if err != nil {
		return err
	}

	tsID, err := strconv.ParseInt(resolvedTodosetID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid todoset ID")
	}

	hillChart, err := app.Account().HillCharts().Get(cmd.Context(), tsID)
	if err != nil {
		// FIXME: BC3 returns 403 when the hill chart is simply disabled (no tracked
		// todolists). Ideally the API would return a more specific status so we don't
		// have to sniff the todoset to distinguish "disabled" from "access denied".
		var sdkErr *basecamp.Error
		if errors.As(err, &sdkErr) && sdkErr.Code == basecamp.CodeForbidden {
			todoset, tsErr := app.Account().Todosets().Get(cmd.Context(), tsID)
			if tsErr == nil && todoset.TodolistsCount == 0 {
				return &output.Error{
					Code:    output.CodeUsage,
					Message: "No todolists to track on the hill chart",
					Hint:    fmt.Sprintf("Create todolists first, then track them:\n  basecamp todolists create \"My list\" --in %s\n  basecamp hillcharts track <id> --in %s", resolvedProjectID, resolvedProjectID),
				}
			}
			if tsErr == nil && todoset.TodolistsCount > 0 {
				return &output.Error{
					Code:    output.CodeUsage,
					Message: "Hill chart is not enabled (no todolists are tracked)",
					Hint:    fmt.Sprintf("Track todolists to enable it:\n  basecamp hillcharts track <todolist-ids> --in %s", resolvedProjectID),
				}
			}
		}
		return convertSDKError(err)
	}

	summary := fmt.Sprintf("Hill chart: %d dot(s) tracked", len(hillChart.Dots))

	return app.OK(hillChart,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "track",
				Cmd:         fmt.Sprintf("basecamp hillcharts track <todolist-ids> --in %s", resolvedProjectID),
				Description: "Track todolists on hill chart",
			},
			output.Breadcrumb{
				Action:      "untrack",
				Cmd:         fmt.Sprintf("basecamp hillcharts untrack <todolist-ids> --in %s", resolvedProjectID),
				Description: "Untrack todolists from hill chart",
			},
		),
	)
}

func newHillchartsTrackCmd(project *string) *cobra.Command {
	var todosetID string

	cmd := &cobra.Command{
		Use:   "track <todolist-ids>",
		Short: "Track todolists on the hill chart",
		Long: `Track one or more todolists on a project's hill chart.

Provide comma-separated todolist IDs or names:
  basecamp hillcharts track 111,222,333 --in MyProject
  basecamp hillcharts track "Shopping list" --in MyProject`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHillchartsUpdateSettings(cmd, *project, todosetID, args[0], "track")
		},
	}

	cmd.Flags().StringVarP(&todosetID, "todoset", "t", "", "Todoset ID (auto-detected from project)")

	return cmd
}

func newHillchartsUntrackCmd(project *string) *cobra.Command {
	var todosetID string

	cmd := &cobra.Command{
		Use:   "untrack <todolist-ids>",
		Short: "Untrack todolists from the hill chart",
		Long: `Remove one or more todolists from a project's hill chart.

Provide comma-separated todolist IDs or names:
  basecamp hillcharts untrack 111,222,333 --in MyProject
  basecamp hillcharts untrack "Shopping list" --in MyProject`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHillchartsUpdateSettings(cmd, *project, todosetID, args[0], "untrack")
		},
	}

	cmd.Flags().StringVarP(&todosetID, "todoset", "t", "", "Todoset ID (auto-detected from project)")

	return cmd
}

func runHillchartsUpdateSettings(cmd *cobra.Command, project, todosetID, listsArg, mode string) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	resolvedProjectID, err := resolveProjectID(cmd, app, project)
	if err != nil {
		return err
	}

	resolvedTodosetID, err := ensureTodoset(cmd, app, resolvedProjectID, todosetID)
	if err != nil {
		return err
	}

	tsID, err := strconv.ParseInt(resolvedTodosetID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid todoset ID")
	}

	// Parse comma-separated todolist IDs (supports names via resolution)
	var todolistIDs []int64
	for idStr := range strings.SplitSeq(listsArg, ",") {
		idStr = strings.TrimSpace(idStr)
		if idStr == "" {
			continue
		}
		resolved, err := resolveTodolistInTodoset(cmd, app, idStr, resolvedProjectID, resolvedTodosetID)
		if err != nil {
			return err
		}
		id, err := strconv.ParseInt(resolved, 10, 64)
		if err != nil {
			return output.ErrUsage(fmt.Sprintf("Invalid todolist ID: %s", idStr))
		}
		todolistIDs = append(todolistIDs, id)
	}

	if len(todolistIDs) == 0 {
		return output.ErrUsage("At least one todolist ID required")
	}

	var tracked, untracked []int64
	if mode == "track" {
		tracked = todolistIDs
	} else {
		untracked = todolistIDs
	}

	hillChart, err := app.Account().HillCharts().UpdateSettings(cmd.Context(), tsID, tracked, untracked)
	if err != nil {
		return convertSDKError(err)
	}

	action := "Tracked"
	if mode == "untrack" {
		action = "Untracked"
	}

	return app.OK(hillChart,
		output.WithSummary(fmt.Sprintf("%s %d todolist(s) on hill chart", action, len(todolistIDs))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("basecamp hillcharts show --in %s", resolvedProjectID),
				Description: "View hill chart",
			},
		),
	)
}
