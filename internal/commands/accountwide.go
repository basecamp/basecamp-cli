package commands

import (
	"fmt"

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
func accountWidePage(page int, all bool) int32 {
	if all {
		return 0
	}
	if page < 1 {
		return 1
	}
	return int32(page)
}

// accountWideRespOpts builds the summary and truncation notice shared by the
// account-wide listings.
func accountWideRespOpts(count int, noun string, meta basecamp.ListMeta) []output.ResponseOption {
	respOpts := []output.ResponseOption{
		output.WithSummary(fmt.Sprintf("%d %s across all projects", count, noun)),
	}
	if notice := output.TruncationNoticeWithTotal(count, meta.TotalCount); notice != "" {
		respOpts = append(respOpts, output.WithNotice(notice))
	}
	return respOpts
}
