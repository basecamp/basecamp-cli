package commands

import (
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewDraftsCmd creates the drafts command for the current user's unpublished
// drafts.
//
// Drafts are personal and cross-project, like bookmarks: only their author can
// see them, so there is no project to scope the listing to. The group has one
// leaf because the API has one endpoint — publishing a draft happens through
// the command for whatever the draft is (messages, docs, uploads).
func NewDraftsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "drafts",
		Short: "List your unpublished drafts",
		Long: `List your unpublished drafts across every project.

Drafts are private to you until published. A draft may be a message, document,
upload, client approval, or client correspondence.

  basecamp drafts list`,
		Annotations: map[string]string{
			"agent_notes": "Account-wide and personal — no --in <project> needed.\n" +
				"Read-only: publish a draft with the command for its type.",
		},
	}

	cmd.AddCommand(newDraftsListCmd())

	return cmd
}

func newDraftsListCmd() *cobra.Command {
	var (
		limit int
		page  int
		all   bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your unpublished drafts",
		Long: `List your unpublished drafts, most recently updated first.

This is a personal feed spanning every project, so the default is bounded: it
walks pages until it has ` + strconv.Itoa(accountWideDefaultLimit) + ` drafts rather than fetching the whole listing
and discarding most of it. --all is how you ask for every page. The server
caps the full listing at 250.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDraftsList(cmd, limit, page, all)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum drafts to return")
	cmd.Flags().IntVar(&page, "page", 0, "Return only this page")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch every page")

	return cmd
}

func runDraftsList(cmd *cobra.Command, limit, page int, all bool) error {
	app := appctx.FromContext(cmd.Context())

	if err := validateAccountWidePaginationFlags(cmd, limit, page, all); err != nil {
		return err
	}
	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	fetch := func(p int32) ([]basecamp.Draft, basecamp.ListMeta, error) {
		result, err := app.Account().Drafts().List(cmd.Context(), p)
		if err != nil {
			return nil, basecamp.ListMeta{}, convertSDKError(err)
		}
		return result.Drafts, result.Meta, nil
	}

	// Page 0 means "follow the Link header across every page", which is what
	// --all asks for and no other path may reach.
	var (
		drafts []basecamp.Draft
		meta   basecamp.ListMeta
		capped bool
	)
	if all || page > 0 {
		sdkPage, err := accountWidePage(page, all)
		if err != nil {
			return err
		}
		if drafts, meta, err = fetch(sdkPage); err != nil {
			return err
		}
	} else {
		effectiveLimit := limit
		if effectiveLimit == 0 {
			effectiveLimit = accountWideDefaultLimit
		}
		var err error
		if drafts, capped, meta, err = accountWideCollect(fetch, accountWideFlatCount[basecamp.Draft], effectiveLimit); err != nil {
			return err
		}
		// The walk stops at a page boundary, so trim to the exact cap.
		if len(drafts) > effectiveLimit {
			drafts = drafts[:effectiveLimit]
		}
	}

	respOpts := accountWideRespOpts(len(drafts), "draft", "drafts", meta, limit > 0)
	if notice := accountWideCapNotice(capped, meta, len(drafts), "drafts"); notice != "" {
		respOpts = append(respOpts, output.WithNotice(notice))
	}
	respOpts = append(respOpts, output.WithDisplayData(flattenDrafts(drafts)))
	respOpts = append(respOpts,
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "messages",
				Cmd:         "basecamp messages list --in <project>",
				Description: "List a project's messages",
			},
			output.Breadcrumb{
				Action:      "files",
				Cmd:         "basecamp files list --in <project>",
				Description: "List a project's docs and files",
			},
		),
	)

	return app.OK(drafts, respOpts...)
}

// flattenDrafts builds the display rows for the draft listing.
//
// A Draft nests its project and its parent, and the renderer's generic column
// detection skips nested objects — so a generic render drops the project a
// draft belongs to, which on a cross-project personal feed is the column that
// makes a row actionable.
//
// Parent and scheduled_posting_at are nil-able, and both nil states are
// meaningful rather than missing: a draft with no parent is filed directly
// under its project, and one with no scheduled time simply is not scheduled.
// Rendering them as blanks would read as absent data, so both states are
// spelled out.
func flattenDrafts(drafts []basecamp.Draft) []map[string]any {
	rows := make([]map[string]any, 0, len(drafts))
	for _, d := range drafts {
		row := map[string]any{
			"id":      d.ID,
			"title":   d.Title,
			"type":    d.Type,
			"project": d.Bucket.Name,
			"updated": d.UpdatedAt,
		}

		row["filed_under"] = "project root"
		if d.Parent != nil {
			row["filed_under"] = d.Parent.Title
		}

		row["scheduled"] = "not scheduled"
		if d.ScheduledPostingAt != nil {
			row["scheduled"] = *d.ScheduledPostingAt
		}

		rows = append(rows, row)
	}
	return rows
}
