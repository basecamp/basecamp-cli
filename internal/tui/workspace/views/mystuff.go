package views

import (
	"strconv"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/recents"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// sectionHeader is a sentinel ID prefix for non-selectable section headers.
const sectionHeader = "header:"

// MyStuff is the personal dashboard view showing recent projects and items.
type MyStuff struct {
	session *workspace.Session
	styles  *tui.Styles

	list          *widget.List
	width, height int

	// recordingProjects maps "recording:<id>" to the project ID from recents
	recordingProjects map[string]int64
}

// NewMyStuff creates the My Stuff personal dashboard view.
func NewMyStuff(session *workspace.Session) *MyStuff {
	styles := session.Styles()

	list := widget.NewList(styles)
	list.SetEmptyText("No recent items yet. Navigate to a project to get started.")
	list.SetFocused(true)

	v := &MyStuff{
		session:           session,
		styles:            styles,
		list:              list,
		recordingProjects: make(map[string]int64),
	}

	v.syncRecents()

	return v
}

// Title implements View.
func (v *MyStuff) Title() string {
	return "My Stuff"
}

// ShortHelp implements View.
func (v *MyStuff) ShortHelp() []key.Binding {
	if v.list.Filtering() {
		return filterHints()
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	}
}

// FullHelp implements View.
func (v *MyStuff) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

// StartFilter implements workspace.Filterable.
func (v *MyStuff) StartFilter() { v.list.StartFilter() }

// InputActive implements workspace.InputCapturer.
func (v *MyStuff) InputActive() bool { return v.list.Filtering() }

// SetSize implements View.
func (v *MyStuff) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.list.SetSize(w, h)
}

// Init implements tea.Model.
func (v *MyStuff) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (v *MyStuff) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case workspace.RefreshMsg:
		v.syncRecents()
		return v, nil

	case workspace.FocusMsg:
		v.syncRecents()
		return v, nil

	case tea.KeyMsg:
		keys := workspace.DefaultListKeyMap()
		switch {
		case key.Matches(msg, keys.Open):
			return v, v.openSelected()
		default:
			cmd := v.list.Update(msg)
			// Skip section headers: if the cursor landed on one, nudge it past
			v.skipHeaders(msg)
			return v, cmd
		}
	}
	return v, nil
}

// View implements tea.Model.
func (v *MyStuff) View() string {
	if v.list.Len() == 0 {
		theme := v.styles.Theme()
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Foreground(theme.Muted).
			Render("No recent items yet.\n\nNavigate to projects and tools — they'll appear here for quick access.")
	}

	return v.list.View()
}

// campfireRecordingType is the canonical type used for campfire recents routing.
const campfireRecordingType = "Campfire"

// syncRecents rebuilds the list from the recents store.
func (v *MyStuff) syncRecents() {
	store := v.session.Recents()
	if store == nil {
		return
	}

	// Reset project lookup on each sync to avoid stale entries
	v.recordingProjects = make(map[string]int64)

	accountID := v.session.Scope().AccountID

	projects := store.Get(recents.TypeProject, accountID, "")
	recordings := store.Get(recents.TypeRecording, accountID, "")

	var items []widget.ListItem

	if len(projects) > 0 {
		items = append(items, widget.ListItem{
			ID:    sectionHeader + "projects",
			Title: "Recent Projects",
		})
		for _, p := range projects {
			items = append(items, widget.ListItem{
				ID:          "project:" + p.ID,
				Title:       p.Title,
				Description: p.Description,
			})
		}
	}

	if len(recordings) > 0 {
		// Add a blank separator if we have both sections
		if len(projects) > 0 {
			items = append(items, widget.ListItem{
				ID: sectionHeader + "sep",
			})
		}
		items = append(items, widget.ListItem{
			ID:    sectionHeader + "recordings",
			Title: "Recent Items",
		})
		for _, r := range recordings {
			desc := r.Description
			if r.Type != "" && desc == "" {
				desc = r.Type
			}
			key := "recording:" + r.ID
			items = append(items, widget.ListItem{
				ID:          key,
				Title:       r.Title,
				Description: desc,
			})
			// Store project ID for cross-project navigation
			if r.ProjectID != "" {
				if pid, err := strconv.ParseInt(r.ProjectID, 10, 64); err == nil {
					v.recordingProjects[key] = pid
				}
			}
		}
	}

	v.list.SetItems(items)
}

// skipHeaders nudges the cursor past section header items.
func (v *MyStuff) skipHeaders(msg tea.KeyMsg) {
	item := v.list.Selected()
	if item == nil {
		return
	}
	if len(item.ID) < len(sectionHeader) || item.ID[:len(sectionHeader)] != sectionHeader {
		return
	}

	// Determine direction from the key pressed
	keys := workspace.DefaultListKeyMap()
	switch {
	case key.Matches(msg, keys.Down):
		// Move one more step down
		v.list.Update(msg)
	case key.Matches(msg, keys.Up):
		// Move one more step up
		v.list.Update(msg)
	}
}

// openSelected navigates to the selected item.
func (v *MyStuff) openSelected() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}

	id := item.ID

	// Section headers are not navigable
	if len(id) >= len(sectionHeader) && id[:len(sectionHeader)] == sectionHeader {
		return nil
	}

	// Project items: "project:<id>"
	if len(id) > 8 && id[:8] == "project:" {
		rawID := id[8:]
		projectID, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			return nil
		}

		scope := v.session.Scope()
		scope.ProjectID = projectID
		scope.ProjectName = item.Title
		return workspace.Navigate(workspace.ViewDock, scope)
	}

	// Recording items: "recording:<id>"
	if len(id) > 10 && id[:10] == "recording:" {
		rawID := id[10:]
		recordingID, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			return nil
		}

		scope := v.session.Scope()
		// Restore project scope from recents metadata
		if pid, ok := v.recordingProjects[id]; ok && pid != 0 {
			scope.ProjectID = pid
		}

		// Campfire entries should reopen the campfire view, not detail
		if item.Description == campfireRecordingType {
			scope.ToolType = "chat"
			scope.ToolID = recordingID
			return workspace.Navigate(workspace.ViewCampfire, scope)
		}

		scope.RecordingID = recordingID
		scope.RecordingType = item.Description
		return workspace.Navigate(workspace.ViewDetail, scope)
	}

	return nil
}
