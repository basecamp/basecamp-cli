package commands

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// NewCommentsCmd creates the comments command group (list/show/update).
func NewCommentsCmd() *cobra.Command {
	var recordingID string
	var limit, page int
	var all bool

	cmd := &cobra.Command{
		Use:   "comments",
		Short: "List and manage comments",
		Long:  "List, show, and update comments on recordings.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Default to list when called without subcommand
			return runCommentsList(cmd, recordingID, limit, page, all)
		},
	}

	cmd.Flags().StringVarP(&recordingID, "on", "r", "", "Recording ID to list comments for")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of comments to fetch (0 = default 100)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all comments (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")

	cmd.AddCommand(
		newCommentsListCmd(),
		newCommentsShowCmd(),
		newCommentsUpdateCmd(),
	)

	return cmd
}

func newCommentsListCmd() *cobra.Command {
	var recordingID string
	var limit, page int
	var all bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List comments on a recording",
		Long:  "List all comments on a recording.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCommentsList(cmd, recordingID, limit, page, all)
		},
	}

	cmd.Flags().StringVarP(&recordingID, "on", "r", "", "Recording ID to list comments for (required)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of comments to fetch (0 = default 100)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all comments (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")
	_ = cmd.MarkFlagRequired("on")

	return cmd
}

func runCommentsList(cmd *cobra.Command, recordingID string, limit, page int, all bool) error {
	app := appctx.FromContext(cmd.Context())

	// Validate flag combinations
	if all && limit > 0 {
		return output.ErrUsage("--all and --limit are mutually exclusive")
	}
	if page > 0 && (all || limit > 0) {
		return output.ErrUsage("--page cannot be combined with --all or --limit")
	}
	if page > 1 {
		return output.ErrUsage("only --page 1 is supported; use --all to fetch everything")
	}

	// Validate user input first, before checking account
	if recordingID == "" {
		return output.ErrUsage("Recording ID required")
	}

	// Extract recording ID from URL if --on is a URL
	recordingID = extractID(recordingID)

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	recID, err := strconv.ParseInt(recordingID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid recording ID")
	}

	// Build pagination options
	opts := &basecamp.CommentListOptions{}
	if all {
		opts.Limit = -1 // SDK treats -1 as unlimited
	} else if limit > 0 {
		opts.Limit = limit
	}
	if page > 0 {
		opts.Page = page
	}

	commentsResult, err := app.Account().Comments().List(cmd.Context(), recID, opts)
	if err != nil {
		return convertSDKError(err)
	}
	comments := commentsResult.Comments

	// Build response options
	respOpts := []output.ResponseOption{
		output.WithSummary(fmt.Sprintf("%d comments on recording #%s", len(comments), recordingID)),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "add",
				Cmd:         fmt.Sprintf("basecamp comment --content <text> --on %s", recordingID),
				Description: "Add comment",
			},
			output.Breadcrumb{
				Action:      "show",
				Cmd:         "basecamp comments show <id>",
				Description: "Show comment",
			},
		),
	}

	// Add truncation notice if results may be limited
	if notice := output.TruncationNoticeWithTotal(len(comments), commentsResult.Meta.TotalCount); notice != "" {
		respOpts = append(respOpts, output.WithNotice(notice))
	}

	return app.OK(comments, respOpts...)
}

func newCommentsShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show comment details",
		Long: `Display detailed information about a comment.

You can pass either a comment ID or a Basecamp URL:
  basecamp comments show 789
  basecamp comments show https://3.basecamp.com/123/buckets/456/todos/111#__recording_789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract comment ID from URL if provided
			// Uses extractCommentWithProject to prefer CommentID from URL fragments
			commentIDStr, _ := extractCommentWithProject(args[0])

			commentID, err := strconv.ParseInt(commentIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid comment ID")
			}

			comment, err := app.Account().Comments().Get(cmd.Context(), commentID)
			if err != nil {
				return convertSDKError(err)
			}

			creatorName := ""
			if comment.Creator != nil {
				creatorName = comment.Creator.Name
			}

			return app.OK(comment,
				output.WithSummary(fmt.Sprintf("Comment #%s by %s", commentIDStr, creatorName)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("basecamp comments update %s --content <text>", commentIDStr),
						Description: "Update comment",
					},
				),
			)
		},
	}
	return cmd
}

func newCommentsUpdateCmd() *cobra.Command {
	var content string

	cmd := &cobra.Command{
		Use:   "update <id|url>",
		Short: "Update a comment",
		Long: `Update an existing comment's content.

You can pass either a comment ID or a Basecamp URL:
  basecamp comments update 789 --content "new text"
  basecamp comments update https://3.basecamp.com/123/buckets/456/todos/111#__recording_789 --content "new text"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract comment ID from URL if provided
			// Uses extractCommentWithProject to prefer CommentID from URL fragments
			commentIDStr, _ := extractCommentWithProject(args[0])

			if content == "" {
				return output.ErrUsage("--content is required")
			}

			commentID, err := strconv.ParseInt(commentIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid comment ID")
			}

			// Convert Markdown content to HTML for Basecamp's rich text fields
			req := &basecamp.UpdateCommentRequest{
				Content: richtext.MarkdownToHTML(content),
			}

			comment, err := app.Account().Comments().Update(cmd.Context(), commentID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(comment,
				output.WithSummary(fmt.Sprintf("Updated comment #%s", commentIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp comments show %s", commentIDStr),
						Description: "View comment",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&content, "content", "c", "", "New content (required)")
	_ = cmd.MarkFlagRequired("content")

	return cmd
}

// NewCommentCmd creates the comment command (shortcut for creating comments).
func NewCommentCmd() *cobra.Command {
	var content string
	var recordingIDs []string

	cmd := &cobra.Command{
		Use:   "comment",
		Short: "Add a comment to recordings",
		Long: `Add a comment to one or more Basecamp recordings (todos, messages, etc.)

Supports batch commenting on multiple recordings at once.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			// Validate user input first, before checking account
			if content == "" {
				return output.ErrUsage("Comment content required")
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// If no recording specified, try interactive resolution
			if len(recordingIDs) == 0 {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				target, err := app.Resolve().Comment(cmd.Context(), "", app.Config.ProjectID)
				if err != nil {
					return err
				}
				recordingIDs = []string{fmt.Sprintf("%d", target.RecordingID)}
			}

			// Expand comma-separated IDs and extract from URLs
			var expandedIDs []string
			for _, id := range recordingIDs {
				parts := strings.SplitSeq(id, ",")
				for p := range parts {
					p = strings.TrimSpace(p)
					if p != "" {
						expandedIDs = append(expandedIDs, extractID(p))
					}
				}
			}

			// Create comments on all recordings
			// Convert Markdown content to HTML for Basecamp's rich text fields
			req := &basecamp.CreateCommentRequest{
				Content: richtext.MarkdownToHTML(content),
			}

			var commented []string
			var commentIDs []string
			var failed []string
			var firstAPIErr error // Capture first API error for better error reporting

			for _, recordingIDStr := range expandedIDs {
				recordingID, parseErr := strconv.ParseInt(recordingIDStr, 10, 64)
				if parseErr != nil {
					failed = append(failed, recordingIDStr)
					continue
				}

				comment, createErr := app.Account().Comments().Create(cmd.Context(), recordingID, req)
				if createErr != nil {
					failed = append(failed, recordingIDStr)
					if firstAPIErr == nil {
						firstAPIErr = createErr
					}
					continue
				}

				commentIDs = append(commentIDs, fmt.Sprintf("%d", comment.ID))
				commented = append(commented, recordingIDStr)
			}

			// If all operations failed, return an error for automation
			if len(commented) == 0 && len(failed) > 0 {
				if firstAPIErr != nil {
					// Convert SDK error to preserve rate-limit hints and exit codes
					converted := convertSDKError(firstAPIErr)
					// If it's an output.Error, preserve its fields but add recording IDs to message
					var outErr *output.Error
					if errors.As(converted, &outErr) {
						return &output.Error{
							Code:       outErr.Code,
							Message:    fmt.Sprintf("Failed to comment on recordings %s: %s", strings.Join(failed, ", "), outErr.Message),
							Hint:       outErr.Hint,
							HTTPStatus: outErr.HTTPStatus,
							Retryable:  outErr.Retryable,
							Cause:      outErr,
						}
					}
					return fmt.Errorf("failed to comment on recordings %s: %w", strings.Join(failed, ", "), converted)
				}
				return output.ErrUsage(fmt.Sprintf("Failed to comment on all recordings: %s", strings.Join(failed, ", ")))
			}

			// Build result
			result := map[string]any{
				"commented_recordings": commented,
				"comment_ids":          commentIDs,
				"failed":               failed,
			}

			var summary string
			if len(failed) > 0 {
				summary = fmt.Sprintf("Added %d comment(s), %d failed: %s", len(commented), len(failed), strings.Join(failed, ", "))
			} else {
				summary = fmt.Sprintf("Added %d comment(s) to: %s", len(commented), strings.Join(commented, ", "))
			}

			return app.OK(result,
				output.WithSummary(summary),
			)
		},
	}

	cmd.Flags().StringVarP(&content, "content", "c", "", "Comment content (required)")
	cmd.Flags().StringSliceVarP(&recordingIDs, "on", "r", nil, "Recording ID(s) to comment on (required)")
	// Note: Required flags are validated manually in RunE for better error messages

	return cmd
}
