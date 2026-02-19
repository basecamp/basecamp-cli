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

// DocsFiles is the list view for a project's vault (Docs & Files).
type DocsFiles struct {
	session *workspace.Session
	pool    *data.Pool[[]data.DocsFilesItemInfo]
	styles  *tui.Styles

	// Layout
	list          *widget.List
	spinner       spinner.Model
	loading       bool
	width, height int

	// Data
	items []data.DocsFilesItemInfo
}

// NewDocsFiles creates the docs & files view.
func NewDocsFiles(session *workspace.Session) *DocsFiles {
	styles := session.Styles()
	scope := session.Scope()
	pool := session.Hub().DocsFiles(scope.ProjectID, scope.ToolID)

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyText("No documents or files found.")
	list.SetFocused(true)

	return &DocsFiles{
		session: session,
		pool:    pool,
		styles:  styles,
		list:    list,
		spinner: s,
		loading: true,
	}
}

// Title implements View.
func (v *DocsFiles) Title() string {
	return "Docs & Files"
}

// ShortHelp implements View.
func (v *DocsFiles) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	}
}

// FullHelp implements View.
func (v *DocsFiles) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

// StartFilter implements workspace.Filterable.
func (v *DocsFiles) StartFilter() { v.list.StartFilter() }

// InputActive implements workspace.InputCapturer.
func (v *DocsFiles) InputActive() bool { return v.list.Filtering() }

// SetSize implements View.
func (v *DocsFiles) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.list.SetSize(w, h)
}

// Init implements tea.Model.
func (v *DocsFiles) Init() tea.Cmd {
	snap := v.pool.Get()
	if snap.Usable() {
		v.items = snap.Data
		v.syncList()
		v.loading = false
		if snap.Fresh() {
			return nil
		}
	}
	return tea.Batch(v.spinner.Tick, v.pool.FetchIfStale(v.session.Context()))
}

// Update implements tea.Model.
func (v *DocsFiles) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
				return v, workspace.ReportError(snap.Err, "loading docs & files")
			}
			if snap.Loading() && !snap.HasData {
				v.loading = true
			}
		}
		return v, nil

	case workspace.RefreshMsg:
		v.pool.Invalidate()
		v.loading = true
		return v, tea.Batch(v.spinner.Tick, v.pool.Fetch(v.session.Context()))

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

func (v *DocsFiles) handleKey(msg tea.KeyMsg) tea.Cmd {
	keys := workspace.DefaultListKeyMap()

	switch {
	case key.Matches(msg, keys.Open):
		return v.openSelectedItem()
	default:
		return v.list.Update(msg)
	}
}

func (v *DocsFiles) openSelectedItem() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}
	var itemID int64
	fmt.Sscanf(item.ID, "%d", &itemID)

	// Find the item's type from our data
	recordType := "Document"
	for _, it := range v.items {
		if it.ID == itemID {
			recordType = it.Type
			break
		}
	}

	// Folders are containers, not recordings — skip navigation for now
	if recordType == "Folder" {
		return workspace.SetStatus(fmt.Sprintf("Folder: %s", item.Title), false)
	}

	// Record in recents
	if r := v.session.Recents(); r != nil {
		r.Add(recents.Item{
			ID:          item.ID,
			Title:       item.Title,
			Description: recordType,
			Type:        recents.TypeRecording,
			AccountID:   v.session.Scope().AccountID,
			ProjectID:   fmt.Sprintf("%d", v.session.Scope().ProjectID),
		})
	}

	scope := v.session.Scope()
	scope.RecordingID = itemID
	scope.RecordingType = recordType
	return workspace.Navigate(workspace.ViewDetail, scope)
}

// View implements tea.Model.
func (v *DocsFiles) View() string {
	if v.loading {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Loading docs & files...")
	}

	return v.list.View()
}

// -- Data sync

func (v *DocsFiles) syncList() {
	items := make([]widget.ListItem, 0, len(v.items))
	for _, it := range v.items {
		desc := it.Type
		if it.Creator != "" {
			desc += " - " + it.Creator
		}
		if it.CreatedAt != "" {
			desc += " - " + it.CreatedAt
		}

		items = append(items, widget.ListItem{
			ID:          fmt.Sprintf("%d", it.ID),
			Title:       it.Title,
			Description: desc,
		})
	}
	v.list.SetItems(items)
}

