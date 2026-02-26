package commands

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewTimelineCmd creates the timeline command for viewing activity feeds.
func NewTimelineCmd() *cobra.Command {
	var project string
	var person string
	var watch bool
	var interval int

	cmd := &cobra.Command{
		Use:   "timeline [me]",
		Short: "View activity timeline",
		Long: `View activity timelines for the account, a project, or a person.

By default, shows the account-wide activity feed (recent activity across all projects).

Use --in to view a specific project's timeline.
Use "me" or --person to view a person's activity timeline.
Use --watch to continuously poll for new activity.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if watch {
				return runTimelineWatch(cmd, args, project, person, time.Duration(interval)*time.Second)
			}
			return runTimeline(cmd, args, project, person)
		},
	}

	cmd.Flags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID or name (alias for --project)")
	cmd.Flags().StringVar(&person, "person", "", "Person ID or name")
	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "Watch for new activity (poll continuously)")
	cmd.Flags().IntVar(&interval, "interval", 30, "Poll interval in seconds (default: 30)")

	return cmd
}

func runTimeline(cmd *cobra.Command, args []string, project, person string) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Validate positional argument - only "me" is supported
	if len(args) > 0 && args[0] != "me" {
		return output.ErrUsageHint(
			fmt.Sprintf("invalid argument %q", args[0]),
			"Only \"me\" is supported as a positional argument. Use --person <name> for other people.",
		)
	}

	// Check for mutually exclusive flags
	if person != "" && project != "" {
		return output.ErrUsage("--person and --project are mutually exclusive")
	}

	// Determine which timeline to show based on args and flags
	// Priority: positional "me" > --person flag > --project flag > default (account-wide)

	// Check for "me" positional argument
	if len(args) > 0 && args[0] == "me" {
		return runPersonTimeline(cmd, "me")
	}

	// Check for --person flag
	if person != "" {
		return runPersonTimeline(cmd, person)
	}

	// Check for --project flag
	if project != "" {
		return runProjectTimeline(cmd, project)
	}

	// Default: account-wide activity feed
	events, err := app.Account().Timeline().Progress(cmd.Context())
	if err != nil {
		return convertSDKError(err)
	}

	return app.OK(events,
		output.WithSummary(fmt.Sprintf("%d recent events", len(events))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "project",
				Cmd:         "basecamp timeline --in <project>",
				Description: "View project timeline",
			},
			output.Breadcrumb{
				Action:      "person",
				Cmd:         "basecamp timeline me",
				Description: "View your activity",
			},
		),
	)
}

func runProjectTimeline(cmd *cobra.Command, project string) error {
	app := appctx.FromContext(cmd.Context())

	// Resolve project name to ID
	resolvedProjectID, projectName, err := app.Names.ResolveProject(cmd.Context(), project)
	if err != nil {
		return err
	}

	projectIDInt, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}

	events, err := app.Account().Timeline().ProjectTimeline(cmd.Context(), projectIDInt)
	if err != nil {
		return convertSDKError(err)
	}

	summary := fmt.Sprintf("%d events in %s", len(events), projectName)
	if projectName == "" {
		summary = fmt.Sprintf("%d events in project #%s", len(events), resolvedProjectID)
	}

	return app.OK(events,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "account",
				Cmd:         "basecamp timeline",
				Description: "View account-wide timeline",
			},
			output.Breadcrumb{
				Action:      "project",
				Cmd:         fmt.Sprintf("basecamp project show %s", resolvedProjectID),
				Description: "View project details",
			},
		),
	)
}

func runPersonTimeline(cmd *cobra.Command, personArg string) error {
	app := appctx.FromContext(cmd.Context())

	// Resolve person name/ID
	resolvedPersonID, personName, err := app.Names.ResolvePerson(cmd.Context(), personArg)
	if err != nil {
		return err
	}

	personID, err := strconv.ParseInt(resolvedPersonID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid person ID")
	}

	result, err := app.Account().Timeline().PersonProgress(cmd.Context(), personID)
	if err != nil {
		return convertSDKError(err)
	}

	// Use name from result if available, otherwise use resolved name
	displayName := personName
	if result.Person != nil && result.Person.Name != "" {
		displayName = result.Person.Name
	}

	summary := fmt.Sprintf("%d events for %s", len(result.Events), displayName)
	if displayName == "" {
		summary = fmt.Sprintf("%d events for person #%s", len(result.Events), resolvedPersonID)
	}

	return app.OK(result.Events,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "account",
				Cmd:         "basecamp timeline",
				Description: "View account-wide timeline",
			},
			output.Breadcrumb{
				Action:      "person",
				Cmd:         fmt.Sprintf("basecamp people show %s", resolvedPersonID),
				Description: "View person details",
			},
		),
	)
}

// watchModel is the bubbletea model for the watch mode TUI.
type watchModel struct {
	spinner     spinner.Model
	events      []basecamp.TimelineEvent
	seenIDs     map[int64]bool
	lastUpdate  time.Time
	interval    time.Duration
	err         error
	fetchFn     func(context.Context) ([]basecamp.TimelineEvent, error)
	ctx         context.Context
	cancel      context.CancelFunc
	description string
	newCount    int
}

type watchTickMsg struct{}
type watchEventsMsg struct {
	events []basecamp.TimelineEvent
	err    error
}

func (m watchModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		m.fetchEvents,
	)
}

func (m watchModel) fetchEvents() tea.Msg {
	// Check if context is already canceled before making API call
	if err := m.ctx.Err(); err != nil {
		return watchEventsMsg{events: nil, err: err}
	}
	events, err := m.fetchFn(m.ctx)
	return watchEventsMsg{events: events, err: err}
}

func (m watchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.cancel()
			return m, tea.Quit
		}
	case watchEventsMsg:
		if msg.err != nil {
			m.err = msg.err
			// Continue polling even on error
		} else {
			// Count and track new events
			newEvents := 0
			for _, e := range msg.events {
				if !m.seenIDs[e.ID] {
					m.seenIDs[e.ID] = true
					newEvents++
				}
			}
			if newEvents > 0 && len(m.events) > 0 {
				m.newCount += newEvents
			}
			m.events = msg.events
			m.lastUpdate = time.Now()
			m.err = nil
		}
		// Schedule next poll
		return m, tea.Tick(m.interval, func(t time.Time) tea.Msg {
			return watchTickMsg{}
		})
	case watchTickMsg:
		return m, m.fetchEvents
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m watchModel) View() string {
	var status string
	if m.err != nil {
		errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
		status = errStyle.Render(fmt.Sprintf("Error: %s", m.err))
	} else if m.lastUpdate.IsZero() {
		status = fmt.Sprintf("%s Fetching %s...", m.spinner.View(), m.description)
	} else {
		elapsed := time.Since(m.lastUpdate).Truncate(time.Second)
		var newIndicator string
		if m.newCount > 0 {
			newStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
			newIndicator = newStyle.Render(fmt.Sprintf(" (+%d new)", m.newCount))
		}
		status = fmt.Sprintf("%s %d events%s (updated %s ago, polling every %s) - Press q to quit",
			m.spinner.View(), len(m.events), newIndicator, elapsed, m.interval)
	}

	// Build output
	var output strings.Builder
	output.WriteString(status + "\n")

	if len(m.events) > 0 {
		output.WriteString("\n")
		// Show latest 10 events
		count := min(len(m.events), 10)
		for i := 0; i < count; i++ {
			e := m.events[i]
			line := formatEvent(e)
			output.WriteString(line + "\n")
		}
		if len(m.events) > 10 {
			muted := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
			output.WriteString(muted.Render(fmt.Sprintf("... and %d more", len(m.events)-10)) + "\n")
		}
	}

	return output.String()
}

func formatEvent(e basecamp.TimelineEvent) string {
	// Format: [time] Creator Action on Title
	timeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	actionStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("3"))

	var timeStr string
	if !e.CreatedAt.IsZero() {
		timeStr = e.CreatedAt.Local().Format("15:04")
	} else {
		timeStr = "--:--"
	}

	creatorName := "Someone"
	if e.Creator != nil && e.Creator.Name != "" {
		creatorName = e.Creator.Name
	}

	action := e.Action
	if action == "" {
		action = "updated"
	}

	title := e.Title
	if title == "" {
		title = e.SummaryExcerpt
	}
	// Truncate at rune boundary for proper Unicode handling
	if len([]rune(title)) > 40 {
		title = string([]rune(title)[:37]) + "..."
	}

	return fmt.Sprintf("%s %s %s %s",
		timeStyle.Render(timeStr),
		nameStyle.Render(creatorName),
		actionStyle.Render(action),
		title,
	)
}

func runTimelineWatch(cmd *cobra.Command, args []string, project, person string, interval time.Duration) error {
	app := appctx.FromContext(cmd.Context())

	if interval < 1 {
		return output.ErrUsage("--interval must be at least 1 second")
	}

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Check for mutually exclusive flags
	if person != "" && project != "" {
		return output.ErrUsage("--person and --project are mutually exclusive")
	}

	// Validate positional argument
	if len(args) > 0 && args[0] != "me" {
		return output.ErrUsageHint(
			fmt.Sprintf("invalid argument %q", args[0]),
			"Only \"me\" is supported as a positional argument.",
		)
	}

	// Set up cancellable context
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	// Handle signals - stop notification when done and exit cleanly
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)
	go func() {
		select {
		case <-sigChan:
			cancel()
		case <-ctx.Done():
			// Context canceled elsewhere; exit goroutine
		}
	}()

	// Determine fetch function and description based on flags
	var fetchFn func(context.Context) ([]basecamp.TimelineEvent, error)
	var description string

	if len(args) > 0 && args[0] == "me" {
		// Personal timeline
		userID := app.Auth.GetUserID()
		if userID == "" {
			return output.ErrAuth("User ID not available")
		}
		personID, err := strconv.ParseInt(userID, 10, 64)
		if err != nil {
			return output.ErrUsage("Invalid user ID")
		}
		description = "your activity"
		fetchFn = func(ctx context.Context) ([]basecamp.TimelineEvent, error) {
			result, err := app.Account().Timeline().PersonProgress(ctx, personID)
			if err != nil {
				return nil, err
			}
			return result.Events, nil
		}
	} else if person != "" {
		// Specific person timeline
		resolvedPersonID, personName, err := app.Names.ResolvePerson(ctx, person)
		if err != nil {
			return err
		}
		personID, err := strconv.ParseInt(resolvedPersonID, 10, 64)
		if err != nil {
			return output.ErrUsage("Invalid person ID")
		}
		description = fmt.Sprintf("activity for %s", personName)
		fetchFn = func(ctx context.Context) ([]basecamp.TimelineEvent, error) {
			result, err := app.Account().Timeline().PersonProgress(ctx, personID)
			if err != nil {
				return nil, err
			}
			return result.Events, nil
		}
	} else if project != "" {
		// Project timeline
		resolvedProjectID, projectName, err := app.Names.ResolveProject(ctx, project)
		if err != nil {
			return err
		}
		projectIDInt, err := strconv.ParseInt(resolvedProjectID, 10, 64)
		if err != nil {
			return output.ErrUsage("Invalid project ID")
		}
		description = fmt.Sprintf("activity in %s", projectName)
		fetchFn = func(ctx context.Context) ([]basecamp.TimelineEvent, error) {
			return app.Account().Timeline().ProjectTimeline(ctx, projectIDInt)
		}
	} else {
		// Account-wide timeline
		description = "account activity"
		fetchFn = func(ctx context.Context) ([]basecamp.TimelineEvent, error) {
			return app.Account().Timeline().Progress(ctx)
		}
	}

	// Create spinner
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	// Create model
	m := watchModel{
		spinner:     s,
		seenIDs:     make(map[int64]bool),
		interval:    interval,
		fetchFn:     fetchFn,
		ctx:         ctx,
		cancel:      cancel,
		description: description,
	}

	// Run TUI
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	if err != nil {
		return err
	}

	return nil
}
