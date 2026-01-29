package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// PageResult represents a page of items from a paginated API.
type PageResult struct {
	Items   []PickerItem
	HasMore bool
	// NextCursor can be used by the PageFetcher to track pagination state.
	// The paginated picker doesn't interpret this value; it just passes it back.
	NextCursor string
}

// PageFetcher fetches a page of items. It receives the cursor from the previous
// page (empty string for the first page) and returns the items and pagination info.
type PageFetcher func(ctx context.Context, cursor string) (*PageResult, error)

// paginatedPickerModel is the bubbletea model for a paginated fuzzy picker.
type paginatedPickerModel struct {
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

	// Pagination state
	fetcher     PageFetcher
	ctx         context.Context
	nextCursor  string
	hasMore     bool
	loadingMore bool
	totalLoaded int
	fetchError  error

	// Initial loading state
	initialLoading bool
	spinner        spinner.Model
	loadingMsg     string

	// Threshold for triggering next page fetch (items from bottom)
	fetchThreshold int
}

// PaginatedPickerOption configures a paginated picker.
type PaginatedPickerOption func(*paginatedPickerModel)

// WithPaginatedPickerTitle sets the picker title.
func WithPaginatedPickerTitle(title string) PaginatedPickerOption {
	return func(m *paginatedPickerModel) {
		m.title = title
	}
}

// WithPaginatedMaxVisible sets the maximum number of visible items.
func WithPaginatedMaxVisible(n int) PaginatedPickerOption {
	return func(m *paginatedPickerModel) {
		m.maxVisible = n
	}
}

// WithFetchThreshold sets how many items from the bottom triggers a fetch.
func WithFetchThreshold(n int) PaginatedPickerOption {
	return func(m *paginatedPickerModel) {
		m.fetchThreshold = n
	}
}

// WithLoadingMessage sets the loading message.
func WithLoadingMessage(msg string) PaginatedPickerOption {
	return func(m *paginatedPickerModel) {
		m.loadingMsg = msg
	}
}

func newPaginatedPickerModel(ctx context.Context, fetcher PageFetcher, opts ...PaginatedPickerOption) paginatedPickerModel {
	ti := textinput.New()
	ti.Placeholder = "Type to filter..."
	ti.Width = 40
	ti.Focus()

	s := spinner.New()
	s.Spinner = spinner.Dot
	styles := NewStyles()
	s.Style = lipgloss.NewStyle().Foreground(styles.theme.Primary)

	m := paginatedPickerModel{
		textInput:      ti,
		styles:         styles,
		title:          "Select an item",
		maxVisible:     10,
		fetcher:        fetcher,
		ctx:            ctx,
		hasMore:        true,
		initialLoading: true,
		spinner:        s,
		loadingMsg:     "Loading...",
		fetchThreshold: 3, // Fetch when 3 items from bottom
	}

	for _, opt := range opts {
		opt(&m)
	}

	return m
}

// pageLoadedMsg is sent when a page of items has been loaded.
type pageLoadedMsg struct {
	items      []PickerItem
	hasMore    bool
	nextCursor string
	err        error
	isInitial  bool
}

func (m paginatedPickerModel) fetchPage(isInitial bool) tea.Cmd {
	return func() tea.Msg {
		result, err := m.fetcher(m.ctx, m.nextCursor)
		if err != nil {
			return pageLoadedMsg{err: err, isInitial: isInitial}
		}
		return pageLoadedMsg{
			items:      result.Items,
			hasMore:    result.HasMore,
			nextCursor: result.NextCursor,
			isInitial:  isInitial,
		}
	}
}

func (m paginatedPickerModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.fetchPage(true))
}

func (m paginatedPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case pageLoadedMsg:
		if msg.err != nil {
			m.fetchError = msg.err
			if msg.isInitial {
				m.initialLoading = false
			}
			m.loadingMore = false
			// Don't quit on error, let user see error message and cancel
			return m, nil
		}

		m.items = append(m.items, msg.items...)
		m.totalLoaded = len(m.items)
		m.hasMore = msg.hasMore
		m.nextCursor = msg.nextCursor
		m.loadingMore = false
		m.fetchError = nil // Clear any previous transient error on success

		if msg.isInitial {
			m.initialLoading = false
			m.filtered = m.filter(m.textInput.Value())
			return m, textinput.Blink
		}

		// Re-filter with new items
		m.filtered = m.filter(m.textInput.Value())

		// If filter still yields no results and more pages exist, continue fetching
		// This auto-drains pages until matches are found or no more pages
		query := strings.TrimSpace(m.textInput.Value())
		if m.hasMore && len(m.filtered) == 0 && query != "" {
			m.loadingMore = true
			return m, tea.Batch(m.spinner.Tick, m.fetchPage(false))
		}
		return m, nil

	case spinner.TickMsg:
		if m.initialLoading || m.loadingMore {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}

	case tea.KeyMsg:
		// In initial loading state, only allow cancel
		if m.initialLoading {
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

			// Check if we need to fetch more items
			// Trigger when cursor is within fetchThreshold of the end of filtered results
			// This works both with and without a filter - new items will be filtered on arrival
			if m.hasMore && !m.loadingMore {
				itemsFromEnd := len(m.filtered) - 1 - m.cursor
				if itemsFromEnd < m.fetchThreshold {
					m.loadingMore = true
					return m, tea.Batch(m.spinner.Tick, m.fetchPage(false))
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

			// If filter yields no results but more pages exist, fetch more
			// This allows discovering matches beyond the initial page
			if m.hasMore && !m.loadingMore && len(m.filtered) == 0 && len(m.items) > 0 {
				m.loadingMore = true
				return m, tea.Batch(cmd, m.spinner.Tick, m.fetchPage(false))
			}
			return m, cmd
		}
	}

	return m, nil
}

func (m paginatedPickerModel) filter(query string) []PickerItem {
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

func (m paginatedPickerModel) View() string {
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

	// Initial loading state
	if m.initialLoading {
		b.WriteString(m.spinner.View() + " " + m.styles.Muted.Render(m.loadingMsg) + "\n")
		return b.String()
	}

	// Error state
	if m.fetchError != nil && len(m.items) == 0 {
		b.WriteString(m.styles.Error.Render("Error: "+m.fetchError.Error()) + "\n")
		b.WriteString(m.styles.Muted.Render("Press esc to cancel"))
		return b.String()
	}

	// Input
	b.WriteString(m.textInput.View() + "\n\n")

	// Items
	if len(m.filtered) == 0 {
		// Show loading indicator during auto-drain, otherwise "No matches found"
		if m.loadingMore {
			b.WriteString(m.spinner.View() + " " + m.styles.Muted.Render("Searching..."))
		} else {
			b.WriteString(m.styles.Muted.Render("No matches found"))
		}
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

		// Show status line
		var statusParts []string

		// Scroll/count indicator
		if len(m.filtered) > m.maxVisible || m.hasMore {
			if m.hasMore {
				statusParts = append(statusParts, fmt.Sprintf("Showing %d-%d of %d+", start+1, end, m.totalLoaded))
			} else {
				statusParts = append(statusParts, fmt.Sprintf("Showing %d-%d of %d", start+1, end, len(m.filtered)))
			}
		}

		// Loading more indicator
		if m.loadingMore {
			statusParts = append(statusParts, m.spinner.View()+" Loading more...")
		}

		// Error during pagination (but we have some items)
		if m.fetchError != nil && len(m.items) > 0 {
			statusParts = append(statusParts, m.styles.Error.Render("(error loading more)"))
		}

		if len(statusParts) > 0 {
			b.WriteString("\n" + m.styles.Muted.Render(strings.Join(statusParts, " ")))
		}
	}

	// Help
	helpStyle := m.styles.Muted.Padding(1, 0, 0, 0)
	b.WriteString("\n" + helpStyle.Render("↑↓ navigate • enter select • esc cancel"))

	return b.String()
}

// PaginatedPicker shows a fuzzy-search picker with progressive pagination.
type PaginatedPicker struct {
	fetcher PageFetcher
	opts    []PaginatedPickerOption
	ctx     context.Context
}

// NewPaginatedPicker creates a new paginated picker.
func NewPaginatedPicker(ctx context.Context, fetcher PageFetcher, opts ...PaginatedPickerOption) *PaginatedPicker {
	return &PaginatedPicker{
		fetcher: fetcher,
		opts:    opts,
		ctx:     ctx,
	}
}

// Run shows the picker and returns the selected item.
// Returns nil if the user canceled.
func (p *PaginatedPicker) Run() (*PickerItem, error) {
	m := newPaginatedPickerModel(p.ctx, p.fetcher, p.opts...)
	program := tea.NewProgram(m)

	finalModel, err := program.Run()
	if err != nil {
		return nil, err
	}

	final := finalModel.(paginatedPickerModel) //nolint:errcheck // type assertion always succeeds here
	if final.quitting {
		return nil, nil
	}
	if final.fetchError != nil && len(final.items) == 0 {
		return nil, final.fetchError
	}
	return final.selected, nil
}

// PickPaginated is a convenience function for paginated picking.
func PickPaginated(ctx context.Context, title string, fetcher PageFetcher) (*PickerItem, error) {
	return NewPaginatedPicker(ctx, fetcher,
		WithPaginatedPickerTitle(title),
	).Run()
}
