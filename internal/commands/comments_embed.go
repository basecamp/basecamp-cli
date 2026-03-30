package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// commentFlags holds the parsed state of --comments / --no-comments / --all-comments.
type commentFlags struct {
	defaultOn   bool
	comments    bool
	noComments  bool
	allComments bool
}

// shouldFetch returns true when the caller should attempt comment fetching.
func (cf *commentFlags) shouldFetch() bool {
	if cf.noComments {
		return false
	}
	return cf.defaultOn || cf.comments || cf.allComments
}

// addCommentFlags registers --comments, --no-comments, and --all-comments on
// cmd and returns the parsed flag holder. When defaultOn is true (e.g.
// basecamp show), comments are fetched by default; when false (typed show
// commands), --comments or --all-comments must be passed to opt in.
func addCommentFlags(cmd *cobra.Command, defaultOn bool) *commentFlags {
	cf := &commentFlags{defaultOn: defaultOn}
	cmd.Flags().BoolVar(&cf.comments, "comments", false, "Include comments in output")
	cmd.Flags().BoolVar(&cf.noComments, "no-comments", false, "Skip comment fetching")
	cmd.Flags().BoolVar(&cf.allComments, "all-comments", false,
		fmt.Sprintf("Fetch all comments instead of the default %d", basecamp.DefaultCommentLimit))
	cmd.MarkFlagsMutuallyExclusive("comments", "no-comments", "all-comments")
	return cf
}

// commentEnrichment holds everything produced by fetchRecordingComments.
type commentEnrichment struct {
	// Comments is the fetched comment slice (nil when skipped or failed).
	Comments []basecamp.Comment

	// Notice is a user-facing truncation notice (empty when all comments
	// were fetched or when fetching was skipped).
	Notice string

	// FetchNotice is a diagnostic notice when fetching failed (empty on success).
	FetchNotice string

	// Breadcrumbs are comment-related breadcrumbs to append to the response.
	Breadcrumbs []output.Breadcrumb

	// CountLabel is a parenthetical like "(3 comments)" for summary augmentation.
	// Empty when the recording has no comments_count field.
	CountLabel string
}

// fetchCommentsForRecording fetches comments for a recording. Does not require
// a data map — derives count from the API response metadata. Use this from
// typed show commands that have a struct, not a map.
func fetchCommentsForRecording(
	ctx context.Context,
	app *appctx.App,
	id string,
	cf *commentFlags,
) *commentEnrichment {
	result := &commentEnrichment{}

	if !cf.shouldFetch() {
		return result
	}

	recordingID, parseErr := strconv.ParseInt(id, 10, 64)
	if parseErr != nil {
		return result
	}

	commentOpts := &basecamp.CommentListOptions{
		Limit: basecamp.DefaultCommentLimit,
	}
	if cf.allComments {
		commentOpts.Limit = -1
	}

	commentsResult, commentsErr := app.Account().Comments().List(
		ctx, recordingID, commentOpts,
	)
	if commentsErr != nil {
		result.FetchNotice = fmt.Sprintf(
			"Comment fetching failed — view: basecamp comments list --all %s", id)
		return result
	}

	result.Comments = commentsResult.Comments
	totalCount := commentsResult.Meta.TotalCount

	if totalCount > 0 {
		result.CountLabel = pluralizeComments(totalCount)
		result.Breadcrumbs = append(result.Breadcrumbs, output.Breadcrumb{
			Action:      "comments",
			Cmd:         fmt.Sprintf("basecamp comments list --all %s", id),
			Description: "View all comments",
		})
	}

	if !cf.allComments {
		result.Notice = commentsTruncationNotice(len(commentsResult.Comments), totalCount)
	}

	return result
}

// fetchRecordingComments wraps fetchCommentsForRecording and additionally
// reads comments_count from the data map. This provides a CountLabel even
// when --no-comments skips the fetch (the parent object carries the count).
func fetchRecordingComments(
	ctx context.Context,
	app *appctx.App,
	id string,
	data map[string]any,
	cf *commentFlags,
) *commentEnrichment {
	result := fetchCommentsForRecording(ctx, app, id, cf)

	commentsCount, hasCommentsCount := recordingCommentsCount(data)

	// When CountLabel wasn't derived from the fetch (e.g. skipped, or
	// Meta.TotalCount missing from response), fall back to the parent's
	// comments_count field.
	if result.CountLabel == "" && hasCommentsCount && commentsCount > 0 {
		result.CountLabel = pluralizeComments(commentsCount)
		result.Breadcrumbs = append(result.Breadcrumbs, output.Breadcrumb{
			Action:      "comments",
			Cmd:         fmt.Sprintf("basecamp comments list --all %s", id),
			Description: "View all comments",
		})
	}

	// When TotalCount was missing from the API response but the parent's
	// comments_count indicates truncation, recompute the notice so the user
	// sees a warning and the --all-comments breadcrumb is appended below.
	if result.Notice == "" && !cf.allComments && hasCommentsCount && commentsCount > 0 &&
		len(result.Comments) > 0 && len(result.Comments) < commentsCount {
		result.Notice = commentsTruncationNotice(len(result.Comments), commentsCount)
	}

	// When fetch failed, enhance error with count from parent.
	if result.FetchNotice != "" && hasCommentsCount && commentsCount > 0 {
		result.FetchNotice = commentsFetchFailedNotice(commentsCount, id)
	}

	// Add the all-comments breadcrumb specific to `basecamp show`.
	if result.Notice != "" {
		result.Breadcrumbs = append(result.Breadcrumbs, output.Breadcrumb{
			Action:      "all-comments",
			Cmd:         fmt.Sprintf("basecamp show --all-comments %s", id),
			Description: "Fetch all comments",
		})
	}

	return result
}

// withComments injects the "comments" key into data. If data is already a
// map[string]any it is modified in place; otherwise it is marshaled to a map
// first (same pattern as withAttachmentMeta). Returns data unchanged when
// comments is nil.
func withComments(data any, comments []basecamp.Comment) any {
	if comments == nil {
		return data
	}

	if m, ok := data.(map[string]any); ok {
		m["comments"] = comments
		return m
	}

	b, err := json.Marshal(data)
	if err != nil {
		return data
	}
	// Decode with UseNumber to preserve integer precision (IDs > 2^53).
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return data
	}
	m["comments"] = comments
	return m
}

// apply merges comments into data and returns the enriched data plus response
// options (notices + breadcrumbs). Every typed show command calls this instead
// of inlining the withComments / applyNotices / breadcrumbs sequence.
func (ce *commentEnrichment) apply(data any, attachmentNotice string) (any, []output.ResponseOption) {
	data = withComments(data, ce.Comments)
	opts := ce.applyNotices(attachmentNotice)
	if len(ce.Breadcrumbs) > 0 {
		opts = append(opts, output.WithBreadcrumbs(ce.Breadcrumbs...))
	}
	return data, opts
}

// applyNotices merges comment and attachment notices into response options.
// Routes fetch-failure diagnostics to WithDiagnostic; normal notices to
// WithNotice. attachmentNotice is folded in so it is never lost.
func (ce *commentEnrichment) applyNotices(attachmentNotice string) []output.ResponseOption {
	var opts []output.ResponseOption

	if ce.FetchNotice != "" {
		diagnostic := joinShowNotices(ce.FetchNotice, attachmentNotice)
		opts = append(opts, output.WithDiagnostic(diagnostic))
	} else {
		notice := joinShowNotices(ce.Notice, attachmentNotice)
		if notice != "" {
			opts = append(opts, output.WithNotice(notice))
		}
	}

	return opts
}
