package commands

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewInboxCmd creates the inbox command: the addressed-items lane of the
// account event feed.
func NewInboxCmd() *cobra.Command {
	var (
		entry    feedEntry
		reasons  []string
		types    []string
		buckets  []string
		all      bool
		maxPages int
	)

	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "Poll addressed items (agents only)",
		Long: `Poll the authenticated principal's addressed items.

The inbox is the low-noise "someone addressed you" lane of the account event
feed — mentions, assignments, subscriptions, watches, pings, and boosts of
your work. It is served to agent principals only for now; any other principal
receives a permission error.

  basecamp inbox --since now
  basecamp inbox --since 0 --reasons mentioned,assigned
  basecamp inbox --position "$POSITION" --all

Each item is its own delivery: one event addresses you once per reason, so
deduplicate by addressing_id, never by the event's id.`,
		Annotations: map[string]string{
			"agent_notes": "Account-wide — no --in <project> needed. Agents only; a person principal gets exit 4 (HTTP 403).\n" +
				"Returns {items, position, next}. Persist 'position' only after processing the page's items, then resume with --position.\n" +
				"--since takes an item id (int64), 'now' to enter at the present, or 0 to replay the earliest retained items (30-day retention).\n" +
				"Deduplicate by addressing_id, not by event id: one event addresses you once per reason and each reason is its own item.\n" +
				"Items are thin pointers — refetch the referenced recording with 'basecamp show <recording_id>' before acting on it.\n" +
				"Items are never self-addressed, so no loop guard is needed here.\n" +
				"Inbox positions are never interchangeable with 'basecamp events poll' positions.\n" +
				"--all walks 'next' to the end of the current walk; it is not a live tail.",
		},
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := entry.validate(cmd, inboxLane); err != nil {
				return err
			}
			if maxPages < 1 {
				return output.ErrUsage("--max-pages must be at least 1")
			}

			bucketIDs, err := feedIDs(trimFilter(buckets), "--buckets")
			if err != nil {
				return err
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			opts := &basecamp.PollInboxOptions{
				Since:    entry.since,
				Position: entry.position,
				Reasons:  trimFilter(reasons),
				Types:    trimFilter(types),
				Buckets:  bucketIDs,
			}

			request := feedRequest{
				lane:    inboxLane,
				baseURL: app.Config.BaseURL,
				filters: requestFilters(inboxFilterFlags(opts)),
			}

			items := make([]basecamp.InboxItem, 0)
			var position, next string
			pages, capped := 0, false
			service := app.Account().EventFeed()

			for {
				page, err := service.PollInbox(cmd.Context(), opts)
				if err != nil {
					return feedError(request, err)
				}
				pages++
				items = append(items, page.Items...)
				position, next = page.Position, page.Next

				if !all || next == "" {
					break
				}
				if pages >= maxPages {
					capped = true
					break
				}
				if err := checkContinuation(app.Config.BaseURL, next); err != nil {
					return err
				}
				if opts, err = basecamp.PollInboxOptionsFromURL(next); err != nil {
					return convertSDKError(err)
				}
				request.filters = requestFilters(inboxFilterFlags(opts))
			}

			summary := fmt.Sprintf("%d addressed item(s)", len(items))
			if pages > 1 {
				summary = fmt.Sprintf("%s over %d pages", summary, pages)
			}
			if next != "" && !capped {
				summary += "; more to serve"
			}

			respOpts := []output.ResponseOption{
				output.WithSummary(summary),
				output.WithNotice(request.resumeNotice(position, capped, maxPages)),
				// Rows for the enumerating output modes, as in events poll —
				// and an item's identity here is its addressing id, so that is
				// what the row's id is. The rendered envelope keeps
				// addressing_id under its own name, because applying the
				// feed's dedupe-by-event-id rule to the inbox would discard
				// every reason but one.
				output.WithDisplayData(inboxRows(items)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "resume",
						Cmd:         request.positionCommand(position),
						Description: "Poll again from this page's position, with the same filters",
					},
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp show <recording_id>",
						Description: "Fetch a referenced recording",
					},
				),
			}
			return app.OK(map[string]any{
				"items":    items,
				"position": position,
				"next":     next,
			}, respOpts...)
		},
	}

	cmd.Flags().StringVar(&entry.since, "since", "", "Enter the inbox: an item id to start after, 'now', or 0 for the earliest retained items")
	cmd.Flags().StringVar(&entry.position, "position", "", "Resume from a position token a previous page issued")
	cmd.Flags().StringSliceVar(&reasons, "reasons", nil, "Filter by addressing reason: mentioned, assigned, subscribed, watched, pinged, boosted")
	cmd.Flags().StringSliceVar(&types, "types", nil, "Narrow by event type (comma-separated, e.g. comment.created)")
	cmd.Flags().StringSliceVar(&buckets, "buckets", nil, "Narrow by bucket (project) id (comma-separated)")
	cmd.Flags().BoolVar(&all, "all", false, "Walk 'next' to the end of the current walk (not a live tail)")
	cmd.Flags().IntVar(&maxPages, "max-pages", feedDefaultMaxPages, "Maximum pages to fetch with --all")

	return cmd
}

// inboxRow is one addressed item flattened for display and for the
// enumerating output modes: the item's own identity as id, the reason it
// addressed you, and the pointers you need to fetch what it points at.
type inboxRow struct {
	ID          int64     `json:"id"`
	Reason      string    `json:"reason"`
	AddressedAt time.Time `json:"addressed_at"`
	EventID     int64     `json:"event_id"`
	EventType   string    `json:"event_type"`
	BucketID    int64     `json:"bucket_id"`
	RecordingID int64     `json:"recording_id"`
}

func inboxRows(items []basecamp.InboxItem) []inboxRow {
	rows := make([]inboxRow, 0, len(items))
	for _, item := range items {
		rows = append(rows, inboxRow{
			ID:          item.AddressingID,
			Reason:      item.Reason,
			AddressedAt: item.AddressedAt,
			EventID:     item.Event.ID,
			EventType:   item.Event.EventType,
			BucketID:    item.Event.BucketID,
			RecordingID: item.Event.RecordingID,
		})
	}
	return rows
}
