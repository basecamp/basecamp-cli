package views

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/empty"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// People is the people directory view showing all account members.
type People struct {
	session *workspace.Session
	pool    *data.Pool[[]data.PersonInfo]
	styles  *tui.Styles

	list    *widget.List
	spinner spinner.Model
	loading bool

	width, height int
}

// NewPeople creates the people directory view.
func NewPeople(session *workspace.Session) *People {
	styles := session.Styles()
	pool := session.Hub().People()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyMessage(empty.NoPeople())
	list.SetFocused(true)

	return &People{
		session: session,
		pool:    pool,
		styles:  styles,
		list:    list,
		spinner: s,
		loading: true,
	}
}

// Title implements View.
func (v *People) Title() string { return "People" }

// ShortHelp implements View.
func (v *People) ShortHelp() []key.Binding {
	if v.list.Filtering() {
		return filterHints()
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
	}
}

// FullHelp implements View.
func (v *People) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

// StartFilter implements workspace.Filterable.
func (v *People) StartFilter() { v.list.StartFilter() }

// InputActive implements workspace.InputCapturer.
func (v *People) InputActive() bool { return v.list.Filtering() }

// SetSize implements View.
func (v *People) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.list.SetSize(w, h)
}

// Init implements tea.Model.
func (v *People) Init() tea.Cmd {
	snap := v.pool.Get()
	if snap.Usable() {
		v.syncPeople(snap.Data)
		v.loading = false
		if snap.Fresh() {
			return nil
		}
	}
	return tea.Batch(v.spinner.Tick, v.pool.FetchIfStale(v.session.Hub().AccountContext()))
}

// Update implements tea.Model.
func (v *People) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case data.PoolUpdatedMsg:
		if msg.Key == v.pool.Key() {
			snap := v.pool.Get()
			if snap.Usable() {
				v.syncPeople(snap.Data)
				v.loading = false
			}
			if snap.State == data.StateError {
				v.loading = false
				return v, workspace.ReportError(snap.Err, "loading people")
			}
			if snap.Loading() && !snap.HasData {
				v.loading = true
			}
		}
		return v, nil

	case workspace.RefreshMsg:
		v.pool.Invalidate()
		v.loading = true
		return v, tea.Batch(v.spinner.Tick, v.pool.Fetch(v.session.Hub().AccountContext()))

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
		switch {
		case key.Matches(msg, keys.Open):
			return v, v.openSelectedPerson()
		default:
			return v, v.list.Update(msg)
		}
	}
	return v, nil
}

// View implements tea.Model.
func (v *People) View() string {
	if v.loading {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Loading people...")
	}
	return v.list.View()
}

func (v *People) openSelectedPerson() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}
	url := fmt.Sprintf("https://3.basecamp.com/%s/people/%s",
		v.session.Scope().AccountID, item.ID)
	return workspace.OpenURL(url)
}

// -- Data sync

func (v *People) syncPeople(people []data.PersonInfo) {
	items := make([]widget.ListItem, 0, len(people))
	for _, p := range people {
		items = append(items, widget.ListItem{
			ID:          fmt.Sprintf("%d", p.ID),
			Title:       personTitle(p),
			Description: personDescription(p),
		})
	}
	v.list.SetItems(items)
}

// personTitle formats a person's name with role badges.
func personTitle(p data.PersonInfo) string {
	title := p.Name
	if title == "" {
		title = p.Email
	}
	if title == "" {
		title = fmt.Sprintf("Person #%d", p.ID)
	}

	var badges []string
	if p.Owner {
		badges = append(badges, "owner")
	} else if p.Admin {
		badges = append(badges, "admin")
	}
	if p.Client {
		badges = append(badges, "client")
	}

	if len(badges) > 0 {
		title += " [" + strings.Join(badges, ", ") + "]"
	}

	return title
}

// personDescription formats a person's secondary info line.
func personDescription(p data.PersonInfo) string {
	var parts []string

	if p.Email != "" {
		parts = append(parts, p.Email)
	}
	if p.Title != "" {
		parts = append(parts, p.Title)
	}
	if p.Company != "" {
		parts = append(parts, p.Company)
	}

	return strings.Join(parts, " - ")
}
