package commands

import (
	"fmt"
	"math"

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
// Groups keep their own pagination flags rather than gaining new ones; each
// maps them onto the page number these endpoints take, where 0 means "follow
// the Link header across every page".

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
// moreFlag names the flag that widens the listing, or is empty for the
// commands I5 gives no pagination flags at all — recommending --all to a
// command that has no --all is worse than saying nothing.
//
// explicitLimit reports whether the user asked for the cap. It changes the
// recovery advice rather than the fact: these commands reject --all alongside
// --limit, so telling someone who passed --limit to add --all names a
// combination the command refuses.
func accountWideRespOpts(count int, singular, plural string, meta basecamp.ListMeta, moreFlag string, explicitLimit bool) []output.ResponseOption {
	noun := plural
	if count == 1 {
		noun = singular
	}
	respOpts := []output.ResponseOption{
		output.WithSummary(fmt.Sprintf("%d %s across all projects", count, noun)),
	}
	if meta.TotalCount > count {
		notice := fmt.Sprintf("Showing %d of %d results", count, meta.TotalCount)
		switch {
		case explicitLimit:
			notice += " (raise or drop --limit for more)"
		case moreFlag != "":
			notice += fmt.Sprintf(" (use %s for the complete list)", moreFlag)
		}
		respOpts = append(respOpts, output.WithNotice(notice))
	}
	return respOpts
}
