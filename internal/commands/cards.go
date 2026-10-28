package commands

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/completion"
	"github.com/basecamp/basecamp-cli/internal/dateparse"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/urlarg"
)

// NewCardsCmd creates the cards command group.
func NewCardsCmd() *cobra.Command {
	var project string
	var cardTable string

	cmd := &cobra.Command{
		Use:         "cards",
		Short:       "Manage cards in Card Tables",
		Long:        "List, show, create, and manage cards in Card Tables (Kanban boards).",
		Annotations: map[string]string{"agent_notes": "--assignee filters the account-wide listing only; within a project, fetch all and filter client-side\nIf a project has multiple card tables, you must specify --card-table <id>\nAssign/unassign shortcuts work on cards: basecamp assign <card_id> --to <person>\nCross-project cards: basecamp recordings cards --json"},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.PersistentFlags().StringVar(&cardTable, "card-table", "", "Card table ID (required if project has multiple)")

	cmd.AddCommand(
		newCardsListCmd(&project, &cardTable),
		newCardsShowCmd(),
		newCardsCreateCmd(&project, &cardTable),
		newCardsUpdateCmd(),
		newCardsMoveCmd(&project, &cardTable),
		newCardsCopyCmd(&project),
		newCardsDoneCmd(&project, &cardTable),
		newCardsColumnsCmd(&project, &cardTable),
		newCardsColumnCmd(&project, &cardTable),
		newCardsWormholesCmd(&project, &cardTable),
		newCardsStepsCmd(&project),
		newCardsStepCmd(&project),
		newRecordableTrashCmd("card"),
		newRecordableArchiveCmd("card"),
		newRecordableRestoreCmd("card"),
	)

	return cmd
}

// cardsListOptions carries the flags `cards list` accepts, including the
// group's persistent --project/--card-table, so the project-scoped and
// account-wide branches read the same values.
type cardsListOptions struct {
	project     string
	column      string
	cardTable   string
	limit       int
	page        int
	pageSet     bool
	all         bool
	allProjects bool
	sortField   string
	reverse     bool
	status      string
	unassigned  bool
	noDueDate   bool
	notNow      bool
	overdue     bool
	assignees   []string
	due         string
}

// Account-wide card listings, one per Everything endpoint.
const (
	cardsSelectorOpen       = "open"
	cardsSelectorCompleted  = "completed"
	cardsSelectorUnassigned = "unassigned"
	cardsSelectorNoDueDate  = "no-due-date"
	cardsSelectorNotNow     = "not-now"
	cardsSelectorOverdue    = "overdue"
)

func newCardsListCmd(project, cardTable *string) *cobra.Command {
	var opts cardsListOptions

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List cards",
		Long: "List the cards in a project's card table, or across every project with --all-projects.\n\n" +
			"Without --column the listing covers every column but stops at 100 cards from each, leaving\n" +
			"the rest out; --all lifts that cap. A capped listing says so in every output mode: a notice\n" +
			"(on stderr with --agent or --quiet) and meta.truncated_columns in the JSON envelope.\n" +
			"--column lists one column in full unless --limit or --page narrows it.\n\n" +
			"With --all-projects the listing comes from the account-wide card aggregates, grouped by\n" +
			"project. --status completed, --unassigned, --no-due-date, --not-now, and --overdue each\n" +
			"select a different aggregate and are account-wide only.\n\n" +
			"--assignee (repeatable) and --due (with, without, overdue) filter those aggregates and are\n" +
			"account-wide only too — the project-scoped card listing has no equivalent for either.\n" +
			"--assignee matches a card assigned to any of the named people; assignees on nested steps\n" +
			"are not considered. --assignee cannot be combined with --unassigned (nothing can match\n" +
			"both), and --due cannot be combined with --overdue or --no-due-date.",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.project = *project
			opts.cardTable = *cardTable
			opts.pageSet = cmd.Flags().Changed("page")
			return runCardsList(cmd, opts)
		},
	}

	cmd.Flags().StringVarP(&opts.column, "column", "c", "", "Filter by column ID or name")
	cmd.Flags().IntVarP(&opts.limit, "limit", "n", 0, "Maximum number of cards to fetch (0 = all in one column, 100 per column across a card table, 100 account-wide; use --all for every page)")
	cmd.Flags().BoolVar(&opts.all, "all", false, "Fetch all cards (no limit)")
	cmd.Flags().IntVar(&opts.page, "page", 0, "Fetch a single page (use --all for everything)")
	cmd.Flags().StringVar(&opts.sortField, "sort", "", "Sort by field (title, created, updated, position, due)")
	cmd.Flags().BoolVar(&opts.reverse, "reverse", false, "Reverse sort order")
	cmd.Flags().BoolVar(&opts.allProjects, "all-projects", false, "List cards across every project")
	cmd.Flags().StringVar(&opts.status, "status", "", "Account-wide only: completed")
	cmd.Flags().BoolVar(&opts.unassigned, "unassigned", false, "Account-wide only: cards with no assignee")
	cmd.Flags().BoolVar(&opts.noDueDate, "no-due-date", false, "Account-wide only: cards with no due date")
	cmd.Flags().BoolVar(&opts.notNow, "not-now", false, "Account-wide only: cards parked in Not now")
	cmd.Flags().StringArrayVar(&opts.assignees, "assignee", nil, "Account-wide only: filter by assignee (repeatable)")
	cmd.Flags().StringVar(&opts.due, "due", "", "Account-wide only: filter by due date (with, without, overdue)")
	cmd.Flags().BoolVar(&opts.overdue, "overdue", false, "Account-wide only: overdue cards, oldest due date first")

	return cmd
}

func runCardsList(cmd *cobra.Command, opts cardsListOptions) error {
	app := appctx.FromContext(cmd.Context())

	// Scope-independent flag validation. Anything that depends on the scope
	// waits until the scope is known, because the two scopes disagree about
	// which pages and sorts are legal.
	if opts.reverse && opts.sortField == "" {
		return output.ErrUsageHint("--reverse requires --sort", "Pick a field to sort by, e.g. --sort due --reverse.")
	}
	if opts.sortField != "" {
		// Validate against the superset of all allowed fields early, before any
		// API calls. Context-specific restrictions (e.g. no position in aggregate)
		// are enforced at each branch below.
		if err := validateSortField(opts.sortField, []string{"title", "created", "updated", "position", "due"}); err != nil {
			return err
		}
	}

	// Scope- and account-independent filter validation: an explicitly empty
	// --due/--assignee, and an unknown --due token. Before ensureAccount, so a
	// usage error that needs no account does not demand one.
	if err := validateTaskFilterValues(cmd, opts.due, opts.assignees); err != nil {
		return err
	}

	selector, selectorFlag, err := cardsAccountWideSelector(opts)
	if err != nil {
		return err
	}

	// Scope precedence: an explicit project wins, then a configured one, then
	// account-wide. --all-projects overrides a configured project and conflicts
	// with an explicit one.
	explicitProject := opts.project != "" || app.Flags.Project != ""
	if opts.allProjects && explicitProject {
		return output.ErrUsageHint(
			"--all-projects cannot be combined with --project",
			"Drop one: --project scopes the listing to a project, --all-projects spans every project.",
		)
	}
	accountWide := opts.allProjects || !projectKnown(app, opts.project)

	// Resolve account (enables interactive prompt if needed)
	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	if accountWide {
		return runCardsListAccountWide(cmd, app, opts, selector)
	}

	// --assignee and --due are parameters on the account-wide aggregates. The
	// project-scoped card listing has no equivalent for either, so they are
	// refused by name rather than silently ignored.
	if len(opts.assignees) > 0 {
		return output.ErrUsageHint(
			"--assignee filters the account-wide card listing only",
			"Pass --all-projects to filter across every project, or drop --assignee to list this project's cards.",
		)
	}
	if opts.due != "" {
		return output.ErrUsageHint(
			"--due filters the account-wide card listing only",
			"Pass --all-projects to filter across every project, or drop --due to list this project's cards.",
		)
	}

	// The endpoint selectors reach account-wide aggregates that have no
	// project-scoped equivalent, so a project in scope makes them unanswerable
	// rather than a no-op.
	if selectorFlag != "" {
		return output.ErrUsageHint(
			fmt.Sprintf("%s lists cards across every project and cannot be combined with a project", selectorFlag),
			"Pass --all-projects (which overrides a configured project), or drop the flag to list this project's cards.",
		)
	}

	return runCardsListInProject(cmd, app, opts)
}

// cardsAccountWideSelector maps the endpoint-selector flags onto the aggregate
// each one picks. The selectors are mutually exclusive: no endpoint combines
// two. The returned flag spelling is empty when the caller passed none.
func cardsAccountWideSelector(opts cardsListOptions) (selector, flag string, err error) {
	var chosen []string
	if opts.status != "" {
		if !strings.EqualFold(strings.TrimSpace(opts.status), cardsSelectorCompleted) {
			return "", "", output.ErrUsageHint(
				fmt.Sprintf("--status %s is not a card listing", opts.status),
				"Only --status completed is supported; open cards are the default.",
			)
		}
		chosen = append(chosen, "--status completed")
	}
	for _, sel := range []struct {
		on   bool
		flag string
		name string
	}{
		{opts.unassigned, "--unassigned", cardsSelectorUnassigned},
		{opts.noDueDate, "--no-due-date", cardsSelectorNoDueDate},
		{opts.notNow, "--not-now", cardsSelectorNotNow},
		{opts.overdue, "--overdue", cardsSelectorOverdue},
	} {
		if sel.on {
			chosen = append(chosen, sel.flag)
		}
	}

	if len(chosen) > 1 {
		return "", "", output.ErrUsage(fmt.Sprintf(
			"%s and %s are mutually exclusive; each selects a different card listing",
			chosen[0], chosen[1]))
	}
	if len(chosen) == 0 {
		return cardsSelectorOpen, "", nil
	}

	switch chosen[0] {
	case "--status completed":
		return cardsSelectorCompleted, chosen[0], nil
	case "--unassigned":
		return cardsSelectorUnassigned, chosen[0], nil
	case "--no-due-date":
		return cardsSelectorNoDueDate, chosen[0], nil
	case "--not-now":
		return cardsSelectorNotNow, chosen[0], nil
	default:
		return cardsSelectorOverdue, chosen[0], nil
	}
}

// runCardsListAccountWide lists cards across every accessible project. The
// paginated aggregates come back grouped by project; --overdue comes back flat
// and unpaginated.
func runCardsListAccountWide(cmd *cobra.Command, app *appctx.App, opts cardsListOptions, selector string) error {
	if err := rejectAccountWideTodolist(app, "card"); err != nil {
		return err
	}
	// Scope-child flags name a container inside one project, so they cannot
	// narrow a listing that spans every project. --card-table also arrives via
	// the group's persistent flag.
	if opts.column != "" {
		return output.ErrUsageHint(
			"--column names a column in one project's card table and cannot narrow an account-wide listing",
			"Scope the listing with --project <id> to filter by column.",
		)
	}
	if opts.cardTable != "" {
		return output.ErrUsageHint(
			"--card-table names one project's card table and cannot narrow an account-wide listing",
			"Scope the listing with --project <id> to pick a card table.",
		)
	}

	if opts.limit < 0 {
		return output.ErrUsage("--limit cannot be negative")
	}
	if opts.pageSet && opts.page == 0 {
		return output.ErrUsageHint(
			"--page 0 is not a page number",
			"Use --all to follow every page, or --page 1 for the first one.",
		)
	}
	if opts.page < 0 {
		return output.ErrUsage("--page cannot be negative")
	}
	if opts.page > math.MaxInt32 {
		return output.ErrUsage("--page is out of range")
	}
	if opts.all && opts.limit > 0 {
		return output.ErrUsage("--all and --limit are mutually exclusive")
	}
	if opts.page > 0 && (opts.all || opts.limit > 0) {
		return output.ErrUsage("--page cannot be combined with --all or --limit")
	}

	if selector == cardsSelectorOverdue {
		// --page has nothing to address on an unpaginated endpoint, but --all
		// does: the listing is capped by default, and --all is how you reach
		// past the cap. Rejecting both would leave card 101 unreachable.
		if opts.pageSet {
			return output.ErrUsageHint(
				"--page is not supported with --overdue (the overdue listing is not paginated)",
				"Cap the results instead: basecamp cards list --overdue --limit <n>",
			)
		}
		return runCardsListOverdue(cmd, app, opts)
	}

	// The grouped payloads nest cards under their project, and raw grouped
	// output cannot represent a globally sorted list.
	if opts.sortField != "" {
		return output.ErrUsageHint(
			"--sort is not supported for grouped account-wide card listings",
			"These aggregates are grouped by project; --overdue returns a flat, sortable list.",
		)
	}

	// Account-wide "all" is the whole account, not one project's cards, so the
	// default is bounded and --all is how you ask for the rest. Walking pages
	// to the cap beats fetching every page and discarding most of it.
	if err := validateAccountWideTaskFilters(opts.assignees, opts.due, opts.unassigned,
		opts.overdue, opts.noDueDate, "cards"); err != nil {
		return err
	}

	// Server-side: these become assignee_ids[] and due= on the request, so the
	// server narrows the listing before it is paginated. The bounded walk's
	// algorithm is unchanged, but its request count is not guaranteed to match
	// an unfiltered run — the cap counts items, so a narrower result can need an
	// extra page. See the note on accountWideTaskFilters.
	taskFilters, err := accountWideTaskFilters(cmd.Context(), app, opts.assignees, opts.due)
	if err != nil {
		return err
	}

	limit := opts.limit
	if limit == 0 {
		limit = accountWideDefaultLimit
	}

	everything := app.Account().Everything()
	fetch := func(page int32) ([]basecamp.BucketCardsGroup, basecamp.ListMeta, error) {
		var (
			groupsPage *basecamp.BucketCardsGroupsPage
			err        error
		)
		switch selector {
		case cardsSelectorCompleted:
			groupsPage, err = everything.CompletedCards(cmd.Context(), page, taskFilters)
		case cardsSelectorUnassigned:
			groupsPage, err = everything.UnassignedCards(cmd.Context(), page, taskFilters)
		case cardsSelectorNoDueDate:
			groupsPage, err = everything.NoDueDateCards(cmd.Context(), page, taskFilters)
		case cardsSelectorNotNow:
			groupsPage, err = everything.NotNowCards(cmd.Context(), page, taskFilters)
		default:
			groupsPage, err = everything.OpenCards(cmd.Context(), page, taskFilters)
		}
		if err != nil {
			return nil, basecamp.ListMeta{}, convertSDKError(err)
		}
		return groupsPage.Groups, groupsPage.Meta, nil
	}

	var (
		groups    []basecamp.BucketCardsGroup
		capped    bool
		truncated bool
	)
	if opts.all || opts.page > 0 {
		sdkPage, err := accountWidePage(opts.page, opts.all)
		if err != nil {
			return err
		}
		pageGroups, meta, err := fetch(sdkPage)
		if err != nil {
			return err
		}
		groups, truncated = pageGroups, meta.Truncated
	} else {
		// --limit counts cards, not project groups: truncating groups would
		// drop whole projects. The walk stops at a page boundary, so trim to
		// the exact cap afterwards.
		collected, more, _, err := accountWideCollect(fetch, countAccountWideCards, limit)
		if err != nil {
			return err
		}
		groups, capped = truncateAccountWideCards(collected, limit), more
	}

	// Meta.TotalCount counts project groups rather than cards, so the item
	// total is ours to compute.
	count := countAccountWideCards(groups)

	respOpts := []output.ResponseOption{
		output.WithSummary(fmt.Sprintf("%d %s cards across %d projects", count, selector, len(groups))),
		output.WithBreadcrumbs(cardsAccountWideBreadcrumbs()...),
	}
	switch {
	case capped:
		respOpts = append(respOpts, output.WithNotice(fmt.Sprintf(
			"Showing the first %d cards; more may exist (use --all for every page, or --limit to raise the cap)", count)))
	case truncated:
		respOpts = append(respOpts, output.WithNotice("More pages are available; results were truncated"))
	}

	// --json and --agent keep the grouping the SDK returned; every other
	// consumer gets flat rows. Nested groups have no id and no title of their
	// own, so a renderer handed them produces unreadable cells, and --ids and
	// --count read right past the cards to count projects.
	respOpts = append(respOpts, output.WithDisplayData(flattenAccountWideCards(groups)))

	return app.OK(groups, respOpts...)
}

// runCardsListOverdue lists overdue cards across every project. The endpoint
// returns a flat, oldest-due-first array, so it renders like the
// project-scoped path and accepts the sort flags.
func runCardsListOverdue(cmd *cobra.Command, app *appctx.App, opts cardsListOptions) error {
	if opts.sortField == "position" {
		return output.ErrUsage("--sort position requires --column (position is per-column)")
	}

	if err := validateAccountWideTaskFilters(opts.assignees, opts.due, opts.unassigned,
		opts.overdue, opts.noDueDate, "cards"); err != nil {
		return err
	}

	taskFilters, err := accountWideTaskFilters(cmd.Context(), app, opts.assignees, opts.due)
	if err != nil {
		return err
	}

	cards, err := app.Account().Everything().OverdueCards(cmd.Context(), taskFilters)
	if err != nil {
		return convertSDKError(err)
	}

	// Sort before truncating: truncating first would sort only the survivors.
	if opts.sortField != "" {
		sortCards(cards, opts.sortField, opts.reverse)
	}

	// The endpoint is unpaginated, so the complete array is already in hand and
	// --all costs nothing beyond skipping the cap. --limit trims locally.
	total := len(cards)
	limit := opts.limit
	if limit == 0 {
		limit = accountWideDefaultLimit
	}
	if !opts.all && total > limit {
		cards = cards[:limit]
	}

	respOpts := []output.ResponseOption{
		output.WithSummary(fmt.Sprintf("%d overdue cards across all projects", len(cards))),
		output.WithBreadcrumbs(cardsAccountWideBreadcrumbs()...),
		output.WithDisplayData(flattenOverdueCards(cards)),
	}
	if len(cards) < total {
		respOpts = append(respOpts, output.WithNotice(fmt.Sprintf(
			"Showing the first %d of %d overdue cards (use --all for the complete list, or --limit to raise the cap)",
			len(cards), total)))
	}

	return app.OK(cards, respOpts...)
}

func runCardsListInProject(cmd *cobra.Command, app *appctx.App, opts cardsListOptions) error {
	// Validate flag combinations
	if opts.all && opts.limit > 0 {
		return output.ErrUsage("--all and --limit are mutually exclusive")
	}
	if opts.page > 0 && (opts.all || opts.limit > 0) {
		return output.ErrUsage("--page cannot be combined with --all or --limit")
	}
	if opts.page > 1 {
		return output.ErrUsage("only --page 1 is supported; use --all to fetch everything")
	}
	if opts.page < 0 {
		return output.ErrUsage("--page cannot be negative")
	}
	if opts.limit < 0 {
		return output.ErrUsage("--limit cannot be negative")
	}

	// --page and --limit only make sense when listing a single column: across
	// columns they would apply per column, which is confusing. --all does make
	// sense there: it lifts the per-column cap on the aggregate listing.
	if opts.column == "" && (opts.page > 0 || opts.limit > 0) {
		return output.ErrUsageHint(
			"Pagination flags require --column",
			"When listing all columns, pagination applies per-column. Use --column <id> to paginate a single column, or --all for every card in every column.",
		)
	}

	// Resolve project from CLI flags and config, with interactive fallback
	projectID := opts.project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}

	// If no project specified, try interactive resolution
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return err
		}
		projectID = app.Config.ProjectID
	}

	// Column name (non-numeric) requires --card-table for resolution
	// Numeric column IDs can be used directly without discovery
	if opts.column != "" && !isNumericID(opts.column) && opts.cardTable == "" {
		return output.ErrUsage("--card-table is required when using --column with a name")
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	// Build pagination options
	listOpts := &basecamp.CardListOptions{}
	if opts.all {
		listOpts.Limit = -1 // SDK treats -1 as "fetch all"
	} else if opts.limit > 0 {
		listOpts.Limit = opts.limit
	}
	if opts.page > 0 {
		listOpts.Page = opts.page
	}

	// Optimization: If column is a numeric ID, skip card table discovery
	// and fetch cards directly from the column endpoint
	if opts.column != "" && isNumericID(opts.column) {
		columnID, err := strconv.ParseInt(opts.column, 10, 64)
		if err != nil {
			return output.ErrUsage("Invalid column ID")
		}

		cardsResult, err := app.Account().Cards().List(cmd.Context(), columnID, listOpts)
		if err != nil {
			return convertSDKError(err)
		}

		if opts.sortField != "" {
			sortCards(cardsResult.Cards, opts.sortField, opts.reverse)
		}

		return app.OK(cardsResult.Cards,
			output.WithSummary(fmt.Sprintf("%d cards", len(cardsResult.Cards))),
			output.WithBreadcrumbs(cardsListBreadcrumbs(resolvedProjectID)...),
		)
	}

	// Get card table ID from project dock
	cardTableID, err := getCardTableID(cmd, app, resolvedProjectID, opts.cardTable)
	if err != nil {
		return err
	}

	cardTableIDInt, err := strconv.ParseInt(cardTableID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid card table ID")
	}

	// Get card table with embedded columns (lists)
	cardTableData, err := app.Account().CardTables().Get(cmd.Context(), cardTableIDInt)
	if err != nil {
		return convertSDKError(err)
	}

	// Get cards from all columns or specific column
	var allCards []basecamp.Card
	var respOpts []output.ResponseOption
	if opts.column != "" {
		// Find column by ID or name
		columnID := resolveColumn(cardTableData.Lists, opts.column)
		if columnID == 0 {
			return output.ErrUsageHint(
				fmt.Sprintf("Column '%s' not found", opts.column),
				"Use column ID or exact name",
			)
		}
		cardsResult, err := app.Account().Cards().List(cmd.Context(), columnID, listOpts)
		if err != nil {
			return convertSDKError(err)
		}
		allCards = cardsResult.Cards

		if opts.sortField != "" {
			sortCards(allCards, opts.sortField, opts.reverse)
		}
	} else {
		// No position in aggregate — it's only meaningful within a single column
		if opts.sortField == "position" {
			return output.ErrUsage("--sort position requires --column (position is per-column)")
		}

		// Get cards from all columns, up to cardsPerColumnLimit from each. A
		// done column holds every card ever finished, and walking all of it
		// cost one request per 50 cards on every listing. --all lifts the cap,
		// and so does --count, which has no way to report a truncated total.
		perColumn := &basecamp.CardListOptions{Limit: cardsPerColumnLimit}
		if opts.all || app.Flags.Count {
			perColumn = nil
		}
		var truncated []string
		var truncatedColumns []map[string]any
		for _, col := range cardTableData.Lists {
			cardsResult, err := app.Account().Cards().List(cmd.Context(), col.ID, perColumn)
			if err != nil {
				continue // Skip columns with errors
			}
			allCards = append(allCards, cardsResult.Cards...)
			if cardsResult.Meta.Truncated {
				truncated = append(truncated, truncatedColumnLabel(col, cardsResult.Meta.TotalCount))
				truncatedColumns = append(truncatedColumns, truncatedColumnMeta(col, len(cardsResult.Cards), cardsResult.Meta.TotalCount))
			}
		}

		if opts.sortField != "" {
			sortCards(allCards, opts.sortField, opts.reverse)
		}

		// The cap drops cards the caller didn't ask to drop, so it is a
		// diagnostic, not an informational notice: --agent and --quiet print
		// data alone and would otherwise hand back a short list in silence.
		if len(truncated) > 0 {
			respOpts = append(respOpts,
				output.WithDiagnostic(fmt.Sprintf(
					"Showing the first %d cards of %s; use --column <id> --all for every card in one column, or --all for every column",
					cardsPerColumnLimit, strings.Join(truncated, ", "))),
				output.WithMeta("truncated_columns", truncatedColumns),
			)
		}
	}

	respOpts = append(respOpts,
		output.WithSummary(fmt.Sprintf("%d cards", len(allCards))),
		output.WithBreadcrumbs(append(cardsListBreadcrumbs(resolvedProjectID),
			output.Breadcrumb{
				Action:      "columns",
				Cmd:         fmt.Sprintf("basecamp cards columns --in %s", resolvedProjectID),
				Description: "List columns with IDs",
			},
		)...),
	)
	return app.OK(allCards, respOpts...)
}

// cardsPerColumnLimit caps each column of a project-wide `cards list`.
const cardsPerColumnLimit = 100

// truncatedColumnLabel names a column the aggregate listing cut short, with its
// total when the API reported one.
func truncatedColumnLabel(col basecamp.CardColumn, total int) string {
	if total > 0 {
		return fmt.Sprintf("%s (%d total)", col.Title, total)
	}
	return col.Title
}

// truncatedColumnMeta describes a column the aggregate listing cut short for
// meta.truncated_columns; total is omitted when the API didn't report one.
func truncatedColumnMeta(col basecamp.CardColumn, shown, total int) map[string]any {
	m := map[string]any{"id": col.ID, "title": col.Title, "shown": shown}
	if total > 0 {
		m["total"] = total
	}
	return m
}

// countAccountWideCards totals the cards inside the project groups.
func countAccountWideCards(groups []basecamp.BucketCardsGroup) int {
	total := 0
	for _, group := range groups {
		total += len(group.Cards)
	}
	return total
}

// truncateAccountWideCards keeps the first limit cards, trimming groups from
// the tail rather than dropping cards out of the middle of a project.
func truncateAccountWideCards(groups []basecamp.BucketCardsGroup, limit int) []basecamp.BucketCardsGroup {
	kept := make([]basecamp.BucketCardsGroup, 0, len(groups))
	remaining := limit
	for _, group := range groups {
		if remaining <= 0 {
			break
		}
		if len(group.Cards) > remaining {
			group.Cards = group.Cards[:remaining]
		}
		remaining -= len(group.Cards)
		kept = append(kept, group)
	}
	return kept
}

// flattenAccountWideCards turns the project groups into one row per card, so
// styled output renders a flat table instead of nested cells.
func flattenAccountWideCards(groups []basecamp.BucketCardsGroup) []map[string]any {
	rows := make([]map[string]any, 0, countAccountWideCards(groups))
	for _, group := range groups {
		for _, card := range group.Cards {
			rows = append(rows, map[string]any{
				"id":      card.ID,
				"title":   card.Title,
				"project": group.Bucket.Name,
				"status":  card.Status,
				"due":     card.DueOn,
			})
		}
	}
	return rows
}

// flattenOverdueCards builds display rows for the flat overdue aggregate. The
// cards arrive with a nested bucket, which both generic renderers skip by name,
// so without this an account-wide overdue listing gives no way to tell which
// project a card belongs to.
func flattenOverdueCards(cards []basecamp.Card) []map[string]any {
	rows := make([]map[string]any, 0, len(cards))
	for _, card := range cards {
		row := map[string]any{
			"id":     card.ID,
			"title":  card.Title,
			"status": card.Status,
			"due":    card.DueOn,
		}
		if card.Bucket != nil {
			row["project"] = card.Bucket.Name
		}
		rows = append(rows, row)
	}
	return rows
}

func cardsAccountWideBreadcrumbs() []output.Breadcrumb {
	return []output.Breadcrumb{
		{Action: "show", Cmd: "basecamp cards show <id>", Description: "Show card details"},
		{Action: "scope", Cmd: "basecamp cards list --in <project_id>", Description: "List one project's cards"},
	}
}

func cardsListBreadcrumbs(resolvedProjectID string) []output.Breadcrumb {
	return []output.Breadcrumb{
		{Action: "create", Cmd: fmt.Sprintf("basecamp cards create <title> --in %s", resolvedProjectID), Description: "Create card"},
		{Action: "show", Cmd: "basecamp cards show <id>", Description: "Show card details"},
		{Action: "archived", Cmd: fmt.Sprintf("basecamp recordings cards --status archived --in %s", resolvedProjectID), Description: "Browse archived cards"},
	}
}

func newCardsShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show card details",
		Long: `Display detailed information about a card.

You can pass either a card ID or a Basecamp URL:
  basecamp cards show 789
  basecamp cards show https://3.basecamp.com/123/buckets/456/card_tables/cards/789`,
		Args: cobra.ExactArgs(1),
	}

	dlDir := addDownloadAttachmentsFlag(cmd)
	cf := addCommentFlags(cmd, false)

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		app := appctx.FromContext(cmd.Context())

		if err := ensureAccount(cmd, app); err != nil {
			return err
		}

		// Extract ID from URL if provided
		cardIDStr, err := extractID(args[0])
		if err != nil {
			return err
		}

		cardID, err := strconv.ParseInt(cardIDStr, 10, 64)
		if err != nil {
			return output.ErrUsage("Invalid card ID")
		}

		card, err := app.Account().Cards().Get(cmd.Context(), cardID)
		if err != nil {
			return convertSDKError(err)
		}
		// A moved card's old id redirects to the card at its new id, and the
		// old id 404s for everything after this read.
		if card.ID != 0 && card.ID != cardID {
			cardID = card.ID
			cardIDStr = strconv.FormatInt(cardID, 10)
		}
		if card.Steps, err = everySubtask(cmd.Context(), app, cardID, card.Steps, card.SubtasksCount); err != nil {
			return err
		}

		enrichment := fetchCommentsForRecording(cmd.Context(), app, cardIDStr, cf)

		opts := []output.ResponseOption{
			output.WithSummary(fmt.Sprintf("Card #%s: %s", cardIDStr, card.Title)),
			output.WithBreadcrumbs(
				output.Breadcrumb{
					Action:      "done",
					Cmd:         fmt.Sprintf("basecamp cards done %s", cardIDStr),
					Description: "Move card to Done",
				},
				output.Breadcrumb{
					Action:      "comment",
					Cmd:         fmt.Sprintf("basecamp comments create %s <text>", cardIDStr),
					Description: "Add comment",
				},
			),
		}

		data := any(card)
		attachmentNotice := ""
		contentAtts := downloadableAttachments(richtext.ParseAttachments(card.Content))
		descAtts := downloadableAttachments(richtext.ParseAttachments(card.Description))
		total := len(contentAtts) + len(descAtts)
		if total > 0 {
			allAtts := append(contentAtts, descAtts...)
			dl := runDownloadAttachments(cmd, app, allAtts, dlDir)
			var contentDL, descDL []attachmentResult
			if dl != nil {
				contentDL = dl.Results[:len(contentAtts)]
				descDL = dl.Results[len(contentAtts):]
			}
			if len(contentAtts) > 0 {
				data = withAttachmentMeta(card, "content", contentAtts, contentDL)
			}
			if len(descAtts) > 0 {
				data = withAttachmentMeta(data, "description", descAtts, descDL)
			}
			attachmentNotice = fmt.Sprintf("%d attachment(s) — download: basecamp attachments download %s",
				total, cardIDStr)
			if dl != nil && dl.Notice != "" {
				attachmentNotice += "; " + dl.Notice
			}
			opts = append(opts,
				output.WithBreadcrumbs(attachmentBreadcrumb(cardIDStr, total)),
			)
		}

		data, extraOpts := enrichment.apply(data, attachmentNotice)
		opts = append(opts, extraOpts...)

		return app.OK(data, opts...)
	}

	return cmd
}

func resolveAssigneeID(ctx context.Context, app *appctx.App, input string) (int64, error) {
	return resolvePersonRoleID(ctx, app, input, "Assignee")
}

// resolvePersonRoleID resolves a single person name or ID. The role label
// ("Assignee", "Completion subscriber", …) names the person's role in error
// messages so callers surface the flag the user actually passed.
func resolvePersonRoleID(ctx context.Context, app *appctx.App, input, role string) (int64, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return 0, output.ErrUsage(fmt.Sprintf("%s cannot be empty", role))
	}

	if id, err := strconv.ParseInt(input, 10, 64); err == nil {
		if id <= 0 {
			return 0, output.ErrUsage(fmt.Sprintf("%s ID must be a positive number", role))
		}
		return id, nil
	}
	resolvedID, _, err := app.Names.ResolvePerson(ctx, input)
	if err != nil {
		return 0, fmt.Errorf("failed to resolve %s '%s': %w", strings.ToLower(role), input, err)
	}
	id, err := strconv.ParseInt(resolvedID, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid resolved ID '%s': %w", resolvedID, err)
	}
	if id <= 0 {
		return 0, fmt.Errorf("resolved %s ID for '%s' is not valid: %d", strings.ToLower(role), input, id)
	}
	return id, nil
}

func newCardsCreateCmd(project, cardTable *string) *cobra.Command {
	var column string
	var assignee string
	var due string
	var attachFiles []string

	cmd := &cobra.Command{
		Use:   "create <title> [body]",
		Short: "Create a new card",
		Long: `Create a new card in a project's card table.

Use - as the body argument to read the body from stdin:
  printf 'Card body' | basecamp cards create "My card" - --in myproject`,
		Example: `  basecamp cards create "My card" --in myproject
  basecamp cards create "Launch review" --due "next friday" --in myproject
  basecamp cards create --in myproject -- "--title with dashes"`,
		// Bounded so a stray third token is a usage error rather than being
		// silently dropped after "-" has already drained stdin.
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked with no title
			if len(args) == 0 {
				return missingArg(cmd, "<title>")
			}

			title := args[0]
			if strings.TrimSpace(title) == "" {
				return cmd.Help()
			}
			// A named column needs --card-table to resolve against, and that is
			// knowable from the flags alone. Attachment paths and the due date
			// are valid or not regardless of the body. All precede the read, so
			// a doomed invocation never costs the caller a drained pipe.
			if column != "" && !isNumericID(column) && *cardTable == "" {
				return output.ErrUsage("--card-table is required when using --column with a name")
			}
			if err := requireNumericID(*cardTable, "card table ID"); err != nil {
				return err
			}
			if err := validateAttachPaths(attachFiles); err != nil {
				return err
			}
			// Every non-empty value is parsed, so a whitespace-only --due is
			// rejected rather than creating a card without the date asked for.
			var parsedDue string
			if due != "" {
				var err error
				if parsedDue, err = parseDueDate(due); err != nil {
					return err
				}
			}

			var content string
			if len(args) > 1 {
				var err error
				content, err = resolveContentValue(cmd, args[1], 1, "[body]")
				if err != nil {
					return err
				}
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Resolve project, with interactive fallback
			projectID := *project
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

			// If column is a numeric ID, use it directly without card table discovery
			var columnID int64
			var cardTableIDVal string
			if column != "" && isNumericID(column) {
				columnID, err = strconv.ParseInt(column, 10, 64)
				if err != nil {
					return output.ErrUsage("Invalid column ID")
				}
				cardTableIDVal = "" // Not needed for numeric column ID
			} else {
				// Need to discover card table and resolve column
				cardTableIDVal, err = getCardTableID(cmd, app, resolvedProjectID, *cardTable)
				if err != nil {
					return err
				}

				cardTableIDInt, err := strconv.ParseInt(cardTableIDVal, 10, 64)
				if err != nil {
					return output.ErrUsage("Invalid card table ID")
				}

				// Get card table with embedded columns (lists)
				cardTableData, err := app.Account().CardTables().Get(cmd.Context(), cardTableIDInt)
				if err != nil {
					return convertSDKError(err)
				}

				// Find target column
				if column != "" {
					columnID = resolveColumn(cardTableData.Lists, column)
					if columnID == 0 {
						return output.ErrUsageHint(
							fmt.Sprintf("Column '%s' not found", column),
							"Use column ID or exact name",
						)
					}
				} else {
					// Use first column
					if len(cardTableData.Lists) == 0 {
						return output.ErrNotFound("columns", resolvedProjectID)
					}
					columnID = cardTableData.Lists[0].ID
				}
			}

			// Pre-resolve assignee before side-effectful work (fail early on bad input)
			var assigneeID int64
			if cmd.Flags().Changed("assignee") || cmd.Flags().Changed("to") {
				assigneeID, err = resolveAssigneeID(cmd.Context(), app, assignee)
				if err != nil {
					return err
				}
			}

			// Convert content through rich text pipeline
			var mentionNotice string
			if content != "" {
				content = richTextToHTML(cmd, content)
				content, err = resolveLocalImages(cmd, app, content)
				if err != nil {
					return err
				}
				mentionResult, mentionErr := resolveMentions(cmd.Context(), app.Names, mentionScope(app, resolvedProjectID), content)
				if mentionErr != nil {
					return mentionErr
				}
				content = mentionResult.HTML
				mentionNotice = unresolvedMentionWarning(mentionResult.Unresolved)
			}

			// Upload explicit --attach files and embed
			if len(attachFiles) > 0 {
				refs, attachErr := uploadAttachments(cmd, app, attachFiles)
				if attachErr != nil {
					return attachErr
				}
				content = richtext.EmbedAttachments(content, refs)
			}

			// Build request
			req := &basecamp.CreateCardRequest{
				Title:   title,
				Content: content,
				DueOn:   parsedDue,
			}

			card, err := app.Account().Cards().Create(cmd.Context(), columnID, req)
			if err != nil {
				return convertSDKError(err)
			}

			if assigneeID != 0 {
				createdCardID := card.ID
				card, err = app.Account().Cards().Update(cmd.Context(), createdCardID, &basecamp.UpdateCardRequest{
					AssigneeIDs: []int64{assigneeID},
				})
				if err != nil {
					sdkErr := convertSDKError(err)
					var e *output.Error
					if errors.As(sdkErr, &e) {
						e.Message = fmt.Sprintf("card %d created but assignment failed: %s", createdCardID, e.Message)
						return e
					}
					return fmt.Errorf("card %d created but assignment failed: %w", createdCardID, sdkErr)
				}
			}

			// Build breadcrumbs - only include --card-table when known
			breadcrumbs := []output.Breadcrumb{
				{
					Action:      "view",
					Cmd:         fmt.Sprintf("basecamp cards show %d", card.ID),
					Description: "View card",
				},
			}
			if cardTableIDVal != "" {
				breadcrumbs = append(breadcrumbs, output.Breadcrumb{
					Action:      "move",
					Cmd:         fmt.Sprintf("basecamp cards move %d --to <column> --card-table %s --in %s", card.ID, cardTableIDVal, resolvedProjectID),
					Description: "Move card",
				})
			} else {
				// When using numeric column ID, move command can also use numeric column ID
				breadcrumbs = append(breadcrumbs, output.Breadcrumb{
					Action:      "move",
					Cmd:         fmt.Sprintf("basecamp cards move %d --to <column-id> --in %s", card.ID, resolvedProjectID),
					Description: "Move card",
				})
			}
			breadcrumbs = append(breadcrumbs, output.Breadcrumb{
				Action:      "list",
				Cmd:         fmt.Sprintf("basecamp cards --in %s", resolvedProjectID),
				Description: "List cards",
			})

			respOpts := []output.ResponseOption{
				output.WithSummary(fmt.Sprintf("Created card #%d", card.ID)),
				output.WithBreadcrumbs(breadcrumbs...),
			}
			if mentionNotice != "" {
				respOpts = append(respOpts, output.WithDiagnostic(mentionNotice))
			}
			return app.OK(card, respOpts...)
		},
	}

	cmd.Flags().StringVarP(&column, "column", "c", "", "Column ID or name (defaults to first column)")
	cmd.Flags().StringVar(&assignee, "assignee", "", "Assignee ID or name")
	cmd.Flags().StringVar(&assignee, "to", "", "Assignee (alias for --assignee)")
	cmd.Flags().StringVarP(&due, "due", "d", "", "Due date (natural language or YYYY-MM-DD)")
	cmd.Flags().StringArrayVar(&attachFiles, "attach", nil, "Attach file (repeatable)")

	allowDash(cmd, "arg:1")

	completer := completion.NewCompleter(nil)
	_ = cmd.RegisterFlagCompletionFunc("assignee", completer.PeopleNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("to", completer.PeopleNameCompletion())

	addRichTextFormatFlag(cmd)
	return cmd
}

func newCardsUpdateCmd() *cobra.Command {
	var title string
	var content string
	var due string
	var assignee string
	var attachFiles []string

	cmd := &cobra.Command{
		Use:   "update <id|url>",
		Short: "Update a card",
		Long: `Update an existing card.

You can pass either a card ID or a Basecamp URL:
  basecamp cards update 789 --title "new title"
  basecamp cards update 789 --body "new body"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(title) == "" && strings.TrimSpace(content) == "" && due == "" && !cmd.Flags().Changed("assignee") && len(attachFiles) == 0 {
				return noChanges(cmd)
			}

			// Extract ID from URL if provided
			cardIDStr, urlProjectID, err := extractWithProject(args[0])
			if err != nil {
				return err
			}

			cardID, err := strconv.ParseInt(cardIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid card ID")
			}

			// Attachment paths and the due date are valid or not regardless of
			// the body, so check both before the pipe is drained.
			if err := validateAttachPaths(attachFiles); err != nil {
				return err
			}
			// Every non-empty value is parsed, not just non-blank ones: the
			// no-change guard above tests due == "", so a whitespace-only
			// --due passes it, and dateparse.Parse trims that to an empty
			// date. Parsing it here answers "Invalid due date" instead of
			// sending an update with nothing in it.
			var parsedDue string
			if due != "" {
				if parsedDue, err = parseDueDate(due); err != nil {
					return err
				}
			}

			// Syntactic checks first, then "-", then account and network: a
			// malformed ID is answered without waiting on the producer, and a
			// blank pipe cannot mask it.
			content, err := resolveContentValue(cmd, content, -1, "--body")
			if err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			req := &basecamp.UpdateCardRequest{}
			if title != "" {
				req.Title = &title
			}

			var mentionNotice string
			var html string
			if content != "" {
				html = richTextToHTML(cmd, content)
				html, err = resolveLocalImages(cmd, app, html)
				if err != nil {
					return err
				}
				mentionResult, mentionErr := resolveMentions(cmd.Context(), app.Names, mentionScope(app, urlProjectID, projectFlagValue(cmd), app.Flags.Project), html)
				if mentionErr != nil {
					return mentionErr
				}
				html = mentionResult.HTML
				mentionNotice = unresolvedMentionWarning(mentionResult.Unresolved)
			}

			// Upload explicit --attach files and embed
			if len(attachFiles) > 0 {
				refs, attachErr := uploadAttachments(cmd, app, attachFiles)
				if attachErr != nil {
					return attachErr
				}
				html = richtext.EmbedAttachments(html, refs)
			}

			if html != "" {
				req.Content = &html
			}
			if parsedDue != "" {
				req.DueOn = &parsedDue
			}
			if cmd.Flags().Changed("assignee") {
				assigneeID, err := resolveAssigneeID(cmd.Context(), app, assignee)
				if err != nil {
					return err
				}
				req.AssigneeIDs = []int64{assigneeID}
			}

			card, err := app.Account().Cards().Update(cmd.Context(), cardID, req)
			if err != nil {
				return convertSDKError(err)
			}

			respOpts := []output.ResponseOption{
				output.WithSummary(fmt.Sprintf("Updated card #%s", cardIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp cards show %s", cardIDStr),
						Description: "View card",
					},
				),
			}
			if mentionNotice != "" {
				respOpts = append(respOpts, output.WithDiagnostic(mentionNotice))
			}
			return app.OK(card, respOpts...)
		},
	}

	cmd.Flags().StringVarP(&title, "title", "t", "", "New title")
	cmd.Flags().StringVarP(&content, "body", "b", "", "New body content; use - to read from stdin")
	cmd.Flags().StringVarP(&due, "due", "d", "", "Due date (natural language or YYYY-MM-DD)")
	cmd.Flags().StringVar(&assignee, "assignee", "", "Assignee ID or name")
	cmd.Flags().StringArrayVar(&attachFiles, "attach", nil, "Attach file (repeatable)")

	// Register tab completion for assignee flag
	completer := completion.NewCompleter(nil)
	_ = cmd.RegisterFlagCompletionFunc("assignee", completer.PeopleNameCompletion())

	allowDash(cmd, "flag:body")

	addRichTextFormatFlag(cmd)
	return cmd
}

func newCardsMoveCmd(project, cardTable *string) *cobra.Command {
	var targetColumn string
	var position int
	var onHold bool
	var toWormhole string
	var toProject, toCardTable string

	cmd := &cobra.Command{
		Use:   "move <id|url>",
		Short: "Move a card to another column, card table or project",
		Long: `Move a card to another column in its project, or into a card table in
another project.

--to accepts any column in the card's project, including a column on another
card table in the same project; the card moves there directly, with no wormhole
needed. Pass a column ID, or a column name with --card-table naming the card
table that column is on.

You can pass either a card ID or a Basecamp URL:
  basecamp cards move 789 --to "Done" --in my-project
  basecamp cards move https://3.basecamp.com/123/buckets/456/card_tables/cards/789 --to "Done"
  basecamp cards move 789 --to "Done" --position 1 --in my-project
  basecamp cards move 789 --on-hold --in my-project
  basecamp cards move 789 --to 456 --on-hold --in my-project
  basecamp cards move 789 --to "Backlog" --card-table 1069479999 --in my-project

--to-wormhole sends a card to another project's card table by teleporting it
through a wormhole already on the card's card table. Pass a wormhole ID, or the
URL of the destination column it targets:
  basecamp cards move 789 --to-wormhole 1069480287 --in my-project
  basecamp cards move 789 --to-wormhole https://3.basecamp.com/123/buckets/456/card_tables/columns/999
The teleport mints a new card id. The command waits for the card to land and
shows it; if it is still teleporting when the wait runs out, basecamp cards show
<old id> returns the card at its new id once it lands. A wormhole is a standing
shortcut ("cards wormholes"), not a step of each move; a move within the project
doesn't need one.

--to-project and --to-card-table move a card into any card table, as the web's
Move… does, with no wormhole. The card lands in that table's Triage, or in Done
if it is completed, under a new id; move it on with --to afterward:
  basecamp cards move 789 --to-project "Marketing"
  basecamp cards move 789 --to-project "Marketing" --to-card-table "Roadmap"
  basecamp cards move 789 --to-card-table https://3.basecamp.com/123/buckets/456/card_tables/999
--to-project alone needs a project with one card table. The command waits for
Basecamp to finish the move and shows the card where it landed.`,
		Args:    cobra.ExactArgs(1),
		Aliases: []string{"mv"},
		Annotations: map[string]string{
			"agent_notes": "--to accepts any column in the card's project, including a column on another card table there; " +
				"the move is direct and needs no wormhole. " +
				"When --on-hold is used without --to, the card moves to the on-hold section of its current column. " +
				"When --on-hold is used with --to, the card moves to the on-hold section of the target column. " +
				"--position cannot be combined with --on-hold. " +
				"--to-wormhole sends the card to another project's card table through a wormhole already on its card table; it is mutually exclusive with --to/--on-hold/--position. " +
				"The teleport mints a new card id; the output is the card where it landed (data.id new, data.source_id old). Write to the new id, not the old. " +
				"Do not create a wormhole just to move a card, and do not delete one afterward. " +
				"--to-project/--to-card-table move the card into another card table (usually another project) with no wormhole, landing in Triage (Done if completed) under a new id; " +
				"--to-project alone needs a project with one card table, otherwise add --to-card-table. They are mutually exclusive with --to/--on-hold/--position/--to-wormhole, and --card-table there is refused because it names the card's own table. " +
				"An in_progress error names its filing: do not move it again; basecamp filings show <filing id> gives its new id once it lands.",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			positionSet := cmd.Flags().Changed("position") || cmd.Flags().Changed("pos")
			wormholeSet := cmd.Flags().Changed("to-wormhole")
			filingSet := cmd.Flags().Changed("to-project") || cmd.Flags().Changed("to-card-table")

			if filingSet {
				if targetColumn != "" || onHold || positionSet || wormholeSet {
					return output.ErrUsage("--to-project and --to-card-table cannot be combined with --to, --on-hold, --position, or --to-wormhole")
				}
				if *cardTable != "" {
					return output.ErrUsageHint(
						"--card-table names the card's own card table, not where it goes",
						"Use --to-card-table for the destination card table",
					)
				}
				if err := validateFilingFlags(toProject, toCardTable); err != nil {
					return err
				}
			} else if wormholeSet {
				if targetColumn != "" || onHold || positionSet {
					return output.ErrUsage("--to-wormhole cannot be combined with --to, --on-hold, or --position")
				}
				if toWormhole == "" {
					return output.ErrUsage("--to-wormhole requires a wormhole ID or destination-column URL")
				}
			} else {
				if targetColumn == "" && !onHold {
					return missingArg(cmd, "--to")
				}
				if positionSet && position <= 0 {
					return output.ErrUsage("--position must be a positive integer (1-indexed)")
				}
				if positionSet && onHold {
					return output.ErrUsage("--position cannot be used with --on-hold")
				}
			}

			app := appctx.FromContext(cmd.Context())
			scope := newFollowUpScope(app)

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if filingSet {
				card, err := parseFilingRef(app, args[0], "card", "cards", "…/card_tables/cards/<id>")
				if err != nil {
					return err
				}
				return moveCardToCardTable(cmd, app, scope, card.id, *project, card.project, toProject, toCardTable)
			}

			cardIDStr, urlProjectID, err := extractWithProject(args[0])
			if err != nil {
				return err
			}

			cardID, err := strconv.ParseInt(cardIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid card ID")
			}

			// --to-wormhole: teleport across projects. Self-contained — it derives
			// the source project and card table from the fetched card, so it runs
			// before (and instead of) the generic project resolution below.
			if wormholeSet {
				return moveCardThroughWormhole(cmd, app, cardID, cardIDStr, *project, urlProjectID, *cardTable, toWormhole)
			}

			isNumericColumn := targetColumn != "" && isNumericID(targetColumn)
			if targetColumn != "" && !isNumericColumn && *cardTable == "" {
				return output.ErrUsage("--card-table is required when --to is a column name")
			}

			projectID := *project
			if projectID == "" && urlProjectID != "" {
				projectID = urlProjectID
			}
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

			// --on-hold: move card to on-hold section of current or target column
			if onHold {
				return moveCardOnHold(cmd, app, cardID, cardIDStr, resolvedProjectID, targetColumn, *cardTable)
			}

			var columnID int64
			var cardTableIDVal string
			if isNumericColumn {
				columnID, err = strconv.ParseInt(targetColumn, 10, 64)
				if err != nil {
					return output.ErrUsage("Invalid column ID")
				}
			} else {
				cardTableIDVal, err = getCardTableID(cmd, app, resolvedProjectID, *cardTable)
				if err != nil {
					return err
				}

				cardTableIDInt, err := strconv.ParseInt(cardTableIDVal, 10, 64)
				if err != nil {
					return output.ErrUsage("Invalid card table ID")
				}

				cardTableData, err := app.Account().CardTables().Get(cmd.Context(), cardTableIDInt)
				if err != nil {
					return convertSDKError(err)
				}

				columnID = resolveColumn(cardTableData.Lists, targetColumn)
				if columnID == 0 {
					return output.ErrUsageHint(
						fmt.Sprintf("Column '%s' not found", targetColumn),
						"Use column ID or exact name",
					)
				}
			}

			if positionSet && position > 0 && cardTableIDVal == "" {
				cardTableIDVal, err = getCardTableID(cmd, app, resolvedProjectID, *cardTable)
				if err != nil {
					return err
				}
			}

			if positionSet && position > 0 {
				cardTableIDInt, parseErr := strconv.ParseInt(cardTableIDVal, 10, 64)
				if parseErr != nil {
					return output.ErrUsage("Invalid card table ID")
				}
				err = app.Account().CardColumns().Move(cmd.Context(), cardTableIDInt, &basecamp.MoveColumnRequest{
					SourceID: cardID,
					TargetID: columnID,
					Position: position,
				})
			} else {
				err = app.Account().Cards().Move(cmd.Context(), cardID, columnID, nil)
			}
			if isNumericColumn && httpStatus(err) == http.StatusNotFound {
				// Basecamp looks the column up in the card's project only, so a
				// column from another project is indistinguishable from a typo.
				return output.ErrNotFoundHint("card or column", fmt.Sprintf("card %s, column %s", cardIDStr, targetColumn), fmt.Sprintf(
					"--to takes a column in the card's own project. To send the card to another project, use: basecamp cards move %s --to-project <project> (or --to-wormhole)",
					cardIDStr))
			}
			if err != nil {
				return convertSDKError(err)
			}

			breadcrumbs := []output.Breadcrumb{
				{
					Action:      "view",
					Cmd:         fmt.Sprintf("basecamp cards show %s --in %s", cardIDStr, resolvedProjectID),
					Description: "View card",
				},
			}
			if cardTableIDVal != "" {
				breadcrumbs = append(breadcrumbs, output.Breadcrumb{
					Action:      "list",
					Cmd:         fmt.Sprintf("basecamp cards --in %s --card-table %s --column \"%s\"", resolvedProjectID, cardTableIDVal, targetColumn),
					Description: "List cards in column",
				})
			}

			result := map[string]any{
				"id":     cardIDStr,
				"status": "moved",
				"column": targetColumn,
			}
			summary := fmt.Sprintf("Moved card #%s to '%s'", cardIDStr, targetColumn)
			if positionSet && position > 0 {
				result["position"] = position
				summary = fmt.Sprintf("Moved card #%s to '%s' at position %d", cardIDStr, targetColumn, position)
			}

			return app.OK(result,
				output.WithSummary(summary),
				output.WithBreadcrumbs(breadcrumbs...),
			)
		},
	}

	cmd.Flags().StringVarP(&targetColumn, "to", "t", "", "Target column ID or name: any column in the card's project, on this or another card table (optional with --on-hold)")
	cmd.Flags().IntVar(&position, "position", 0, "Position in column (1-indexed)")
	cmd.Flags().IntVar(&position, "pos", 0, "Position in column (alias for --position)")
	cmd.Flags().BoolVar(&onHold, "on-hold", false, "Move card to the on-hold section of its current (or target) column")
	cmd.Flags().StringVar(&toWormhole, "to-wormhole", "", "Send the card to another project's card table through an existing wormhole (wormhole ID or destination-column URL)")
	addFilingDestinationFlags(cmd, &toProject, &toCardTable, "Move")

	return guardFilingFlags(cmd, "card-table")
}

// moveCardThroughWormhole teleports a card across projects through a wormhole to
// a column on another card table. wormholeRef is either an all-digits value —
// always interpreted as a wormhole ID — or a Basecamp column URL identifying the
// wormhole's destination column. (A bare numeric destination-column ID is not
// accepted: it is indistinguishable from a wormhole ID, so a column must be given
// as a URL.)
//
// The teleport is destructive and asynchronous: Cards().Move returns once it is
// accepted, then the server copies the card into the destination column under a
// new id and deletes the original. Once that is done, reading the original id
// redirects to the new card, which is how the command learns where it landed.
// The move endpoint resolves the column_id against the whole project bucket, so
// a sibling board's wormhole would happily teleport the card elsewhere. Because that is irreversible, this fails closed: it always fetches
// the card, derives the source project and table from the card itself, rejects
// conflicting caller context, and requires the referenced wormhole to be present
// and linked on that exact table before issuing the move. The output is the new
// card; if it has not landed when the wait runs out, the output reports the
// source under source_id (not id) and omits a same-id "view card" breadcrumb.
func moveCardThroughWormhole(cmd *cobra.Command, app *appctx.App, cardID int64, cardIDStr, flagProject, urlProjectID, cardTableFlag, wormholeRef string) error {
	// Validate the reference before any network call.
	byID := isNumericID(wormholeRef)
	var wantWormholeID, wantDestColumnID int64
	if byID {
		id, err := strconv.ParseInt(wormholeRef, 10, 64)
		if err != nil || id <= 0 {
			return output.ErrUsage("--to-wormhole must be a positive wormhole ID or a destination-column URL")
		}
		wantWormholeID = id
	} else {
		id, err := parseColumnID(wormholeRef)
		if err != nil {
			return err
		}
		wantDestColumnID = id
	}

	card, err := app.Account().Cards().Get(cmd.Context(), cardID)
	if err != nil {
		return convertSDKError(err)
	}
	if card.Bucket == nil || card.Bucket.ID == 0 {
		return output.ErrNotFound("card", cardIDStr)
	}
	sourceProjectID := strconv.FormatInt(card.Bucket.ID, 10)

	// Reject an explicit project (flag or card URL) that contradicts the card's
	// own project — the card's bucket is authoritative for a cross-project move.
	if err := rejectProjectConflict(cmd, app, "card", flagProject, urlProjectID, card.Bucket.ID); err != nil {
		return err
	}

	cardTableID, cardTableData, err := resolveCardTableForCard(cmd, app, sourceProjectID, cardTableFlag, card)
	if err != nil {
		return err
	}
	// resolveCardTableForCard trusts an explicit --card-table; confirm the card
	// actually lives on the resolved table before searching its wormholes.
	if err := requireCardInTable(card, cardTableData); err != nil {
		return err
	}

	var wormhole *basecamp.Wormhole
	if byID {
		if wormhole = findWormholeByID(cardTableData.Wormholes, wantWormholeID); wormhole == nil {
			return output.ErrUsageHint(
				fmt.Sprintf("Wormhole %d is not on this card's card table", wantWormholeID),
				wormholeHint(cardTableData.Wormholes),
			)
		}
	} else {
		if wormhole = findWormholeByDestinationColumn(cardTableData.Wormholes, wantDestColumnID); wormhole == nil {
			return output.ErrUsageHint(
				fmt.Sprintf("No wormhole on this card's card table teleports to column %d", wantDestColumnID),
				wormholeHint(cardTableData.Wormholes),
			)
		}
	}
	if !wormhole.Linked {
		return output.ErrUsageHint(
			fmt.Sprintf("Wormhole %d is unlinked — its destination column no longer exists", wormhole.ID),
			fmt.Sprintf("Point it at a live column with: basecamp cards wormholes update %d --to-column <id|url> --in %s", wormhole.ID, sourceProjectID),
		)
	}

	if err := app.Account().Cards().Move(cmd.Context(), cardID, wormhole.ID, nil); err != nil {
		return convertSDKError(err)
	}

	var landed *basecamp.Card
	if moved, _ := pollUntil(cmd.Context(), func(ctx context.Context) (bool, error) {
		card, err := app.Account().Cards().Get(ctx, cardID)
		if httpStatus(err) == http.StatusNotFound {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		landed = card
		return card.ID != cardID, nil
	}); moved {
		return filedOK(app, "card", "Teleported", cardID, filedCard(landed, cardID))
	}

	result := map[string]any{
		"source_id":   cardIDStr,
		"status":      "teleporting",
		"wormhole":    wormhole.ID,
		"destination": wormhole.Title,
	}
	summary := fmt.Sprintf("Teleporting card #%s through wormhole %d (%s) — still in progress; once it lands, basecamp cards show %s returns the card at its new id", cardIDStr, wormhole.ID, wormhole.Title, cardIDStr)

	return app.OK(result,
		output.WithSummary(summary),
		output.WithBreadcrumbs(wormholeMoveBreadcrumbs(sourceProjectID, cardTableID)...),
	)
}

// wormholeListBreadcrumb builds the "list wormholes" follow-up. A wormhole's
// parent is the card table it lives on, so it pins --card-table when known,
// keeping the follow-up unambiguous on multi-table projects.
func wormholeListBreadcrumb(bucketID int64, wormhole *basecamp.Wormhole) output.Breadcrumb {
	cmd := fmt.Sprintf("basecamp cards wormholes list --in %d", bucketID)
	if wormhole != nil && wormhole.Parent != nil && wormhole.Parent.ID != 0 {
		cmd += fmt.Sprintf(" --card-table %d", wormhole.Parent.ID)
	}
	return output.Breadcrumb{Action: "list", Cmd: cmd, Description: "List wormholes"}
}

// wormholeMoveBreadcrumbs points at the source card table's wormhole list. It
// pins --card-table (the resolved table id) so the follow-up is unambiguous on
// multi-table projects, and deliberately offers no same-id "view card" action —
// that id is about to 404.
func wormholeMoveBreadcrumbs(sourceProjectID, cardTableID string) []output.Breadcrumb {
	return []output.Breadcrumb{{
		Action:      "wormholes",
		Cmd:         fmt.Sprintf("basecamp cards wormholes list --in %s --card-table %s", sourceProjectID, cardTableID),
		Description: "List wormholes on this card table",
	}}
}

// requireCardInTable confirms the card's parent column belongs to the resolved
// card table, so an explicit --card-table can't point the wormhole search at a
// table the card isn't on. The teleport is irreversible, so it fails closed: if
// the card's parent column or the table's columns are unavailable, the placement
// can't be verified and the move is refused rather than trusted.
func requireCardInTable(card *basecamp.Card, table *basecamp.CardTable) error {
	if card.Parent == nil || card.Parent.ID == 0 || table == nil {
		return output.ErrUsageHint(
			"Could not verify which card table the card is on before teleporting",
			"Re-run without --card-table so the card's own table is resolved, and check the card ID",
		)
	}
	if cardTableContainsColumn(table.Lists, card.Parent.ID) {
		return nil
	}
	return output.ErrUsageHint(
		"The card is not on the specified card table",
		"Omit --card-table (the card's own table is used) or pass the table that contains the card",
	)
}

// findWormholeByID returns the wormhole with the given id, or nil.
func findWormholeByID(wormholes []basecamp.Wormhole, id int64) *basecamp.Wormhole {
	for i := range wormholes {
		if wormholes[i].ID == id {
			return &wormholes[i]
		}
	}
	return nil
}

// findWormholeByDestinationColumn returns the wormhole whose destination_url
// resolves to columnID, or nil. Unlinked wormholes (nil destination_url) never
// match.
func findWormholeByDestinationColumn(wormholes []basecamp.Wormhole, columnID int64) *basecamp.Wormhole {
	for i := range wormholes {
		wormhole := &wormholes[i]
		if wormhole.DestinationURL == nil {
			continue
		}
		id, err := extractID(*wormhole.DestinationURL)
		if err != nil {
			continue
		}
		if parsed, err := strconv.ParseInt(id, 10, 64); err == nil && parsed == columnID {
			return wormhole
		}
	}
	return nil
}

// wormholeHint builds a usage hint listing a card table's wormholes by ID and
// title, so a failed match points at the reachable options.
func wormholeHint(wormholes []basecamp.Wormhole) string {
	if len(wormholes) == 0 {
		return "This card table has no wormholes — create one with: basecamp cards wormholes create --to-column <id|url>"
	}
	parts := make([]string, 0, len(wormholes))
	for i := range wormholes {
		wormhole := &wormholes[i]
		parts = append(parts, fmt.Sprintf("#%d (%s)", wormhole.ID, wormhole.Title))
	}
	return "Target one of the wormholes on this card table: " + strings.Join(parts, ", ")
}

// parseColumnID resolves a destination-column reference — a positive numeric ID
// or a Basecamp column URL (.../card_tables/columns/{id}) — to a positive column
// ID. Any other Basecamp URL (a card, a project, a cards collection) is rejected
// rather than silently accepting its trailing numeric id.
func parseColumnID(ref string) (int64, error) {
	invalid := func() (int64, error) {
		return 0, output.ErrUsageHint(
			fmt.Sprintf("Invalid destination column %q", ref),
			"Pass a positive column ID or a Basecamp column URL (.../card_tables/columns/{id})",
		)
	}
	idStr := ref
	if urlarg.IsURL(ref) {
		parsed := urlarg.Parse(ref)
		if parsed == nil || parsed.Type != "columns" || parsed.IsCollection {
			return invalid()
		}
		idStr = parsed.RecordingID
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		return invalid()
	}
	return id, nil
}

func newCardsCopyCmd(project *string) *cobra.Command {
	var toProject, toCardTable string
	var includeComments bool

	cmd := &cobra.Command{
		Use:   "copy <id|url>",
		Short: "Copy a card to a card table in this or another project",
		Long: `Copy a card into a card table, as the web's Copy… does. The original stays
where it is. The copy lands in the card table's Triage, or in Done if the card
is completed.

With no destination, the copy goes onto the card's own card table:
  basecamp cards copy 789
  basecamp cards copy 789 --to-project "Marketing"
  basecamp cards copy 789 --to-project "Marketing" --to-card-table "Roadmap"
  basecamp cards copy 789 --to-card-table 1069479999 --include-comments

Basecamp makes the copy in the background; the command waits for it and shows
the new card.`,
		Args: cobra.ExactArgs(1),
		Annotations: map[string]string{
			"agent_notes": "Without --to-project/--to-card-table the copy goes onto the card's own card table. " +
				"--to-project alone uses that project's only card table and fails as ambiguous when it has several; add --to-card-table. " +
				"The copy lands in Triage, or Done if the card is completed; move it on with cards move --to. " +
				"Output is the new card; its id is data.id and the original's is data.source_id. " +
				"Never re-run a copy that reported in_progress: each run makes another copy; check an in_progress one with basecamp filings show <filing id>.",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			filingSet := cmd.Flags().Changed("to-project") || cmd.Flags().Changed("to-card-table")
			if filingSet {
				if err := validateFilingFlags(toProject, toCardTable); err != nil {
					return err
				}
			}

			app := appctx.FromContext(cmd.Context())
			scope := newFollowUpScope(app)
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			ref, err := parseFilingRef(app, args[0], "card", "cards", "…/card_tables/cards/<id>")
			if err != nil {
				return err
			}

			var table *basecamp.CardTable
			if filingSet {
				if table, err = resolveFilingTable(cmd, app, toProject, toCardTable); err != nil {
					return err
				}
			}

			card, err := app.Account().Cards().Get(cmd.Context(), ref.id)
			if err != nil {
				return convertSDKError(err)
			}
			if err := requireFilableCard(card, ref.id); err != nil {
				return err
			}
			if err := rejectProjectConflict(cmd, app, "card", *project, ref.project, card.Bucket.ID); err != nil {
				return err
			}

			if table == nil {
				if _, table, err = resolveCardTableForCard(cmd, app, strconv.FormatInt(card.Bucket.ID, 10), "", card); err != nil {
					return err
				}
			}

			return runFiling(cmd, app, scope, cardFiling(app, basecamp.FilingCopy, card, table, includeComments))
		},
	}

	addFilingDestinationFlags(cmd, &toProject, &toCardTable, "Copy")
	cmd.Flags().BoolVar(&includeComments, "include-comments", false, "Copy the card's comments too")

	return guardFilingFlags(cmd)
}

// addFilingDestinationFlags adds --to-project and --to-card-table, which name
// the card table a card is moved or copied into.
func addFilingDestinationFlags(cmd *cobra.Command, toProject, toCardTable *string, verb string) {
	cmd.Flags().StringVar(toProject, "to-project", "", verb+" the card into this project's card table (ID or name); it lands in Triage, or Done if completed")
	cmd.Flags().StringVar(toCardTable, "to-card-table", "", verb+" the card into this card table (ID, URL, or a name with --to-project)")
}

// validateFilingFlags checks --to-project and --to-card-table before any
// request.
func validateFilingFlags(toProject, toCardTable string) error {
	if toProject == "" && toCardTable == "" {
		return output.ErrUsage("--to-project or --to-card-table needs a value")
	}
	if toProject == "" && !isNumericID(toCardTable) && !urlarg.IsURL(toCardTable) {
		return output.ErrUsageHint(
			fmt.Sprintf("Card table %q is a name, which needs a project to look it up in", toCardTable),
			"Add --to-project <project>, or pass the card table's ID or URL",
		)
	}
	return nil
}

// requireFilableCard refuses what the card read returned when it is not a
// card. A moved message's old id redirects through the card endpoint to the
// message, so the read alone does not prove the id names a card.
func requireFilableCard(card *basecamp.Card, requestedID int64) error {
	if card.Bucket == nil {
		return output.ErrNotFound("card", strconv.FormatInt(requestedID, 10))
	}
	if card.Type != "" && card.Type != "Kanban::Card" {
		return output.ErrUsage(fmt.Sprintf("%d is a %s, not a card", requestedID, card.Type))
	}
	return nil
}

// moveCardToCardTable moves a card into another card table, usually in
// another project, as the web's Move… dialog does. A move within the card's
// own table is a column change, which --to makes directly.
func moveCardToCardTable(cmd *cobra.Command, app *appctx.App, scope followUpScope, cardID int64, flagProject, urlProjectID, toProject, toCardTable string) error {
	table, err := resolveFilingTable(cmd, app, toProject, toCardTable)
	if err != nil {
		return err
	}
	card, err := app.Account().Cards().Get(cmd.Context(), cardID)
	if err != nil {
		return convertSDKError(err)
	}
	if err := requireFilableCard(card, cardID); err != nil {
		return err
	}
	if err := rejectProjectConflict(cmd, app, "card", flagProject, urlProjectID, card.Bucket.ID); err != nil {
		return err
	}
	if card.Parent != nil && cardTableContainsColumn(table.Lists, card.Parent.ID) {
		return output.ErrUsageHint(
			fmt.Sprintf("Card #%d is already on card table %d (%s)", cardID, table.ID, table.Title),
			fmt.Sprintf("Move it between that table's columns with: basecamp cards move %d --to <column> --card-table %d --in %d", cardID, table.ID, cardTableProjectID(table)),
		)
	}
	return runFiling(cmd, app, scope, cardFiling(app, basecamp.FilingMove, card, table, false))
}

// resolveFilingTable finds the card table named by --to-project and
// --to-card-table: a card table ID or URL on its own, a project's only card
// table, or one of a project's card tables picked by ID or title. Only card
// tables on the project's dock count, as in the web's dialogs. A URL is held
// to parseFilingRef's host and account checks, and its project must agree
// with --to-project.
func resolveFilingTable(cmd *cobra.Command, app *appctx.App, toProject, toCardTable string) (*basecamp.CardTable, error) {
	var urlProjectID string
	if urlarg.IsURL(toCardTable) {
		ref, err := parseFilingRef(app, toCardTable, "card table", "card_tables", "…/card_tables/<id>")
		if err != nil {
			return nil, err
		}
		toCardTable, urlProjectID = strconv.FormatInt(ref.id, 10), ref.project
	}

	if toProject == "" {
		tableID, err := strconv.ParseInt(toCardTable, 10, 64)
		if err != nil || tableID <= 0 {
			return nil, output.ErrUsage("--to-card-table must be a positive card table ID")
		}
		table, err := getCardTable(cmd, app, tableID)
		if err != nil {
			return nil, err
		}
		if urlProjectID != "" && !sameNumericID(urlProjectID, strconv.FormatInt(cardTableProjectID(table), 10)) {
			return nil, output.ErrUsage(fmt.Sprintf("The --to-card-table URL names project %s, but card table %d is in project %d", urlProjectID, table.ID, cardTableProjectID(table)))
		}
		return table, nil
	}

	projectID, projectName, err := app.Names.ResolveProject(cmd.Context(), toProject)
	if err != nil {
		return nil, err
	}
	if urlProjectID != "" && !sameNumericID(urlProjectID, projectID) {
		return nil, output.ErrUsageHint(
			fmt.Sprintf("The --to-card-table URL is in project %s, but --to-project points at project %s", urlProjectID, projectID),
			"Drop --to-project, since the card table URL names its own project",
		)
	}
	all, err := listProjectCardTables(cmd, app, projectID)
	if err != nil {
		return nil, err
	}
	var tables []projectCardTable
	for _, ct := range all {
		if ct.Enabled {
			tables = append(tables, ct)
		}
	}

	if toCardTable == "" {
		switch len(tables) {
		case 0:
			if projectName == "" {
				projectName = projectID
				if id, perr := strconv.ParseInt(projectID, 10, 64); perr == nil {
					if name, nerr := app.Names.ProjectName(cmd.Context(), id); nerr == nil && name != "" {
						projectName = name
					}
				}
			}
			return nil, &output.Error{
				Code:    output.CodeNotFound,
				Message: fmt.Sprintf("Project %s has no card table", projectName),
				Hint:    "Pick a project that has one, or add a card table there first",
			}
		case 1:
			return getCardTable(cmd, app, tables[0].ID)
		default:
			return nil, ambiguousCardTablesErrorFor("--to-card-table", tables)
		}
	}
	var named []projectCardTable
	for _, ct := range tables {
		if strconv.FormatInt(ct.ID, 10) == toCardTable {
			return getCardTable(cmd, app, ct.ID)
		}
		if strings.EqualFold(ct.Title, toCardTable) {
			named = append(named, ct)
		}
	}
	switch len(named) {
	case 1:
		return getCardTable(cmd, app, named[0].ID)
	case 0:
	default:
		return nil, ambiguousCardTablesErrorFor("--to-card-table", named)
	}
	return nil, output.ErrUsageHint(
		fmt.Sprintf("Card table '%s' not found in project %s", toCardTable, projectID),
		fmt.Sprintf("Available card tables: %s", formatCardTableIDs(tables)),
	)
}

func getCardTable(cmd *cobra.Command, app *appctx.App, tableID int64) (*basecamp.CardTable, error) {
	table, err := app.Account().CardTables().Get(cmd.Context(), tableID)
	if err != nil {
		return nil, convertSDKError(err)
	}
	return table, nil
}

func cardTableProjectID(table *basecamp.CardTable) int64 {
	if table.Bucket == nil {
		return 0
	}
	return table.Bucket.ID
}

// cardFiling is a Move… or Copy… of a card into a card table, as the web's
// dialogs make it: the card lands in the table's Triage, or in Done if it is
// completed.
func cardFiling(app *appctx.App, kind basecamp.FilingType, card *basecamp.Card, table *basecamp.CardTable, includeComments bool) recordingFiling {
	return recordingFiling{
		kind:            kind,
		sourceID:        card.ID,
		noun:            "card",
		destinationID:   table.ID,
		includeComments: includeComments,
		destination:     fmt.Sprintf("card table %d (%s)", table.ID, table.Title),
		showCmd:         "basecamp cards show",
		lookFor:         copyLookFor("Kanban::Card", card.Status, filingTarget{id: table.ID, appURL: table.AppURL, projectID: cardTableProjectID(table)}),
		match:           copyMatch(card.ID, card.Title, table.Title, table.ID),
		read: func(ctx context.Context, id int64) (filedRecording, error) {
			filed, err := app.Account().Cards().Get(ctx, id)
			if err != nil {
				return filedRecording{}, err
			}
			return filedCard(filed, card.ID), nil
		},
	}
}

// filedCardResult is a card that a move, copy or teleport made, with the id of
// the card it came from.
type filedCardResult struct {
	*basecamp.Card
	SourceID int64 `json:"source_id"`
}

func filedCard(card *basecamp.Card, sourceID int64) filedRecording {
	var projectFlag string
	if card.Bucket != nil {
		projectFlag = fmt.Sprintf(" --in %d", card.Bucket.ID)
	}
	return filedRecording{
		result: filedCardResult{Card: card, SourceID: sourceID},
		id:     card.ID,
		bucket: card.Bucket,
		parent: card.Parent,
		breadcrumbs: []output.Breadcrumb{
			{
				Action:      "view",
				Cmd:         fmt.Sprintf("basecamp cards show %d%s", card.ID, projectFlag),
				Description: "View the card",
			},
			{
				Action:      "move",
				Cmd:         fmt.Sprintf("basecamp cards move %d --to <column>%s", card.ID, projectFlag),
				Description: "Move it to a column",
			},
		},
	}
}

func newCardsDoneCmd(project, cardTable *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "done <id|url>",
		Short: "Move a card to the Done column",
		Long: `Move a card to the Done column in its card table.

You can pass either a card ID or a Basecamp URL:
  basecamp cards done 789 --in my-project
  basecamp cards done https://3.basecamp.com/123/buckets/456/card_tables/cards/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			cardIDStr, urlProjectID, err := extractWithProject(args[0])
			if err != nil {
				return err
			}
			cardID, err := strconv.ParseInt(cardIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid card ID")
			}

			card, err := app.Account().Cards().Get(cmd.Context(), cardID)
			if err != nil {
				return convertSDKError(err)
			}

			alreadyInDone := card.Parent != nil && card.Parent.Type == "Kanban::DoneColumn"
			if alreadyInDone || card.Completed {
				summary := fmt.Sprintf("Card #%s is already in 'Done'", cardIDStr)
				if !alreadyInDone && card.Completed {
					summary = fmt.Sprintf("Card #%s is already completed", cardIDStr)
				}
				return app.OK(card,
					output.WithSummary(summary),
					output.WithBreadcrumbs(output.Breadcrumb{
						Action:      "view",
						Cmd:         fmt.Sprintf("basecamp cards show %s", cardIDStr),
						Description: "View card",
					}),
				)
			}

			projectID := *project
			if projectID == "" && urlProjectID != "" {
				projectID = urlProjectID
			}
			if projectID == "" && card.Bucket != nil && card.Bucket.ID != 0 {
				projectID = fmt.Sprintf("%d", card.Bucket.ID)
			}
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

			cardTableIDVal, cardTableData, err := resolveCardTableForCard(cmd, app, resolvedProjectID, *cardTable, card)
			if err != nil {
				return err
			}

			doneColumn := findDoneColumn(cardTableData.Lists)
			if doneColumn == nil {
				return output.ErrUsageHint(
					fmt.Sprintf("Card table '%s' does not have a Done column", cardTableData.Title),
					fmt.Sprintf("Inspect available columns: basecamp cards columns --card-table %s --in %s", cardTableIDVal, resolvedProjectID),
				)
			}

			if err := app.Account().Cards().Move(cmd.Context(), cardID, doneColumn.ID, nil); err != nil {
				return convertSDKError(err)
			}

			updatedCard, err := app.Account().Cards().Get(cmd.Context(), cardID)
			if err != nil {
				result := map[string]any{
					"id":        cardIDStr,
					"status":    "completed",
					"column":    doneColumn.Title,
					"column_id": doneColumn.ID,
				}
				return app.OK(result,
					output.WithSummary(fmt.Sprintf("Moved card #%s to '%s'", cardIDStr, doneColumn.Title)),
					output.WithBreadcrumbs(cardDoneBreadcrumbs(cardIDStr, resolvedProjectID, cardTableIDVal, doneColumn.Title)...),
				)
			}

			return app.OK(updatedCard,
				output.WithSummary(fmt.Sprintf("Moved card #%s to '%s'", cardIDStr, doneColumn.Title)),
				output.WithBreadcrumbs(cardDoneBreadcrumbs(cardIDStr, resolvedProjectID, cardTableIDVal, doneColumn.Title)...),
			)
		},
	}

	return cmd
}

func cardDoneBreadcrumbs(cardIDStr, projectID, cardTableID, doneColumn string) []output.Breadcrumb {
	breadcrumbs := []output.Breadcrumb{
		{
			Action:      "view",
			Cmd:         fmt.Sprintf("basecamp cards show %s --in %s", cardIDStr, projectID),
			Description: "View card",
		},
	}
	if cardTableID != "" {
		breadcrumbs = append(breadcrumbs, output.Breadcrumb{
			Action:      "list",
			Cmd:         fmt.Sprintf("basecamp cards list --in %s --card-table %s --column %q", projectID, cardTableID, doneColumn),
			Description: "List cards in Done",
		})
	}
	return breadcrumbs
}

func moveCardOnHold(cmd *cobra.Command, app *appctx.App, cardID int64, cardIDStr, projectID, targetColumn, cardTableFlag string) error {
	var column *basecamp.CardColumn

	if targetColumn != "" && isNumericID(targetColumn) {
		columnID, err := strconv.ParseInt(targetColumn, 10, 64)
		if err != nil {
			return output.ErrUsage("Invalid column ID")
		}
		col, err := app.Account().CardColumns().Get(cmd.Context(), columnID)
		if err != nil {
			return convertSDKError(err)
		}
		column = col
	} else if targetColumn == "" {
		card, err := app.Account().Cards().Get(cmd.Context(), cardID)
		if err != nil {
			return convertSDKError(err)
		}
		if card.Parent == nil {
			return output.ErrUsageHint(
				"Card has no parent column",
				fmt.Sprintf("Specify the target column: basecamp cards move %s --to <column-id> --on-hold", cardIDStr),
			)
		}
		col, err := app.Account().CardColumns().Get(cmd.Context(), card.Parent.ID)
		if err != nil {
			return convertSDKError(err)
		}
		column = col
	} else {
		cardTableIDVal, err := getCardTableID(cmd, app, projectID, cardTableFlag)
		if err != nil {
			return err
		}
		cardTableIDInt, err := strconv.ParseInt(cardTableIDVal, 10, 64)
		if err != nil {
			return output.ErrUsage("Invalid card table ID")
		}
		cardTableData, err := app.Account().CardTables().Get(cmd.Context(), cardTableIDInt)
		if err != nil {
			return convertSDKError(err)
		}
		colID := resolveColumn(cardTableData.Lists, targetColumn)
		if colID == 0 {
			return output.ErrUsageHint(
				fmt.Sprintf("Column '%s' not found", targetColumn),
				"Use column ID or exact name",
			)
		}
		for i := range cardTableData.Lists {
			if cardTableData.Lists[i].ID == colID {
				column = &cardTableData.Lists[i]
				break
			}
		}
	}

	if column.OnHold == nil || column.OnHold.ID == 0 {
		return output.ErrUsageHint(
			fmt.Sprintf("Column '%s' does not have an on-hold section", column.Title),
			fmt.Sprintf("Enable on-hold with: basecamp cards column on-hold %d", column.ID),
		)
	}

	err := app.Account().Cards().Move(cmd.Context(), cardID, column.OnHold.ID, nil)
	if err != nil {
		return convertSDKError(err)
	}

	result := map[string]any{
		"id":      cardIDStr,
		"status":  "moved",
		"column":  column.Title,
		"on_hold": true,
	}
	summary := fmt.Sprintf("Moved card #%s to on-hold in '%s'", cardIDStr, column.Title)

	return app.OK(result,
		output.WithSummary(summary),
		output.WithBreadcrumbs(output.Breadcrumb{
			Action:      "view",
			Cmd:         fmt.Sprintf("basecamp cards show %s --in %s", cardIDStr, projectID),
			Description: "View card",
		}),
	)
}

func newCardsColumnsCmd(project, cardTable *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "columns",
		Short: "List columns",
		Long:  "List all columns in a project's card table with their IDs.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Resolve project, with interactive fallback
			projectID := *project
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

			// Get card table ID from project dock
			cardTableID, err := getCardTableID(cmd, app, resolvedProjectID, *cardTable)
			if err != nil {
				return err
			}

			cardTableIDInt, err := strconv.ParseInt(cardTableID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid card table ID")
			}

			// Get card table with embedded columns (lists)
			cardTableData, err := app.Account().CardTables().Get(cmd.Context(), cardTableIDInt)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(cardTableData.Lists,
				output.WithSummary(fmt.Sprintf("%d columns", len(cardTableData.Lists))),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "cards",
						Cmd:         fmt.Sprintf("basecamp cards --in %s --card-table %s --column <id>", resolvedProjectID, cardTableID),
						Description: "List cards in column",
					},
					output.Breadcrumb{
						Action:      "create",
						Cmd:         fmt.Sprintf("basecamp cards create <title> --in %s --card-table %s --column <id>", resolvedProjectID, cardTableID),
						Description: "Create card in column",
					},
				),
			)
		},
	}
	return cmd
}

// newCardsColumnCmd creates the column management subcommand.
func newCardsColumnCmd(project, cardTable *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "column",
		Short: "Manage columns",
		Long:  "Show, create, and manage card table columns.",
	}

	cmd.AddCommand(
		newCardsColumnShowCmd(),
		newCardsColumnCreateCmd(project, cardTable),
		newCardsColumnUpdateCmd(),
		newCardsColumnMoveCmd(project, cardTable),
		newFilingCmd(columnFamily(), basecamp.FilingCopy, cardsColumnCopyHelp, false),
		newCardsColumnWatchCmd(),
		newCardsColumnUnwatchCmd(),
		newCardsColumnOnHoldCmd(project),
		newCardsColumnNoOnHoldCmd(project),
		newCardsColumnColorCmd(project),
	)

	return cmd
}

func newCardsColumnShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show column details",
		Long: `Display detailed information about a column.

You can pass either a column ID or a Basecamp URL:
  basecamp cards column show 789
  basecamp cards column show https://3.basecamp.com/123/buckets/456/card_tables/columns/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// The column is read by its account-wide ID, so no project is needed.
			columnIDStr, _, err := extractWithProject(args[0])
			if err != nil {
				return err
			}
			columnID, err := strconv.ParseInt(columnIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid column ID")
			}

			col, err := app.Account().CardColumns().Get(cmd.Context(), columnID)
			if err != nil {
				return convertSDKError(err)
			}
			// A moved column's old id redirects to the column at its new id,
			// and only the new id takes writes.
			columnIDStr = strconv.FormatInt(col.ID, 10)

			return app.OK(col,
				output.WithSummary(fmt.Sprintf("%s (%d cards)", col.Title, col.CardsCount)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("basecamp cards column update %s --in %s", columnIDStr, recordProject(col.Bucket)),
						Description: "Update column",
					},
					output.Breadcrumb{
						Action:      "columns",
						Cmd:         fmt.Sprintf("basecamp cards columns --in %s", recordProject(col.Bucket)),
						Description: "List all columns",
					},
				),
			)
		},
	}
	return cmd
}

func newCardsColumnCreateCmd(project, cardTable *string) *cobra.Command {
	var description string

	cmd := &cobra.Command{
		Use:   "create <title>",
		Short: "Create a column",
		Long:  "Create a new column in the card table.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked with no title
			if len(args) == 0 {
				return missingArg(cmd, "<title>")
			}

			title := args[0]

			app := appctx.FromContext(cmd.Context())

			if err := requireNumericID(*cardTable, "card table ID"); err != nil {
				return err
			}

			description, err := resolveContentValue(cmd, description, -1, "--description")
			if err != nil {
				return err
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Resolve project, with interactive fallback
			projectID := *project
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

			// Get card table ID
			cardTableID, err := getCardTableID(cmd, app, resolvedProjectID, *cardTable)
			if err != nil {
				return err
			}

			cardTableIDInt, err := strconv.ParseInt(cardTableID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid card table ID")
			}

			req := &basecamp.CreateColumnRequest{
				Title:       title,
				Description: description,
			}

			col, err := app.Account().CardColumns().Create(cmd.Context(), cardTableIDInt, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(col,
				output.WithSummary(fmt.Sprintf("Created column: %s", col.Title)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "column",
						Cmd:         fmt.Sprintf("basecamp cards column show %d --in %s", col.ID, resolvedProjectID),
						Description: "View column",
					},
					output.Breadcrumb{
						Action:      "columns",
						Cmd:         fmt.Sprintf("basecamp cards columns --in %s", resolvedProjectID),
						Description: "List columns",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&description, "description", "d", "", "Column description; use - to read from stdin")

	allowDash(cmd, "flag:description")

	return cmd
}

func newCardsColumnUpdateCmd() *cobra.Command {
	var title string
	var description string

	cmd := &cobra.Command{
		Use:   "update <id|url>",
		Short: "Update a column",
		Long: `Update an existing card table column.

You can pass either a column ID or a Basecamp URL:
  basecamp cards column update 789 --title "new name"
  basecamp cards column update 789 --description "new desc"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if title == "" && description == "" {
				return noChanges(cmd)
			}

			// Extract ID from URL if provided
			columnIDStr, err := extractID(args[0])
			if err != nil {
				return err
			}
			columnID, err := strconv.ParseInt(columnIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid column ID")
			}

			// Syntactic checks first, then "-", then account and network.
			description, err := resolveContentValue(cmd, description, -1, "--description")
			if err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			req := &basecamp.UpdateColumnRequest{
				Title:       title,
				Description: description,
			}

			col, err := app.Account().CardColumns().Update(cmd.Context(), columnID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(col,
				output.WithSummary(fmt.Sprintf("Updated column #%s", columnIDStr)),
			)
		},
	}

	cmd.Flags().StringVarP(&title, "title", "t", "", "New title")
	cmd.Flags().StringVarP(&description, "description", "d", "", "New description; use - to read from stdin")

	allowDash(cmd, "flag:description")

	return cmd
}

func newCardsColumnMoveCmd(project, cardTable *string) *cobra.Command {
	var position int
	var toProject, toCardTable string

	cmd := &cobra.Command{
		Use:   "move <id|url>",
		Short: "Move a column within its card table or to another project's card table",
		Long: `Reposition a column within its card table, or move it with its cards to a
card table in another project.

--position repositions it within its card table. You can pass either a column
ID or a Basecamp URL:
  basecamp cards column move 789 --position 2 --in my-project
  basecamp cards column move https://3.basecamp.com/123/buckets/456/card_tables/columns/789 --position 2

--to-project and --to-card-table move it, cards and all, onto another card
table, as the web's Move… does. It lands under a new ID:
  basecamp cards column move 789 --to-project "Marketing"
  basecamp cards column move 789 --to-project "Marketing" --to-card-table "Roadmap"
  basecamp cards column move 789 --to-card-table https://3.basecamp.com/123/buckets/999/card_tables/555
Only the columns you add move this way; Basecamp refuses Triage, Not now, Done
and On hold, which every card table has.`,
		Args: cobra.ExactArgs(1),
		Annotations: map[string]string{"agent_notes": "--position repositions the column within its card table and keeps its ID. " +
			"--to-project/--to-card-table move the column with its cards onto another card table (usually in another project) under a NEW id: output data.id is the new id and data.source_id the old. " +
			"--to-project alone needs a project with one card table, otherwise add --to-card-table. --position and --card-table (the column's own table) cannot be combined with them; the column's own table is refused. " +
			"Triage, Not now, Done and On hold columns cannot be moved or copied. " +
			"An in_progress error names its filing: do not move it again; basecamp filings show <filing id> gives its new id once it lands."},
		RunE: func(cmd *cobra.Command, args []string) error {
			if familyFilingSet(cmd, columnFamily()) {
				if cmd.Flags().Changed("position") || cmd.Flags().Changed("pos") {
					return output.ErrUsageHint("--position repositions the column within its card table, and cannot be combined with --to-project or --to-card-table",
						"Move it to the other card table first, then reposition it there with --position")
				}
				return runFamilyFiling(cmd, columnFamily(), basecamp.FilingMove, args, toProject, toCardTable, false)
			}
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID and project from URL if provided
			columnIDStr, urlProjectID, err := extractWithProject(args[0])
			if err != nil {
				return err
			}
			columnID, err := strconv.ParseInt(columnIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Column ID must be numeric")
			}

			if position <= 0 {
				return output.ErrUsage("--position required (1-indexed)")
			}

			// Resolve project - use URL > flag > config, with interactive fallback
			projectID := *project
			if projectID == "" && urlProjectID != "" {
				projectID = urlProjectID
			}
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

			// Get card table ID
			cardTableID, err := getCardTableID(cmd, app, resolvedProjectID, *cardTable)
			if err != nil {
				return err
			}

			cardTableIDInt, err := strconv.ParseInt(cardTableID, 10, 64)
			if err != nil {
				return output.ErrUsage("Card table ID must be numeric")
			}

			req := &basecamp.MoveColumnRequest{
				SourceID: columnID,
				TargetID: cardTableIDInt,
				Position: position,
			}

			err = app.Account().CardColumns().Move(cmd.Context(), cardTableIDInt, req)
			if err != nil {
				e := output.AsError(convertSDKError(err))
				if e.Code == output.CodeNotFound {
					notFound := *e
					notFound.Hint = fmt.Sprintf("--position reorders a column within its own card table, which may not be card table %d. To move it to a card table in another project use: basecamp cards column move %d --to-card-table %d", cardTableIDInt, columnID, cardTableIDInt)
					return &notFound
				}
				return e
			}

			return app.OK(map[string]any{
				"moved":    true,
				"id":       columnIDStr,
				"position": position,
			}, output.WithSummary(fmt.Sprintf("Moved column #%s to position %d", columnIDStr, position)))
		},
	}

	cmd.Flags().IntVar(&position, "position", 0, "Target position (1-indexed)")
	cmd.Flags().IntVar(&position, "pos", 0, "Target position (alias for --position)")
	addFamilyFilingFlags(cmd, columnFamily(), "Move", &toProject, &toCardTable)

	return guardFilingFlags(cmd, "card-table")
}

var cardsColumnCopyHelp = filingHelp{
	use:   "copy <id|url>",
	short: "Copy a column with its cards to a card table in this or another project",
	long: `Copy a column, with its cards, onto a card table, as the web's Copy… does.
The original stays where it is.

With no destination, the copy goes onto the column's own card table:
  basecamp cards column copy 789
  basecamp cards column copy 789 --to-project "Marketing"
  basecamp cards column copy 789 --to-project "Marketing" --to-card-table "Roadmap" --include-comments

Only the columns you add copy this way; Basecamp refuses Triage, Not now, Done
and On hold. The command waits for Basecamp to make the copy and shows it.`,
	notes: "Without --to-project/--to-card-table the copy goes onto the column's own card table. " +
		"--to-project alone uses that project's only card table and fails as ambiguous when it has several; add --to-card-table. " +
		"Output is the new column; its id is data.id and the original's is data.source_id. Triage, Not now, Done and On hold columns cannot be copied. " +
		"Never re-run a copy that reported in_progress or a may-have-started error: each run makes another copy; check an in_progress one with basecamp filings show <filing id>.",
}

// columnTypes are the columns a card table has: Kanban::Column is one a
// person added, the rest are the ones every card table has.
var columnTypes = []string{standardColumnType, "Kanban::Triage", "Kanban::NotNowColumn", "Kanban::DoneColumn", "Kanban::OnHold"}

// columnFamily is how a card table's columns move and copy between card
// tables, cards and all.
func columnFamily() filingFamily {
	return filingFamily{
		noun:     "column",
		group:    "basecamp cards column",
		types:    columnTypes,
		urlKinds: []string{"columns"},
		urlShape: "…/card_tables/columns/<id>",
		into: filingContainer{
			dockName: "kanban_board",
			noun:     "card table",
			flag:     "--to-card-table",
			urlKind:  "card_tables",
			urlShape: "…/card_tables/<id>",
			get: func(ctx context.Context, app *appctx.App, id int64) (filingTarget, error) {
				table, err := app.Account().CardTables().Get(ctx, id)
				if err != nil {
					return filingTarget{}, err
				}
				return bucketTarget(table.ID, table.Title, table.AppURL, table.Bucket), nil
			},
		},
		ownFlags: []string{"card-table"},
		get: func(ctx context.Context, app *appctx.App, id int64) (any, filingSource, error) {
			c, err := app.Account().CardColumns().Get(ctx, id)
			if err != nil {
				return nil, filingSource{}, err
			}
			return c, filingSource{id: c.ID, typ: c.Type, status: c.Status, title: c.Title, bucket: c.Bucket, parent: c.Parent}, nil
		},
		check: func(src filingSource) error {
			if src.typ == standardColumnType {
				return nil
			}
			return output.ErrUsageHint(
				fmt.Sprintf("Column #%d (%s) is one every card table has, which Basecamp does not move or copy", src.id, src.title),
				"Only the columns you add move and copy; move its cards instead with: basecamp cards move <card> --to-project <project>",
			)
		},
		sameHint: func(src filingSource, target filingTarget) string {
			return fmt.Sprintf("Reposition it within that card table with: basecamp cards column move %d --position <n> --card-table %d --in %d", src.id, target.id, target.projectID)
		},
		crumbs: func(id, projectID int64) []output.Breadcrumb {
			return []output.Breadcrumb{{
				Action:      "show",
				Cmd:         fmt.Sprintf("basecamp cards column show %d --in %d", id, projectID),
				Description: "View the column",
			}}
		},
	}
}

func newCardsColumnWatchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch <id|url>",
		Short: "Watch a column",
		Long: `Subscribe to updates for a column.

You can pass either a column ID or a Basecamp URL:
  basecamp cards column watch 789 --in my-project
  basecamp cards column watch https://3.basecamp.com/123/buckets/456/card_tables/columns/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			columnIDStr, err := extractID(args[0])
			if err != nil {
				return err
			}
			columnID, err := strconv.ParseInt(columnIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid column ID")
			}

			_, err = app.Account().CardColumns().Watch(cmd.Context(), columnID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{
				"watching": true,
				"id":       columnIDStr,
			}, output.WithSummary(fmt.Sprintf("Now watching column #%s", columnIDStr)))
		},
	}
	return cmd
}

func newCardsColumnUnwatchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unwatch <id|url>",
		Short: "Unwatch a column",
		Long: `Unsubscribe from updates for a column.

You can pass either a column ID or a Basecamp URL:
  basecamp cards column unwatch 789 --in my-project
  basecamp cards column unwatch https://3.basecamp.com/123/buckets/456/card_tables/columns/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			columnIDStr, err := extractID(args[0])
			if err != nil {
				return err
			}
			columnID, err := strconv.ParseInt(columnIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid column ID")
			}

			err = app.Account().CardColumns().Unwatch(cmd.Context(), columnID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{
				"watching": false,
				"id":       columnIDStr,
			}, output.WithSummary(fmt.Sprintf("Stopped watching column #%s", columnIDStr)))
		},
	}
	return cmd
}

func newCardsColumnOnHoldCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "on-hold <id|url>",
		Short: "Enable on-hold section",
		Long: `Enable on-hold section for a column.

You can pass either a column ID or a Basecamp URL:
  basecamp cards column on-hold 789 --in my-project
  basecamp cards column on-hold https://3.basecamp.com/123/buckets/456/card_tables/columns/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID and project from URL if provided
			columnIDStr, urlProjectID, err := extractWithProject(args[0])
			if err != nil {
				return err
			}
			columnID, err := strconv.ParseInt(columnIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid column ID")
			}

			if err := requireStandardColumn(cmd, app, columnID, "Enabling on-hold"); err != nil {
				return err
			}

			bucketID, err := resolveColumnBucketID(cmd, app, *project, urlProjectID)
			if err != nil {
				return err
			}

			col, err := app.Account().CardColumns().EnableOnHold(cmd.Context(), bucketID, columnID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(col,
				output.WithSummary(fmt.Sprintf("Enabled on-hold for column #%s", columnIDStr)),
			)
		},
	}
	return cmd
}

func newCardsColumnNoOnHoldCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "no-on-hold <id|url>",
		Short: "Disable on-hold section",
		Long: `Disable on-hold section for a column.

You can pass either a column ID or a Basecamp URL:
  basecamp cards column no-on-hold 789 --in my-project
  basecamp cards column no-on-hold https://3.basecamp.com/123/buckets/456/card_tables/columns/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID and project from URL if provided
			columnIDStr, urlProjectID, err := extractWithProject(args[0])
			if err != nil {
				return err
			}
			columnID, err := strconv.ParseInt(columnIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid column ID")
			}

			if err := requireStandardColumn(cmd, app, columnID, "Disabling on-hold"); err != nil {
				return err
			}

			bucketID, err := resolveColumnBucketID(cmd, app, *project, urlProjectID)
			if err != nil {
				return err
			}

			col, err := app.Account().CardColumns().DisableOnHold(cmd.Context(), bucketID, columnID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(col,
				output.WithSummary(fmt.Sprintf("Disabled on-hold for column #%s", columnIDStr)),
			)
		},
	}
	return cmd
}

func newCardsColumnColorCmd(project *string) *cobra.Command {
	var color string

	cmd := &cobra.Command{
		Use:   "color <id|url>",
		Short: "Set column color",
		Long: `Set the color for a column.

You can pass either a column ID or a Basecamp URL:
  basecamp cards column color 789 --color blue --in my-project
  basecamp cards column color https://3.basecamp.com/123/buckets/456/card_tables/columns/789 --color blue`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked with no color flag
			if color == "" {
				return missingArg(cmd, "--color")
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID and project from URL if provided
			columnIDStr, urlProjectID, err := extractWithProject(args[0])
			if err != nil {
				return err
			}
			columnID, err := strconv.ParseInt(columnIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid column ID")
			}

			if err := requireStandardColumn(cmd, app, columnID, "Setting a column color"); err != nil {
				return err
			}

			bucketID, err := resolveColumnBucketID(cmd, app, *project, urlProjectID)
			if err != nil {
				return err
			}

			col, err := app.Account().CardColumns().SetColor(cmd.Context(), bucketID, columnID, color)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(col,
				output.WithSummary(fmt.Sprintf("Set column #%s color to %s", columnIDStr, color)),
			)
		},
	}

	cmd.Flags().StringVarP(&color, "color", "c", "", "Column color")

	return cmd
}

// newCardsWormholesCmd creates the wormhole management group. A wormhole is a
// standing shortcut on a card table that teleports any card dropped into it to a
// column on another card table, usually in another project. A one-off move to
// another project uses cards move --to-project, and within a project cards move
// --to reaches any column directly.
func newCardsWormholesCmd(project, cardTable *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wormholes",
		Short: "Manage card-table wormholes",
		Long: `Manage wormholes — standing shortcuts that teleport cards to a column on another card table, usually in another project.

Drop a card into one (with "cards move --to-wormhole") and the server teleports
it to the wormhole's destination column, minting a new card and deleting the
original. A card table holds at most four wormholes. A one-off move to another
project needs no wormhole: "cards move --to-project" sends the card to that
project's card table.

A move within a project needs no wormhole: "cards move --to" accepts any column
in the card's project, including one on another card table. Create a wormhole
when a card table should keep a shortcut to another project, not for each move.`,
	}

	cmd.AddCommand(
		newCardsWormholesListCmd(project, cardTable),
		newCardsWormholesCreateCmd(project, cardTable),
		newCardsWormholesUpdateCmd(project),
		newCardsWormholesDeleteCmd(project),
	)

	return cmd
}

func newCardsWormholesListCmd(project, cardTable *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List wormholes",
		Long:    "List the wormholes on a project's card table with their IDs, destinations, and linked status.",
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			resolvedProjectID, err := resolveCardsProjectID(cmd, app, *project)
			if err != nil {
				return err
			}

			cardTableID, err := getCardTableID(cmd, app, resolvedProjectID, *cardTable)
			if err != nil {
				return err
			}

			cardTableIDInt, err := strconv.ParseInt(cardTableID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid card table ID")
			}

			cardTableData, err := app.Account().CardTables().Get(cmd.Context(), cardTableIDInt)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(cardTableData.Wormholes,
				output.WithSummary(fmt.Sprintf("%d wormholes", len(cardTableData.Wormholes))),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "create",
						Cmd:         fmt.Sprintf("basecamp cards wormholes create --to-column <id|url> --in %s --card-table %s", resolvedProjectID, cardTableID),
						Description: "Create a wormhole",
					},
					output.Breadcrumb{
						Action:      "move",
						Cmd:         "basecamp cards move <card> --to-wormhole <id>",
						Description: "Teleport a card through a wormhole",
					},
				),
			)
		},
	}
	return cmd
}

func newCardsWormholesCreateCmd(project, cardTable *string) *cobra.Command {
	var toColumn string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a wormhole",
		Long: `Create a wormhole on this project's card table: a standing shortcut that teleports cards to a column on another card table, usually in another project.

Not needed to move a card within its project; "cards move --to" reaches any column
there directly.

Pass the destination column as an ID or a Basecamp column URL:
  basecamp cards wormholes create --to-column 1069480051 --in my-project
  basecamp cards wormholes create --to-column https://3.basecamp.com/123/buckets/456/card_tables/columns/789

A card table holds at most four wormholes.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if toColumn == "" {
				return missingArg(cmd, "--to-column")
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			resolvedProjectID, err := resolveCardsProjectID(cmd, app, *project)
			if err != nil {
				return err
			}

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Project ID must be numeric")
			}

			cardTableID, err := getCardTableID(cmd, app, resolvedProjectID, *cardTable)
			if err != nil {
				return err
			}

			cardTableIDInt, err := strconv.ParseInt(cardTableID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid card table ID")
			}

			destColumnID, err := parseColumnID(toColumn)
			if err != nil {
				return err
			}

			wormhole, err := app.Account().Wormholes().Create(cmd.Context(), bucketID, cardTableIDInt, destColumnID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(wormhole,
				output.WithSummary(fmt.Sprintf("Created wormhole: %s", wormhole.Title)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("basecamp cards wormholes list --in %s --card-table %s", resolvedProjectID, cardTableID),
						Description: "List wormholes",
					},
					output.Breadcrumb{
						Action:      "move",
						Cmd:         fmt.Sprintf("basecamp cards move <card> --to-wormhole %d", wormhole.ID),
						Description: "Teleport a card through this wormhole",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&toColumn, "to-column", "", "Destination column ID or URL (required)")

	return cmd
}

func newCardsWormholesUpdateCmd(project *string) *cobra.Command {
	var toColumn string

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a wormhole's destination",
		Long: `Point an existing wormhole at a new destination column.

  basecamp cards wormholes update 1069480287 --to-column 1069480051 --in my-project`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if toColumn == "" {
				return missingArg(cmd, "--to-column")
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			wormholeIDStr, urlProjectID, err := extractWithProject(args[0])
			if err != nil {
				return err
			}
			wormholeID, err := strconv.ParseInt(wormholeIDStr, 10, 64)
			if err != nil || wormholeID <= 0 {
				return output.ErrUsage("Invalid wormhole ID")
			}

			bucketID, err := resolveColumnBucketID(cmd, app, *project, urlProjectID)
			if err != nil {
				return err
			}

			destColumnID, err := parseColumnID(toColumn)
			if err != nil {
				return err
			}

			wormhole, err := app.Account().Wormholes().Update(cmd.Context(), bucketID, wormholeID, destColumnID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(wormhole,
				output.WithSummary(fmt.Sprintf("Updated wormhole: %s", wormhole.Title)),
				output.WithBreadcrumbs(wormholeListBreadcrumb(bucketID, wormhole)),
			)
		},
	}

	cmd.Flags().StringVar(&toColumn, "to-column", "", "New destination column ID or URL (required)")

	return cmd
}

func newCardsWormholesDeleteCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "delete <id>",
		Short:   "Delete a wormhole",
		Long:    "Remove a wormhole from a card table. Cards already teleported are unaffected.",
		Args:    cobra.ExactArgs(1),
		Aliases: []string{"rm"},
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			wormholeIDStr, urlProjectID, err := extractWithProject(args[0])
			if err != nil {
				return err
			}
			wormholeID, err := strconv.ParseInt(wormholeIDStr, 10, 64)
			if err != nil || wormholeID <= 0 {
				return output.ErrUsage("Invalid wormhole ID")
			}

			bucketID, err := resolveColumnBucketID(cmd, app, *project, urlProjectID)
			if err != nil {
				return err
			}

			if err := app.Account().Wormholes().Delete(cmd.Context(), bucketID, wormholeID); err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"id": wormholeIDStr, "status": "deleted"},
				output.WithSummary(fmt.Sprintf("Deleted wormhole #%s", wormholeIDStr)),
			)
		},
	}
	return cmd
}

// newCardsStepsCmd creates the steps listing subcommand.
func newCardsStepsCmd(project *string) *cobra.Command {
	var cardID string

	cmd := &cobra.Command{
		Use:   "steps <card-id|url>",
		Short: "List steps on a card",
		Long:  "Display all steps (checklist items) on a card.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Accept card ID as positional arg or flag
			if len(args) > 0 {
				cardID = args[0]
			}
			if cardID == "" {
				return missingArg(cmd, "<card-id|url>")
			}

			cardIDInt, err := strconv.ParseInt(cardID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid card ID")
			}

			// Resolve project, with interactive fallback
			projectID := *project
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

			// Get card with steps
			card, err := app.Account().Cards().Get(cmd.Context(), cardIDInt)
			if err != nil {
				return convertSDKError(err)
			}
			// A moved card's old id redirects to the card at its new id, in
			// another project, and the old id 404s for everything after this.
			if card.ID != 0 && card.ID != cardIDInt {
				cardIDInt = card.ID
				cardID = strconv.FormatInt(card.ID, 10)
				if card.Bucket != nil {
					resolvedProjectID = strconv.FormatInt(card.Bucket.ID, 10)
				}
			}
			steps, err := everySubtask(cmd.Context(), app, cardIDInt, card.Steps, card.SubtasksCount)
			if err != nil {
				return err
			}

			return app.OK(steps,
				output.WithSummary(fmt.Sprintf("%d steps on card #%s", len(steps), cardID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "create",
						Cmd:         fmt.Sprintf("basecamp cards step create --title <title> --card %s --in %s", cardID, resolvedProjectID),
						Description: "Add step",
					},
					output.Breadcrumb{
						Action:      "card",
						Cmd:         fmt.Sprintf("basecamp cards show %s --in %s", cardID, resolvedProjectID),
						Description: "View card",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&cardID, "card", "c", "", "Card ID")

	return cmd
}

// newCardsStepCmd creates the step management subcommand.
func newCardsStepCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "step",
		Short: "Manage steps",
		Long:  "Create, complete, and manage card steps.",
	}

	cmd.AddCommand(
		newCardsStepCreateCmd(project),
		newCardsStepUpdateCmd(),
		newCardsStepCompleteCmd(),
		newCardsStepUncompleteCmd(),
		newCardsStepMoveCmd(),
		newCardsStepDeleteCmd(),
	)

	return cmd
}

func newCardsStepCreateCmd(project *string) *cobra.Command {
	var cardID string
	var dueOn string
	var assignees string

	cmd := &cobra.Command{
		Use:   "create <title>",
		Short: "Create a step",
		Long:  "Add a new step (checklist item) to a card.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked with no title
			if len(args) == 0 {
				return missingArg(cmd, "<title>")
			}

			title := args[0]

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if cardID == "" {
				return output.ErrUsage("--card is required")
			}

			cardIDInt, err := strconv.ParseInt(cardID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid card ID")
			}

			// Resolve project, with interactive fallback
			projectID := *project
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

			req := &basecamp.CreateStepRequest{
				Title: title,
			}
			if dueOn != "" {
				req.DueOn = dateparse.Parse(dueOn)
			}
			if assignees != "" {
				assigneeIDs, err := resolveAssigneeIDs(cmd.Context(), app, assignees)
				if err != nil {
					return err
				}
				req.AssigneeIDs = assigneeIDs
			}

			step, err := app.Account().CardSteps().Create(cmd.Context(), cardIDInt, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(step,
				output.WithSummary(fmt.Sprintf("Created step: %s", step.Title)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "complete",
						Cmd:         fmt.Sprintf("basecamp cards step complete %d --in %s", step.ID, resolvedProjectID),
						Description: "Complete step",
					},
					output.Breadcrumb{
						Action:      "steps",
						Cmd:         fmt.Sprintf("basecamp cards steps %s --in %s", cardID, resolvedProjectID),
						Description: "List steps",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&cardID, "card", "c", "", "Card ID (required)")
	cmd.Flags().StringVarP(&dueOn, "due", "d", "", "Due date (natural language or YYYY-MM-DD)")
	cmd.Flags().StringVar(&assignees, "assignees", "", "Assignees (IDs or names, comma-separated)")

	return cmd
}

func newCardsStepUpdateCmd() *cobra.Command {
	var dueOn string
	var assignees string

	cmd := &cobra.Command{
		Use:   "update <step_id|url> [title]",
		Short: "Update a step",
		Long: `Update an existing step on a card.

You can pass either a step ID or a Basecamp URL:
  basecamp cards step update 789 "new title"
  basecamp cards step update https://3.basecamp.com/123/buckets/456/card_tables/cards/steps/789 "new title"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked with no args
			if len(args) == 0 {
				return missingArg(cmd, "<step_id|url>")
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			stepIDStr, err := extractID(args[0])
			if err != nil {
				return err
			}
			stepID, err := strconv.ParseInt(stepIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid step ID")
			}

			var title string
			if len(args) > 1 {
				title = args[1]
			}

			if len(args) < 2 && dueOn == "" && assignees == "" {
				return noChanges(cmd)
			}

			req := &basecamp.UpdateStepRequest{}
			if title != "" {
				req.Title = title
			}
			if dueOn != "" {
				req.DueOn = basecamp.Ptr(dateparse.Parse(dueOn))
			}
			if assignees != "" {
				assigneeIDs, err := resolveAssigneeIDs(cmd.Context(), app, assignees)
				if err != nil {
					return err
				}
				req.AssigneeIDs = assigneeIDs
			}

			step, err := app.Account().CardSteps().Update(cmd.Context(), stepID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(step,
				output.WithSummary(fmt.Sprintf("Updated step #%s", stepIDStr)),
			)
		},
	}

	cmd.Flags().StringVarP(&dueOn, "due", "d", "", "Due date (natural language or YYYY-MM-DD)")
	cmd.Flags().StringVar(&assignees, "assignees", "", "Assignees (IDs or names, comma-separated)")

	return cmd
}

func newCardsStepCompleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "complete <step_id|url>",
		Short: "Complete a step",
		Long: `Mark a step as completed.

You can pass either a step ID or a Basecamp URL:
  basecamp cards step complete 789 --in my-project
  basecamp cards step complete https://3.basecamp.com/123/buckets/456/card_tables/cards/steps/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			stepIDStr, err := extractID(args[0])
			if err != nil {
				return err
			}
			stepID, err := strconv.ParseInt(stepIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid step ID")
			}

			step, err := app.Account().CardSteps().Complete(cmd.Context(), stepID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(step,
				output.WithSummary(fmt.Sprintf("Completed step #%s", stepIDStr)),
			)
		},
	}
	return cmd
}

func newCardsStepUncompleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uncomplete <step_id|url>",
		Short: "Uncomplete a step",
		Long: `Mark a step as not completed.

You can pass either a step ID or a Basecamp URL:
  basecamp cards step uncomplete 789 --in my-project
  basecamp cards step uncomplete https://3.basecamp.com/123/buckets/456/card_tables/cards/steps/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			stepIDStr, err := extractID(args[0])
			if err != nil {
				return err
			}
			stepID, err := strconv.ParseInt(stepIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid step ID")
			}

			step, err := app.Account().CardSteps().Uncomplete(cmd.Context(), stepID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(step,
				output.WithSummary(fmt.Sprintf("Uncompleted step #%s", stepIDStr)),
			)
		},
	}
	return cmd
}

func newCardsStepMoveCmd() *cobra.Command {
	var cardID string
	var position int

	cmd := &cobra.Command{
		Use:   "move <step_id|url>",
		Short: "Move a step",
		Long: `Reposition a step within a card (1-based: 1 is the top).

You can pass either a step ID or a Basecamp URL:
  basecamp cards step move 789 --card 456 --position 1 --in my-project
  basecamp cards step move https://3.basecamp.com/123/buckets/456/card_tables/cards/steps/789 --card 456 --position 1`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked with no card flag
			if cardID == "" {
				return missingArg(cmd, "--card")
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			stepIDStr, err := extractID(args[0])
			if err != nil {
				return err
			}
			stepID, err := strconv.ParseInt(stepIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Step ID must be numeric")
			}
			// bc3 has always counted step positions from 1; its docs once
			// said "zero indexed", and the SDK now refuses 0.
			if !cmd.Flags().Changed("position") && !cmd.Flags().Changed("pos") {
				return output.ErrUsage("--position is required (1-based)")
			}
			if position < 1 {
				return output.ErrUsage("--position must be 1 or more (1 = top)")
			}

			cardIDInt, err := strconv.ParseInt(cardID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid card ID")
			}

			err = app.Account().CardSteps().Reposition(cmd.Context(), cardIDInt, stepID, position)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{
				"moved":    true,
				"id":       stepIDStr,
				"position": position,
			}, output.WithSummary(fmt.Sprintf("Moved step #%s to position %d", stepIDStr, position)))
		},
	}

	cmd.Flags().StringVarP(&cardID, "card", "c", "", "Card ID (required)")
	cmd.Flags().IntVar(&position, "position", 0, "Target position (1-based)")
	cmd.Flags().IntVar(&position, "pos", 0, "Target position (alias for --position)")

	return cmd
}

func newCardsStepDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <step_id|url>",
		Short: "Delete a step",
		Long: `Permanently delete a step from a card.

You can pass either a step ID or a Basecamp URL:
  basecamp cards step delete 789 --in my-project
  basecamp cards step delete https://3.basecamp.com/123/buckets/456/card_tables/cards/steps/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			stepIDStr, err := extractID(args[0])
			if err != nil {
				return err
			}
			stepID, err := strconv.ParseInt(stepIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid step ID")
			}

			err = app.Account().CardSteps().Delete(cmd.Context(), stepID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"deleted": true},
				output.WithSummary(fmt.Sprintf("Deleted step #%s", stepIDStr)),
			)
		},
	}

	return cmd
}

type projectCardTable struct {
	ID      int64
	Title   string
	Enabled bool
}

// resolveCardsProjectID resolves the project for a card command from the
// flagProject value with the standard flag > env > config > interactive
// precedence, then maps it to a numeric project ID via the name resolver.
func resolveCardsProjectID(cmd *cobra.Command, app *appctx.App, flagProject string) (string, error) {
	projectID := flagProject
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

// getCardTableID retrieves the card table ID from a project's dock.
// If the project has multiple card tables and no explicit cardTableID is provided,
// an error is returned with the available card table IDs.
func getCardTableID(cmd *cobra.Command, app *appctx.App, projectID, explicitCardTableID string) (string, error) {
	cardTables, err := listProjectCardTables(cmd, app, projectID)
	if err != nil {
		return "", err
	}
	if len(cardTables) == 0 {
		return "", output.ErrNotFound("card table", projectID)
	}

	if explicitCardTableID != "" {
		idInt, parseErr := strconv.ParseInt(explicitCardTableID, 10, 64)
		if parseErr == nil {
			for _, ct := range cardTables {
				if ct.ID == idInt {
					return explicitCardTableID, nil
				}
			}
		}
		return "", output.ErrUsageHint(
			fmt.Sprintf("Card table '%s' not found", explicitCardTableID),
			fmt.Sprintf("Available card tables: %s", formatCardTableIDs(cardTables)),
		)
	}

	if len(cardTables) == 1 {
		return fmt.Sprintf("%d", cardTables[0].ID), nil
	}

	return "", ambiguousCardTablesError(cardTables)
}

// resolveColumnBucketID resolves the numeric project (bucket) ID for a column
// command that takes a column ID or URL plus the --in/--project flag.
//
// Precedence: URL > flag > config > interactive. This intentionally differs from
// most card commands, which seed from the flag first: a column URL encodes the
// column's actual bucket, so honoring it over a mismatched --in avoids targeting
// the wrong bucket (a guaranteed 404).
func resolveColumnBucketID(cmd *cobra.Command, app *appctx.App, project, urlProjectID string) (int64, error) {
	projectID := urlProjectID
	if projectID == "" {
		projectID = project
	}
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return 0, err
		}
		projectID = app.Config.ProjectID
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return 0, err
	}

	bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return 0, output.ErrUsage("Project ID must be numeric")
	}
	return bucketID, nil
}

// standardColumnType is the only card column type that supports color and
// on-hold sections. The API rejects these actions on Triage, Not now, and Done
// columns with a bare 404, so we check the type up front and return a clearer
// error.
const standardColumnType = "Kanban::Column"

// requireStandardColumn verifies the column supports color/on-hold actions.
// Triage, Not now, and Done columns don't, so it returns a friendly usage error
// instead of the API's bare 404. action names the attempted operation, e.g.
// "Enabling on-hold".
func requireStandardColumn(cmd *cobra.Command, app *appctx.App, columnID int64, action string) error {
	col, err := app.Account().CardColumns().Get(cmd.Context(), columnID)
	if err != nil {
		return convertSDKError(err)
	}
	if col.Type != standardColumnType {
		return output.ErrUsageHint(
			fmt.Sprintf("%s is only available on standard columns", action),
			fmt.Sprintf("%q is not a standard column — color and on-hold sections apply only to regular columns, not Triage, Not now, or Done", col.Title),
		)
	}
	return nil
}

func listProjectCardTables(cmd *cobra.Command, app *appctx.App, projectID string) ([]projectCardTable, error) {
	path := fmt.Sprintf("/projects/%s.json", projectID)
	resp, err := app.Account().Get(cmd.Context(), path)
	if err != nil {
		return nil, convertSDKError(err)
	}

	var project struct {
		Dock []struct {
			Name    string `json:"name"`
			ID      int64  `json:"id"`
			Title   string `json:"title"`
			Enabled bool   `json:"enabled"`
		} `json:"dock"`
	}
	if err := resp.UnmarshalData(&project); err != nil {
		return nil, fmt.Errorf("failed to parse project: %w", err)
	}

	var cardTables []projectCardTable
	for _, item := range project.Dock {
		if item.Name == "kanban_board" {
			cardTables = append(cardTables, projectCardTable{ID: item.ID, Title: item.Title, Enabled: item.Enabled})
		}
	}

	return cardTables, nil
}

func resolveCardTableForCard(cmd *cobra.Command, app *appctx.App, projectID, explicitCardTableID string, card *basecamp.Card) (string, *basecamp.CardTable, error) {
	cardTables, err := listProjectCardTables(cmd, app, projectID)
	if err != nil {
		return "", nil, err
	}
	if len(cardTables) == 0 {
		return "", nil, output.ErrNotFound("card table", projectID)
	}

	if explicitCardTableID != "" {
		explicitID, parseErr := strconv.ParseInt(explicitCardTableID, 10, 64)
		if parseErr != nil {
			return "", nil, output.ErrUsageHint(
				fmt.Sprintf("Card table '%s' not found", explicitCardTableID),
				fmt.Sprintf("Available card tables: %s", formatCardTableIDs(cardTables)),
			)
		}
		for _, ct := range cardTables {
			if ct.ID != explicitID {
				continue
			}
			cardTableData, err := app.Account().CardTables().Get(cmd.Context(), ct.ID)
			if err != nil {
				return "", nil, convertSDKError(err)
			}
			return fmt.Sprintf("%d", ct.ID), cardTableData, nil
		}
		return "", nil, output.ErrUsageHint(
			fmt.Sprintf("Card table '%s' not found", explicitCardTableID),
			fmt.Sprintf("Available card tables: %s", formatCardTableIDs(cardTables)),
		)
	}

	if len(cardTables) == 1 {
		cardTableData, err := app.Account().CardTables().Get(cmd.Context(), cardTables[0].ID)
		if err != nil {
			return "", nil, convertSDKError(err)
		}
		return fmt.Sprintf("%d", cardTables[0].ID), cardTableData, nil
	}

	if card == nil || card.Parent == nil || card.Parent.ID == 0 {
		return "", nil, ambiguousCardTablesError(cardTables)
	}

	if cardTableIDVal, ok := resolveCardTableIDFromParentColumn(cmd, app, cardTables, card.Parent.ID); ok {
		cardTableIDInt, parseErr := strconv.ParseInt(cardTableIDVal, 10, 64)
		if parseErr != nil {
			return "", nil, output.ErrUsage("Invalid card table ID")
		}
		cardTableData, err := app.Account().CardTables().Get(cmd.Context(), cardTableIDInt)
		if err != nil {
			return "", nil, convertSDKError(err)
		}
		return cardTableIDVal, cardTableData, nil
	}

	var matchedID string
	var matchedTable *basecamp.CardTable
	for _, ct := range cardTables {
		cardTableData, err := app.Account().CardTables().Get(cmd.Context(), ct.ID)
		if err != nil {
			return "", nil, convertSDKError(err)
		}
		if !cardTableContainsColumn(cardTableData.Lists, card.Parent.ID) {
			continue
		}
		if matchedTable != nil {
			return "", nil, ambiguousCardTablesError(cardTables)
		}
		matchedID = fmt.Sprintf("%d", ct.ID)
		matchedTable = cardTableData
	}
	if matchedTable == nil {
		return "", nil, ambiguousCardTablesError(cardTables)
	}

	return matchedID, matchedTable, nil
}

func resolveCardTableIDFromParentColumn(cmd *cobra.Command, app *appctx.App, cardTables []projectCardTable, parentColumnID int64) (string, bool) {
	column, err := app.Account().CardColumns().Get(cmd.Context(), parentColumnID)
	if err != nil || column == nil || column.Parent == nil || column.Parent.ID == 0 {
		return "", false
	}
	for _, ct := range cardTables {
		if ct.ID == column.Parent.ID {
			return fmt.Sprintf("%d", ct.ID), true
		}
	}
	return "", false
}

func cardTableContainsColumn(columns []basecamp.CardColumn, columnID int64) bool {
	for _, column := range columns {
		if column.ID == columnID {
			return true
		}
		if column.OnHold != nil && column.OnHold.ID == columnID {
			return true
		}
	}
	return false
}

func findDoneColumn(columns []basecamp.CardColumn) *basecamp.CardColumn {
	for i := range columns {
		if columns[i].Type == "Kanban::DoneColumn" {
			return &columns[i]
		}
	}
	return nil
}

func ambiguousCardTablesError(cardTables []projectCardTable) error {
	return ambiguousCardTablesErrorFor("--card-table", cardTables)
}

func ambiguousCardTablesErrorFor(flag string, cardTables []projectCardTable) error {
	lines := make([]string, 0, len(cardTables))
	for _, ct := range cardTables {
		title := ct.Title
		if title == "" {
			title = "card table"
		}
		lines = append(lines, fmt.Sprintf("  %d  %s", ct.ID, title))
	}
	return &output.Error{
		Code:    output.CodeAmbiguous,
		Message: "Multiple card tables found",
		Hint:    fmt.Sprintf("Specify one with %s <id>:\n%s", flag, strings.Join(lines, "\n")),
	}
}

// formatCardTableIDs formats card table IDs for error messages.
func formatCardTableIDs(cardTables []projectCardTable) string {
	ids := make([]string, len(cardTables))
	for i, ct := range cardTables {
		if ct.Title != "" {
			ids[i] = fmt.Sprintf("%d (%s)", ct.ID, ct.Title)
		} else {
			ids[i] = fmt.Sprintf("%d", ct.ID)
		}
	}
	return fmt.Sprintf("%v", ids)
}

// isNumericID checks if a string consists only of digits (matches bash: [[ "$s" =~ ^[0-9]+$ ]]).
func isNumericID(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// resolveColumn finds a column by ID or name.
func resolveColumn(columns []basecamp.CardColumn, identifier string) int64 {
	// Try by ID first
	idInt, err := strconv.ParseInt(identifier, 10, 64)
	if err == nil {
		for _, col := range columns {
			if col.ID == idInt {
				return col.ID
			}
		}
	}

	// Fall back to name match
	for _, col := range columns {
		if col.Title == identifier {
			return col.ID
		}
	}

	return 0
}

func resolveAssigneeIDs(ctx context.Context, app *appctx.App, input string) ([]int64, error) {
	return resolvePersonRoleIDs(ctx, app, input, "Assignee")
}

// resolvePersonRoleIDs resolves a comma-separated list of person names or IDs,
// labeling errors with the given role (see resolvePersonRoleID).
func resolvePersonRoleIDs(ctx context.Context, app *appctx.App, input, role string) ([]int64, error) {
	parts := strings.Split(input, ",")
	ids := make([]int64, 0, len(parts))

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		id, err := resolvePersonRoleID(ctx, app, part, role)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}

	if len(ids) == 0 {
		return nil, output.ErrUsage(fmt.Sprintf("No valid %ss provided", strings.ToLower(role)))
	}

	return ids, nil
}
