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

// commentFlags holds the parsed state of --no-comments / --all-comments.
type commentFlags struct {
	noComments  bool
	allComments bool
}

// shouldFetch returns true when the caller should attempt comment fetching.
func (cf *commentFlags) shouldFetch() bool {
	return !cf.noComments
}

// addCommentFlags registers --no-comments and --all-comments on cmd and
// returns the parsed flag holder. Follows the addDownloadAttachmentsFlag
// pattern.
func addCommentFlags(cmd *cobra.Command) *commentFlags {
	cf := &commentFlags{}
	cmd.Flags().BoolVar(&cf.noComments, "no-comments", false, "Skip comment fetching")
	cmd.Flags().BoolVar(&cf.allComments, "all-comments", false,
		fmt.Sprintf("Fetch all comments instead of the default %d", basecamp.DefaultCommentLimit))
	cmd.MarkFlagsMutuallyExclusive("no-comments", "all-comments")
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

// fetchRecordingComments fetches comments for a recording and returns an
// enrichment bundle. Handles the full lifecycle: skip check, fetch with
// limit, truncation notice, failure notice, and breadcrumb generation.
func fetchRecordingComments(
	ctx context.Context,
	app *appctx.App,
	id string,
	data map[string]any,
	cf *commentFlags,
) *commentEnrichment {
	commentsCount, hasCommentsCount := recordingCommentsCount(data)

	result := &commentEnrichment{}

	if hasCommentsCount && commentsCount > 0 {
		result.CountLabel = pluralizeComments(commentsCount)
		result.Breadcrumbs = append(result.Breadcrumbs, output.Breadcrumb{
			Action:      "comments",
			Cmd:         fmt.Sprintf("basecamp comments list --all %s", id),
			Description: "View all comments",
		})
	}

	if !cf.shouldFetch() || !hasCommentsCount || commentsCount <= 0 {
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
		result.FetchNotice = commentsFetchFailedNotice(commentsCount, id)
		return result
	}

	result.Comments = commentsResult.Comments

	if !cf.allComments {
		totalComments := commentsCount
		if commentsResult.Meta.TotalCount > totalComments {
			totalComments = commentsResult.Meta.TotalCount
		}
		notice := commentsTruncationNotice(len(commentsResult.Comments), totalComments)
		result.Notice = notice
		if notice != "" {
			result.Breadcrumbs = append(result.Breadcrumbs, output.Breadcrumb{
				Action:      "all-comments",
				Cmd:         fmt.Sprintf("basecamp show --all-comments %s", id),
				Description: "Fetch all comments",
			})
		}
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
