package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewFilingsCmd creates the filings command, which reads the background move
// or copy that a move or copy command started.
//
// A filing is account-wide and personal, like a draft: only the person who
// started it can read it, so there is no project to scope it to. Every
// family's move and copy commands start one, so it has a group of its own
// rather than a leaf under any one of them.
func NewFilingsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "filings",
		Short: "Check a move or copy that is still running",
		Long: `Check a move or copy that Basecamp runs in the background.

Every move and copy command waits for its filing to finish. When the wait ends
first, the command names the filing; read it here.

  basecamp filings show 4`,
		Annotations: map[string]string{
			"agent_notes": "Account-wide and personal: no --in <project> needed, and only the person who started the move or copy can read it.\n" +
				"Read-only. Use it when a move or copy command reports the filing still running.",
		},
	}

	cmd.AddCommand(newFilingsShowCmd())

	return cmd
}

func newFilingsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <filing_id>",
		Short: "Show where a move or copy got to",
		Long: `Show a move's or copy's status, what it was of, and once it completes, the
recording it made.

A filing is pending or processing while Basecamp works, then completed or
failed. A completed move names the recording's new id; a completed copy names
the copy.

  basecamp filings show 4`,
		Annotations: map[string]string{
			"agent_notes": "Takes the filing id a move or copy command names when its wait ends, not a recording id.\n" +
				"pending/processing: do not move or copy the recording again; check this again later.\n" +
				"completed: destination_recording is the moved recording at its new id, or the copy.\n" +
				"failed: a move is safe to try again. Basecamp does not say whether a failed copy left anything behind, so look in the destination before copying again.\n" +
				"Not found: only the person who started it can read it, and a completed one whose recording was since deleted is gone.",
		},
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || id <= 0 {
				return output.ErrUsage("Filing ID must be a positive number")
			}

			filing, err := app.Account().Recordings().GetFiling(cmd.Context(), id)
			if err != nil {
				if sdkErr := basecamp.AsError(err); sdkErr != nil && sdkErr.Code == basecamp.CodeNotFound {
					return output.ErrNotFoundHint("filing", args[0],
						"Only the person who started a move or copy can read its filing, and a completed one whose recording was since deleted is gone")
				}
				return convertSDKError(err)
			}
			return app.OK(filing, filingReport(filing)...)
		},
	}
}

// filingReport summarizes a filing by its status, with what to do next.
func filingReport(filing *basecamp.Filing) []output.ResponseOption {
	verb, past := "move", "Moved"
	if filing.Type == basecamp.FilingCopy {
		verb, past = "copy", "Copied"
	}
	again := output.Breadcrumb{Action: "check", Cmd: fmt.Sprintf("basecamp filings show %d", filing.ID), Description: "Check it again"}

	switch dest := filing.DestinationRecording; {
	case filing.Status == basecamp.FilingCompleted && dest != nil:
		summary := fmt.Sprintf("%s #%d as %s #%d", past, filing.SourceRecordingID, dest.Type, dest.ID)
		return []output.ResponseOption{
			output.WithSummary(summary),
			output.WithBreadcrumbs(output.Breadcrumb{Action: "show", Cmd: filedShowCmd(dest), Description: "View it"}),
		}
	case filing.Status == basecamp.FilingFailed && filing.Type == basecamp.FilingCopy:
		return []output.ResponseOption{
			output.WithSummary(fmt.Sprintf("Basecamp could not copy #%d", filing.SourceRecordingID)),
			output.WithNotice("Basecamp does not say whether a failed copy left anything behind. Look in the destination before copying again."),
		}
	case filing.Status == basecamp.FilingFailed:
		return []output.ResponseOption{
			output.WithSummary(fmt.Sprintf("Basecamp could not move #%d", filing.SourceRecordingID)),
			output.WithNotice("A move is safe to try again: Basecamp refuses one that is running or has landed."),
		}
	default:
		return []output.ResponseOption{
			output.WithSummary(fmt.Sprintf("The %s of #%d is %s", verb, filing.SourceRecordingID, filing.Status)),
			output.WithNotice(fmt.Sprintf("Do not %s #%d again while this runs.", verb, filing.SourceRecordingID)),
			output.WithBreadcrumbs(again),
		}
	}
}

// filedShowCmds are the commands that show a recording of each type a move or
// copy makes, by its account-wide id.
var filedShowCmds = map[string]string{
	"Todo":            "basecamp todos show",
	"Todolist":        "basecamp todolists show",
	"Message":         "basecamp messages show",
	"Document":        "basecamp files show",
	"Upload":          "basecamp files show",
	"Vault":           "basecamp files show",
	"CloudFile":       "basecamp files show",
	"GoogleDocument":  "basecamp files show",
	"Kanban::Card":    "basecamp cards show",
	"Kanban::Column":  "basecamp cards column show",
	"Schedule::Entry": "basecamp schedule show",
	"Question":        "basecamp checkins question show",
	"Inbox::Forward":  "basecamp forwards show",
}

// filedShowCmd shows the recording a filing made: by its type's own command,
// or, for a dock tool, by its URL.
func filedShowCmd(dest *basecamp.FilingDestinationRecording) string {
	if show, ok := filedShowCmds[dest.Type]; ok {
		return fmt.Sprintf("%s %d", show, dest.ID)
	}
	if dest.AppURL != "" {
		return "basecamp show " + dest.AppURL
	}
	return fmt.Sprintf("basecamp show %d", dest.ID)
}
