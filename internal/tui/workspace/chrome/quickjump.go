package chrome

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/recents"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
)

// quickJumpItem represents a single entry in the quick-jump list.
type quickJumpItem struct {
	ID       string
	Title    string
	Category string // "recent", "bookmark", "project"
	Navigate func() tea.Cmd
}

// QuickJumpCloseMsg is sent when the quick-jump overlay is dismissed.
type QuickJumpCloseMsg struct{}

// QuickJumpExecMsg carries the navigation command from the selected item.
type QuickJumpExecMsg struct {
	Cmd tea.Cmd
}

// QuickJump is an overlay for jumping to projects and recent items.
type QuickJump struct {
	styles *tui.Styles

	input    textinput.Model
	items    []quickJumpItem
	filtered []quickJumpItem
	cursor   int

	width, height int
}

// NewQuickJump creates a new quick-jump overlay component.
func NewQuickJump(styles *tui.Styles) QuickJump {
	ti := textinput.New()
	ti.Placeholder = "Jump to..."
	ti.CharLimit = 128
	ti.Prompt = "> "

	return QuickJump{
		styles: styles,
		input:  ti,
	}
}

// QuickJumpSource provides the data needed to populate the quick-jump list.
// This avoids importing workspace/data and recents directly, breaking the
// dependency direction.
type QuickJumpSource struct {
	RecentProjects   []recents.Item
	RecentRecordings []recents.Item
	Projects         []data.ProjectInfo
	AccountID        string
	// NavigateProject is called with (projectID, accountID) to produce a nav command.
	NavigateProject func(projectID int64, accountID string) tea.Cmd
	// NavigateRecording is called with (recordingID, projectID, accountID) to produce a nav command.
	NavigateRecording func(recordingID, projectID int64, accountID string) tea.Cmd
	// NavigateTool is called with (toolName, toolID, projectID, accountID) to produce a nav command.
	NavigateTool func(toolName string, toolID, projectID int64, accountID string) tea.Cmd
}

// Focus activates the text input and populates items from the given source.
func (q *QuickJump) Focus(src QuickJumpSource) tea.Cmd {
	q.input.SetValue("")
	q.cursor = 0
	q.populateItems(src)
	q.refilter()
	return q.input.Focus()
}

// Blur deactivates the text input.
func (q *QuickJump) Blur() {
	q.input.Blur()
}

// SetSize sets the available dimensions for the overlay.
func (q *QuickJump) SetSize(width, height int) {
	q.width = width
	q.height = height
	q.input.Width = max(0, width-8)
}

// Update handles key messages while the quick-jump is active.
func (q *QuickJump) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return q.handleKey(msg)
	}
	return nil
}

func (q *QuickJump) handleKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		return func() tea.Msg { return QuickJumpCloseMsg{} }

	case "enter":
		if len(q.filtered) > 0 && q.cursor < len(q.filtered) {
			item := q.filtered[q.cursor]
			cmd := item.Navigate()
			return tea.Batch(
				func() tea.Msg { return QuickJumpCloseMsg{} },
				func() tea.Msg { return QuickJumpExecMsg{Cmd: cmd} },
			)
		}
		return nil

	case "up", "ctrl+k":
		if q.cursor > 0 {
			q.cursor--
		}
		return nil

	case "down", "ctrl+j":
		if q.cursor < len(q.filtered)-1 {
			q.cursor++
		}
		return nil

	default:
		var cmd tea.Cmd
		q.input, cmd = q.input.Update(msg)
		q.refilter()
		return cmd
	}
}

func (q *QuickJump) populateItems(src QuickJumpSource) {
	q.items = q.items[:0]
	seen := make(map[string]bool)

	// 1. Recent projects
	for _, r := range src.RecentProjects {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		projectID, err := strconv.ParseInt(r.ID, 10, 64)
		if err != nil {
			continue
		}
		acctID := r.AccountID
		if acctID == "" {
			acctID = src.AccountID
		}
		nav := src.NavigateProject
		q.items = append(q.items, quickJumpItem{
			ID:       r.ID,
			Title:    r.Title,
			Category: "recent",
			Navigate: func() tea.Cmd { return nav(projectID, acctID) },
		})
	}

	// 2. Recent recordings
	for _, r := range src.RecentRecordings {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		recordingID, err := strconv.ParseInt(r.ID, 10, 64)
		if err != nil {
			continue
		}
		var projID int64
		if r.ProjectID != "" {
			projID, _ = strconv.ParseInt(r.ProjectID, 10, 64)
		}
		acctID := r.AccountID
		if acctID == "" {
			acctID = src.AccountID
		}
		nav := src.NavigateRecording
		title := r.Title
		if r.Description != "" {
			title = r.Title + " (" + r.Description + ")"
		}
		q.items = append(q.items, quickJumpItem{
			ID:       r.ID,
			Title:    title,
			Category: "recent",
			Navigate: func() tea.Cmd { return nav(recordingID, projID, acctID) },
		})
	}

	// 3. Bookmarked projects
	for _, p := range src.Projects {
		id := fmt.Sprintf("%d", p.ID)
		if seen[id] || !p.Bookmarked {
			continue
		}
		seen[id] = true
		projectID := p.ID
		acctID := p.AccountID
		nav := src.NavigateProject
		q.items = append(q.items, quickJumpItem{
			ID:       id,
			Title:    p.Name,
			Category: "bookmark",
			Navigate: func() tea.Cmd { return nav(projectID, acctID) },
		})
	}

	// 4. All remaining projects
	for _, p := range src.Projects {
		id := fmt.Sprintf("%d", p.ID)
		if seen[id] {
			continue
		}
		seen[id] = true
		projectID := p.ID
		acctID := p.AccountID
		nav := src.NavigateProject
		q.items = append(q.items, quickJumpItem{
			ID:       id,
			Title:    p.Name,
			Category: "project",
			Navigate: func() tea.Cmd { return nav(projectID, acctID) },
		})
	}

	// 5. Tool entries for recent projects (up to 5 projects)
	if src.NavigateTool != nil {
		toolProjects := recentProjectInfos(src)
		for _, p := range toolProjects {
			for _, tool := range p.Dock {
				if !tool.Enabled {
					continue
				}
				displayName := toolDisplayName(tool.Name)
				if displayName == "" {
					continue
				}
				id := fmt.Sprintf("tool:%d:%d", p.ID, tool.ID)
				projectID := p.ID
				acctID := p.AccountID
				toolName := tool.Name
				toolID := tool.ID
				nav := src.NavigateTool
				q.items = append(q.items, quickJumpItem{
					ID:       id,
					Title:    p.Name + " > " + displayName,
					Category: "tool",
					Navigate: func() tea.Cmd { return nav(toolName, toolID, projectID, acctID) },
				})
			}
		}
	}
}

// recentProjectInfos returns ProjectInfo for the most recent projects (up to 5),
// matched by ID from recent projects to the full project list.
func recentProjectInfos(src QuickJumpSource) []data.ProjectInfo {
	const maxToolProjects = 5
	projectByID := make(map[string]data.ProjectInfo, len(src.Projects))
	for _, p := range src.Projects {
		projectByID[fmt.Sprintf("%d", p.ID)] = p
	}

	var result []data.ProjectInfo
	seen := make(map[string]bool)
	for _, r := range src.RecentProjects {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		if p, ok := projectByID[r.ID]; ok {
			result = append(result, p)
			if len(result) >= maxToolProjects {
				break
			}
		}
	}
	return result
}

// toolDisplayName maps dock tool API names to human-readable titles.
func toolDisplayName(name string) string {
	switch name {
	case "todoset":
		return "Todos"
	case "message_board":
		return "Message Board"
	case "chat":
		return "Campfire"
	case "schedule":
		return "Schedule"
	case "questionnaire":
		return "Check-ins"
	case "vault":
		return "Docs & Files"
	case "kanban_board":
		return "Card Table"
	case "inbox":
		return "Forwards"
	default:
		return ""
	}
}

func (q *QuickJump) refilter() {
	query := strings.TrimSpace(q.input.Value())
	if query == "" {
		q.filtered = make([]quickJumpItem, len(q.items))
		copy(q.filtered, q.items)
	} else {
		q.filtered = q.filtered[:0]
		for _, item := range q.items {
			if quickJumpFuzzyMatch(item.Title, query) {
				q.filtered = append(q.filtered, item)
			}
		}
	}
	if q.cursor >= len(q.filtered) {
		q.cursor = len(q.filtered) - 1
	}
	if q.cursor < 0 {
		q.cursor = 0
	}
}

// quickJumpFuzzyMatch performs subsequence matching.
func quickJumpFuzzyMatch(s, query string) bool {
	s = strings.ToLower(s)
	queryRunes := []rune(strings.ToLower(query))
	qi := 0
	for _, r := range s {
		if qi < len(queryRunes) && r == queryRunes[qi] {
			qi++
		}
	}
	return qi == len(queryRunes)
}

// maxJumpVisibleItems is the maximum number of rows shown in the quick-jump overlay.
const maxJumpVisibleItems = 12

// View renders the quick-jump overlay.
func (q QuickJump) View() string {
	theme := q.styles.Theme()

	boxWidth := 60
	if q.width-8 < boxWidth {
		boxWidth = q.width - 8
	}
	if boxWidth < 30 {
		boxWidth = min(30, q.width-2)
	}
	if boxWidth < 10 {
		boxWidth = 10
	}
	if q.width > 0 && boxWidth > q.width {
		boxWidth = q.width
	}

	// Title
	title := lipgloss.NewStyle().
		Foreground(theme.Primary).
		Bold(true).
		Render("Jump to...")

	// Separator
	sep := lipgloss.NewStyle().
		Foreground(theme.Border).
		Width(max(1, boxWidth-4)).
		Render(strings.Repeat("─", max(1, boxWidth-4)))

	// Input line
	inputLine := q.input.View()

	// Items
	var rows []string

	// Scroll window around cursor
	start := 0
	if q.cursor >= maxJumpVisibleItems {
		start = q.cursor - maxJumpVisibleItems + 1
	}
	end := start + maxJumpVisibleItems
	if end > len(q.filtered) {
		end = len(q.filtered)
	}

	visible := q.filtered[start:end]
	for vi, item := range visible {
		i := start + vi
		badge := lipgloss.NewStyle().Foreground(theme.Muted).Render("  " + item.Category)
		name := lipgloss.NewStyle().Foreground(theme.Primary).Render(item.Title)
		line := lipgloss.NewStyle().Width(boxWidth - 4).Render(name + badge)

		if i == q.cursor {
			line = lipgloss.NewStyle().
				Background(theme.Border).
				Width(boxWidth - 4).
				Render(
					lipgloss.NewStyle().Foreground(theme.Primary).Background(theme.Border).Render(item.Title) +
						lipgloss.NewStyle().Foreground(theme.Muted).Background(theme.Border).Render("  "+item.Category),
				)
		}
		rows = append(rows, line)
	}

	if len(q.filtered) == 0 {
		rows = append(rows, lipgloss.NewStyle().Foreground(theme.Muted).Render("No matches"))
	}

	// Footer
	footer := lipgloss.NewStyle().Foreground(theme.Muted).Render("↑/↓ navigate  enter jump  esc cancel")

	// Assemble
	sections := make([]string, 0, 4+len(rows)+2)
	sections = append(sections, title)
	sections = append(sections, sep)
	sections = append(sections, inputLine)
	sections = append(sections, sep)
	sections = append(sections, rows...)
	sections = append(sections, sep)
	sections = append(sections, footer)

	content := lipgloss.JoinVertical(lipgloss.Left, sections...)

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Primary).
		Padding(0, 1).
		Width(boxWidth)

	rendered := box.Render(content)

	return lipgloss.NewStyle().
		Width(q.width).
		Align(lipgloss.Center).
		Render(rendered)
}
