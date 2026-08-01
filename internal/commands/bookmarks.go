package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewBookmarksCmd creates the bookmarks command for the current user's personal
// bookmarks.
//
// Bookmarks are per-person: they are visible only to whoever created them, so
// there is no project to scope them to and no --project flag. Every leaf here
// addresses a recording by id or URL, since a bookmark is a link between the
// current user and one recording rather than a resource with an id of its own
// that anyone would paste.
func NewBookmarksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bookmarks",
		Short: "Manage your personal bookmarks",
		Long: `Manage your personal bookmarks.

Bookmarks are private to you — nobody else can see what you have bookmarked.
Each one points at a single recording (a to-do, message, document, card, and
so on), addressed by its id or by pasting its Basecamp URL.

  basecamp bookmarks list
  basecamp bookmarks add https://3.basecamp.com/1234567/buckets/89/todos/42
  basecamp bookmarks check 42
  basecamp bookmarks remove 42`,
		Annotations: map[string]string{
			"agent_notes": "Account-wide and personal — no --in <project> needed.\n" +
				"add/remove are idempotent; check reports true/false and always exits 0.",
		},
	}

	cmd.AddCommand(
		newBookmarksListCmd(),
		newBookmarksAddCmd(),
		newBookmarksRemoveCmd(),
		newBookmarksCheckCmd(),
	)

	return cmd
}

func newBookmarksListCmd() *cobra.Command {
	var (
		limit int
		page  int
		all   bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your bookmarks",
		Long: `List your bookmarks, most recently bookmarked first.

This is a personal feed spanning every project, so the default is bounded:
it walks pages until it has ` + strconv.Itoa(accountWideDefaultLimit) + ` bookmarks rather than fetching the whole
listing and discarding most of it. --all is how you ask for every page.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBookmarksList(cmd, limit, page, all)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum bookmarks to return")
	cmd.Flags().IntVar(&page, "page", 0, "Return only this page")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch every page")

	return cmd
}

func runBookmarksList(cmd *cobra.Command, limit, page int, all bool) error {
	app := appctx.FromContext(cmd.Context())

	if err := validateAccountWidePaginationFlags(cmd, limit, page, all); err != nil {
		return err
	}
	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	fetch := func(p int32) ([]basecamp.Bookmark, basecamp.ListMeta, error) {
		result, err := app.Account().Bookmarks().List(cmd.Context(), p)
		if err != nil {
			return nil, basecamp.ListMeta{}, convertSDKError(err)
		}
		return result.Bookmarks, result.Meta, nil
	}

	// Page 0 is the SDK's "follow the Link header across every page", which is
	// what --all asks for. No other path may reach it: a bounded default that
	// fetched everything and then trimmed would be the same defect the
	// account-wide listings were rewritten to remove.
	var (
		bookmarks []basecamp.Bookmark
		meta      basecamp.ListMeta
		capped    bool
	)
	if all || page > 0 {
		sdkPage, err := accountWidePage(page, all)
		if err != nil {
			return err
		}
		if bookmarks, meta, err = fetch(sdkPage); err != nil {
			return err
		}
	} else {
		effectiveLimit := limit
		if effectiveLimit == 0 {
			effectiveLimit = accountWideDefaultLimit
		}
		var err error
		if bookmarks, capped, meta, err = accountWideCollect(fetch, accountWideFlatCount[basecamp.Bookmark], effectiveLimit); err != nil {
			return err
		}
		// The walk stops at a page boundary, so trim to the exact cap.
		if len(bookmarks) > effectiveLimit {
			bookmarks = bookmarks[:effectiveLimit]
		}
	}

	respOpts := accountWideRespOpts(len(bookmarks), "bookmark", "bookmarks", meta, limit > 0)
	if notice := accountWideCapNotice(capped, meta, len(bookmarks), "bookmarks"); notice != "" {
		respOpts = append(respOpts, output.WithNotice(notice))
	}
	respOpts = append(respOpts, output.WithDisplayData(flattenBookmarks(bookmarks)))
	respOpts = append(respOpts,
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "remove",
				Cmd:         "basecamp bookmarks remove <id>",
				Description: "Remove a bookmark",
			},
			output.Breadcrumb{
				Action:      "show",
				Cmd:         "basecamp recordings show <id>",
				Description: "View a bookmarked recording",
			},
		),
	)

	return app.OK(bookmarks, respOpts...)
}

// flattenBookmarks builds the display rows for the bookmark listing.
//
// A Bookmark's own id and timestamps say nothing about what was bookmarked —
// the recording is nested, and the renderer's generic column detection skips
// nested objects. Rendering one generically therefore produces a table of ids
// and dates with the actual subject missing, so the rows are built by hand:
// the recording's id is what every other command takes as an argument, and the
// project is what makes a cross-project personal feed attributable.
func flattenBookmarks(bookmarks []basecamp.Bookmark) []map[string]any {
	rows := make([]map[string]any, 0, len(bookmarks))
	for _, b := range bookmarks {
		row := map[string]any{
			"id":            b.Recording.ID,
			"title":         b.Recording.Title,
			"type":          b.Recording.Type,
			"bookmarked_at": b.CreatedAt,
		}
		if b.Recording.Bucket != nil {
			row["project"] = b.Recording.Bucket.Name
		}
		rows = append(rows, row)
	}
	return rows
}

func newBookmarksAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <id|url>",
		Short: "Bookmark a recording",
		Long: `Bookmark a recording so it shows up in your personal bookmarks.

Idempotent: bookmarking something you have already bookmarked returns the
existing bookmark rather than failing or creating a duplicate.

  basecamp bookmarks add 42
  basecamp bookmarks add https://3.basecamp.com/1234567/buckets/89/todos/42`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			recordingID, err := bookmarkRecordingID(args[0])
			if err != nil {
				return err
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			bookmark, err := app.Account().Bookmarks().Create(cmd.Context(), recordingID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(bookmark,
				output.WithSummary(fmt.Sprintf("Bookmarked %s", bookmarkLabel(bookmark.Recording))),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "list",
						Cmd:         "basecamp bookmarks list",
						Description: "List your bookmarks",
					},
					output.Breadcrumb{
						Action:      "remove",
						Cmd:         fmt.Sprintf("basecamp bookmarks remove %d", recordingID),
						Description: "Remove this bookmark",
					},
				),
			)
		},
	}
}

func newBookmarksRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <id|url>",
		Short: "Remove a bookmark",
		Long: `Remove a recording from your personal bookmarks.

Idempotent: removing something you have not bookmarked also succeeds, so this
is safe to run without checking first.

  basecamp bookmarks remove 42`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			recordingID, err := bookmarkRecordingID(args[0])
			if err != nil {
				return err
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if err := app.Account().Bookmarks().Delete(cmd.Context(), recordingID); err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"id": recordingID, "bookmarked": false},
				output.WithSummary(fmt.Sprintf("Removed bookmark on recording %d", recordingID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "list",
						Cmd:         "basecamp bookmarks list",
						Description: "List your bookmarks",
					},
				),
			)
		},
	}
}

func newBookmarksCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check <id|url>",
		Short: "Report whether you have bookmarked a recording",
		Long: `Report whether you have bookmarked a recording.

Reports the answer rather than signalling it through the exit code: both
outcomes exit 0, and "not bookmarked" is a successful answer. Exit codes here
mean a request failed, so reserving a nonzero code for "false" would be
indistinguishable from a real error.

  basecamp bookmarks check 42
  basecamp bookmarks check 42 --json    # {"bookmarked": false}`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			recordingID, err := bookmarkRecordingID(args[0])
			if err != nil {
				return err
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			bookmarked, err := app.Account().Bookmarks().Get(cmd.Context(), recordingID)
			if err != nil {
				return convertSDKError(err)
			}

			summary := fmt.Sprintf("Recording %d is not bookmarked", recordingID)
			breadcrumb := output.Breadcrumb{
				Action:      "add",
				Cmd:         fmt.Sprintf("basecamp bookmarks add %d", recordingID),
				Description: "Bookmark it",
			}
			if bookmarked {
				summary = fmt.Sprintf("Recording %d is bookmarked", recordingID)
				breadcrumb = output.Breadcrumb{
					Action:      "remove",
					Cmd:         fmt.Sprintf("basecamp bookmarks remove %d", recordingID),
					Description: "Remove this bookmark",
				}
			}

			return app.OK(map[string]any{"id": recordingID, "bookmarked": bookmarked},
				output.WithSummary(summary),
				output.WithBreadcrumbs(breadcrumb),
			)
		},
	}
}

// bookmarkRecordingID resolves the <id|url> positional every bookmark verb
// takes. With no way to browse bookmarkable recordings from this group, a
// pasted URL is the natural way to name one.
func bookmarkRecordingID(arg string) (int64, error) {
	id, err := strconv.ParseInt(extractID(arg), 10, 64)
	if err != nil {
		return 0, output.ErrUsageHint(
			fmt.Sprintf("%q is not a recording id or Basecamp URL", arg),
			"Pass a numeric recording id, or paste the recording's Basecamp URL",
		)
	}
	return id, nil
}

// bookmarkLabel names a recording for a summary line, falling back to the type
// and id when the projection carries no title.
func bookmarkLabel(r basecamp.Recording) string {
	if r.Title != "" {
		return fmt.Sprintf("%q", r.Title)
	}
	if r.Type != "" {
		return fmt.Sprintf("%s %d", r.Type, r.ID)
	}
	return fmt.Sprintf("recording %d", r.ID)
}
