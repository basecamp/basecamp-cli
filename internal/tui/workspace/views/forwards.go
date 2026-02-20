package views

import (
	"fmt"

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

// Forwards shows email forwards in the project inbox.
type Forwards struct {
	session *workspace.Session
	pool    *data.Pool[[]data.ForwardInfo]
	styles  *tui.Styles

	list          *widget.List
	spinner       spinner.Model
	loading       bool
	width, height int

	items []data.ForwardInfo
}

// NewForwards creates the email forwards view.
func NewForwards(session *workspace.Session) *Forwards {
	styles := session.Styles()
	scope := session.Scope()
	pool := session.Hub().Forwards(scope.ProjectID, scope.ToolID)

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyText("No email forwards in this project.")
	list.SetFocused(true)

	return &Forwards{
		session: session,
		pool:    pool,
		styles:  styles,
		list:    list,
		spinner: s,
		loading: true,
	}
}

func (v *Forwards) Title() string { return "Email Forwards" }

func (v *Forwards) ShortHelp() []key.Binding {
	if v.list.Filtering() {
		return filterHints()
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	}
}

func (v *Forwards) FullHelp() [][]key.Binding { return [][]key.Binding{v.ShortHelp()} }

// StartFilter implements workspace.Filterable.
func (v *Forwards) StartFilter() { v.list.StartFilter() }

// InputActive implements workspace.InputCapturer.
func (v *Forwards) InputActive() bool { return v.list.Filtering() }

func (v *Forwards) SetSize(w, h int) { v.width = w; v.height = h; v.list.SetSize(w, h) }

func (v *Forwards) Init() tea.Cmd {
	snap := v.pool.Get()
	if snap.Usable() {
		v.items = snap.Data
		v.syncList()
		v.loading = false
		if snap.Fresh() {
			return nil
		}
	}
	return tea.Batch(v.spinner.Tick, v.pool.FetchIfStale(v.session.Hub().ProjectContext()))
}

func (v *Forwards) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case data.PoolUpdatedMsg:
		if msg.Key == v.pool.Key() {
			snap := v.pool.Get()
			if snap.Usable() {
				v.items = snap.Data
				v.syncList()
				v.loading = false
			}
			if snap.State == data.StateError {
				v.loading = false
				return v, workspace.ReportError(snap.Err, "loading forwards")
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
		keys := workspace.DefaultListKeyMap()
		if key.Matches(msg, keys.Open) {
			return v, v.openSelected()
		}
		return v, v.list.Update(msg)
	}
	return v, nil
}

func (v *Forwards) View() string {
	if v.loading {
		return lipgloss.NewStyle().
			Width(v.width).Height(v.height).Padding(1, 2).
			Render(v.spinner.View() + " Loading email forwards...")
	}
	return v.list.View()
}

func (v *Forwards) syncList() {
	items := make([]widget.ListItem, 0, len(v.items))
	for _, f := range v.items {
		items = append(items, widget.ListItem{
			ID:          fmt.Sprintf("%d", f.ID),
			Title:       f.Subject,
			Description: f.From,
		})
	}
	v.list.SetItems(items)
}

func (v *Forwards) openSelected() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}
	var fwdID int64
	fmt.Sscanf(item.ID, "%d", &fwdID)

	if r := v.session.Recents(); r != nil {
		r.Add(recents.Item{
			ID:          item.ID,
			Title:       item.Title,
			Description: "Forward",
			Type:        recents.TypeRecording,
			AccountID:   v.session.Scope().AccountID,
			ProjectID:   fmt.Sprintf("%d", v.session.Scope().ProjectID),
		})
	}

	scope := v.session.Scope()
	scope.RecordingID = fwdID
	scope.RecordingType = "Forward"
	return workspace.Navigate(workspace.ViewDetail, scope)
}
