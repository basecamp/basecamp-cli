package views

import (
	"fmt"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/recents"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// cardsKeyMap defines card-specific keybindings.
type cardsKeyMap struct {
	Left  key.Binding
	Right key.Binding
	Up    key.Binding
	Down  key.Binding
	Move  key.Binding
}

func defaultCardsKeyMap() cardsKeyMap {
	return cardsKeyMap{
		Left: key.NewBinding(
			key.WithKeys("h"),
			key.WithHelp("h", "prev column"),
		),
		Right: key.NewBinding(
			key.WithKeys("l"),
			key.WithHelp("l", "next column"),
		),
		Up: key.NewBinding(
			key.WithKeys("k"),
			key.WithHelp("k", "prev card"),
		),
		Down: key.NewBinding(
			key.WithKeys("j"),
			key.WithHelp("j", "next card"),
		),
		Move: key.NewBinding(
			key.WithKeys("m"),
			key.WithHelp("m", "move card"),
		),
	}
}

// Cards is the kanban board view for a card table.
type Cards struct {
	session *workspace.Session
	store   *data.Store
	styles  *tui.Styles
	keys    cardsKeyMap

	// Layout
	kanban        *widget.Kanban
	width, height int

	// Loading
	spinner spinner.Model
	loading bool

	// Move mode
	moving         bool
	moveSourceCol  int   // column index the card is moving from
	moveSourceCard int64 // card ID being moved
	moveTargetCol  int   // column index currently highlighted as target

	// Data (local copy for optimistic moves and rendering)
	columns []workspace.CardColumnInfo
}

// NewCards creates the kanban board cards view.
func NewCards(session *workspace.Session, store *data.Store) *Cards {
	styles := session.Styles()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	kanban := widget.NewKanban(styles)

	return &Cards{
		session: session,
		store:   store,
		styles:  styles,
		keys:    defaultCardsKeyMap(),
		kanban:  kanban,
		spinner: s,
		loading: true,
	}
}

// Title implements View.
func (v *Cards) Title() string {
	return "Card Table"
}

// IsModal implements workspace.ModalActive.
func (v *Cards) IsModal() bool {
	return v.moving
}

// ShortHelp implements View.
func (v *Cards) ShortHelp() []key.Binding {
	if v.moving {
		return []key.Binding{
			key.NewBinding(key.WithKeys("h/l"), key.WithHelp("h/l", "pick column")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		}
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("h/l"), key.WithHelp("h/l", "columns")),
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "cards")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		v.keys.Move,
	}
}

// FullHelp implements View.
func (v *Cards) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

// SetSize implements View.
func (v *Cards) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.kanban.SetSize(w, h)
}

// Init implements tea.Model.
func (v *Cards) Init() tea.Cmd {
	return tea.Batch(v.spinner.Tick, v.fetchColumns())
}

// Update implements tea.Model.
func (v *Cards) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case workspace.CardColumnsLoadedMsg:
		v.loading = false
		if msg.Err != nil {
			return v, workspace.ReportError(msg.Err, "loading card table")
		}
		v.columns = msg.Columns
		v.syncKanban()
		return v, nil

	case workspace.CardMovedMsg:
		if msg.Err != nil {
			v.revertMoveScoped(msg.CardID, msg.ColumnID, msg.SourceColIdx)
			return v, workspace.ReportError(msg.Err, "moving card")
		}
		return v, nil

	case workspace.RefreshMsg:
		v.loading = true
		return v, tea.Batch(v.spinner.Tick, v.fetchColumns())

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
		if v.moving {
			return v, v.handleMoveKey(msg)
		}
		return v, v.handleKey(msg)
	}
	return v, nil
}

func (v *Cards) handleKey(msg tea.KeyMsg) tea.Cmd {
	listKeys := workspace.DefaultListKeyMap()

	switch {
	case key.Matches(msg, listKeys.Open):
		return v.openFocusedCard()
	case key.Matches(msg, v.keys.Left):
		v.kanban.MoveLeft()
	case key.Matches(msg, v.keys.Right):
		v.kanban.MoveRight()
	case key.Matches(msg, v.keys.Up):
		v.kanban.MoveUp()
	case key.Matches(msg, v.keys.Down):
		v.kanban.MoveDown()
	case key.Matches(msg, v.keys.Move):
		return v.enterMoveMode()
	}
	return nil
}

func (v *Cards) openFocusedCard() tea.Cmd {
	card := v.kanban.FocusedCard()
	if card == nil {
		return nil
	}

	var cardID int64
	fmt.Sscanf(card.ID, "%d", &cardID)

	if r := v.session.Recents(); r != nil {
		r.Add(recents.Item{
			ID:          card.ID,
			Title:       card.Title,
			Description: "Card",
			Type:        recents.TypeRecording,
			AccountID:   v.session.Scope().AccountID,
			ProjectID:   fmt.Sprintf("%d", v.session.Scope().ProjectID),
		})
	}

	scope := v.session.Scope()
	scope.RecordingID = cardID
	scope.RecordingType = "Card"
	return workspace.Navigate(workspace.ViewDetail, scope)
}

func (v *Cards) handleMoveKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "h":
		if v.moveTargetCol > 0 {
			v.moveTargetCol--
		}
	case "l":
		if v.moveTargetCol < len(v.columns)-1 {
			v.moveTargetCol++
		}
	case "enter":
		return v.confirmMove()
	case "esc":
		v.moving = false
	}
	return nil
}

func (v *Cards) enterMoveMode() tea.Cmd {
	card := v.kanban.FocusedCard()
	if card == nil {
		return nil
	}

	var cardID int64
	fmt.Sscanf(card.ID, "%d", &cardID)

	v.moving = true
	v.moveSourceCol = v.kanban.FocusedColumn()
	v.moveSourceCard = cardID
	v.moveTargetCol = v.moveSourceCol

	return workspace.SetStatus("Move mode: h/l to pick column, Enter to confirm, Esc to cancel", false)
}

func (v *Cards) confirmMove() tea.Cmd {
	v.moving = false

	if v.moveTargetCol == v.moveSourceCol {
		return nil // no-op, same column
	}
	if v.moveTargetCol < 0 || v.moveTargetCol >= len(v.columns) {
		return nil
	}

	targetColumnID := v.columns[v.moveTargetCol].ID

	// Optimistic move: relocate card in local data
	sourceColIdx := v.moveSourceCol
	v.optimisticMove(v.moveSourceCard, sourceColIdx, v.moveTargetCol)
	v.syncKanban()

	return v.moveCard(v.moveSourceCard, targetColumnID, sourceColIdx)
}

func (v *Cards) optimisticMove(cardID int64, fromColIdx, toColIdx int) {
	if fromColIdx < 0 || fromColIdx >= len(v.columns) || toColIdx < 0 || toColIdx >= len(v.columns) {
		return
	}

	// Find and remove from source
	src := &v.columns[fromColIdx]
	var moved *workspace.CardInfo
	for i, c := range src.Cards {
		if c.ID == cardID {
			moved = &src.Cards[i]
			src.Cards = append(src.Cards[:i], src.Cards[i+1:]...)
			break
		}
	}
	if moved == nil {
		return
	}

	// Append to target
	dst := &v.columns[toColIdx]
	dst.Cards = append(dst.Cards, *moved)
}

// View implements tea.Model.
func (v *Cards) View() string {
	if v.loading {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Loading card table...")
	}

	if len(v.columns) == 0 {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Foreground(v.styles.Theme().Muted).
			Render("No columns found in this card table.")
	}

	if v.moving {
		return v.renderMoveMode()
	}

	return v.kanban.View()
}

func (v *Cards) renderMoveMode() string {
	theme := v.styles.Theme()

	// Show a header indicating move mode
	var header strings.Builder
	header.WriteString(lipgloss.NewStyle().Bold(true).Foreground(theme.Warning).Render("MOVE"))
	header.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).Render(" > "))
	for i, col := range v.columns {
		style := lipgloss.NewStyle().Foreground(theme.Muted)
		if i == v.moveTargetCol {
			style = lipgloss.NewStyle().Bold(true).Foreground(theme.Primary).Underline(true)
		}
		if i == v.moveSourceCol {
			style = style.Italic(true)
		}
		header.WriteString(style.Render(col.Title))
		if i < len(v.columns)-1 {
			header.WriteString("  ")
		}
	}
	header.WriteString("\n\n")

	boardHeight := v.height - 3 // account for header
	if boardHeight < 1 {
		boardHeight = 1
	}
	v.kanban.SetSize(v.width, boardHeight)
	board := v.kanban.View()
	v.kanban.SetSize(v.width, v.height) // restore

	return header.String() + board
}

// syncKanban rebuilds the kanban widget columns from local data.
func (v *Cards) syncKanban() {
	cols := make([]widget.KanbanColumn, 0, len(v.columns))
	for _, col := range v.columns {
		items := make([]widget.KanbanCard, 0, len(col.Cards))
		for _, card := range col.Cards {
			subtitle := ""
			if card.DueOn != "" {
				subtitle = card.DueOn
			}
			if len(card.Assignees) > 0 {
				who := strings.Join(card.Assignees, ", ")
				if subtitle != "" {
					subtitle += " - "
				}
				subtitle += who
			}
			items = append(items, widget.KanbanCard{
				ID:       fmt.Sprintf("%d", card.ID),
				Title:    card.Title,
				Subtitle: subtitle,
			})
		}
		cols = append(cols, widget.KanbanColumn{
			ID:    fmt.Sprintf("%d", col.ID),
			Title: col.Title,
			Items: items,
		})
	}
	v.kanban.SetColumns(cols)
}

// -- Commands (tea.Cmd factories)

func (v *Cards) fetchColumns() tea.Cmd {
	scope := v.session.Scope()
	ctx := v.session.Context()
	client := v.session.AccountClient()
	return func() tea.Msg {
		// 1. Get card table to learn columns
		cardTable, err := client.CardTables().Get(ctx, scope.ProjectID, scope.ToolID)
		if err != nil {
			return workspace.CardColumnsLoadedMsg{Err: err}
		}

		// 2. Fetch cards for each column in parallel
		type colResult struct {
			idx   int
			cards []workspace.CardInfo
			err   error
		}
		results := make([]colResult, len(cardTable.Lists))
		var wg sync.WaitGroup
		for i, col := range cardTable.Lists {
			wg.Add(1)
			go func(idx int, columnID int64) {
				defer wg.Done()
				listResult, err := client.Cards().List(ctx, scope.ProjectID, columnID, &basecamp.CardListOptions{})
				if err != nil {
					results[idx] = colResult{idx: idx, err: err}
					return
				}
				cards := make([]workspace.CardInfo, 0, len(listResult.Cards))
				for _, c := range listResult.Cards {
					names := make([]string, 0, len(c.Assignees))
					for _, a := range c.Assignees {
						names = append(names, a.Name)
					}
					cards = append(cards, workspace.CardInfo{
						ID:        c.ID,
						Title:     c.Title,
						Assignees: names,
						DueOn:     c.DueOn,
						Position:  c.Position,
					})
				}
				results[idx] = colResult{idx: idx, cards: cards}
			}(i, col.ID)
		}
		wg.Wait()

		// 3. Assemble columns
		columns := make([]workspace.CardColumnInfo, 0, len(cardTable.Lists))
		for i, col := range cardTable.Lists {
			if results[i].err != nil {
				return workspace.CardColumnsLoadedMsg{Err: fmt.Errorf("loading cards for %q: %w", col.Title, results[i].err)}
			}
			columns = append(columns, workspace.CardColumnInfo{
				ID:    col.ID,
				Title: col.Title,
				Color: col.Color,
				Cards: results[i].cards,
			})
		}

		return workspace.CardColumnsLoadedMsg{Columns: columns}
	}
}

func (v *Cards) moveCard(cardID, targetColumnID int64, sourceColIdx int) tea.Cmd {
	scope := v.session.Scope()
	ctx := v.session.Context()
	client := v.session.AccountClient()
	return func() tea.Msg {
		err := client.Cards().Move(ctx, scope.ProjectID, cardID, targetColumnID)
		return workspace.CardMovedMsg{CardID: cardID, ColumnID: targetColumnID, SourceColIdx: sourceColIdx, Err: err}
	}
}

// revertMoveScoped uses the request-scoped source column index for safe rollback.
func (v *Cards) revertMoveScoped(cardID, failedTargetColumnID int64, sourceColIdx int) {
	// Find the card in the failed target column
	var fromIdx = -1
	for i, col := range v.columns {
		if col.ID == failedTargetColumnID {
			fromIdx = i
			break
		}
	}
	if fromIdx < 0 || sourceColIdx < 0 || sourceColIdx >= len(v.columns) {
		return
	}
	v.optimisticMove(cardID, fromIdx, sourceColIdx)
	v.syncKanban()
}
