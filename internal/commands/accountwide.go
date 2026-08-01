package commands

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// Account-wide listings.
//
// The resource groups are project-scoped: they resolve a project from
// --project/--in, then the global flag, then config, and otherwise prompt for
// one. When none of those supply a project, the SDK's account-wide aggregate
// endpoints answer the same question across every accessible project, so the
// groups list account-wide instead of prompting.
//
// Every account-wide listing needs a bounded default and a way to ask for more.
// A listing with no escape hatch cannot recover from a server error mid-crawl,
// which is why the two commands that once had no pagination flags now have
// them. Each command maps --page/--all onto the page number these endpoints
// take, where 0 means "follow the Link header across every page".
//
// The default is bounded because project-scoped "all" is one project's items
// while account-wide "all" is the whole account: the same number means very
// different work. --all is how you ask for the account.

// projectKnown reports whether a project is available without prompting.
func projectKnown(app *appctx.App, projectFlag string) bool {
	if app == nil {
		return false
	}
	return projectFlag != "" || app.Flags.Project != "" || app.Config.ProjectID != ""
}

// accountWidePage maps a group's existing --page/--all pair onto the page
// number the account-wide endpoints take. Groups that only support page 1
// pass their validated page through unchanged.
//
// A page beyond int32 is a usage error rather than a clamp. Clamping would
// serve a different page than the one asked for, and I3 forbids silently
// altering a flag just as much as silently dropping it.
func accountWidePage(page int, all bool) (int32, error) {
	if all {
		return 0, nil
	}
	if page < 1 {
		return 1, nil
	}
	if page > math.MaxInt32 {
		return 0, output.ErrUsage("--page is out of range")
	}
	return int32(page), nil
}

// accountWideDefaultLimit bounds every paginated account-wide listing that has
// no reason to differ. Project-scoped defaults are untouched: capping one
// project's items and capping the whole account are not the same promise.
const accountWideDefaultLimit = 100

// accountWideMaxPages bounds the walk so a server that never returns an empty
// page cannot spin forever. It is a runaway backstop, not a tuning knob: the
// default cap of 100 items is met within a handful of pages, and hitting this
// ceiling is reported as a capped result rather than a complete one.
const accountWideMaxPages = 1000

// accountWideCollect walks positive pages until it has collected at least limit
// items, which is far cheaper than fetching every page only to throw most of it
// away. It is the shared engine behind every bounded account-wide listing.
//
// fetch returns one page. count reports how many items a collected slice holds:
// for a flat payload that is len, but for a payload grouped by project it is
// the number of items nested inside the groups, since capping groups would drop
// whole projects from the listing.
//
// The walk stops on the first of: an empty page, reaching the cap, exhausting
// the server's reported total, or the page ceiling.
//
// Two things callers must know:
//
//   - Meta.TotalCount is compared against len(items) — the top-level elements,
//     which for a grouped payload are the groups, never the inner-item counter.
//     The two counters measure different things and mixing them ends the walk
//     early on grouped feeds.
//   - This does not trim. It stops at the first page boundary at or past the
//     cap, so it can overshoot by up to one page. Every caller trims exactly
//     afterwards.
//
// capped reports that collection stopped at the cap rather than at the end of
// the listing, which is what lets a caller say "more may exist" honestly. meta
// is the first page's, whose X-Total-Count is the account-wide total.
func accountWideCollect[T any](
	fetch func(page int32) ([]T, basecamp.ListMeta, error),
	count func([]T) int,
	limit int,
) ([]T, bool, basecamp.ListMeta, error) {
	// Non-nil so an empty first page renders as [] rather than null. The
	// SDK-backed --all path returns an empty slice, and consumers iterating
	// .data[] should not have to handle both.
	items := make([]T, 0)
	var meta basecamp.ListMeta

	for page := int32(1); page <= accountWideMaxPages; page++ {
		pageItems, pageMeta, err := fetch(page)
		if err != nil {
			return nil, false, basecamp.ListMeta{}, err
		}
		if page == 1 {
			meta = pageMeta
		}
		// An empty page is the server saying the listing has ended. It is the
		// only content-based stop: a page carrying groups that happen to hold
		// no items is sparse, not terminal, and later pages may still have
		// results. Stopping on one used to drop every project after it.
		if len(pageItems) == 0 {
			return items, false, meta, nil
		}

		items = append(items, pageItems...)
		collected := count(items)

		// Exhaustion is tested before the cap, and against the first page's
		// total rather than this page's.
		//
		// Order matters: a listing that ends exactly at the cap is complete,
		// not truncated, and checking the cap first would report "more may
		// exist" about a listing with nothing left in it.
		//
		// The total comes from meta because the first page's is the one this
		// helper documents as authoritative. Reading it from pageMeta let a
		// later page that omits X-Total-Count silently switch the bound off
		// and walk past the declared end of the listing.
		if meta.TotalCount > 0 && len(items) >= meta.TotalCount {
			// Everything the listing has is in hand. The caller still trims to
			// the cap, so it is only complete if the trim drops nothing.
			return items, collected > limit, meta, nil
		}
		if collected >= limit {
			return items, true, meta, nil
		}
	}

	// Ran out of page budget with the server still handing back items. Report
	// it as capped: whatever remains is unreached, and saying so beats
	// presenting a bounded walk as a complete listing.
	return items, true, meta, nil
}

// accountWideFlatCount is the count function for the flat listings, where an
// item is its own unit and the top-level length is the item total.
func accountWideFlatCount[T any](items []T) int { return len(items) }

// accountWideCapNotice reports that the bounded walk stopped at the cap rather
// than at the end of the listing. Without it a trimmed listing reads as a
// complete one.
//
// It defers to accountWideRespOpts whenever the server reported a total, since
// "N of M" is strictly more informative than "more may exist". The walk stops
// early by design, so on the feeds that withhold X-Total-Count the count is all
// there is to say.
func accountWideCapNotice(capped bool, meta basecamp.ListMeta, count int, plural string) string {
	// Any server-reported total wins, not just one larger than the count.
	// When the total equals or trails the count there is demonstrably nothing
	// more to show, and "more may exist" would contradict the server.
	if !capped || meta.TotalCount > 0 {
		return ""
	}
	return fmt.Sprintf(
		"Showing the first %d %s; more may exist (use --all for every page, or --limit to raise the cap)",
		count, plural)
}

// Account-wide task filters (--assignee, --due).
//
// These map onto EverythingTaskFilters, which the todo and card aggregates
// accept as a trailing parameter. They are genuinely server-side there: the
// request carries assignee_ids[] and due=, and the server narrows the listing
// before paginating.
//
// What that does *not* mean is a constant request count. The bounded walk's cap
// counts items, so a narrower filter can return fewer per page and need another
// page to reach the cap — a production soak measured 2 requests for 100
// unfiltered todos against 3 for --assignee's 21. Filtering leaves the walk's
// algorithm untouched; the number of requests it takes is a property of the
// result, not of the filter.
//
// Project-scoped --assignee is a different animal — see the note on
// filterTodosByAssignees.

// dueFilterValues are the tokens --due accepts. These are categories, not
// dates: internal/dateparse is deliberately not involved, since "overdue" is
// not a date and "with" is not a date range.
var dueFilterValues = []string{"with", "without", "overdue"}

// rejectEmptyTaskFilterValues refuses an explicitly empty --due or --assignee.
//
// Every other check in this file tests the flag's *value*, which makes `--due=`
// indistinguishable from never passing --due: the project-scoped guard stops
// rejecting it, the account-wide path builds no filter, and the caller gets a
// full unfiltered listing believing they narrowed it. Presence is what makes it
// a request, so presence is what has to be tested — and it has to happen before
// account resolution, which can otherwise prompt on the way to a listing that
// was never going to be filtered.
//
// `--assignee=` is the same mistake in the other direction: StringArrayVar
// appends the empty string, so len(assignees) > 0 sends a filter that names
// nobody.
// It also validates the --due token here rather than only in
// validateAccountWideTaskFilters, which runs after ensureAccount. Neither the
// token set nor the emptiness check depends on the account or the scope, so
// leaving them late meant `todos list --due tomorrow` with no account
// configured hit account resolution first — an interactive session got the
// account picker and a noninteractive one got "--account is required", and the
// real error was never shown. A usage error that needs no account should not
// require one.
func validateTaskFilterValues(cmd *cobra.Command, due string, assignees []string) error {
	if cmd.Flags().Changed("due") && due == "" {
		return output.ErrUsageHint(
			"--due needs a value",
			fmt.Sprintf("Pass one of: %s", strings.Join(dueFilterValues, ", ")))
	}
	if err := validateDueFilter(due); err != nil {
		return err
	}
	for _, assignee := range assignees {
		if strings.TrimSpace(assignee) == "" {
			return output.ErrUsageHint(
				"--assignee needs a value",
				"Pass a name or id, or drop the flag to list everyone's.")
		}
	}
	return nil
}

// validateDueFilter rejects an unknown --due token, naming the alternatives.
func validateDueFilter(due string) error {
	if due == "" {
		return nil
	}
	for _, valid := range dueFilterValues {
		if due == valid {
			return nil
		}
	}
	return output.ErrUsageHint(
		fmt.Sprintf("%q is not a valid --due filter", due),
		"Pick one of: "+strings.Join(dueFilterValues, ", "),
	)
}

// rejectAssigneeWithUnassigned refuses --assignee alongside the unassigned
// selector.
//
// The server builds that selector as todos_recordings.remaining.not_assigned,
// over a relation the assignee filter has already narrowed
// (bc3:app/controllers/concerns/everything/todos/recordings.rb:24). "Assigned
// to Ann" intersected with "assigned to nobody" is necessarily empty, so the
// combination cannot return a row. Refusing it beats returning zero results
// that look like a real answer.
func rejectAssigneeWithUnassigned(noun string) error {
	return output.ErrUsageHint(
		"--assignee and --unassigned cannot be combined (nothing can match both)",
		fmt.Sprintf("Drop --unassigned to see that person's %s, or drop --assignee to see unassigned ones", noun),
	)
}

// validateAccountWideTaskFilters enforces the combinations the filters cannot
// honor, before any request is issued.
//
// --due names the same axis as the dedicated due-date selectors: --overdue and
// --no-due-date each pick their own endpoint, and --due narrows a different
// one. Combining them asks two endpoints for one answer, so the flag that would
// be ignored is named instead.
func validateAccountWideTaskFilters(assignees []string, due string, unassigned, overdue, noDueDate bool, noun string) error {
	if err := validateDueFilter(due); err != nil {
		return err
	}
	if len(assignees) > 0 && unassigned {
		return rejectAssigneeWithUnassigned(noun)
	}
	if due != "" {
		switch {
		case overdue:
			return output.ErrUsageHint(
				"--due and --overdue cannot be combined (each selects a different listing)",
				"Use --overdue on its own, or --due overdue to narrow another listing")
		case noDueDate:
			return output.ErrUsageHint(
				"--due and --no-due-date cannot be combined (each selects a different listing)",
				"Use --no-due-date on its own, or --due without to narrow another listing")
		}
	}
	return nil
}

// accountWideTaskFilters resolves --assignee/--due into the SDK filter struct,
// returning nil when neither was passed so the call stays byte-identical to an
// unfiltered one.
func accountWideTaskFilters(ctx context.Context, app *appctx.App, assignees []string, due string) (*basecamp.EverythingTaskFilters, error) {
	if len(assignees) == 0 && due == "" {
		return nil, nil
	}

	filters := &basecamp.EverythingTaskFilters{Due: due}
	for _, assignee := range assignees {
		// Each value may itself be a comma-separated list, so --assignee is
		// repeatable and comma-separated both, matching how the other
		// people-taking flags already behave.
		ids, err := resolvePersonRoleIDs(ctx, app, assignee, "Assignee")
		if err != nil {
			return nil, err
		}
		filters.AssigneeIDs = append(filters.AssigneeIDs, ids...)
	}
	return filters, nil
}

// validateAccountWidePaginationFlags enforces the combination rules every
// bounded account-wide listing shares: --all and --limit both answer "how much",
// --page answers "which one", and mixing them asks for two different things at
// once.
//
// The older listings spell these rules out inline, one copy each. New listings
// call this instead; the existing copies are left alone rather than swept into
// this change, since rewriting eight working call sites to prove a helper is a
// bigger diff than the helper earns here.
func validateAccountWidePaginationFlags(cmd *cobra.Command, limit, page int, all bool) error {
	if all && limit > 0 {
		return output.ErrUsage("--all and --limit are mutually exclusive")
	}
	if page > 0 && (all || limit > 0) {
		return output.ErrUsage("--page cannot be combined with --all or --limit")
	}
	if cmd.Flags().Changed("page") && page < 1 {
		return output.ErrUsageHint(
			"--page must be a positive page number",
			"Omit --page, or pass --all, to follow every page",
		)
	}
	if limit < 0 {
		return output.ErrUsage("--limit must be zero or positive")
	}
	return nil
}

// rejectScopedPaginationFlags refuses --limit/--page/--all on a path that has
// no pagination to thread them onto.
//
// A flag registered on a command but ignored on one of its paths is exactly the
// defect this contract exists to prevent: the operator gets a listing that
// silently disregards what they asked for. Saying so by name costs one error
// and keeps the flag honest everywhere it is accepted.
func rejectScopedPaginationFlags(cmd *cobra.Command, scope, hint string) error {
	for _, name := range []string{"limit", "page", "all"} {
		if cmd.Flags().Changed(name) {
			return output.ErrUsageHint(fmt.Sprintf("--%s does not apply to %s", name, scope), hint)
		}
	}
	return nil
}

// flattenAccountWideRecordings builds the display rows for the four aggregate
// feeds that return []Recording. The renderer's generic column detection drops
// the nested bucket, which on an account-wide listing removes the one thing
// that makes a row actionable — the project it came from. Title also falls
// back to Subject, since a Comment's own title is the generic type name while
// its subject carries the thread.
func flattenAccountWideRecordings(recordings []basecamp.Recording) []map[string]any {
	rows := make([]map[string]any, 0, len(recordings))
	for _, r := range recordings {
		title := r.Title
		if r.Subject != nil && *r.Subject != "" {
			title = *r.Subject
		}
		row := map[string]any{
			"id":      r.ID,
			"title":   title,
			"type":    r.Type,
			"created": r.CreatedAt,
		}
		if r.Bucket != nil {
			row["project"] = r.Bucket.Name
		}
		if r.Creator != nil {
			row["creator"] = r.Creator.Name
		}
		rows = append(rows, row)
	}
	return rows
}

// rejectAccountWideTodolist refuses the root-level --todolist on an
// account-wide listing. It names a container inside one project, so it can no
// more narrow an account-wide feed than a project can — and being a root
// global rather than a group flag, it reaches every one of these commands
// whether or not the command has any notion of a todolist.
func rejectAccountWideTodolist(app *appctx.App, noun string) error {
	if app == nil || app.Flags.Todolist == "" {
		return nil
	}
	return output.ErrUsageHint(
		fmt.Sprintf("--todolist cannot scope an account-wide %s listing", noun),
		"Drop --todolist, or pass --project/--in to list within that project.")
}

// accountWideRespOpts builds the summary and truncation notice shared by the
// account-wide listings. singular/plural are the noun in both forms, since a
// one-item account-wide listing is common enough that "1 boosts" shows up.
//
// Every account-wide listing now has an --all, so the recovery advice can name
// it unconditionally. It did not always: the two flagless commands used to get
// no advice at all, since recommending --all to a command that has none is
// worse than saying nothing.
//
// explicitLimit reports whether the user asked for the cap. It changes the
// recovery advice rather than the fact: these commands reject --all alongside
// --limit, so telling someone who passed --limit to add --all names a
// combination the command refuses.
func accountWideRespOpts(count int, singular, plural string, meta basecamp.ListMeta, explicitLimit bool) []output.ResponseOption {
	noun := plural
	if count == 1 {
		noun = singular
	}
	respOpts := []output.ResponseOption{
		output.WithSummary(fmt.Sprintf("%d %s across all projects", count, noun)),
	}
	if meta.TotalCount > count {
		notice := fmt.Sprintf("Showing %d of %d results", count, meta.TotalCount)
		if explicitLimit {
			// Not "drop --limit": with an explicit limit above the default,
			// dropping it falls back to 100 and returns fewer.
			notice += " (raise --limit, or use --all for the complete list)"
		} else {
			notice += " (use --all for the complete list)"
		}
		respOpts = append(respOpts, output.WithNotice(notice))
	}
	return respOpts
}
