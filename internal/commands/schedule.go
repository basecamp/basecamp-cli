package commands

import (
	"cmp"
	"context"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/urlarg"
)

// NewScheduleCmd creates the schedule command for managing schedules.
func NewScheduleCmd() *cobra.Command {
	var project string
	var scheduleID string

	cmd := &cobra.Command{
		Use:   "schedule [action]",
		Short: "Manage schedules and entries",
		Long: `Manage project schedules and schedule entries.

Use 'basecamp schedule info' to view the project schedule.
Use 'basecamp schedule entries' to list schedule entries.
Use 'basecamp schedule create' to create new entries.

For a cross-project view of your upcoming schedule, use 'basecamp reports schedule'.`,
		Annotations: map[string]string{"agent_notes": "Each project has one schedule\nRecurring events: use --date on show to get a specific occurrence\nschedule settings --include-due makes todo/card due dates appear on the schedule\nNatural dates work for --starts-at: tomorrow, next monday"},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.PersistentFlags().StringVarP(&scheduleID, "schedule", "s", "", "Schedule ID (auto-detected)")

	cmd.AddCommand(
		newScheduleShowCmd(&project, &scheduleID),
		newScheduleEntriesCmd(&project, &scheduleID),
		newScheduleEntryShowCmd(),
		newScheduleCreateCmd(&project, &scheduleID),
		newScheduleUpdateCmd(&project),
		newScheduleSettingsCmd(&project, &scheduleID),
		newFilingCmd(scheduleEntryFamily(), basecamp.FilingMove, scheduleMoveHelp, false),
		newFilingCmd(scheduleEntryFamily(), basecamp.FilingCopy, scheduleCopyHelp, false),
	)

	return cmd
}

func newScheduleShowCmd(project, scheduleID *string) *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Show schedule info",
		Long:  "Display project schedule information.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			// Resolve account (enables interactive prompt if needed)
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			return runScheduleShow(cmd, app, *project, *scheduleID)
		},
	}
}

func runScheduleShow(cmd *cobra.Command, app *appctx.App, project, scheduleID string) error {
	// Resolve project from CLI flags and config, with interactive fallback
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}

	// If no project specified, try interactive resolution
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

	// Get schedule ID from dock if not specified
	if scheduleID == "" {
		scheduleID, err = getScheduleID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	}

	scheduleIDInt, _ := strconv.ParseInt(scheduleID, 10, 64)

	schedule, err := app.Account().Schedules().Get(cmd.Context(), scheduleIDInt)
	if err != nil {
		return convertSDKError(err)
	}

	summary := fmt.Sprintf("%d entries (include due assignments: %t)", schedule.EntriesCount, schedule.IncludeDueAssignments)

	return app.OK(schedule,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "entries",
				Cmd:         fmt.Sprintf("basecamp schedule entries --project %s", resolvedProjectID),
				Description: "View schedule entries",
			},
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("basecamp schedule create \"Event\" --starts-at <datetime> --ends-at <datetime> --project %s", resolvedProjectID),
				Description: "Create entry",
			},
		),
	)
}

func newScheduleEntriesCmd(project, scheduleID *string) *cobra.Command {
	var status string
	var limit int
	var page int
	var all bool
	var sortField string
	var reverse bool

	cmd := &cobra.Command{
		Use:   "entries",
		Short: "List schedule entries",
		Long:  "List all entries in a project schedule.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			// Resolve account (enables interactive prompt if needed)
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			return runScheduleEntries(cmd, app, *project, *scheduleID, status, limit, page, all, sortField, reverse)
		},
	}

	cmd.Flags().StringVar(&status, "status", "active", "Filter by status (active, archived, trashed)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of entries to fetch (0 = all)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all entries (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")
	cmd.Flags().StringVar(&sortField, "sort", "", "Sort by field (title, created, updated)")
	cmd.Flags().BoolVar(&reverse, "reverse", false, "Reverse sort order")

	return cmd
}

func runScheduleEntries(cmd *cobra.Command, app *appctx.App, project, scheduleID, status string, limit, page int, all bool, sortField string, reverse bool) error {
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
	if sortField != "" {
		if err := validateSortField(sortField, []string{"title", "created", "updated"}); err != nil {
			return err
		}
	}

	// Resolve project from CLI flags and config, with interactive fallback
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}

	// If no project specified, try interactive resolution
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

	// Get schedule ID from dock if not specified
	if scheduleID == "" {
		scheduleID, err = getScheduleID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	}

	scheduleIDInt, _ := strconv.ParseInt(scheduleID, 10, 64)

	// Build pagination options
	opts := &basecamp.ScheduleEntryListOptions{}
	if all {
		opts.Limit = -1 // SDK treats -1 as "fetch all"
	} else if limit > 0 {
		opts.Limit = limit
	}
	if page > 0 {
		opts.Page = page
	}
	if status != "" {
		opts.Status = status
	}

	entriesResult, err := app.Account().Schedules().ListEntries(cmd.Context(), scheduleIDInt, opts)
	if err != nil {
		return convertSDKError(err)
	}
	entries := entriesResult.Entries

	if sortField != "" {
		sortScheduleEntries(entries, sortField, reverse)
	}

	summary := fmt.Sprintf("%d schedule entries", len(entries))

	return app.OK(entries,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("basecamp schedule show <id> --project %s", resolvedProjectID),
				Description: "View entry details",
			},
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("basecamp schedule create \"Event\" --starts-at <datetime> --ends-at <datetime> --project %s", resolvedProjectID),
				Description: "Create entry",
			},
		),
	)
}

func newScheduleEntryShowCmd() *cobra.Command {
	var occurrenceDate string

	cmd := &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show schedule entry",
		Long: `Display details of a schedule entry.

You can pass either an entry ID or a Basecamp URL:
  basecamp schedule show 789
  basecamp schedule show https://3.basecamp.com/123/buckets/456/schedule_entries/789`,
		Args: cobra.ExactArgs(1),
	}

	cf := addCommentFlags(cmd, false)

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		app := appctx.FromContext(cmd.Context())
		if err := ensureAccount(cmd, app); err != nil {
			return err
		}
		return runScheduleEntryShow(cmd, app, args[0], occurrenceDate, cf)
	}

	cmd.Flags().StringVar(&occurrenceDate, "date", "", "Access specific occurrence of recurring entry (YYYYMMDD)")
	cmd.Flags().StringVar(&occurrenceDate, "occurrence", "", "Access specific occurrence (alias for --date)")

	return cmd
}

func runScheduleEntryShow(cmd *cobra.Command, app *appctx.App, entryID, occurrenceDate string, cf *commentFlags) error {
	// Extract ID and occurrence date from URL if provided. The entry is read
	// by its account-wide ID, so no project is needed.
	if parsed := urlarg.Parse(entryID); parsed != nil {
		if parsed.RecordingID == "" {
			return output.ErrUsageHint(
				"This URL does not point to a schedule entry",
				"Paste a schedule entry URL or pass an entry ID",
			)
		}
		entryID = parsed.RecordingID
		if occurrenceDate == "" {
			occurrenceDate = parsed.OccurrenceDate
		}
	}

	entryIDInt, _ := strconv.ParseInt(entryID, 10, 64)

	// Use SDK method for specific occurrences of recurring entries
	if occurrenceDate != "" {
		entry, err := app.Account().Schedules().GetEntryOccurrence(cmd.Context(), entryIDInt, occurrenceDate)
		if err != nil {
			return convertSDKError(err)
		}

		title := entry.Summary
		if title == "" {
			title = entry.Title
		}
		if title == "" {
			title = "Entry"
		}

		// The occurrence is a recording of its own, with its own comments.
		// The API cannot update it, so there is no update breadcrumb.
		occurrenceID := strconv.FormatInt(entry.ID, 10)
		enrichment := fetchCommentsForRecording(cmd.Context(), app, occurrenceID, cf)
		data, commentOpts := enrichment.apply(entry, "")
		summary := fmt.Sprintf("%s: %s -> %s", title, entry.StartsAt.Format("2006-01-02 15:04"), entry.EndsAt.Format("2006-01-02 15:04"))

		opts := make([]output.ResponseOption, 0, 2+len(commentOpts))
		opts = append(opts,
			output.WithSummary(summary),
			output.WithBreadcrumbs(
				output.Breadcrumb{
					Action:      "comment",
					Cmd:         fmt.Sprintf("basecamp comments create %s <text>", occurrenceID),
					Description: "Comment on this occurrence",
				},
				output.Breadcrumb{
					Action:      "entries",
					Cmd:         fmt.Sprintf("basecamp schedule entries --project %s", recordProject(entry.Bucket)),
					Description: "View all entries",
				},
			),
		)
		opts = append(opts, commentOpts...)

		return app.OK(data, opts...)
	}

	entry, err := app.Account().Schedules().GetEntry(cmd.Context(), entryIDInt)
	if err != nil {
		return convertSDKError(err)
	}
	// A moved event's old id redirects to the event at its new id, and only
	// the new id takes writes. A recurring event reads as an occurrence whose
	// parent is the event.
	entryID = strconv.FormatInt(entry.ID, 10)
	if entry.Type == occurrenceType && entry.Parent != nil {
		entryID = strconv.FormatInt(entry.Parent.ID, 10)
	}

	title := entry.Summary
	if title == "" {
		title = entry.Title
	}
	if title == "" {
		title = "Entry"
	}

	enrichment := fetchCommentsForRecording(cmd.Context(), app, entryID, cf)
	data, commentOpts := enrichment.apply(entry, "")
	summary := fmt.Sprintf("%s: %s -> %s", title, entry.StartsAt.Format("2006-01-02 15:04"), entry.EndsAt.Format("2006-01-02 15:04"))

	opts := make([]output.ResponseOption, 0, 2+len(commentOpts))
	opts = append(opts,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "update",
				Cmd:         fmt.Sprintf("basecamp schedule update %s --summary \"...\" --project %s", entryID, recordProject(entry.Bucket)),
				Description: "Update entry",
			},
			output.Breadcrumb{
				Action:      "entries",
				Cmd:         fmt.Sprintf("basecamp schedule entries --project %s", recordProject(entry.Bucket)),
				Description: "View all entries",
			},
		),
	)
	opts = append(opts, commentOpts...)

	return app.OK(data, opts...)
}

func newScheduleCreateCmd(project, scheduleID *string) *cobra.Command {
	var summary string
	var startsAt string
	var endsAt string
	var description string
	var allDay bool
	var notify bool
	var participants string
	var subscribe string
	var noSubscribe bool
	var attachFiles []string
	var visibleToClients bool

	cmd := &cobra.Command{
		Use:   "create <summary>",
		Short: "Create a schedule entry",
		Long:  "Create a new entry in the project schedule.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			entrySummary := summary
			if len(args) > 0 {
				entrySummary = args[0]
			}

			// Show help when invoked with no arguments
			if entrySummary == "" {
				return missingArg(cmd, "<summary>")
			}

			if startsAt == "" {
				return output.ErrUsage("--starts-at required (ISO 8601 datetime)")
			}
			if endsAt == "" {
				return output.ErrUsage("--ends-at required (ISO 8601 datetime)")
			}
			if err := validateScheduleTimestamp("starts-at", startsAt); err != nil {
				return err
			}
			if err := validateScheduleTimestamp("ends-at", endsAt); err != nil {
				return err
			}

			if err := rejectSubscribeConflict(cmd.Flags().Changed("subscribe"), noSubscribe, subscribe); err != nil {
				return err
			}
			if err := requireNumericID(*scheduleID, "schedule ID"); err != nil {
				return err
			}

			// Attachment paths are readable or not regardless of the body, so
			// check them before the pipe is drained.
			if err := validateAttachPaths(attachFiles); err != nil {
				return err
			}

			// Local validation, then "-", then account: a bad stdin gets the
			// stdin error rather than "--account is required".
			description, err := resolveContentValue(cmd, description, -1, "--description")
			if err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			return runScheduleCreate(cmd, app, *project, *scheduleID, entrySummary, startsAt, endsAt, description, allDay, notify, visibleToClients, participants, subscribe, noSubscribe, attachFiles)
		},
	}

	cmd.Flags().StringVar(&summary, "summary", "", "Event title/summary")
	cmd.Flags().StringVar(&summary, "title", "", "Event title (alias for --summary)")
	cmd.Flags().StringVar(&startsAt, "starts-at", "", "Start time (ISO 8601)")
	cmd.Flags().StringVar(&startsAt, "start", "", "Start time (alias)")
	cmd.Flags().StringVar(&endsAt, "ends-at", "", "End time (ISO 8601)")
	cmd.Flags().StringVar(&endsAt, "end", "", "End time (alias)")
	cmd.Flags().StringVar(&description, "description", "", "Detailed description; use - to read from stdin")
	cmd.Flags().StringVar(&description, "desc", "", "Description (alias)")
	cmd.Flags().BoolVar(&allDay, "all-day", false, "Mark as all-day event")
	cmd.Flags().BoolVar(&notify, "notify", false, "Notify participants")
	cmd.Flags().StringVar(&participants, "participants", "", "Comma-separated person IDs")
	cmd.Flags().StringVar(&participants, "people", "", "Person IDs (alias)")
	cmd.Flags().StringVar(&subscribe, "subscribe", "", "Subscribe specific people (comma-separated names, emails, IDs, or \"me\")")

	allowDash(cmd, "flag:description", "flag:desc")
	cmd.Flags().BoolVar(&noSubscribe, "no-subscribe", false, "Don't subscribe anyone else (silent, no notifications)")
	cmd.Flags().StringArrayVar(&attachFiles, "attach", nil, "Attach file (repeatable)")
	cmd.Flags().BoolVar(&visibleToClients, "visible-to-clients", false, "Make the schedule entry visible to clients on the project (omit for the server default; client-authenticated callers always post client-visible)")

	addRichTextFormatFlag(cmd)
	return cmd
}

func runScheduleCreate(cmd *cobra.Command, app *appctx.App, project, scheduleID, summary, startsAt, endsAt, description string, allDay, notify, visibleToClients bool, participants, subscribe string, noSubscribe bool, attachFiles []string) error {
	// Resolve subscription flags early (fail fast on bad input)
	subs, err := applySubscribeFlags(cmd.Context(), app.Names, subscribe, cmd.Flags().Changed("subscribe"), noSubscribe)
	if err != nil {
		return err
	}

	// Resolve project, with interactive fallback
	projectID := project
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

	// Get schedule ID from dock if not specified
	if scheduleID == "" {
		scheduleID, err = getScheduleID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	}

	scheduleIDInt, _ := strconv.ParseInt(scheduleID, 10, 64)

	// Convert description through rich text pipeline
	var mentionNotice string
	if description != "" {
		description = richTextToHTML(cmd, description)
		description, err = resolveLocalImages(cmd, app, description)
		if err != nil {
			return err
		}
		mentionResult, mentionErr := resolveMentions(cmd.Context(), app.Names, mentionScope(app, resolvedProjectID), description)
		if mentionErr != nil {
			return mentionErr
		}
		description = mentionResult.HTML
		mentionNotice = unresolvedMentionWarning(mentionResult.Unresolved)
	}

	// Upload explicit --attach files and embed
	if len(attachFiles) > 0 {
		refs, attachErr := uploadAttachments(cmd, app, attachFiles)
		if attachErr != nil {
			return attachErr
		}
		description = richtext.EmbedAttachments(description, refs)
	}

	// Build request
	req := &basecamp.CreateScheduleEntryRequest{
		Summary:       summary,
		StartsAt:      startsAt,
		EndsAt:        endsAt,
		Description:   description,
		AllDay:        &allDay,
		Notify:        notify,
		Subscriptions: subs,
	}

	// Set client visibility only when the flag was provided. Omitting it uses the
	// server's default: team-only when posting as a team member, but a
	// client-authenticated caller always creates client-visible records (an
	// explicit false is overridden server-side).
	if cmd.Flags().Changed("visible-to-clients") {
		req.VisibleToClients = &visibleToClients
	}

	if participants != "" {
		var ids []int64
		for idStr := range strings.SplitSeq(participants, ",") {
			idStr = strings.TrimSpace(idStr)
			if id, err := strconv.ParseInt(idStr, 10, 64); err == nil {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			req.ParticipantIDs = ids
		}
	}

	entry, err := app.Account().Schedules().CreateEntry(cmd.Context(), scheduleIDInt, req)
	if err != nil {
		return convertSDKError(err)
	}

	resultSummary := fmt.Sprintf("Created schedule entry #%d: %s", entry.ID, summary)

	respOpts := []output.ResponseOption{
		output.WithSummary(resultSummary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("basecamp schedule show %d --project %s", entry.ID, resolvedProjectID),
				Description: "View entry",
			},
			output.Breadcrumb{
				Action:      "entries",
				Cmd:         fmt.Sprintf("basecamp schedule entries --project %s", resolvedProjectID),
				Description: "View all entries",
			},
		),
	}
	if mentionNotice != "" {
		respOpts = append(respOpts, output.WithDiagnostic(mentionNotice))
	}
	return app.OK(entry, respOpts...)
}

func newScheduleUpdateCmd(project *string) *cobra.Command {
	var summary string
	var startsAt string
	var endsAt string
	var description string
	var allDay bool
	var notify bool
	var participants string
	var attachFiles []string

	cmd := &cobra.Command{
		Use:   "update <id|url>",
		Short: "Update a schedule entry",
		Long: `Update an existing schedule entry.

You can pass either an entry ID or a Basecamp URL:
  basecamp schedule update 789 --summary "new title" --in my-project
  basecamp schedule update https://3.basecamp.com/123/buckets/456/schedule_entries/789 --summary "new title"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			// Extract ID and project from URL if provided. Purely syntactic,
			// so it precedes the stdin read like every other target-ID check.
			entryID, urlProjectID, err := extractWithProject(args[0])
			if err != nil {
				return err
			}

			// This command used to send a malformed ID to the server as 0 —
			// the ParseInt further down discarded its error. Reject it here
			// instead, like every sibling update command, and before the pipe
			// is drained.
			entryIDInt, err := strconv.ParseInt(entryID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid schedule entry ID")
			}

			// The timestamp formats and the attachment paths are decidable from
			// the flags alone, as they already are on the create path — so they
			// precede the read rather than following account and project
			// resolution.
			if startsAt != "" {
				if err := validateScheduleTimestamp("starts-at", startsAt); err != nil {
					return err
				}
			}
			if endsAt != "" {
				if err := validateScheduleTimestamp("ends-at", endsAt); err != nil {
					return err
				}
			}
			if err := validateAttachPaths(attachFiles); err != nil {
				return err
			}

			// Syntactic checks first, then "-", then account and network.
			description, err = resolveContentValue(cmd, description, -1, "--description")
			if err != nil {
				return err
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
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

			// Build request with provided fields only
			req := &basecamp.UpdateScheduleEntryRequest{}
			hasChanges := false

			if summary != "" {
				req.Summary = basecamp.Ptr(summary)
				hasChanges = true
			}
			if startsAt != "" {
				req.StartsAt = basecamp.Ptr(startsAt)
				hasChanges = true
			}
			if endsAt != "" {
				req.EndsAt = basecamp.Ptr(endsAt)
				hasChanges = true
			}
			var mentionNotice string
			if description != "" {
				html := richTextToHTML(cmd, description)
				html, err = resolveLocalImages(cmd, app, html)
				if err != nil {
					return err
				}
				mentionResult, mentionErr := resolveMentions(cmd.Context(), app.Names, mentionScope(app, urlProjectID, resolvedProjectID), html)
				if mentionErr != nil {
					return mentionErr
				}
				html = mentionResult.HTML
				mentionNotice = unresolvedMentionWarning(mentionResult.Unresolved)

				// Upload explicit --attach files and embed
				if len(attachFiles) > 0 {
					refs, attachErr := uploadAttachments(cmd, app, attachFiles)
					if attachErr != nil {
						return attachErr
					}
					html = richtext.EmbedAttachments(html, refs)
				}

				req.Description = basecamp.Ptr(html)
				hasChanges = true
			}
			if cmd.Flags().Changed("all-day") {
				req.AllDay = &allDay
				hasChanges = true
			}
			if cmd.Flags().Changed("notify") {
				req.Notify = basecamp.Ptr(notify)
				hasChanges = true
			}
			if participants != "" {
				var ids []int64
				for idStr := range strings.SplitSeq(participants, ",") {
					idStr = strings.TrimSpace(idStr)
					if id, err := strconv.ParseInt(idStr, 10, 64); err == nil {
						ids = append(ids, id)
					}
				}
				if len(ids) > 0 {
					req.ParticipantIDs = basecamp.Ptr(ids)
					hasChanges = true
				}
			}

			// Handle attachments when no description text provided —
			// fetch the current description so we append rather than replace
			if description == "" && len(attachFiles) > 0 {
				refs, attachErr := uploadAttachments(cmd, app, attachFiles)
				if attachErr != nil {
					return attachErr
				}
				existing, fetchErr := app.Account().Schedules().GetEntry(cmd.Context(), entryIDInt)
				if fetchErr != nil {
					return convertSDKError(fetchErr)
				}
				req.Description = basecamp.Ptr(richtext.EmbedAttachments(existing.Description, refs))
				hasChanges = true
			}

			if !hasChanges {
				return noChanges(cmd)
			}

			entry, err := app.Account().Schedules().UpdateEntry(cmd.Context(), entryIDInt, req)
			if err != nil {
				return convertSDKError(err)
			}

			resultSummary := fmt.Sprintf("Updated schedule entry #%s", entryID)

			respOpts := []output.ResponseOption{
				output.WithSummary(resultSummary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp schedule show %s --project %s", entryID, resolvedProjectID),
						Description: "View entry",
					},
				),
			}
			if mentionNotice != "" {
				respOpts = append(respOpts, output.WithDiagnostic(mentionNotice))
			}
			return app.OK(entry, respOpts...)
		},
	}

	cmd.Flags().StringVar(&summary, "summary", "", "Event title/summary")
	cmd.Flags().StringVar(&summary, "title", "", "Event title (alias)")
	cmd.Flags().StringVar(&startsAt, "starts-at", "", "Start time (ISO 8601)")
	cmd.Flags().StringVar(&startsAt, "start", "", "Start time (alias)")
	cmd.Flags().StringVar(&endsAt, "ends-at", "", "End time (ISO 8601)")
	cmd.Flags().StringVar(&endsAt, "end", "", "End time (alias)")
	cmd.Flags().StringVar(&description, "description", "", "Detailed description; use - to read from stdin")
	cmd.Flags().StringVar(&description, "desc", "", "Description (alias)")
	cmd.Flags().BoolVar(&allDay, "all-day", false, "Mark as all-day event")
	cmd.Flags().BoolVar(&notify, "notify", false, "Notify participants")
	cmd.Flags().StringVar(&participants, "participants", "", "Comma-separated person IDs")
	cmd.Flags().StringVar(&participants, "people", "", "Person IDs (alias)")
	cmd.Flags().StringArrayVar(&attachFiles, "attach", nil, "Attach file (repeatable)")

	allowDash(cmd, "flag:description", "flag:desc")

	addRichTextFormatFlag(cmd)
	return cmd
}

func newScheduleSettingsCmd(project, scheduleID *string) *cobra.Command {
	var includeDue bool

	cmd := &cobra.Command{
		Use:   "settings",
		Short: "Update schedule settings",
		Long:  "Update schedule settings like including due assignments.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if !cmd.Flags().Changed("include-due") {
				return output.ErrUsage("--include-due required (true or false)")
			}

			// Resolve project, with interactive fallback
			projectID := *project
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

			// Get schedule ID from dock if not specified
			effectiveScheduleID := *scheduleID
			if effectiveScheduleID == "" {
				effectiveScheduleID, err = getScheduleID(cmd, app, resolvedProjectID)
				if err != nil {
					return err
				}
			}

			scheduleIDInt, _ := strconv.ParseInt(effectiveScheduleID, 10, 64)

			req := &basecamp.UpdateScheduleSettingsRequest{
				IncludeDueAssignments: includeDue,
			}

			schedule, err := app.Account().Schedules().UpdateSettings(cmd.Context(), scheduleIDInt, req)
			if err != nil {
				return convertSDKError(err)
			}

			resultSummary := "Updated schedule settings"

			return app.OK(schedule,
				output.WithSummary(resultSummary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp schedule --project %s", resolvedProjectID),
						Description: "View schedule",
					},
				),
			)
		},
	}

	cmd.Flags().BoolVar(&includeDue, "include-due", false, "Include due dates from todos/cards")
	cmd.Flags().BoolVar(&includeDue, "include-due-assignments", false, "Include due assignments (alias)")

	return cmd
}

// getScheduleID retrieves the schedule ID from a project's dock, handling multi-dock projects.
func getScheduleID(cmd *cobra.Command, app *appctx.App, projectID string) (string, error) {
	return getDockToolID(cmd.Context(), app, projectID, "schedule", "", "schedule", "schedule")
}

// validateScheduleTimestamp accepts what bc3 accepts: an RFC 3339 timestamp,
// or a bare date (2026-06-01), which renders the entry all-day. The SDK
// stopped validating these client-side at v0.13.0 so bare dates could
// round-trip; the CLI still fails garbage locally as a usage error rather
// than spending a request to surface it as a 422.
func validateScheduleTimestamp(flag, value string) error {
	if _, err := time.Parse(time.RFC3339, value); err == nil {
		return nil
	}
	if _, err := time.Parse("2006-01-02", value); err == nil {
		return nil
	}
	return output.ErrUsage(fmt.Sprintf("Invalid --%s: %q is neither an RFC 3339 timestamp (2026-06-01T09:00:00Z) nor a date (2026-06-01)", flag, value))
}

var scheduleMoveHelp = filingHelp{
	use:   "move <id|url>",
	short: "Move a schedule entry (event) to another project's schedule",
	long: `Move a schedule entry, an event, to a schedule in another project, as the
web's Move… does. A recurring event moves as a whole, every occurrence with it.

  basecamp schedule move 789 --to-project "Marketing"
  basecamp schedule move https://3.basecamp.com/123/buckets/456/schedule_entries/789 --to-project "Marketing"
  basecamp schedule move 789 --to-schedule 1069479999

--to-project alone needs a project with one schedule; add --to-schedule when it
has several, or pass a schedule's ID or URL alone. The command waits for
Basecamp to finish the move and shows the event where it landed, under a new ID.`,
	notes: "--to-project <project> (ID or name) moves the event to that project's schedule; --to-schedule <id|url> names a schedule directly, which is how to name an employee calendar's schedule (a calendar is not a project). " +
		"A recurring event moves as a whole; an occurrence URL moves the whole series. " +
		"Output is the moved event: data.id is its NEW id and data.source_id the old one. Write to the new id; the old id stops taking changes, and reading it follows the move. " +
		"--schedule names the event's own schedule and is refused; moving onto the schedule it is already on is refused. " +
		"An in_progress error means Basecamp is still moving it: basecamp schedule show <old id> returns it once it lands.",
}

var scheduleCopyHelp = filingHelp{
	use:   "copy <id|url>",
	short: "Copy a schedule entry (event) to a schedule in this or another project",
	long: `Copy a schedule entry, an event, onto a schedule, as the web's Copy… does. The
original stays where it is; a recurring event is copied as a whole series.

With no destination, the copy goes onto the event's own schedule:
  basecamp schedule copy 789
  basecamp schedule copy 789 --to-project "Marketing"
  basecamp schedule copy 789 --to-schedule 1069479999 --include-comments

Basecamp makes the copy in the background; the command waits for it and shows
the new event.`,
	notes: "Without --to-project/--to-schedule the copy goes onto the event's own schedule; --to-schedule <id|url> also names an employee calendar's schedule. " +
		"Output is the new event; its id is data.id and the original's is data.source_id. --include-comments copies the comments too. " +
		"Never re-run a copy that reported in_progress or a may-have-started error: each run makes another copy.",
}

// scheduleEntryFamily is how schedule entries move and copy between
// schedules.
func scheduleEntryFamily() filingFamily {
	return filingFamily{
		noun:     "schedule entry",
		group:    "basecamp schedule",
		types:    []string{"Schedule::Entry"},
		urlKinds: []string{"schedule_entries"},
		urlShape: "…/schedule_entries/<id>",
		into: filingContainer{
			dockName: "schedule",
			noun:     "schedule",
			flag:     "--to-schedule",
			urlKind:  "schedules",
			urlShape: "…/schedules/<id>",
			get: func(ctx context.Context, app *appctx.App, id int64) (filingTarget, error) {
				schedule, err := app.Account().Schedules().Get(ctx, id)
				if err != nil {
					return filingTarget{}, err
				}
				return bucketTarget(schedule.ID, schedule.Title, schedule.Bucket), nil
			},
		},
		ownFlags: []string{"schedule"},
		get:      getScheduleEntryForFiling,
		landing:  "The copy lands on that schedule",
		lookFor: func(src filingSource, t filingTarget) string {
			if t.calendar && src.status != "drafted" {
				// The recordings listing serves projects, not calendars.
				return fmt.Sprintf("basecamp schedule entries --in %d --schedule %d --status %s --all", t.projectID, t.id, cmp.Or(src.status, "active"))
			}
			return recordingCopyLookFor("Schedule::Entry", t.projectID, src.status)
		},
		crumbs: func(id, projectID int64) []output.Breadcrumb {
			return []output.Breadcrumb{{
				Action:      "show",
				Cmd:         fmt.Sprintf("basecamp schedule show %d --in %d", id, projectID),
				Description: "View the event",
			}}
		},
	}
}

// occurrenceType is what Basecamp answers a recurring event's read with: it
// redirects to an occurrence, whose parent is the event.
const occurrenceType = "Schedule::Entry::Occurrence"

// getScheduleEntryForFiling reads an event to move or copy. A recurring
// event's read answers with one occurrence, so the event is its parent and
// files as a whole series; the output names the event, with the occurrence
// read as occurrence_id, and seriesSchedule finds the schedule it is on.
func getScheduleEntryForFiling(ctx context.Context, app *appctx.App, id int64) (any, filingSource, error) {
	e, err := app.Account().Schedules().GetEntry(ctx, id)
	if err != nil {
		return nil, filingSource{}, err
	}
	src := filingSource{id: e.ID, typ: e.Type, status: e.Status, title: e.Summary, bucket: e.Bucket, parent: e.Parent}
	if e.Type != occurrenceType || e.Parent == nil {
		return e, src, nil
	}

	src.id, src.typ, src.parent = e.Parent.ID, "Schedule::Entry", nil
	if e.Bucket != nil {
		if src.parent, err = seriesSchedule(ctx, app, e.Bucket, src.id, e.Status); err != nil {
			return nil, filingSource{}, err
		}
	}
	record, ok := withSourceID(e, 0).(map[string]any)
	if !ok {
		return e, src, nil
	}
	delete(record, "source_id")
	record["occurrence_id"] = record["id"]
	record["id"] = src.id
	record["type"] = src.typ
	record["parent"] = src.parent
	return record, src, nil
}

// seriesSchedule finds the schedule a recurring event is on, which its
// occurrence's read does not name. A calendar, such as an employee's, has one
// schedule, which the calendar's read links to; a project's schedules are on
// its dock, and of several, the one whose entries list the event, in its
// status (an entries listing shows one status), is it. It returns nil when
// none does.
func seriesSchedule(ctx context.Context, app *appctx.App, bucket *basecamp.Bucket, eventID int64, status string) (*basecamp.Parent, error) {
	scheduleOf := func(id int64) (*basecamp.Parent, error) {
		schedule, err := app.Account().Schedules().Get(ctx, id)
		if err != nil {
			return nil, err
		}
		return &basecamp.Parent{ID: schedule.ID, Title: schedule.Title, Type: "Schedule"}, nil
	}

	if bucket.Type == "Calendar" {
		calendar, err := app.Account().Calendars().Get(ctx, bucket.ID)
		if err != nil {
			return nil, err
		}
		id, err := strconv.ParseInt(strings.TrimSuffix(path.Base(calendar.ScheduleURL), ".json"), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("calendar %d links to no schedule (%q)", bucket.ID, calendar.ScheduleURL)
		}
		return scheduleOf(id)
	}

	_, schedules, err := getDockTools(ctx, app, strconv.FormatInt(bucket.ID, 10), "schedule")
	if err != nil {
		return nil, err
	}
	if len(schedules) == 1 {
		return &basecamp.Parent{ID: schedules[0].ID, Title: schedules[0].Title, Type: "Schedule"}, nil
	}
	var opts *basecamp.ScheduleEntryListOptions
	if status == "archived" || status == "trashed" {
		opts = &basecamp.ScheduleEntryListOptions{Status: status}
	}
	for _, schedule := range schedules {
		entries, err := app.Account().Schedules().ListEntries(ctx, schedule.ID, opts)
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(entries.Entries, func(e basecamp.ScheduleEntry) bool { return e.ID == eventID }) {
			return &basecamp.Parent{ID: schedule.ID, Title: schedule.Title, Type: "Schedule"}, nil
		}
	}
	return nil, nil
}
