package views

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/recents"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// Checkins is the list view for a project's check-in questions.
type Checkins struct {
	session *workspace.Session
	pool    *data.Pool[[]data.CheckinQuestionInfo]
	styles  *tui.Styles

	// Layout
	list          *widget.List
	spinner       spinner.Model
	loading       bool
	width, height int

	// Data
	questions []data.CheckinQuestionInfo
}

// NewCheckins creates the check-ins view.
func NewCheckins(session *workspace.Session) *Checkins {
	styles := session.Styles()
	scope := session.Scope()
	pool := session.Hub().Checkins(scope.ProjectID, scope.ToolID)

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyText("No check-in questions found.")
	list.SetFocused(true)

	return &Checkins{
		session: session,
		pool:    pool,
		styles:  styles,
		list:    list,
		spinner: s,
		loading: true,
	}
}

// Title implements View.
func (v *Checkins) Title() string {
	return "Check-ins"
}

// ShortHelp implements View.
func (v *Checkins) ShortHelp() []key.Binding {
	if v.list.Filtering() {
		return filterHints()
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	}
}

// FullHelp implements View.
func (v *Checkins) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

// StartFilter implements workspace.Filterable.
func (v *Checkins) StartFilter() { v.list.StartFilter() }

// InputActive implements workspace.InputCapturer.
func (v *Checkins) InputActive() bool { return v.list.Filtering() }

// SetSize implements View.
func (v *Checkins) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.list.SetSize(w, h)
}

// Init implements tea.Model.
func (v *Checkins) Init() tea.Cmd {
	snap := v.pool.Get()
	if snap.Usable() {
		v.questions = snap.Data
		v.syncList()
		v.loading = false
		if snap.Fresh() {
			return nil
		}
	}
	return tea.Batch(v.spinner.Tick, v.pool.FetchIfStale(v.session.Hub().ProjectContext()))
}

// Update implements tea.Model.
func (v *Checkins) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case data.PoolUpdatedMsg:
		if msg.Key == v.pool.Key() {
			snap := v.pool.Get()
			if snap.Usable() {
				v.questions = snap.Data
				v.syncList()
				v.loading = false
			}
			if snap.State == data.StateError {
				v.loading = false
				return v, workspace.ReportError(snap.Err, "loading check-in questions")
			}
			if snap.Loading() && !snap.HasData {
				v.loading = true
			}
		}
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
		return v, v.handleKey(msg)
	}
	return v, nil
}

func (v *Checkins) handleKey(msg tea.KeyMsg) tea.Cmd {
	keys := workspace.DefaultListKeyMap()

	switch {
	case key.Matches(msg, keys.Open):
		return v.openSelectedQuestion()
	default:
		return v.list.Update(msg)
	}
}

func (v *Checkins) openSelectedQuestion() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}
	var questionID int64
	fmt.Sscanf(item.ID, "%d", &questionID)

	// Record in recents
	if r := v.session.Recents(); r != nil {
		r.Add(recents.Item{
			ID:          item.ID,
			Title:       item.Title,
			Description: "Question",
			Type:        recents.TypeRecording,
			AccountID:   v.session.Scope().AccountID,
			ProjectID:   fmt.Sprintf("%d", v.session.Scope().ProjectID),
		})
	}

	scope := v.session.Scope()
	scope.RecordingID = questionID
	scope.RecordingType = "Question"
	return workspace.Navigate(workspace.ViewDetail, scope)
}

// View implements tea.Model.
func (v *Checkins) View() string {
	if v.loading {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Loading check-ins...")
	}

	return v.list.View()
}

// -- Data sync

func (v *Checkins) syncList() {
	items := make([]widget.ListItem, 0, len(v.questions))
	for _, q := range v.questions {
		var parts []string
		if q.Frequency != "" {
			parts = append(parts, formatFrequency(q.Frequency))
		}
		if q.AnswersCount > 0 {
			parts = append(parts, fmt.Sprintf("%d answers", q.AnswersCount))
		}
		if q.Paused {
			parts = append(parts, "paused")
		}

		items = append(items, widget.ListItem{
			ID:          fmt.Sprintf("%d", q.ID),
			Title:       q.Title,
			Description: strings.Join(parts, " - "),
		})
	}
	v.list.SetItems(items)
}

func formatFrequency(freq string) string {
	switch freq {
	case "every_day":
		return "Daily"
	case "every_week":
		return "Weekly"
	case "every_other_week":
		return "Biweekly"
	case "every_month":
		return "Monthly"
	case "on_certain_days":
		return "Certain days"
	default:
		return freq
	}
}
