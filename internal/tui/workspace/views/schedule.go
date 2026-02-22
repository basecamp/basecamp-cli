package views

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/dateparse"
	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/recents"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// scheduleEntryCreatedMsg is sent after a schedule entry is created.
type scheduleEntryCreatedMsg struct{ err error }

// scheduleTrashResultMsg is sent after a schedule entry is trashed.
type scheduleTrashResultMsg struct{ err error }

// scheduleTrashTimeoutMsg resets the double-press trash confirmation.
type scheduleTrashTimeoutMsg struct{}

// Schedule is the list view for a project's schedule entries.
type Schedule struct {
	session *workspace.Session
	pool    *data.Pool[[]data.ScheduleEntryInfo]
	styles  *tui.Styles

	// Layout
	list          *widget.List
	spinner       spinner.Model
	loading       bool
	width, height int

	// Data
	entries []data.ScheduleEntryInfo

	// Inline creation (multi-step)
	creating      bool
	createStep    int // 0=summary, 1=start date, 2=end date
	createSummary string
	createStart   string
	createInput   textinput.Model

	// Trash (double-press)
	trashPending   bool
	trashPendingID string
}

// NewSchedule creates the schedule view.
func NewSchedule(session *workspace.Session) *Schedule {
	styles := session.Styles()
	scope := session.Scope()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyText("No schedule entries found.")
	list.SetFocused(true)

	pool := session.Hub().ScheduleEntries(scope.ProjectID, scope.ToolID)

	return &Schedule{
		session: session,
		pool:    pool,
		styles:  styles,
		list:    list,
		spinner: s,
		loading: true,
	}
}

// Title implements View.
func (v *Schedule) Title() string {
	return "Schedule"
}

// ShortHelp implements View.
func (v *Schedule) ShortHelp() []key.Binding {
	if v.list.Filtering() {
		return filterHints()
	}
	if v.creating {
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		}
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new event")),
		key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "trash")),
	}
}

// FullHelp implements View.
func (v *Schedule) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

// StartFilter implements workspace.Filterable.
func (v *Schedule) StartFilter() { v.list.StartFilter() }

// InputActive implements workspace.InputCapturer.
func (v *Schedule) InputActive() bool { return v.list.Filtering() || v.creating }

// IsModal implements workspace.ModalActive.
func (v *Schedule) IsModal() bool { return v.creating }

// SetSize implements View.
func (v *Schedule) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.list.SetSize(w, h)
}

// Init implements tea.Model.
func (v *Schedule) Init() tea.Cmd {
	snap := v.pool.Get()
	if snap.Usable() {
		v.entries = snap.Data
		v.syncList()
		v.loading = false
		if snap.Fresh() {
			return nil
		}
	}
	return tea.Batch(v.spinner.Tick, v.pool.FetchIfStale(v.session.Hub().ProjectContext()))
}

// Update implements tea.Model.
func (v *Schedule) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case data.PoolUpdatedMsg:
		if msg.Key == v.pool.Key() {
			snap := v.pool.Get()
			if snap.Usable() {
				v.entries = snap.Data
				v.syncList()
				v.loading = false
			}
			if snap.State == data.StateError {
				v.loading = false
				return v, workspace.ReportError(snap.Err, "loading schedule entries")
			}
			if snap.Loading() && !snap.HasData {
				v.loading = true
			}
		}
		return v, nil

	case scheduleEntryCreatedMsg:
		if msg.err != nil {
			return v, workspace.ReportError(msg.err, "creating schedule entry")
		}
		v.pool.Invalidate()
		return v, tea.Batch(
			v.pool.Fetch(v.session.Hub().ProjectContext()),
			workspace.SetStatus("Event created", false),
		)

	case scheduleTrashResultMsg:
		if msg.err != nil {
			return v, workspace.ReportError(msg.err, "trashing schedule entry")
		}
		v.pool.Invalidate()
		return v, tea.Batch(
			v.pool.Fetch(v.session.Hub().ProjectContext()),
			workspace.SetStatus("Trashed", false),
		)

	case scheduleTrashTimeoutMsg:
		v.trashPending = false
		v.trashPendingID = ""
		return v, nil

	case workspace.RefreshMsg:
		v.pool.Invalidate()
		v.loading = true
		return v, tea.Batch(v.spinner.Tick, v.pool.Fetch(v.session.Hub().ProjectContext()))

	case spinner.TickMsg:
		if v.loading {
			var cmd tea.Cmd
			v.spinner, cmd = v.spinner.Update(msg)
			return v, cmd
		}

	case tea.KeyMsg:
		if v.loading {
			return v, nil
		}
		if v.creating {
			return v, v.handleCreateKey(msg)
		}
		return v, v.handleKey(msg)
	}
	return v, nil
}

func (v *Schedule) handleKey(msg tea.KeyMsg) tea.Cmd {
	if v.list.Filtering() {
		v.trashPending = false
		v.trashPendingID = ""
		return v.list.Update(msg)
	}

	// Reset trash confirmation on non-t keys
	if msg.String() != "t" {
		v.trashPending = false
		v.trashPendingID = ""
	}

	keys := workspace.DefaultListKeyMap()

	switch {
	case msg.String() == "n":
		return v.startCreate()
	case msg.String() == "t":
		return v.trashSelected()
	case key.Matches(msg, keys.Open):
		return v.openSelectedEntry()
	default:
		return v.list.Update(msg)
	}
}

// -- Create (multi-step inline)

func (v *Schedule) startCreate() tea.Cmd {
	v.creating = true
	v.createStep = 0
	v.createSummary = ""
	v.createStart = ""
	v.createInput = textinput.New()
	v.createInput.Placeholder = "Event name..."
	v.createInput.CharLimit = 256
	v.createInput.Focus()
	return textinput.Blink
}

func (v *Schedule) handleCreateKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		return v.advanceCreate()
	case "esc":
		v.creating = false
		return nil
	default:
		var cmd tea.Cmd
		v.createInput, cmd = v.createInput.Update(msg)
		return cmd
	}
}

func (v *Schedule) advanceCreate() tea.Cmd {
	val := strings.TrimSpace(v.createInput.Value())

	switch v.createStep {
	case 0: // summary
		if val == "" {
			v.creating = false
			return nil
		}
		v.createSummary = val
		v.createStep = 1
		v.createInput = textinput.New()
		v.createInput.Placeholder = "Start date (tomorrow, fri, 2026-03-15)..."
		v.createInput.CharLimit = 64
		v.createInput.Focus()
		return textinput.Blink

	case 1: // start date
		if val == "" {
			v.creating = false
			return nil
		}
		if !dateparse.IsValid(val) {
			return workspace.SetStatus("Unrecognized date: "+val, true)
		}
		v.createStart = dateparse.Parse(val)
		v.createStep = 2
		v.createInput = textinput.New()
		v.createInput.Placeholder = "End date (leave empty for single day)..."
		v.createInput.CharLimit = 64
		v.createInput.Focus()
		return textinput.Blink

	case 2: // end date
		endDate := v.createStart
		if val != "" {
			if !dateparse.IsValid(val) {
				return workspace.SetStatus("Unrecognized date: "+val, true)
			}
			endDate = dateparse.Parse(val)
			if endDate < v.createStart {
				return workspace.SetStatus("End date must be on or after start date", true)
			}
		}
		v.creating = false
		return v.dispatchCreate(v.createSummary, v.createStart, endDate)
	}
	return nil
}

func (v *Schedule) dispatchCreate(summary, startDate, endDate string) tea.Cmd {
	scope := v.session.Scope()
	hub := v.session.Hub()
	ctx := hub.ProjectContext()
	req := &basecamp.CreateScheduleEntryRequest{
		Summary:  summary,
		StartsAt: startDate + "T00:00:00Z",
		EndsAt:   endDate + "T00:00:00Z",
		AllDay:   true,
	}
	return func() tea.Msg {
		err := hub.CreateScheduleEntry(ctx, scope.AccountID, scope.ProjectID, scope.ToolID, req)
		return scheduleEntryCreatedMsg{err: err}
	}
}

// -- Trash (double-press)

func (v *Schedule) trashSelected() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}

	if v.trashPending && v.trashPendingID == item.ID {
		v.trashPending = false
		v.trashPendingID = ""
		var entryID int64
		fmt.Sscanf(item.ID, "%d", &entryID)
		scope := v.session.Scope()
		hub := v.session.Hub()
		ctx := hub.ProjectContext()
		return func() tea.Msg {
			err := hub.TrashRecording(ctx, scope.AccountID, scope.ProjectID, entryID)
			return scheduleTrashResultMsg{err: err}
		}
	}

	v.trashPending = true
	v.trashPendingID = item.ID
	return tea.Batch(
		workspace.SetStatus("Press t again to trash", false),
		tea.Tick(3*time.Second, func(time.Time) tea.Msg { return scheduleTrashTimeoutMsg{} }),
	)
}

// -- Open

func (v *Schedule) openSelectedEntry() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}
	var entryID int64
	fmt.Sscanf(item.ID, "%d", &entryID)

	// Record in recents
	if r := v.session.Recents(); r != nil {
		r.Add(recents.Item{
			ID:          item.ID,
			Title:       item.Title,
			Description: "Schedule::Entry",
			Type:        recents.TypeRecording,
			AccountID:   v.session.Scope().AccountID,
			ProjectID:   fmt.Sprintf("%d", v.session.Scope().ProjectID),
		})
	}

	scope := v.session.Scope()
	scope.RecordingID = entryID
	scope.RecordingType = "Schedule::Entry"
	return workspace.Navigate(workspace.ViewDetail, scope)
}

// View implements tea.Model.
func (v *Schedule) View() string {
	if v.loading {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Loading schedule...")
	}

	var b strings.Builder
	b.WriteString(v.list.View())

	if v.creating {
		b.WriteString("\n")
		theme := v.styles.Theme()
		var prefix string
		switch v.createStep {
		case 0:
			prefix = "  + "
		case 1:
			prefix = "  Start: "
		case 2:
			prefix = "  End: "
		}
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).Render(prefix) + v.createInput.View())
	}

	return b.String()
}

// -- Data sync

func (v *Schedule) syncList() {
	items := make([]widget.ListItem, 0, len(v.entries))
	for _, e := range v.entries {
		title := e.Summary
		if title == "" {
			title = "(untitled)"
		}

		desc := e.StartsAt
		if e.EndsAt != "" && e.EndsAt != e.StartsAt {
			desc += " - " + e.EndsAt
		}
		if len(e.Participants) > 0 {
			if desc != "" {
				desc += "  "
			}
			desc += strings.Join(e.Participants, ", ")
		}

		items = append(items, widget.ListItem{
			ID:          fmt.Sprintf("%d", e.ID),
			Title:       title,
			Description: desc,
		})
	}
	v.list.SetItems(items)
}
