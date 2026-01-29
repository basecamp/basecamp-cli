package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// PickerItem represents an item in a picker.
type PickerItem struct {
	ID          string
	Title       string
	Description string
}

func (i PickerItem) String() string {
	return i.Title
}

// FilterValue returns the string to filter on.
func (i PickerItem) FilterValue() string {
	return i.Title + " " + i.Description
}

// pickerModel is the bubbletea model for a fuzzy picker.
type pickerModel struct {
	items        []PickerItem
	filtered     []PickerItem
	textInput    textinput.Model
	cursor       int
	selected     *PickerItem
	quitting     bool
	styles       *Styles
	title        string
	maxVisible   int
	scrollOffset int

	// Loading state
	loading    bool
	loadingMsg string
	spinner    spinner.Model
}

// PickerOption configures a picker.
type PickerOption func(*pickerModel)

// WithPickerTitle sets the picker title.
func WithPickerTitle(title string) PickerOption {
	return func(m *pickerModel) {
		m.title = title
	}
}

// WithMaxVisible sets the maximum number of visible items.
func WithMaxVisible(n int) PickerOption {
	return func(m *pickerModel) {
		m.maxVisible = n
	}
}

// WithLoading sets the picker to start in loading state.
func WithLoading(msg string) PickerOption {
	return func(m *pickerModel) {
		m.loading = true
		m.loadingMsg = msg
	}
}

func newPickerModel(items []PickerItem, opts ...PickerOption) pickerModel {
	ti := textinput.New()
	ti.Placeholder = "Type to filter..."
	ti.Width = 40
	ti.Focus()

	s := spinner.New()
	s.Spinner = spinner.Dot
	styles := NewStyles()
	s.Style = lipgloss.NewStyle().Foreground(styles.theme.Primary)

	m := pickerModel{
		items:      items,
		filtered:   items,
		textInput:  ti,
		styles:     styles,
		title:      "Select an item",
		maxVisible: 10,
		spinner:    s,
		loadingMsg: "Loading...",
	}

	for _, opt := range opts {
		opt(&m)
	}

	return m
}

// PickerItemsLoadedMsg is sent when items are loaded asynchronously.
type PickerItemsLoadedMsg struct {
	Items []PickerItem
	Err   error
}

func (m pickerModel) Init() tea.Cmd {
	if m.loading {
		return m.spinner.Tick
	}
	return textinput.Blink
}

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case PickerItemsLoadedMsg:
		m.loading = false
		if msg.Err != nil {
			m.quitting = true
			return m, tea.Quit
		}
		m.items = msg.Items
		m.filtered = m.filter(m.textInput.Value())
		return m, textinput.Blink

	case spinner.TickMsg:
		if m.loading {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}

	case tea.KeyMsg:
		// In loading state, only allow cancel
		if m.loading {
			if msg.String() == "ctrl+c" || msg.String() == "esc" {
				m.quitting = true
				return m, tea.Quit
			}
			return m, nil
		}

		switch msg.String() {
		case "ctrl+c", "esc":
			m.quitting = true
			return m, tea.Quit
		case "enter":
			if len(m.filtered) > 0 && m.cursor < len(m.filtered) {
				m.selected = &m.filtered[m.cursor]
			}
			return m, tea.Quit
		case "up", "ctrl+p":
			if m.cursor > 0 {
				m.cursor--
				if m.cursor < m.scrollOffset {
					m.scrollOffset = m.cursor
				}
			}
		case "down", "ctrl+n":
			if m.cursor < len(m.filtered)-1 {
				m.cursor++
				if m.cursor >= m.scrollOffset+m.maxVisible {
					m.scrollOffset = m.cursor - m.maxVisible + 1
				}
			}
		case "tab":
			// Tab to select first match
			if len(m.filtered) > 0 {
				m.selected = &m.filtered[0]
			}
			return m, tea.Quit
		default:
			var cmd tea.Cmd
			m.textInput, cmd = m.textInput.Update(msg)
			m.filtered = m.filter(m.textInput.Value())
			m.cursor = 0
			m.scrollOffset = 0
			return m, cmd
		}
	}

	return m, nil
}

func (m pickerModel) filter(query string) []PickerItem {
	if query == "" {
		return m.items
	}

	query = strings.ToLower(query)
	var result []PickerItem
	for _, item := range m.items {
		if strings.Contains(strings.ToLower(item.FilterValue()), query) {
			result = append(result, item)
		}
	}
	return result
}

func (m pickerModel) View() string {
	if m.quitting {
		return ""
	}

	var b strings.Builder

	// Title
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(m.styles.theme.Primary).
		MarginBottom(1)
	b.WriteString(titleStyle.Render(m.title) + "\n\n")

	// Loading state
	if m.loading {
		b.WriteString(m.spinner.View() + " " + m.styles.Muted.Render(m.loadingMsg) + "\n")
		return b.String()
	}

	// Input
	b.WriteString(m.textInput.View() + "\n\n")

	// Items
	if len(m.filtered) == 0 {
		b.WriteString(m.styles.Muted.Render("No matches found"))
	} else {
		// Calculate visible range
		start := m.scrollOffset
		end := start + m.maxVisible
		if end > len(m.filtered) {
			end = len(m.filtered)
		}

		for i := start; i < end; i++ {
			item := m.filtered[i]
			cursor := "  "
			style := m.styles.Body

			if i == m.cursor {
				cursor = m.styles.Cursor.Render("> ")
				style = m.styles.Selected
			}

			line := cursor + style.Render(item.Title)
			if item.Description != "" {
				line += m.styles.Muted.Render(" - " + item.Description)
			}
			b.WriteString(line + "\n")
		}

		// Show scroll indicator if needed
		if len(m.filtered) > m.maxVisible {
			showing := fmt.Sprintf("\n%s", m.styles.Muted.Render(
				fmt.Sprintf("Showing %d-%d of %d", start+1, end, len(m.filtered)),
			))
			b.WriteString(showing)
		}
	}

	// Help
	helpStyle := m.styles.Muted.Padding(1, 0, 0, 0)
	b.WriteString("\n" + helpStyle.Render("↑↓ navigate • enter select • esc cancel"))

	return b.String()
}

// ItemLoader is a function that loads items asynchronously.
type ItemLoader func() ([]PickerItem, error)

// Picker shows a fuzzy-search picker and returns the selected item.
type Picker struct {
	items  []PickerItem
	opts   []PickerOption
	loader ItemLoader
}

// NewPicker creates a new picker.
func NewPicker(items []PickerItem, opts ...PickerOption) *Picker {
	return &Picker{
		items: items,
		opts:  opts,
	}
}

// NewPickerWithLoader creates a picker that loads items asynchronously.
func NewPickerWithLoader(loader ItemLoader, opts ...PickerOption) *Picker {
	return &Picker{
		loader: loader,
		opts:   opts,
	}
}

// Run shows the picker and returns the selected item.
// Returns nil if the user canceled.
func (p *Picker) Run() (*PickerItem, error) {
	if p.loader != nil {
		return p.runWithLoader()
	}

	m := newPickerModel(p.items, p.opts...)
	program := tea.NewProgram(m)

	finalModel, err := program.Run()
	if err != nil {
		return nil, err
	}

	final := finalModel.(pickerModel) //nolint:errcheck // type assertion always succeeds here
	if final.quitting {
		return nil, nil
	}
	return final.selected, nil
}

func (p *Picker) runWithLoader() (*PickerItem, error) {
	opts := append(p.opts, WithLoading("Loading..."))
	m := newPickerModel(nil, opts...)
	program := tea.NewProgram(m)

	// Load items in background
	go func() {
		items, err := p.loader()
		program.Send(PickerItemsLoadedMsg{Items: items, Err: err})
	}()

	finalModel, err := program.Run()
	if err != nil {
		return nil, err
	}

	final := finalModel.(pickerModel) //nolint:errcheck // type assertion always succeeds here
	if final.quitting {
		return nil, nil
	}
	return final.selected, nil
}

// Pick is a convenience function for simple picking.
func Pick(title string, items []PickerItem) (*PickerItem, error) {
	return NewPicker(items, WithPickerTitle(title)).Run()
}

// PickProject shows a picker for projects.
func PickProject(projects []PickerItem) (*PickerItem, error) {
	return Pick("Select a project", projects)
}

// PickTodolist shows a picker for todolists.
func PickTodolist(todolists []PickerItem) (*PickerItem, error) {
	return Pick("Select a todolist", todolists)
}

// PickPerson shows a picker for people.
func PickPerson(people []PickerItem) (*PickerItem, error) {
	return Pick("Select a person", people)
}

// PickAccount shows a picker for accounts.
func PickAccount(accounts []PickerItem) (*PickerItem, error) {
	return Pick("Select an account", accounts)
}

// PickHost shows a picker for hosts/environments.
func PickHost(hosts []PickerItem) (*PickerItem, error) {
	return Pick("Select a host", hosts)
}
