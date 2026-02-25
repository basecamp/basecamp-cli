package views

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/empty"
	"github.com/basecamp/basecamp-cli/internal/tui/format"
	"github.com/basecamp/basecamp-cli/internal/tui/recents"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

const searchDebounce = 400 * time.Millisecond

// searchKeyMap defines search-specific keybindings.
type searchKeyMap struct {
	Submit key.Binding
	Select key.Binding
}

func defaultSearchKeyMap() searchKeyMap {
	return searchKeyMap{
		Submit: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "search / open"),
		),
		Select: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "focus results"),
		),
	}
}

// searchFocus tracks whether the text input or list has focus.
type searchFocus int

const (
	searchFocusInput searchFocus = iota
	searchFocusList
)

// searchDebounceMsg is sent after the debounce timer expires.
type searchDebounceMsg struct {
	query string
	seq   int
}

// Search is the full-screen search view with text input and results list.
// When multiple accounts are available, search fans out across all accounts.
type Search struct {
	session *workspace.Session
	styles  *tui.Styles
	keys    searchKeyMap

	// Layout
	textInput     textinput.Model
	list          *widget.List
	focus         searchFocus
	width, height int

	// State
	query       string // last submitted/debounced query
	debounceSeq int    // monotonic counter to discard stale debounce msgs
	searching   bool
	spinner     spinner.Model

	// Result metadata (not in list widget)
	resultMeta map[string]workspace.SearchResultInfo // keyed by item ID
}

// NewSearch creates the search view.
func NewSearch(session *workspace.Session) *Search {
	styles := session.Styles()

	ti := textinput.New()
	ti.Placeholder = "Search Basecamp..."
	ti.CharLimit = 256
	ti.Focus()

	list := widget.NewList(styles)
	list.SetEmptyMessage(empty.NoSearchResults(""))
	list.SetFocused(false)

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	return &Search{
		session:    session,
		styles:     styles,
		keys:       defaultSearchKeyMap(),
		textInput:  ti,
		list:       list,
		focus:      searchFocusInput,
		spinner:    s,
		resultMeta: make(map[string]workspace.SearchResultInfo),
	}
}

// Title implements View.
func (v *Search) Title() string {
	return "Search"
}

// IsModal implements workspace.ModalActive.
func (v *Search) IsModal() bool {
	return v.focus == searchFocusList
}

// InputActive implements workspace.InputCapturer.
// Active only when the text input has focus (not the results list),
// or the results list is in filter mode.
func (v *Search) InputActive() bool {
	return v.focus == searchFocusInput || v.list.Filtering()
}

// StartFilter implements workspace.Filterable.
func (v *Search) StartFilter() { v.list.StartFilter() }

// FocusedItem implements workspace.FocusedRecording.
func (v *Search) FocusedItem() workspace.FocusedItemScope {
	item := v.list.Selected()
	if item == nil {
		return workspace.FocusedItemScope{}
	}
	meta, ok := v.resultMeta[item.ID]
	if !ok {
		return workspace.FocusedItemScope{}
	}
	return workspace.FocusedItemScope{
		AccountID:   meta.AccountID,
		ProjectID:   meta.ProjectID,
		RecordingID: meta.ID,
	}
}

// ShortHelp implements View.
func (v *Search) ShortHelp() []key.Binding {
	if v.list.Filtering() {
		return filterHints()
	}
	return []key.Binding{
		v.keys.Submit,
		v.keys.Select,
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
	}
}

// FullHelp implements View.
func (v *Search) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

// SetSize implements View.
func (v *Search) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.textInput.Width = max(0, w-4)
	// Reserve 2 lines for the input bar + separator
	listHeight := h - 2
	if listHeight < 1 {
		listHeight = 1
	}
	v.list.SetSize(w, listHeight)
}

// Init implements tea.Model.
func (v *Search) Init() tea.Cmd {
	return textinput.Blink
}

// Update implements tea.Model.
func (v *Search) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case workspace.SearchResultsMsg:
		return v, v.handleResults(msg)

	case workspace.RefreshMsg:
		if v.query != "" {
			v.searching = true
			return v, tea.Batch(v.spinner.Tick, v.fetchResults(v.query))
		}
		return v, nil

	case searchDebounceMsg:
		if msg.seq == v.debounceSeq && msg.query != v.query {
			v.query = msg.query
			v.searching = true
			return v, tea.Batch(v.spinner.Tick, v.fetchResults(msg.query))
		}
		return v, nil

	case spinner.TickMsg:
		if v.searching {
			var cmd tea.Cmd
			v.spinner, cmd = v.spinner.Update(msg)
			return v, cmd
		}

	case tea.KeyMsg:
		return v, v.handleKey(msg)
	}
	return v, nil
}

func (v *Search) handleResults(msg workspace.SearchResultsMsg) tea.Cmd {
	// Discard stale results from a superseded query
	if msg.Query != v.query {
		return nil
	}
	v.searching = false
	if msg.Err != nil {
		return workspace.ReportError(msg.Err, "searching")
	}

	v.resultMeta = make(map[string]workspace.SearchResultInfo, len(msg.Results))
	accounts := sessionAccounts(v.session)
	items := make([]widget.ListItem, 0, len(msg.Results))
	for _, r := range msg.Results {
		id := fmt.Sprintf("%d", r.ID)
		v.resultMeta[id] = r
		desc := r.Excerpt
		if desc == "" {
			desc = r.Project
			if r.Account != "" {
				desc = r.Account + " > " + desc
			}
		}
		items = append(items, widget.ListItem{
			ID:          id,
			Title:       r.Title,
			Description: desc,
			Extra:       accountExtra(accounts, r.AccountID, r.Type),
		})
	}
	v.list.SetItems(items)

	if len(items) == 0 {
		v.list.SetEmptyMessage(empty.NoSearchResults(msg.Query))
	}

	if msg.PartialErr != nil {
		return workspace.SetStatus(msg.PartialErr.Error(), true)
	}
	return nil
}

func (v *Search) handleKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, v.keys.Select):
		return v.toggleFocus()

	case key.Matches(msg, v.keys.Submit):
		if v.focus == searchFocusInput {
			return v.submitQuery()
		}
		return v.openSelected()

	default:
		if v.focus == searchFocusInput {
			// Esc while typing navigates back (exits search)
			if msg.String() == "esc" {
				return workspace.NavigateBack()
			}
			return v.updateInput(msg)
		}

		switch msg.String() {
		case "esc":
			// Esc while browsing results returns to input
			v.focus = searchFocusInput
			v.list.SetFocused(false)
			v.textInput.Focus()
			return nil
		default:
			return v.list.Update(msg)
		}
	}
}

func (v *Search) toggleFocus() tea.Cmd {
	if v.focus == searchFocusInput {
		v.focus = searchFocusList
		v.textInput.Blur()
		v.list.SetFocused(true)
	} else {
		v.focus = searchFocusInput
		v.list.SetFocused(false)
		v.textInput.Focus()
	}
	return nil
}

func (v *Search) submitQuery() tea.Cmd {
	q := strings.TrimSpace(v.textInput.Value())
	if q == "" || q == v.query {
		return nil
	}
	v.query = q
	v.searching = true
	return tea.Batch(v.spinner.Tick, v.fetchResults(q))
}

func (v *Search) openSelected() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}

	meta, ok := v.resultMeta[item.ID]
	if !ok {
		return nil
	}

	scope := v.session.Scope()
	scope.RecordingID = meta.ID
	scope.RecordingType = meta.Type
	if meta.ProjectID != 0 {
		scope.ProjectID = meta.ProjectID
	}
	if meta.AccountID != "" {
		scope.AccountID = meta.AccountID
	}

	if r := v.session.Recents(); r != nil {
		r.Add(recents.Item{
			ID:          item.ID,
			Title:       item.Title,
			Description: meta.Type,
			Type:        recents.TypeRecording,
			AccountID:   scope.AccountID,
			ProjectID:   fmt.Sprintf("%d", scope.ProjectID),
		})
	}

	return workspace.Navigate(workspace.ViewDetail, scope)
}

func (v *Search) updateInput(msg tea.KeyMsg) tea.Cmd {
	var cmd tea.Cmd
	v.textInput, cmd = v.textInput.Update(msg)

	// Schedule a debounced search if the input changed
	q := strings.TrimSpace(v.textInput.Value())
	if q != "" && q != v.query {
		v.debounceSeq++
		seq := v.debounceSeq
		query := q
		debounceCmd := tea.Tick(searchDebounce, func(time.Time) tea.Msg {
			return searchDebounceMsg{query: query, seq: seq}
		})
		return tea.Batch(cmd, debounceCmd)
	}
	return cmd
}

// View implements tea.Model.
func (v *Search) View() string {
	if v.width == 0 || v.height == 0 {
		return ""
	}

	theme := v.styles.Theme()

	// Input line
	inputLine := "  " + v.textInput.View()

	// Separator
	separator := lipgloss.NewStyle().
		Width(v.width).
		Foreground(theme.Border).
		Render(strings.Repeat("─", v.width))

	// Results
	var results string
	if v.searching {
		results = lipgloss.NewStyle().
			Padding(0, 2).
			Render(v.spinner.View() + " Searching...")
	} else {
		results = v.list.View()
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		inputLine,
		separator,
		results,
	)
}

// -- Commands

func (v *Search) fetchResults(query string) tea.Cmd {
	ms := v.session.MultiStore()
	ctx := v.session.Hub().Global().Context()
	client := v.session.AccountClient()
	accountID := v.session.Scope().AccountID
	return func() tea.Msg {
		accounts := ms.Accounts()

		// Multi-account search: fan out across all accounts
		if len(accounts) > 1 {
			return v.fetchMultiAccountResults(ctx, ms, query)
		}

		// Single-account fallback
		return v.fetchSingleAccountResults(ctx, client, accountID, query)
	}
}

func (v *Search) fetchMultiAccountResults(ctx context.Context, ms *data.MultiStore, query string) workspace.SearchResultsMsg {
	results := ms.FanOut(ctx, func(acct data.AccountInfo, client *basecamp.AccountClient) (any, error) {
		recs, err := client.Search().Search(ctx, query, &basecamp.SearchOptions{})
		if err != nil {
			return nil, err
		}
		var infos []workspace.SearchResultInfo
		for _, r := range recs {
			infos = append(infos, searchResultToInfo(r, acct.ID, acct.Name))
		}
		return infos, nil
	})

	var allResults []workspace.SearchResultInfo
	var failedAccounts []string
	succeeded := 0
	for _, r := range results {
		if r.Err != nil {
			failedAccounts = append(failedAccounts, r.Account.Name)
			continue
		}
		succeeded++
		if infos, ok := r.Data.([]workspace.SearchResultInfo); ok {
			allResults = append(allResults, infos...)
		}
	}

	// Sort by creation time descending (numeric timestamp)
	sort.Slice(allResults, func(i, j int) bool {
		return allResults[i].CreatedAtTS > allResults[j].CreatedAtTS
	})

	// Cap at 50
	if len(allResults) > 50 {
		allResults = allResults[:50]
	}

	msg := workspace.SearchResultsMsg{Results: allResults, Query: query}
	if len(failedAccounts) > 0 && succeeded > 0 {
		msg.PartialErr = fmt.Errorf("could not search: %s", strings.Join(failedAccounts, ", "))
	} else if len(failedAccounts) > 0 {
		msg.Err = fmt.Errorf("could not search: %s", strings.Join(failedAccounts, ", "))
	}
	return msg
}

func (v *Search) fetchSingleAccountResults(ctx context.Context, client *basecamp.AccountClient, accountID, query string) workspace.SearchResultsMsg {
	results, err := client.Search().Search(ctx, query, &basecamp.SearchOptions{})
	if err != nil {
		return workspace.SearchResultsMsg{Query: query, Err: err}
	}

	infos := make([]workspace.SearchResultInfo, 0, len(results))
	for _, r := range results {
		infos = append(infos, searchResultToInfo(r, accountID, ""))
	}
	return workspace.SearchResultsMsg{Results: infos, Query: query}
}

func searchResultToInfo(r basecamp.SearchResult, accountID, accountName string) workspace.SearchResultInfo {
	project := ""
	var projectID int64
	if r.Bucket != nil {
		project = r.Bucket.Name
		projectID = r.Bucket.ID
	}

	title := r.Title
	if title == "" {
		title = r.Subject
	}

	excerpt := r.Description
	if excerpt == "" && r.Content != "" {
		excerpt = truncateExcerpt(r.Content, 120)
	}

	return workspace.SearchResultInfo{
		ID:          r.ID,
		Title:       title,
		Excerpt:     excerpt,
		Type:        r.Type,
		Project:     project,
		ProjectID:   projectID,
		Account:     accountName,
		AccountID:   accountID,
		CreatedAt:   r.CreatedAt.Format("Jan 2"),
		CreatedAtTS: r.CreatedAt.Unix(),
	}
}

// truncateExcerpt truncates s to maxLen runes, appending "..." if truncated.
func truncateExcerpt(s string, maxLen int) string {
	s = strings.TrimSpace(format.StripHTML(s))
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
