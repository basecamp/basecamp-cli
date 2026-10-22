package commands

import (
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/hostutil"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/urlarg"
)

// NewSpotlightCmd creates the spotlight command, which puts a recording's card
// on its project or template home page.
func NewSpotlightCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "spotlight <id|url>",
		Short: "Spotlight a recording on its project page",
		Long: `Put a recording's card in the Spotlights area of its project (or template)
home page. Same as "Spotlight on project page" in Basecamp.

You can pass either an ID or a Basecamp URL:
  basecamp spotlight 789
  basecamp spotlight https://3.basecamp.com/123/buckets/456/messages/789

Messages, documents, folders, files, cloud files, Google docs, to-do lists,
to-dos, card table columns, cards, check-in questions and schedule entries can
be spotlighted while they are active and in a project or template. A comment
cannot, so a comment's URL (…#__recording_<id>) spotlights the item the
comment sits on.

Spotlighting a recording that is already spotlighted succeeds and changes
nothing. Requires edit access to the project.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			persistentAccount := hasPersistentAccount(app.Config)
			recordingID, err := spotlightTarget(cmd, app, args[0])
			if err != nil {
				return err
			}

			recording, err := app.Account().Recordings().Spotlight(cmd.Context(), recordingID)
			if err != nil {
				return spotlightError(err)
			}

			accountArg := replyAccountArg(persistentAccount, app.Config.AccountID)
			return app.OK(recording,
				output.WithSummary(fmt.Sprintf("Spotlighted #%d on its project page", recordingID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "unspotlight",
						Cmd:         fmt.Sprintf("basecamp unspotlight %d%s", recordingID, accountArg),
						Description: "Remove the spotlight",
					},
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp show %d%s", recordingID, accountArg),
						Description: "View item",
					},
				),
			)
		},
	}
}

// NewUnspotlightCmd creates the unspotlight command, which takes a recording's
// card off its project or template home page.
func NewUnspotlightCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unspotlight <id|url>",
		Short: "Remove a recording's spotlight",
		Long: `Remove a recording's card from the Spotlights area of its project (or
template) home page. Same as "Remove Spotlight" in Basecamp.

You can pass either an ID or a Basecamp URL:
  basecamp unspotlight 789
  basecamp unspotlight https://3.basecamp.com/123/buckets/456/messages/789

A comment's URL (…#__recording_<id>) acts on the item the comment sits on.

Removing the spotlight from a recording that has none succeeds and changes
nothing. Requires edit access to the project.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			persistentAccount := hasPersistentAccount(app.Config)
			recordingID, err := spotlightTarget(cmd, app, args[0])
			if err != nil {
				return err
			}

			if err := app.Account().Recordings().Unspotlight(cmd.Context(), recordingID); err != nil {
				return convertSDKError(err)
			}

			accountArg := replyAccountArg(persistentAccount, app.Config.AccountID)
			return app.OK(map[string]any{"id": recordingID, "spotlighted": false},
				output.WithSummary(fmt.Sprintf("Spotlight removed from #%d", recordingID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "spotlight",
						Cmd:         fmt.Sprintf("basecamp spotlight %d%s", recordingID, accountArg),
						Description: "Spotlight it again",
					},
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp show %d%s", recordingID, accountArg),
						Description: "View item",
					},
				),
			)
		},
	}
}

// spotlightKinds are the URL types that name a recording bc3 may spotlight:
// one of the spotlightable kinds, or a generic recordings/<id> URL, which is
// as good as its bare ID. bc3 still decides each case; the list keeps a URL
// for something else (a person, a Campfire, a path the router only matched by
// shape) from sending its ID out as a recording's.
var spotlightKinds = []string{
	"cards", "cloud_files", "columns", "documents", "google_documents", "messages",
	"questions", "recordings", "schedule_entries", "todolists", "todos", "uploads", "vaults",
}

// spotlightTarget reads the recording a spotlight command acts on: an ID, or
// the URL of a single item on a trusted host. The URL router is host-agnostic,
// so the host check keeps a look-alike URL from retargeting the configured
// account. A URL's account is adopted when none is configured and refused when
// it differs, so a same-numbered recording in another account is never
// touched. A comment's URL names the item it sits on, since comments cannot be
// spotlighted.
func spotlightTarget(cmd *cobra.Command, app *appctx.App, arg string) (int64, error) {
	idStr, urlAccountID := arg, ""
	if urlarg.IsURL(arg) {
		if !hostutil.IsTrustedBasecampHost(arg, app.Config.BaseURL) {
			return 0, spotlightUsage(cmd, "not a Basecamp URL", spotlightHint)
		}
		parsed := urlarg.Parse(arg)
		if parsed == nil || parsed.IsCollection || !slices.Contains(spotlightKinds, parsed.Type) {
			return 0, spotlightUsage(cmd, "the URL names something else", spotlightHint)
		}
		// The URL's ID is the recurring event's, so acting on it would spotlight
		// the whole series. The occurrence is a recording of its own.
		if parsed.OccurrenceDate != "" {
			return 0, spotlightUsage(cmd, "the URL names one occurrence of a recurring event",
				"Pass the occurrence's own ID, which basecamp schedule show <url> prints, or the event's ID to act on the whole series")
		}
		idStr, urlAccountID = parsed.RecordingID, parsed.AccountID
	}
	recordingID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, output.ErrUsage("Invalid ID")
	}
	if urlAccountID != "" {
		switch {
		case app.Config.AccountID == "":
			app.Config.AccountID = urlAccountID
		case urlAccountID != app.Config.AccountID:
			return 0, output.ErrUsage(fmt.Sprintf("URL account %s does not match the configured account %s", urlAccountID, app.Config.AccountID))
		}
	}
	if err := ensureAccount(cmd, app); err != nil {
		return 0, err
	}
	return recordingID, nil
}

const spotlightHint = "Pass its ID or its own URL (…/messages/<id>, …/todos/<id>, …); basecamp spotlight --help lists the types"

func spotlightUsage(cmd *cobra.Command, reason, hint string) error {
	return output.ErrUsageHint(cmd.Name()+" takes a single item: "+reason, hint)
}

// spotlightError says why bc3 refused a spotlight. Its 422 carries no reason
// the SDK surfaces, and the usual cause is a comment or a trashed item.
func spotlightError(err error) error {
	var sdkErr *basecamp.Error
	if !errors.As(err, &sdkErr) || sdkErr.Code != basecamp.CodeValidation {
		return convertSDKError(err)
	}
	return &output.Error{
		Code:       output.CodeValidation,
		Message:    "Basecamp refused the spotlight",
		Hint:       "Only an active recording in a project or template can be spotlighted, and not a comment; see basecamp spotlight --help for the types",
		HTTPStatus: sdkErr.HTTPStatus,
		Cause:      sdkErr,
	}
}
