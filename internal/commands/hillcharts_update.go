package commands

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// newHillchartsUpdateCmd creates `hillcharts update`: move dots on a hill
// chart and post a progress update, as updating the hill in Basecamp does.
// Todolists not named keep their position, so one dot can move alone.
func newHillchartsUpdateCmd(project *string) *cobra.Command {
	var todosetID string
	var dots []string
	var note string
	var notify string

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Move dots on the hill chart and post a progress update",
		Long: `Move tracked todolists on a project's hill chart and post a progress update.

Each --dot moves one tracked todolist, named by ID or name, to a position from
0 (the bottom left, still figuring it out) to 100 (the bottom right, done);
50 is the top of the hill. Todolists you leave out keep their position.

--note is a note about the update, as Markdown (converted to rich text; pass
--format html to send HTML as written). Use - to read it from stdin.

Nobody else is notified unless named with --notify (names, emails, IDs or
"me", comma-separated). --notify alone posts the chart as it stands and tells
the people named.

  basecamp hillcharts update --dot "Design=60" --in MyProject
  basecamp hillcharts update --dot 111=40 --dot 222=100 --note "Design is done" --in MyProject
  echo "Building starts Monday" | basecamp hillcharts update --dot Research=55 --note - --notify "Jason,Ann" --in MyProject`,
		Example: `basecamp hillcharts update --dot "Design=60" --note "Over the hill" --in MyProject`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHillchartsUpdate(cmd, *project, todosetID, dots, note, notify)
		},
	}

	cmd.Flags().StringVarP(&todosetID, "todoset", "t", "", "Todoset ID (auto-detected from project)")
	cmd.Flags().StringArrayVar(&dots, "dot", nil, "Move a tracked todolist: <todolist-id|name>=<0-100> (repeatable)")
	cmd.Flags().StringVar(&note, "note", "", "Note about the update (Markdown); use - to read from stdin")
	cmd.Flags().StringVar(&notify, "notify", "", "People to notify (comma-separated names, emails, IDs, or \"me\")")
	addRichTextFormatFlag(cmd)
	allowDash(cmd, "flag:note")

	return cmd
}

// hillDotArg is one parsed --dot: the todolist as typed, and its position.
type hillDotArg struct {
	todolist string
	position int
}

// parseHillDotArg reads "<todolist>=<position>". It splits at the last "=",
// so a todolist name may itself contain one.
func parseHillDotArg(arg string) (hillDotArg, error) {
	i := strings.LastIndex(arg, "=")
	if i < 0 {
		return hillDotArg{}, output.ErrUsageHint(
			fmt.Sprintf("--dot %q needs a position", arg),
			`Use --dot <todolist-id|name>=<0-100>, e.g. --dot "Design=60"`)
	}
	todolist := strings.TrimSpace(arg[:i])
	posStr := strings.TrimSpace(arg[i+1:])
	if todolist == "" {
		return hillDotArg{}, output.ErrUsageHint(
			fmt.Sprintf("--dot %q names no todolist", arg),
			`Use --dot <todolist-id|name>=<0-100>, e.g. --dot "Design=60"`)
	}
	pos, err := strconv.Atoi(posStr)
	if err != nil || pos < 0 || pos > 100 {
		return hillDotArg{}, output.ErrUsage(fmt.Sprintf(
			"--dot %q: the position must be a whole number from 0 to 100", arg))
	}
	return hillDotArg{todolist: todolist, position: pos}, nil
}

func runHillchartsUpdate(cmd *cobra.Command, project, todosetID string, dotArgs []string, note, notify string) error {
	// Flag checks first: they need neither the account nor the network.
	parsed := make([]hillDotArg, 0, len(dotArgs))
	for _, arg := range dotArgs {
		d, err := parseHillDotArg(arg)
		if err != nil {
			return err
		}
		parsed = append(parsed, d)
	}
	if todosetID != "" && !isAllDigits(todosetID) {
		return output.ErrUsage("Invalid todoset ID: " + todosetID)
	}
	notifying := cmd.Flags().Changed("notify")
	if notifying && strings.TrimSpace(strings.ReplaceAll(notify, ",", "")) == "" {
		return output.ErrUsage("--notify needs at least one person")
	}
	// --notify alone is a post: it records the chart as it stands and tells
	// the people named, which the API allows.
	if len(parsed) == 0 && !cmd.Flags().Changed("note") && !notifying {
		return noChanges(cmd)
	}

	// Then stdin, then the account: a bad pipe gets the stdin error rather
	// than "--account is required".
	note, err := resolveContentValue(cmd, note, -1, "--note")
	if err != nil {
		return err
	}
	if cmd.Flags().Changed("note") && strings.TrimSpace(note) == "" && len(parsed) == 0 && !notifying {
		return output.ErrUsage("--note is empty, and no --dot or --notify was given: there is nothing to post")
	}

	app := appctx.FromContext(cmd.Context())
	followUp := newFollowUpScope(app)

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	resolvedProjectID, resolvedTodosetID, tsID, err := resolveHillchartTodoset(cmd, app, project, todosetID)
	if err != nil {
		return err
	}

	req := &basecamp.CreateHillChartVersionRequest{}

	seen := make(map[int64]string, len(parsed))
	for _, d := range parsed {
		resolved, err := resolveTodolistInTodoset(cmd, app, d.todolist, resolvedProjectID, resolvedTodosetID)
		if err != nil {
			return err
		}
		id, err := strconv.ParseInt(resolved, 10, 64)
		if err != nil {
			return output.ErrUsage(fmt.Sprintf("Invalid todolist ID: %s", d.todolist))
		}
		if first, dup := seen[id]; dup {
			return output.ErrUsage(fmt.Sprintf(
				"--dot %q and --dot %q move the same todolist (%d)", first, d.todolist, id))
		}
		seen[id] = d.todolist
		req.Dots = append(req.Dots, basecamp.HillChartDotPosition{ID: id, Position: d.position})
	}

	var mentionNotice string
	if strings.TrimSpace(note) != "" {
		html := richTextToHTML(cmd, note)
		mentions, err := resolveMentions(cmd.Context(), app.Names, mentionScope(app, resolvedProjectID), html)
		if err != nil {
			return err
		}
		req.Description = mentions.HTML
		mentionNotice = unresolvedMentionWarning(mentions.Unresolved)
	}

	if notify != "" {
		ids, err := resolvePersonIDs(cmd.Context(), app.Names, notify)
		if err != nil {
			return err
		}
		req.Subscriptions = &ids
	}

	version, err := app.Account().HillCharts().CreateVersion(cmd.Context(), tsID, req)
	if err != nil {
		return convertSDKError(err)
	}

	accountArgs := followUp.args(app.Config.AccountID)
	scope := hillchartScope(resolvedProjectID, todosetID) + accountArgs

	respOpts := []output.ResponseOption{
		output.WithSummary(fmt.Sprintf("Posted progress update #%d: %d dot(s) on the hill", version.ID, len(version.Dots))),
		output.WithEntity("hill_chart_version"),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "hill",
				Cmd:         fmt.Sprintf("basecamp hillcharts show %s", scope),
				Description: "View the hill chart",
			},
			output.Breadcrumb{
				Action:      "updates",
				Cmd:         fmt.Sprintf("basecamp hillcharts updates %s", scope),
				Description: "List progress updates",
			},
			output.Breadcrumb{
				Action:      "comments",
				Cmd:         fmt.Sprintf("basecamp comments list %d%s", version.ID, accountArgs),
				Description: "Read the discussion on this update",
			},
		),
	}
	if mentionNotice != "" {
		respOpts = append(respOpts, output.WithDiagnostic(mentionNotice))
	}

	return app.OK(version, respOpts...)
}
