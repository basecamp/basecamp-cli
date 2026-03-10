package commands

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewRecordingsCmd creates the recordings command for cross-project browsing.
func NewRecordingsCmd() *cobra.Command {
	var recordingType string
	var project string
	var status string
	var sortBy string
	var direction string
	var limit int
	var page int
	var all bool
	var assignee string

	cmd := &cobra.Command{
		Use:   "recordings [type]",
		Short: "Browse content across projects",
		Long: `Browse content across projects by type.

Provides filtered view of content across all projects.
Type is required: todos, messages, documents, comments, cards, uploads.`,
		Annotations: map[string]string{"agent_notes": "Does NOT include assignee data — cannot filter by person\nFor assigned todos use: basecamp reports assigned --json\nDefault status is active — use --status archived or --status trashed for other states\nTypes: todos, messages, documents, comments, cards, uploads"},
		Args:        cobra.MaximumNArgs(1),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked bare (no type given) — skip account check
			effectiveType := normalizeRecordingType(recordingType)
			if len(args) > 0 {
				effectiveType = normalizeRecordingType(args[0])
			}
			if effectiveType == "" && !cmd.Flags().Changed("assignee") {
				return nil // RunE will show help
			}

			if cmd.Flags().Changed("assignee") {
				return recordingsAssigneeRedirect(assignee, project)
			}
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}
			return ensureAccount(cmd, app)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			// Normalize both flag and positional arg values
			effectiveType := normalizeRecordingType(recordingType)
			if len(args) > 0 {
				effectiveType = normalizeRecordingType(args[0])
			}

			if effectiveType == "" {
				return missingArg(cmd, "<type>")
			}

			return runRecordingsList(cmd, app, effectiveType, project, status, sortBy, direction, limit, page, all)
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Filter by project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	cmd.Flags().StringVarP(&recordingType, "type", "t", "", "Content type (todo, message, document, comment, card, upload)")
	cmd.Flags().StringVarP(&status, "status", "s", "active", "Status filter (active, trashed, archived)")
	cmd.Flags().StringVar(&sortBy, "sort", "updated_at", "Sort field (updated_at, created_at)")
	cmd.Flags().StringVar(&direction, "direction", "desc", "Sort direction (asc, desc)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of items to fetch (0 = default 100)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all items (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")
	cmd.Flags().StringVar(&assignee, "assignee", "", "Not supported — use reports assigned instead")
	_ = cmd.Flags().MarkHidden("assignee")

	cmd.AddCommand(
		newRecordingsListCmd(&project),
		newRecordingsTrashCmd(),
		newRecordingsArchiveCmd(),
		newRecordingsRestoreCmd(),
		newRecordingsVisibilityCmd(),
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

func recordingsAssigneeRedirect(assignee, project string) error {
	if project != "" {
		return output.ErrUsageHint(
			"recordings does not support --assignee (no assignee data available)",
			fmt.Sprintf("Use: basecamp todos --assignee %q --in %q --json", assignee, project),
		)
	}
	if assignee != "" && !strings.EqualFold(assignee, "me") {
		return output.ErrUsageHint(
			"recordings does not support --assignee (no assignee data available)",
			fmt.Sprintf("Use: basecamp reports assigned %q --json", assignee),
		)
	}
	return output.ErrUsageHint(
		"recordings does not support --assignee (no assignee data available)",
		"Use: basecamp reports assigned --json",
	)
}

func newRecordingsListCmd(project *string) *cobra.Command {
	var recordingType string
	var status string
	var sortBy string
	var direction string
	var limit int
	var page int
	var all bool
	var assignee string

	cmd := &cobra.Command{
		Use:   "list [type]",
		Short: "List content by type",
		Long:  "List all items of a specific type across projects.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("assignee") {
				return recordingsAssigneeRedirect(assignee, *project)
			}

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
					"Use --type or shorthand: basecamp recordings list todos|messages|documents|comments|cards|uploads",
				)
			}

			return runRecordingsList(cmd, app, effectiveType, *project, status, sortBy, direction, limit, page, all)
		},
	}

	cmd.Flags().StringVarP(&recordingType, "type", "t", "", "Content type")
	cmd.Flags().StringVarP(&status, "status", "s", "active", "Status filter (active, trashed, archived)")
	cmd.Flags().StringVar(&sortBy, "sort", "updated_at", "Sort field")
	cmd.Flags().StringVar(&direction, "direction", "desc", "Sort direction (asc, desc)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of items to fetch (0 = default 100)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all items (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")
	cmd.Flags().StringVar(&assignee, "assignee", "", "Not supported — use reports assigned instead")
	_ = cmd.Flags().MarkHidden("assignee")

	return cmd
}

func runRecordingsList(cmd *cobra.Command, app *appctx.App, recordingType, project, status, sortBy, direction string, limit, page int, all bool) error {
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

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Build options
	opts := &basecamp.RecordingsListOptions{
		Status:    status,
		Sort:      sortBy,
		Direction: direction,
	}

	// Apply pagination options
	if all {
		opts.Limit = -1 // SDK treats -1 as "fetch all"
	} else if limit > 0 {
		opts.Limit = limit
	}
	if page > 0 {
		opts.Page = page
	}

	if project != "" {
		resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), project)
		if err != nil {
			return err
		}
		projectID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
		if err != nil {
			return output.ErrUsage("Invalid project ID")
		}
		opts.Bucket = []int64{projectID}
	}

	recordingsResult, err := app.Account().Recordings().List(cmd.Context(), basecamp.RecordingType(recordingType), opts)
	if err != nil {
		return convertSDKError(err)
	}
	recordings := recordingsResult.Recordings

	displayType := recordingDisplayName(recordingType)
	summary := fmt.Sprintf("%d %s", len(recordings), displayType)

	respOpts := []output.ResponseOption{
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         "basecamp show <id> --project <project_id>",
				Description: "Show item details",
			},
		),
	}

	// Add truncation notice if results may be limited
	if notice := output.TruncationNoticeWithTotal(len(recordings), recordingsResult.Meta.TotalCount); notice != "" {
		respOpts = append(respOpts, output.WithNotice(notice))
	}

	return app.OK(recordings, respOpts...)
}

func newRecordingsTrashCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "trash <id|url>",
		Aliases: []string{"trashed"},
		Short:   "Move an item to trash",
		Long: `Move an item to the trash.

You can pass either an ID or a Basecamp URL:
  basecamp recordings trash 789
  basecamp recordings trash https://3.basecamp.com/123/buckets/456/recordings/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			return runRecordingsStatus(cmd, app, args[0], "trashed")
		},
	}
	return cmd
}

func newRecordingsArchiveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "archive <id|url>",
		Aliases: []string{"archived"},
		Short:   "Archive an item",
		Long: `Archive an item to remove it from active view.

You can pass either an ID or a Basecamp URL:
  basecamp recordings archive 789
  basecamp recordings archive https://3.basecamp.com/123/buckets/456/recordings/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			return runRecordingsStatus(cmd, app, args[0], "archived")
		},
	}
	return cmd
}

func newRecordingsRestoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "restore <id|url>",
		Aliases: []string{"active"},
		Short:   "Restore an item",
		Long: `Restore an item from trash or archive to active status.

You can pass either an ID or a Basecamp URL:
  basecamp recordings restore 789
  basecamp recordings restore https://3.basecamp.com/123/buckets/456/recordings/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			return runRecordingsStatus(cmd, app, args[0], "active")
		},
	}
	return cmd
}

func runRecordingsStatus(cmd *cobra.Command, app *appctx.App, recordingIDStr, newStatus string) error {
	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Extract ID from URL if provided
	recordingIDStr = extractID(recordingIDStr)

	// Parse recording ID
	recordingID, err := strconv.ParseInt(recordingIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid ID")
	}

	// Call appropriate SDK method based on status
	switch newStatus {
	case "trashed":
		err = app.Account().Recordings().Trash(cmd.Context(), recordingID)
	case "archived":
		err = app.Account().Recordings().Archive(cmd.Context(), recordingID)
	case "active":
		// Unarchive() sets status to active via PUT /status/active.json
		// This works for both archived AND trashed recordings
		err = app.Account().Recordings().Unarchive(cmd.Context(), recordingID)
	default:
		return output.ErrUsage(fmt.Sprintf("Unknown status: %s", newStatus))
	}

	if err != nil {
		return convertSDKError(err)
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

	summary := fmt.Sprintf("%s #%s", statusMsg, recordingIDStr)

	return app.OK(map[string]any{"id": recordingID, "status": newStatus},
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("basecamp show %s", recordingIDStr),
				Description: "View item",
			},
		),
	)
}

func newRecordingsVisibilityCmd() *cobra.Command {
	var visible bool
	var hidden bool

	cmd := &cobra.Command{
		Use:     "visibility <id|url>",
		Aliases: []string{"client-visibility"},
		Short:   "Set client visibility",
		Long: `Set whether an item is visible to clients.

You can pass either an ID or a Basecamp URL:
  basecamp recordings visibility 789 --visible
  basecamp recordings visibility https://3.basecamp.com/123/buckets/456/recordings/789 --visible`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			recordingIDStr := extractID(args[0])

			recordingID, err := strconv.ParseInt(recordingIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid ID")
			}

			// Determine visibility
			var isVisible bool
			if visible && hidden {
				return output.ErrUsage("Cannot specify both --visible and --hidden")
			}
			if !visible && !hidden {
				return output.ErrUsage("Must specify --visible or --hidden")
			}
			isVisible = visible

			recording, err := app.Account().Recordings().SetClientVisibility(cmd.Context(), recordingID, isVisible)
			if err != nil {
				return convertSDKError(err)
			}

			var summary string
			if isVisible {
				summary = fmt.Sprintf("#%s now visible to clients", recordingIDStr)
			} else {
				summary = fmt.Sprintf("#%s now hidden from clients", recordingIDStr)
			}

			return app.OK(recording,
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp show %s", recordingIDStr),
						Description: "View item",
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

// recordingDisplayName maps SDK recording type names to human-friendly display names.
func recordingDisplayName(sdkType string) string {
	switch sdkType {
	case "Kanban::Card":
		return "cards"
	case "Todolist::Todo":
		return "todos"
	case "Inbox::Forward":
		return "forwards"
	case "Schedule::Entry":
		return "schedule entries"
	case "Question::Answer":
		return "check-in answers"
	case "Vault::Document":
		return "documents"
	default:
		return sdkType + "s"
	}
}
