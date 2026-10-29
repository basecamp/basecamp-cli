package commands

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// newHillchartsUpdatesCmd creates `hillcharts updates`: a hill chart's
// progress updates, its history. Run bare (or as `list`) it lists them,
// newest first; `show` reads one.
//
// A progress update is what bc3 saves each time someone updates the hill: the
// note they wrote and where every tracked todolist sat at that moment. bc3
// routes them under the todoset (/todosets/:id/hills/versions), so they
// resolve their todoset the way `hillcharts show` does.
func newHillchartsUpdatesCmd(project *string) *cobra.Command {
	var todosetID string
	var lf hillchartUpdatesListFlags

	cmd := &cobra.Command{
		Use:   "updates",
		Short: "List a hill chart's progress updates",
		Long: `List the progress updates on a project's hill chart, newest first.

Each progress update is the note someone wrote when they updated the hill
chart, and where each tracked todolist sat on the hill at that moment.

  basecamp hillcharts updates --in MyProject
  basecamp hillcharts updates --todoset 12345 --all
  basecamp hillcharts updates show 67890 --in MyProject`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return output.ErrUsageHint(
					fmt.Sprintf("hillcharts updates takes no arguments (got %q)", args[0]),
					"To show one progress update: basecamp hillcharts updates show <id|url>")
			}
			return runHillchartsUpdatesList(cmd, *project, todosetID, lf)
		},
	}

	cmd.PersistentFlags().StringVarP(&todosetID, "todoset", "t", "", "Todoset ID (auto-detected from project)")
	lf.register(cmd)

	cmd.AddCommand(
		newHillchartsUpdatesListCmd(project, &todosetID),
		newHillchartsUpdatesShowCmd(project, &todosetID),
	)

	return cmd
}

type hillchartUpdatesListFlags struct {
	limit int
	page  int
	all   bool
}

func (f *hillchartUpdatesListFlags) register(cmd *cobra.Command) {
	cmd.Flags().IntVarP(&f.limit, "limit", "n", 0,
		fmt.Sprintf("Maximum number of progress updates to fetch (0 = default %d)", basecamp.DefaultHillChartVersionLimit))
	cmd.Flags().BoolVar(&f.all, "all", false, "Fetch all progress updates (no limit)")
	cmd.Flags().IntVar(&f.page, "page", 0, "Fetch a single page (use --all for everything)")
}

func newHillchartsUpdatesListCmd(project, todosetID *string) *cobra.Command {
	var lf hillchartUpdatesListFlags

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List a hill chart's progress updates",
		Long: `List the progress updates on a project's hill chart, newest first.

  basecamp hillcharts updates list --in MyProject
  basecamp hillcharts updates list --todoset 12345 --limit 5`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHillchartsUpdatesList(cmd, *project, *todosetID, lf)
		},
	}

	lf.register(cmd)

	return cmd
}

func runHillchartsUpdatesList(cmd *cobra.Command, project, todosetID string, lf hillchartUpdatesListFlags) error {
	// Flag checks first: they need neither the account nor the network.
	if lf.limit < 0 {
		return output.ErrUsage("--limit must be zero or positive")
	}
	if lf.all && lf.limit > 0 {
		return output.ErrUsage("--all and --limit are mutually exclusive")
	}
	if lf.page > 0 && (lf.all || lf.limit > 0) {
		return output.ErrUsage("--page cannot be combined with --all or --limit")
	}
	if cmd.Flags().Changed("page") && lf.page < 1 {
		return output.ErrUsage("--page must be 1 or greater")
	}

	app := appctx.FromContext(cmd.Context())
	followUp := newFollowUpScope(app)

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	resolvedProjectID, _, tsID, err := resolveHillchartTodoset(cmd, app, project, todosetID)
	if err != nil {
		return err
	}

	opts := &basecamp.HillChartVersionListOptions{}
	switch {
	case lf.all:
		opts.Limit = -1 // SDK treats -1 as unlimited
	case lf.limit > 0:
		opts.Limit = lf.limit
	}
	if lf.page > 0 {
		opts.Page = lf.page
	}

	result, err := app.Account().HillCharts().ListVersions(cmd.Context(), tsID, opts)
	if err != nil {
		return convertSDKError(err)
	}
	versions := result.Versions
	if versions == nil {
		versions = []basecamp.HillChartVersion{}
	}

	scope := hillchartScope(resolvedProjectID, todosetID) + followUp.args(app.Config.AccountID)

	respOpts := []output.ResponseOption{
		output.WithSummary(fmt.Sprintf("%d progress update(s)", len(versions))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("basecamp hillcharts updates show <id> %s", scope),
				Description: "Show a progress update",
			},
			output.Breadcrumb{
				Action:      "hill",
				Cmd:         fmt.Sprintf("basecamp hillcharts show %s", scope),
				Description: "View the hill chart now",
			},
		),
	}
	if notice := output.TruncationNoticeWithTotal(len(versions), result.Meta.TotalCount); notice != "" {
		respOpts = append(respOpts, output.WithNotice(notice))
	}
	respOpts = append(respOpts, output.WithEntity("hill_chart_version"))

	return app.OK(versions, respOpts...)
}

func newHillchartsUpdatesShowCmd(project, todosetID *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show a hill chart progress update",
		Long: `Show one progress update on a project's hill chart: its note, who made it,
and where each tracked todolist sat on the hill.

Pass the update's ID with the project (or --todoset), or paste its Basecamp
URL, which names the todoset itself:
  basecamp hillcharts updates show 67890 --in MyProject
  basecamp hillcharts updates show https://3.basecamp.com/123/buckets/456/todosets/789/hills/versions#__recording_67890`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHillchartsUpdatesShow(cmd, *project, *todosetID, args[0])
		},
	}
}

func runHillchartsUpdatesShow(cmd *cobra.Command, project, todosetID, arg string) error {
	ref, err := hillchartUpdateArg(arg)
	if err != nil {
		return err
	}

	app := appctx.FromContext(cmd.Context())
	followUp := newFollowUpScope(app)

	// A pasted URL names its account and todoset: hold it to a trusted host
	// and the configured account before anything is resolved or requested.
	if err := timesheetURLAccount(app, arg, ref.accountID); err != nil {
		return err
	}

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	var resolvedProjectID string
	var tsID int64
	if ref.todosetID != 0 {
		// The URL's todoset wins, and a flag that names another one is a
		// contradiction to report rather than a choice to make silently.
		if todosetID != "" && todosetID != strconv.FormatInt(ref.todosetID, 10) {
			return output.ErrUsage(fmt.Sprintf(
				"--todoset %s does not match the URL's todoset %d", todosetID, ref.todosetID))
		}
		urlTodosetID := strconv.FormatInt(ref.todosetID, 10)
		if project != "" {
			resolvedProjectID, err = resolveProjectID(cmd, app, project)
			if err != nil {
				return err
			}
			switch {
			case ref.projectID == "":
				// The account-level API URL names no project; ask the todoset.
				if err := validateTodosetOwnership(cmd, app, urlTodosetID, resolvedProjectID); err != nil {
					return err
				}
			case resolvedProjectID != ref.projectID:
				return output.ErrUsage(fmt.Sprintf(
					"the URL is in project %s, not %s", ref.projectID, resolvedProjectID))
			}
		}
		tsID = ref.todosetID
		todosetID = urlTodosetID
	} else {
		resolvedProjectID, _, tsID, err = resolveHillchartTodoset(cmd, app, project, todosetID)
		if err != nil {
			return err
		}
	}

	version, err := app.Account().HillCharts().GetVersion(cmd.Context(), tsID, ref.versionID)
	if err != nil {
		return convertSDKError(err)
	}

	accountArgs := followUp.args(app.Config.AccountID)
	scope := hillchartScope(resolvedProjectID, todosetID) + accountArgs
	summary := fmt.Sprintf("Progress update #%d: %d dot(s)", version.ID, len(version.Dots))
	if version.Creator != nil && version.Creator.Name != "" {
		summary = fmt.Sprintf("Progress update #%d by %s: %d dot(s)", version.ID, version.Creator.Name, len(version.Dots))
	}

	return app.OK(version,
		output.WithSummary(summary),
		output.WithEntity("hill_chart_version"),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "comments",
				Cmd:         fmt.Sprintf("basecamp comments list %d%s", version.ID, accountArgs),
				Description: "Read the discussion on this update",
			},
			output.Breadcrumb{
				Action:      "updates",
				Cmd:         fmt.Sprintf("basecamp hillcharts updates %s", scope),
				Description: "List progress updates",
			},
		),
	)
}

// hillchartUpdateRef is what a `hillcharts updates show` argument names: the
// progress update, and, when it is a URL, the account and todoset it lives
// under, and its project when the URL names one.
type hillchartUpdateRef struct {
	versionID int64
	todosetID int64
	projectID string
	accountID string
}

// hillchartVersionRoute matches the path of a progress update's URL, web or
// API: /{account}[/buckets/{project}]/todosets/{todoset}/hills/versions, then
// either /{id}[.json] (the API's url) or nothing, with the update named by a
// #__recording_{id} fragment (the web's app_url). The project segment is
// optional because the API also routes the todoset by account alone, as the
// SDK requests it.
var hillchartVersionRoute = regexp.MustCompile(
	`^/(\d+)(?:/buckets/(\d+))?/todosets/(\d+)/hills/versions(?:/(\d+)(?:\.json)?)?/?$`)

var hillchartVersionFragment = regexp.MustCompile(`^__recording_(\d+)$`)

// hillchartUpdateArg reads a progress update ID or URL. Any other URL is
// refused rather than read as an ID: a todolist's or todoset's URL names
// something else.
func hillchartUpdateArg(arg string) (hillchartUpdateRef, error) {
	raw := strings.TrimSpace(arg)
	if isAllDigits(raw) {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return hillchartUpdateRef{}, output.ErrUsage(fmt.Sprintf("%q is not a progress update ID", arg))
		}
		return hillchartUpdateRef{versionID: id}, nil
	}

	notAnUpdate := output.ErrUsageHint(
		fmt.Sprintf("%q does not name a hill chart progress update", arg),
		"Pass the update's ID with --in or --todoset, or its URL (…/todosets/<id>/hills/versions#__recording_<id>); "+
			"basecamp hillcharts updates lists them")

	u := pastedURL(raw)
	if u == nil {
		return hillchartUpdateRef{}, notAnUpdate
	}
	m := hillchartVersionRoute.FindStringSubmatch(u.Path)
	if m == nil {
		return hillchartUpdateRef{}, notAnUpdate
	}

	idStr := m[4]
	if idStr == "" {
		f := hillchartVersionFragment.FindStringSubmatch(u.Fragment)
		if f == nil {
			return hillchartUpdateRef{}, output.ErrUsageHint(
				"That URL is the list of progress updates, not one of them",
				"List them with: basecamp hillcharts updates --todoset "+m[3]+" --account "+m[1])
		}
		idStr = f[1]
	}

	versionID, err1 := strconv.ParseInt(idStr, 10, 64)
	todosetID, err2 := strconv.ParseInt(m[3], 10, 64)
	if err1 != nil || err2 != nil || versionID <= 0 || todosetID <= 0 {
		return hillchartUpdateRef{}, notAnUpdate
	}

	return hillchartUpdateRef{
		versionID: versionID,
		todosetID: todosetID,
		projectID: m[2],
		accountID: m[1],
	}, nil
}
