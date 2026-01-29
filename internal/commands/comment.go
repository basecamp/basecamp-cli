package commands

import (
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

	cmd := &cobra.Command{
		Use:   "comments",
		Short: "List and manage comments",
		Long:  "List, show, and update comments on recordings.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Default to list when called without subcommand
			return runCommentsList(cmd, project, recordingID)
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.Flags().StringVarP(&recordingID, "on", "r", "", "Recording ID to list comments for")

	cmd.AddCommand(
		newCommentsListCmd(&project),
		newCommentsShowCmd(&project),
		newCommentsUpdateCmd(&project),
	)

	return cmd
}

func newCommentsListCmd(project *string) *cobra.Command {
	var recordingID string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List comments on a recording",
		Long:  "List all comments on a recording.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCommentsList(cmd, *project, recordingID)
		},
	}

	cmd.Flags().StringVarP(&recordingID, "on", "r", "", "Recording ID to list comments for (required)")
	_ = cmd.MarkFlagRequired("on")

	return cmd
}

func runCommentsList(cmd *cobra.Command, project, recordingID string) error {
	app := appctx.FromContext(cmd.Context())

	// Validate user input first, before checking account
	if recordingID == "" {
		return output.ErrUsage("Recording ID required")
	}

	if err := app.RequireAccount(); err != nil {
		return err
	}

	// Resolve project
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		return output.ErrUsage("--project is required")
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

	comments, err := app.Account().Comments().List(cmd.Context(), bucketID, recID)
	if err != nil {
		return convertSDKError(err)
	}

	return app.OK(comments,
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
	)
}

func newCommentsShowCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show comment details",
		Long:  "Display detailed information about a comment.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.RequireAccount(); err != nil {
				return err
			}

			commentIDStr := args[0]

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
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
		Use:   "update <id>",
		Short: "Update a comment",
		Long:  "Update an existing comment's content.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.RequireAccount(); err != nil {
				return err
			}

			commentIDStr := args[0]

			if content == "" {
				return output.ErrUsage("--content is required")
			}

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
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

			// Resolve project
			projectID := project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
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

			// Expand comma-separated IDs
			var expandedIDs []string
			for _, id := range recordingIDs {
				parts := strings.Split(id, ",")
				for _, p := range parts {
					p = strings.TrimSpace(p)
					if p != "" {
						expandedIDs = append(expandedIDs, p)
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
