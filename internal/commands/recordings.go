package commands

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// Recording represents a Basecamp recording from the recordings endpoint.
type Recording struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title,omitempty"`
	Content   string `json:"content,omitempty"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updated_at"`
	Bucket    struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"bucket"`
}

// NewRecordingsCmd creates the recordings command for cross-project browsing.
func NewRecordingsCmd() *cobra.Command {
	var recordingType string
	var project string
	var status string
	var sortBy string
	var direction string
	var limit int

	cmd := &cobra.Command{
		Use:   "recordings [type]",
		Short: "List recordings across projects",
		Long: `List recordings across projects by type.

Provides filtered view of content across all projects.
Type is required: todos, messages, documents, comments, cards, uploads.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			// Validate type before checking account
			// Normalize both flag and positional arg values
			effectiveType := normalizeRecordingType(recordingType)
			if len(args) > 0 {
				effectiveType = normalizeRecordingType(args[0])
			}

			if effectiveType == "" {
				return output.ErrUsageHint(
					"Type required",
					"Use --type or shorthand: bcq recordings todos|messages|documents|comments|cards|uploads",
				)
			}

			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			return runRecordingsList(cmd, app, effectiveType, project, status, sortBy, direction, limit)
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Filter by project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	cmd.Flags().StringVarP(&recordingType, "type", "t", "", "Recording type (Todo, Message, Document, Comment, Kanban::Card, Upload)")
	cmd.Flags().StringVarP(&status, "status", "s", "active", "Recording status (active, trashed, archived)")
	cmd.Flags().StringVar(&sortBy, "sort", "updated_at", "Sort field (updated_at, created_at)")
	cmd.Flags().StringVar(&direction, "direction", "desc", "Sort direction (asc, desc)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Limit number of results")

	cmd.AddCommand(
		newRecordingsListCmd(&project),
		newRecordingsTrashCmd(&project),
		newRecordingsArchiveCmd(&project),
		newRecordingsRestoreCmd(&project),
		newRecordingsVisibilityCmd(&project),
	)

	return cmd
}

func normalizeRecordingType(input string) string {
	typeMap := map[string]string{
		"todos":     "Todo",
		"todo":      "Todo",
		"messages":  "Message",
		"message":   "Message",
		"documents": "Document",
		"document":  "Document",
		"doc":       "Document",
		"comments":  "Comment",
		"comment":   "Comment",
		"cards":     "Kanban::Card",
		"card":      "Kanban::Card",
		"uploads":   "Upload",
		"upload":    "Upload",
	}

	if normalized, ok := typeMap[input]; ok {
		return normalized
	}
	return input
}

func newRecordingsListCmd(project *string) *cobra.Command {
	var recordingType string
	var status string
	var sortBy string
	var direction string
	var limit int

	cmd := &cobra.Command{
		Use:   "list [type]",
		Short: "List recordings by type",
		Long:  "List all recordings of a specific type across projects.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			// Validate type before checking account
			// Normalize both flag and positional arg values
			effectiveType := normalizeRecordingType(recordingType)
			if len(args) > 0 {
				effectiveType = normalizeRecordingType(args[0])
			}

			if effectiveType == "" {
				return output.ErrUsageHint(
					"Type required",
					"Use --type or shorthand: bcq recordings list todos|messages|documents|comments|cards|uploads",
				)
			}

			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			return runRecordingsList(cmd, app, effectiveType, *project, status, sortBy, direction, limit)
		},
	}

	cmd.Flags().StringVarP(&recordingType, "type", "t", "", "Recording type")
	cmd.Flags().StringVarP(&status, "status", "s", "active", "Recording status (active, trashed, archived)")
	cmd.Flags().StringVar(&sortBy, "sort", "updated_at", "Sort field")
	cmd.Flags().StringVar(&direction, "direction", "desc", "Sort direction (asc, desc)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Limit number of results")

	return cmd
}

func runRecordingsList(cmd *cobra.Command, app *appctx.App, recordingType, project, status, sortBy, direction string, limit int) error {
	// Build query string
	params := url.Values{}
	params.Set("type", recordingType)
	params.Set("status", status)
	params.Set("sort", sortBy)
	params.Set("direction", direction)

	if project != "" {
		resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), project)
		if err != nil {
			return err
		}
		params.Set("bucket", resolvedProjectID)
	}

	path := fmt.Sprintf("/projects/recordings.json?%s", params.Encode())
	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var recordings []json.RawMessage
	if err := resp.UnmarshalData(&recordings); err != nil {
		return fmt.Errorf("failed to parse recordings: %w", err)
	}

	// Apply client-side limit if specified
	if limit > 0 && len(recordings) > limit {
		recordings = recordings[:limit]
	}

	summary := fmt.Sprintf("%d %ss", len(recordings), recordingType)

	return app.Output.OK(recordings,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         "bcq show <id> --project <bucket.id>",
				Description: "Show recording (use bucket.id from result)",
			},
		),
	)
}

func newRecordingsTrashCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "trash <id>",
		Aliases: []string{"trashed"},
		Short:   "Move a recording to trash",
		Long:    "Move a recording to the trash.",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}
			return runRecordingsStatus(cmd, app, args[0], *project, "trashed")
		},
	}
	return cmd
}

func newRecordingsArchiveCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "archive <id>",
		Aliases: []string{"archived"},
		Short:   "Archive a recording",
		Long:    "Archive a recording to remove it from active view.",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}
			return runRecordingsStatus(cmd, app, args[0], *project, "archived")
		},
	}
	return cmd
}

func newRecordingsRestoreCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "restore <id>",
		Aliases: []string{"active"},
		Short:   "Restore a recording",
		Long:    "Restore a recording from trash or archive to active status.",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}
			return runRecordingsStatus(cmd, app, args[0], *project, "active")
		},
	}
	return cmd
}

func runRecordingsStatus(cmd *cobra.Command, app *appctx.App, recordingID, project, newStatus string) error {
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

	path := fmt.Sprintf("/buckets/%s/recordings/%s/status/%s.json", resolvedProjectID, recordingID, newStatus)
	resp, err := app.API.Put(cmd.Context(), path, map[string]string{})
	if err != nil {
		return err
	}

	// Handle 204 No Content
	var data any = map[string]any{}
	if len(resp.Data) > 0 {
		data = json.RawMessage(resp.Data)
	}

	var statusMsg string
	switch newStatus {
	case "trashed":
		statusMsg = "Trashed"
	case "archived":
		statusMsg = "Archived"
	case "active":
		statusMsg = "Restored"
	default:
		statusMsg = fmt.Sprintf("Changed to %s", newStatus)
	}

	summary := fmt.Sprintf("%s recording #%s", statusMsg, recordingID)

	return app.Output.OK(data,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("bcq show %s --in %s", recordingID, resolvedProjectID),
				Description: "View recording",
			},
		),
	)
}

func newRecordingsVisibilityCmd(project *string) *cobra.Command {
	var visible bool
	var hidden bool

	cmd := &cobra.Command{
		Use:     "visibility <id>",
		Aliases: []string{"client-visibility"},
		Short:   "Set client visibility",
		Long:    "Set whether a recording is visible to clients.",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			recordingID := args[0]

			// Determine visibility
			var isVisible bool
			if visible && hidden {
				return output.ErrUsage("Cannot specify both --visible and --hidden")
			}
			if !visible && !hidden {
				return output.ErrUsage("Must specify --visible or --hidden")
			}
			isVisible = visible

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

			body := map[string]bool{
				"visible_to_clients": isVisible,
			}

			path := fmt.Sprintf("/buckets/%s/recordings/%s/client_visibility.json", resolvedProjectID, recordingID)
			resp, err := app.API.Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			// Handle 204 No Content
			var data any = map[string]any{}
			if len(resp.Data) > 0 {
				data = json.RawMessage(resp.Data)
			}

			var summary string
			if isVisible {
				summary = fmt.Sprintf("Recording #%s now visible to clients", recordingID)
			} else {
				summary = fmt.Sprintf("Recording #%s now hidden from clients", recordingID)
			}

			return app.Output.OK(data,
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq show %s --in %s", recordingID, resolvedProjectID),
						Description: "View recording",
					},
				),
			)
		},
	}

	cmd.Flags().BoolVar(&visible, "visible", false, "Make visible to clients")
	cmd.Flags().BoolVar(&visible, "show", false, "Make visible to clients (alias)")
	cmd.Flags().BoolVar(&hidden, "hidden", false, "Hide from clients")
	cmd.Flags().BoolVar(&hidden, "hide", false, "Hide from clients (alias)")

	return cmd
}
