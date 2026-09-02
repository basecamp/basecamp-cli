package commands

import (
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewBubbleUpCmd creates the bubble-up command for resurfacing a recording in
// the current user's readings — the BC5 successor to "save".
//
// Bubble-up is per-person, like bookmarks: it links the current user to one
// recording, so there is no project to scope it to and no --in flag. Every leaf
// addresses a recording by id or URL.
func NewBubbleUpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bubble-up",
		Short: "Bubble a recording up in your readings",
		Long: `Bubble a recording up so it resurfaces in your readings.

Bubble-up is private to you and points at a single recording (a to-do, message,
document, card, and so on), addressed by its id or by pasting its Basecamp URL.

  basecamp bubble-up add 42
  basecamp bubble-up add 42 --at tomorrow
  basecamp bubble-up remove 42`,
		Annotations: map[string]string{
			"agent_notes": "Account-wide and personal — no --in <project> needed.\n" +
				"add/remove are idempotent. add takes --at to schedule; bc3 requires a\n" +
				"value, so add sends \"now\" when --at is omitted. There is no status\n" +
				"read (per-recording GET is an unrenderable API gap); the full list is\n" +
				"basecamp notifications.",
		},
	}

	cmd.AddCommand(
		newBubbleUpAddCmd(),
		newBubbleUpRemoveCmd(),
	)

	return cmd
}

func newBubbleUpAddCmd() *cobra.Command {
	var at string

	cmd := &cobra.Command{
		Use:   "add <id|url>",
		Short: "Bubble a recording up",
		Long: `Bubble a recording up so it resurfaces in your readings.

By default it bubbles up now. Pass --at to schedule it instead: a keyword
("today", "tomorrow", "weekend", "next_week") or an ISO8601 date.

Idempotent: bubbling up something already bubbled up is a no-op that still
succeeds.

  basecamp bubble-up add 42
  basecamp bubble-up add 42 --at tomorrow
  basecamp bubble-up add https://3.basecamp.com/1234567/buckets/89/todos/42`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			recordingID, err := bubbleUpRecordingID(args[0])
			if err != nil {
				return err
			}
			if err := validateBubbleUpAt(at); err != nil {
				return err
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// bc3 requires a value for `at` (an omitted param raises server-side
			// on Date.iso8601(nil)), so default the immediate case to "now"
			// rather than sending nothing.
			when := at
			if when == "" {
				when = "now"
			}

			if err := app.Account().BubbleUps().Create(cmd.Context(), recordingID, &when); err != nil {
				return convertSDKError(err)
			}

			immediate := when == "now"
			summary := fmt.Sprintf("Bubbled up recording %d", recordingID)
			if !immediate {
				summary = fmt.Sprintf("Scheduled recording %d to bubble up %s", recordingID, when)
			}

			return app.OK(map[string]any{"id": recordingID, "bubbled_up": immediate, "at": when},
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "remove",
						Cmd:         fmt.Sprintf("basecamp bubble-up remove %d", recordingID),
						Description: "Pop this bubble-up",
					},
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp show %d", recordingID),
						Description: "View the recording",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&at, "at", "", `When to bubble up: "now" (default), a keyword ("today", "tomorrow", "weekend", "next_week"), or an ISO8601 date`)

	return cmd
}

func newBubbleUpRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <id|url>",
		Short: "Pop a bubble-up",
		Long: `Remove a recording's bubble-up from your readings.

Idempotent: popping something not bubbled up also succeeds, so this is safe to
run without checking first.

  basecamp bubble-up remove 42`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			recordingID, err := bubbleUpRecordingID(args[0])
			if err != nil {
				return err
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if err := app.Account().BubbleUps().Delete(cmd.Context(), recordingID); err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"id": recordingID, "bubbled_up": false},
				output.WithSummary(fmt.Sprintf("Popped bubble-up on recording %d", recordingID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "add",
						Cmd:         fmt.Sprintf("basecamp bubble-up add %d", recordingID),
						Description: "Bubble it up again",
					},
				),
			)
		},
	}
}

// bubbleUpRecordingID resolves the <id|url> positional every bubble-up verb
// takes. With no way to browse bubble-uppable recordings from this group, a
// pasted URL is the natural way to name one.
func bubbleUpRecordingID(arg string) (int64, error) {
	id, err := strconv.ParseInt(extractID(arg), 10, 64)
	if err != nil || id <= 0 {
		return 0, output.ErrUsageHint(
			fmt.Sprintf("%q is not a recording id or Basecamp URL", arg),
			"Pass a numeric recording id, or paste the recording's Basecamp URL",
		)
	}
	return id, nil
}

// validateBubbleUpAt rejects an --at value bc3 cannot parse before any account
// resolution or network call, so a typo like "tomorow" is a local usage error
// rather than a server-side Date.iso8601 raise. Valid values are the empty
// default, "now", the schedule keywords, or an ISO8601 date.
func validateBubbleUpAt(at string) error {
	switch at {
	case "", "now", "today", "tomorrow", "weekend", "next_week":
		return nil
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339, "2006-01-02T15:04:05"} {
		if _, err := time.Parse(layout, at); err == nil {
			return nil
		}
	}
	return output.ErrUsageHint(
		fmt.Sprintf("%q is not a valid --at value", at),
		`Use "now", a keyword ("today", "tomorrow", "weekend", "next_week"), or an ISO8601 date`,
	)
}
