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

// dockKeyMap defines dock-specific keybindings.
type dockKeyMap struct {
	Todos    key.Binding
	Campfire key.Binding
	Messages key.Binding
	Cards    key.Binding
	Schedule key.Binding
}

func defaultDockKeyMap() dockKeyMap {
	return dockKeyMap{
		Todos: key.NewBinding(
			key.WithKeys("t"),
			key.WithHelp("t", "todos"),
		),
		Campfire: key.NewBinding(
			key.WithKeys("c"),
			key.WithHelp("c", "campfire"),
		),
		Messages: key.NewBinding(
			key.WithKeys("m"),
			key.WithHelp("m", "messages"),
		),
		Cards: key.NewBinding(
			key.WithKeys("k"),
			key.WithHelp("k", "cards"),
		),
		Schedule: key.NewBinding(
			key.WithKeys("s"),
			key.WithHelp("s", "schedule"),
		),
	}
}

// Dock shows a project's tool grid with peek previews.
type Dock struct {
	session *workspace.Session
	styles  *tui.Styles

	projectInfo *data.ProjectInfo
	list        *widget.List
	spinner     spinner.Model
	loading     bool
	keys        dockKeyMap

	width, height int
}

// NewDock creates the project dock view.
func NewDock(session *workspace.Session, projectID int64) *Dock {
	styles := session.Styles()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyText("No tools enabled for this project.")
	list.SetFocused(true)

	v := &Dock{
		session: session,
		styles:  styles,
		list:    list,
		spinner: s,
		keys:    defaultDockKeyMap(),
	}

	// Try to find project in the Hub's Projects pool
	snap := session.Hub().Projects().Get()
	if snap.Usable() {
		for i := range snap.Data {
			if snap.Data[i].ID == projectID {
				v.projectInfo = &snap.Data[i]
				v.syncTools()
				break
			}
		}
	}
	if v.projectInfo == nil {
		v.loading = true
	}

	return v
}

// Title implements View.
func (v *Dock) Title() string {
	if v.projectInfo != nil {
		return v.projectInfo.Name
	}
	return "Project"
}

// ShortHelp implements View.
func (v *Dock) ShortHelp() []key.Binding {
	if v.list.Filtering() {
		return filterHints()
	}
	return []key.Binding{
		v.keys.Todos,
		v.keys.Campfire,
		v.keys.Messages,
		v.keys.Cards,
	}
}

// FullHelp implements View.
func (v *Dock) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{v.keys.Todos, v.keys.Campfire, v.keys.Messages},
		{v.keys.Cards, v.keys.Schedule},
	}
}

// StartFilter implements workspace.Filterable.
func (v *Dock) StartFilter() { v.list.StartFilter() }

// InputActive implements workspace.InputCapturer.
func (v *Dock) InputActive() bool { return v.list.Filtering() }

// SetSize implements View.
func (v *Dock) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.list.SetSize(w, h)
}

// Init implements tea.Model.
func (v *Dock) Init() tea.Cmd {
	// Record project visit in recents
	if v.projectInfo != nil {
		if r := v.session.Recents(); r != nil {
			r.Add(recents.Item{
				ID:        fmt.Sprintf("%d", v.projectInfo.ID),
				Title:     v.projectInfo.Name,
				Type:      recents.TypeProject,
				AccountID: v.session.Scope().AccountID,
			})
		}
	}

	if v.loading {
		return tea.Batch(v.spinner.Tick, v.fetchProject())
	}
	return nil
}

// Update implements tea.Model.
func (v *Dock) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case workspace.DockLoadedMsg:
		v.loading = false
		if msg.Err != nil {
			return v, workspace.ReportError(msg.Err, "loading project")
		}
		dock := make([]data.DockToolInfo, 0, len(msg.Project.Dock))
		for _, d := range msg.Project.Dock {
			dock = append(dock, data.DockToolInfo{
				ID:      d.ID,
				Name:    d.Name,
				Title:   d.Title,
				Enabled: d.Enabled,
			})
		}
		v.projectInfo = &data.ProjectInfo{
			ID:          msg.Project.ID,
			Name:        msg.Project.Name,
			Description: msg.Project.Description,
			Purpose:     msg.Project.Purpose,
			Bookmarked:  msg.Project.Bookmarked,
			Dock:        dock,
		}
		v.syncTools()
		return v, nil

	case workspace.RefreshMsg:
		v.loading = true
		return v, tea.Batch(v.spinner.Tick, v.fetchProject())

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

func (v *Dock) handleKey(msg tea.KeyMsg) tea.Cmd {
	dk := v.keys
	listKeys := workspace.DefaultListKeyMap()

	switch {
	case key.Matches(msg, dk.Todos):
		return v.navigateToTool("todoset", workspace.ViewTodos)
	case key.Matches(msg, dk.Campfire):
		return v.navigateToTool("chat", workspace.ViewCampfire)
	case key.Matches(msg, dk.Messages):
		return v.navigateToTool("message_board", workspace.ViewMessages)
	case key.Matches(msg, dk.Cards):
		return v.navigateToTool("kanban_board", workspace.ViewCards)
	case key.Matches(msg, dk.Schedule):
		return v.navigateToTool("schedule", workspace.ViewSchedule)
	case key.Matches(msg, listKeys.Open):
		return v.openSelectedTool()
	default:
		return v.list.Update(msg)
	}
}

// View implements tea.Model.
func (v *Dock) View() string {
	if v.loading {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Loading project...")
	}

	return v.list.View()
}

func (v *Dock) syncTools() {
	if v.projectInfo == nil {
		return
	}

	var items []widget.ListItem
	for _, tool := range v.projectInfo.Dock {
		if !tool.Enabled {
			continue
		}
		title := tool.Title
		if title == "" {
			title = dockToolDisplayName(tool.Name)
		}
		items = append(items, widget.ListItem{
			ID:          fmt.Sprintf("%d", tool.ID),
			Title:       title,
			Description: dockToolDisplayName(tool.Name),
			Extra:       toolHotkey(tool.Name),
		})
	}
	v.list.SetItems(items)
}

func toolHotkey(name string) string {
	switch name {
	case "todoset":
		return "t"
	case "chat":
		return "c"
	case "message_board":
		return "m"
	case "kanban_board":
		return "k"
	case "schedule":
		return "s"
	default:
		return ""
	}
}

func toolNameToView(name string) (workspace.ViewTarget, bool) {
	switch name {
	case "todoset":
		return workspace.ViewTodos, true
	case "chat":
		return workspace.ViewCampfire, true
	case "message_board":
		return workspace.ViewMessages, true
	case "kanban_board":
		return workspace.ViewCards, true
	case "schedule":
		return workspace.ViewSchedule, true
	case "vault":
		return workspace.ViewDocsFiles, true
	case "questionnaire":
		return workspace.ViewCheckins, true
	case "inbox":
		return workspace.ViewForwards, true
	default:
		return 0, false
	}
}

func (v *Dock) navigateToTool(toolName string, target workspace.ViewTarget) tea.Cmd {
	if v.projectInfo == nil {
		return nil
	}

	for _, tool := range v.projectInfo.Dock {
		if tool.Name == toolName && tool.Enabled {
			scope := v.session.Scope()
			scope.ToolType = toolName
			scope.ToolID = tool.ID
			return workspace.Navigate(target, scope)
		}
	}

	return workspace.SetStatus(fmt.Sprintf("No %s in this project", strings.ReplaceAll(toolName, "_", " ")), true)
}

func (v *Dock) openSelectedTool() tea.Cmd {
	item := v.list.Selected()
	if item == nil || v.projectInfo == nil {
		return nil
	}

	// Find the dock tool by ID
	var toolID int64
	fmt.Sscanf(item.ID, "%d", &toolID)

	for _, tool := range v.projectInfo.Dock {
		if tool.ID == toolID {
			if target, ok := toolNameToView(tool.Name); ok {
				scope := v.session.Scope()
				scope.ToolType = tool.Name
				scope.ToolID = tool.ID
				return workspace.Navigate(target, scope)
			}
			return workspace.SetStatus(dockToolDisplayName(tool.Name), false)
		}
	}
	return nil
}

func (v *Dock) fetchProject() tea.Cmd {
	scope := v.session.Scope()
	ctx := v.session.Hub().ProjectContext()
	client := v.session.AccountClient()
	return func() tea.Msg {
		result, err := client.Projects().Get(ctx, scope.ProjectID)
		if err != nil {
			return workspace.DockLoadedMsg{Err: err}
		}
		return workspace.DockLoadedMsg{Project: *result}
	}
}
