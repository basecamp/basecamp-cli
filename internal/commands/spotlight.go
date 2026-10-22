package commands

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
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
			recordingID, err := spotlightTarget(cmd, app, args[0])
			if err != nil {
				return err
			}

			recording, err := app.Account().Recordings().Spotlight(cmd.Context(), recordingID)
			if err != nil {
				return spotlightError(err)
			}

			return app.OK(recording,
				output.WithSummary(fmt.Sprintf("Spotlighted #%d on its project page", recordingID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "unspotlight",
						Cmd:         fmt.Sprintf("basecamp unspotlight %d", recordingID),
						Description: "Remove the spotlight",
					},
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp show %d", recordingID),
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
			recordingID, err := spotlightTarget(cmd, app, args[0])
			if err != nil {
				return err
			}

			if err := app.Account().Recordings().Unspotlight(cmd.Context(), recordingID); err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"id": recordingID, "spotlighted": false},
				output.WithSummary(fmt.Sprintf("Spotlight removed from #%d", recordingID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "spotlight",
						Cmd:         fmt.Sprintf("basecamp spotlight %d", recordingID),
						Description: "Spotlight it again",
					},
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp show %d", recordingID),
						Description: "View item",
					},
				),
			)
		},
	}
}

// spotlightTarget reads the recording a spotlight command acts on. A comment
// link names its parent, since comments cannot be spotlighted, and a project
// link names no recording at all, so its project ID must not go out as one.
func spotlightTarget(cmd *cobra.Command, app *appctx.App, arg string) (int64, error) {
	if err := ensureAccount(cmd, app); err != nil {
		return 0, err
	}
	recordingIDStr, _ := extractWithProject(arg)
	recordingID, err := strconv.ParseInt(recordingIDStr, 10, 64)
	if err != nil {
		return 0, output.ErrUsage("Invalid ID")
	}
	return recordingID, nil
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
