package commands

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// NewCommentsCmd creates the comments command group (list/show/update).
func NewCommentsCmd() *cobra.Command {
	var project string
	var recordingID string
	var limit, page int
	var all bool

	cmd := &cobra.Command{
		Use:   "comments",
		Short: "List and manage comments",
		Long:  "List, show, and update comments on recordings.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Default to list when called without subcommand
			return runCommentsList(cmd, project, recordingID, limit, page, all)
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.Flags().StringVarP(&recordingID, "on", "r", "", "Recording ID to list comments for")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of comments to fetch (0 = default 100)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all comments (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Disable pagination and return first page only")

	cmd.AddCommand(
		newCommentsListCmd(&project),
		newCommentsShowCmd(&project),
		newCommentsUpdateCmd(&project),
	)

	return cmd
}

func newCommentsListCmd(project *string) *cobra.Command {
	var recordingID string
	var limit, page int
	var all bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List comments on a recording",
		Long:  "List all comments on a recording.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCommentsList(cmd, *project, recordingID, limit, page, all)
		},
	}

	cmd.Flags().StringVarP(&recordingID, "on", "r", "", "Recording ID to list comments for (required)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of comments to fetch (0 = default 100)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all comments (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Disable pagination and return first page only")
	_ = cmd.MarkFlagRequired("on")

	return cmd
}

func runCommentsList(cmd *cobra.Command, project, recordingID string, limit, page int, all bool) error {
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

	// Extract recording ID and project from URL if --on is a URL
	var urlProjectID string
	recordingID, urlProjectID = extractWithProject(recordingID)

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Resolve project - URL > flag > config, with interactive fallback
	projectID := project
	if projectID == "" && urlProjectID != "" {
		projectID = urlProjectID
	}
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return err
		}
		projectID = app.Config.ProjectID
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
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

	commentsResult, err := app.Account().Comments().List(cmd.Context(), bucketID, recID, opts)
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
				Cmd:         fmt.Sprintf("bcq comment --content <text> --on %s --in %s", recordingID, resolvedProjectID),
				Description: "Add comment",
			},
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("bcq comments show <id> --in %s", resolvedProjectID),
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

func newCommentsShowCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show comment details",
		Long: `Display detailed information about a comment.

You can pass either a comment ID or a Basecamp URL:
  bcq comments show 789 --in my-project
  bcq comments show https://3.basecamp.com/123/buckets/456/todos/111#__recording_789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract comment ID and project from URL if provided
			// Uses extractCommentWithProject to prefer CommentID from URL fragments
			commentIDStr, urlProjectID := extractCommentWithProject(args[0])

			// Resolve project - use URL > flag > config, with interactive fallback
			projectID := *project
			if projectID == "" && urlProjectID != "" {
				projectID = urlProjectID
			}
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			commentID, err := strconv.ParseInt(commentIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid comment ID")
			}

			comment, err := app.Account().Comments().Get(cmd.Context(), bucketID, commentID)
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
						Cmd:         fmt.Sprintf("bcq comments update %s --content <text> --in %s", commentIDStr, resolvedProjectID),
						Description: "Update comment",
					},
				),
			)
		},
	}
	return cmd
}

func newCommentsUpdateCmd(project *string) *cobra.Command {
	var content string

	cmd := &cobra.Command{
		Use:   "update <id|url>",
		Short: "Update a comment",
		Long: `Update an existing comment's content.

You can pass either a comment ID or a Basecamp URL:
  bcq comments update 789 --content "new text" --in my-project
  bcq comments update https://3.basecamp.com/123/buckets/456/todos/111#__recording_789 --content "new text"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract comment ID and project from URL if provided
			// Uses extractCommentWithProject to prefer CommentID from URL fragments
			commentIDStr, urlProjectID := extractCommentWithProject(args[0])

			if content == "" {
				return output.ErrUsage("--content is required")
			}

			// Resolve project - use URL > flag > config, with interactive fallback
			projectID := *project
			if projectID == "" && urlProjectID != "" {
				projectID = urlProjectID
			}
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			commentID, err := strconv.ParseInt(commentIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid comment ID")
			}

			req := &basecamp.UpdateCommentRequest{
				Content: content,
			}

			comment, err := app.Account().Comments().Update(cmd.Context(), bucketID, commentID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(comment,
				output.WithSummary(fmt.Sprintf("Updated comment #%s", commentIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq comments show %s --in %s", commentIDStr, resolvedProjectID),
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
	var project string

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

			// Extract URLs from --on flags before project resolution
			var urlProjectID string
			if len(recordingIDs) > 0 {
				// Check first recording for URL project
				_, urlProjectID = extractWithProject(recordingIDs[0])
			}

			// Resolve project - URL > flag > config, with interactive fallback
			projectID := project
			if projectID == "" && urlProjectID != "" {
				projectID = urlProjectID
			}
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			// If no recording specified, try interactive resolution
			if len(recordingIDs) == 0 {
				target, err := app.Resolve().Comment(cmd.Context(), "", resolvedProjectID)
				if err != nil {
					return err
				}
				recordingIDs = []string{fmt.Sprintf("%d", target.RecordingID)}
			}

			// Expand comma-separated IDs and extract from URLs
			var expandedIDs []string
			for _, id := range recordingIDs {
				parts := strings.Split(id, ",")
				for _, p := range parts {
					p = strings.TrimSpace(p)
					if p != "" {
						expandedIDs = append(expandedIDs, extractID(p))
					}
				}
			}

			// Create comments on all recordings
			req := &basecamp.CreateCommentRequest{
				Content: content,
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

				comment, createErr := app.Account().Comments().Create(cmd.Context(), bucketID, recordingID, req)
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
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("bcq todos --in %s", resolvedProjectID),
						Description: "List todos",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&content, "content", "c", "", "Comment content (required)")
	cmd.Flags().StringSliceVarP(&recordingIDs, "on", "r", nil, "Recording ID(s) to comment on (required)")
	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	// Note: Required flags are validated manually in RunE for better error messages

	return cmd
}
