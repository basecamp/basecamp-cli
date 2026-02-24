package views

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/recents"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// cardTrashResultMsg is sent after a trash operation on a card.
type cardTrashResultMsg struct {
	itemID string
	err    error
}

// cardTrashTimeoutMsg resets the double-press trash confirmation.
type cardTrashTimeoutMsg struct{}

// cardsKeyMap defines card-specific keybindings.
type cardsKeyMap struct {
	Left  key.Binding
	Right key.Binding
	Up    key.Binding
	Down  key.Binding
	Move  key.Binding
	New   key.Binding
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
		New: key.NewBinding(
			key.WithKeys("n"),
			key.WithHelp("n", "new card"),
		),
	}
}

// Cards is the kanban board view for a card table.
type Cards struct {
	session *workspace.Session
	pool    *data.MutatingPool[[]data.CardColumnInfo]
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

	// Inline creation
	creating    bool
	createInput textinput.Model

	// Data (local copy from pool for rendering)
	columns []workspace.CardColumnInfo

	// Double-press trash confirmation
	trashPending   bool
	trashPendingID string
}

// NewCards creates the kanban board cards view.
func NewCards(session *workspace.Session) *Cards {
	styles := session.Styles()
	scope := session.Scope()
	pool := session.Hub().Cards(scope.ProjectID, scope.ToolID)

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	kanban := widget.NewKanban(styles)

	ti := textinput.New()
	ti.Placeholder = "New card..."
	ti.CharLimit = 256

	return &Cards{
		session:     session,
		pool:        pool,
		styles:      styles,
		keys:        defaultCardsKeyMap(),
		kanban:      kanban,
		spinner:     s,
		loading:     true,
		createInput: ti,
	}
}

// Title implements View.
func (v *Cards) Title() string {
	return "Card Table"
}

// InputActive implements workspace.InputCapturer.
func (v *Cards) InputActive() bool {
	return v.creating || v.moving
}

// IsModal implements workspace.ModalActive.
func (v *Cards) IsModal() bool {
	return v.moving || v.creating
}

// ShortHelp implements View.
func (v *Cards) ShortHelp() []key.Binding {
	if v.creating {
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "create")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		}
	}
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
		v.keys.New,
		key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "trash")),
		key.NewBinding(key.WithKeys("b", "B"), key.WithHelp("b", "boost")),
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
	snap := v.pool.Get()
	if snap.Usable() {
		v.columns = snap.Data
		v.syncKanban()
		v.loading = false
		if snap.Fresh() {
			return nil
		}
	}
	return tea.Batch(v.spinner.Tick, v.pool.FetchIfStale(v.session.Hub().ProjectContext()))
}

// Update implements tea.Model.
func (v *Cards) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case data.PoolUpdatedMsg:
		if msg.Key == v.pool.Key() {
			snap := v.pool.Get()
			if snap.Usable() {
				v.columns = snap.Data
				v.syncKanban()
				v.loading = false
			}
			if snap.State == data.StateError {
				v.loading = false
				return v, workspace.ReportError(snap.Err, "loading card table")
			}
			if snap.Loading() && !snap.HasData {
				v.loading = true
			}
		}
		return v, nil

	case data.MutationErrorMsg:
		if msg.Key == v.pool.Key() {
			// Mutation failed — pool already rolled back. Re-read columns.
			snap := v.pool.Get()
			if snap.Usable() {
				v.columns = snap.Data
				v.syncKanban()
			}
			return v, workspace.ReportError(msg.Err, "moving card")
		}
		return v, nil

	case cardTrashResultMsg:
		if msg.err != nil {
			return v, workspace.ReportError(msg.err, "trashing card")
		}
		v.pool.Invalidate()
		return v, tea.Batch(
			workspace.SetStatus("Trashed", false),
			v.pool.Fetch(v.session.Hub().ProjectContext()),
		)

	case cardTrashTimeoutMsg:
		v.trashPending = false
		v.trashPendingID = ""
		return v, nil

	case workspace.RefreshMsg:
		v.pool.Invalidate()
		v.loading = true
		return v, tea.Batch(v.spinner.Tick, v.pool.Fetch(v.session.Hub().ProjectContext()))

	case workspace.BoostCreatedMsg:
		// Optimistically update the boost count in the card
		if msg.Target.RecordingID != 0 {
			for colIdx, col := range v.columns {
				for cardIdx, card := range col.Cards {
					if card.ID == msg.Target.RecordingID {
						// Update the local data
						v.columns[colIdx].Cards[cardIdx].BoostsSummary.Count++
						v.syncKanban()
						break
					}
				}
			}
		}
		return v, nil

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
		if v.creating {
			return v, v.handleCreatingKey(msg)
		}
		if v.moving {
			return v, v.handleMoveKey(msg)
		}
		return v, v.handleKey(msg)
	}
	return v, nil
}

func (v *Cards) handleKey(msg tea.KeyMsg) tea.Cmd {
	// Reset trash confirmation on non-t keys
	if msg.String() != "t" {
		v.trashPending = false
		v.trashPendingID = ""
	}

	listKeys := workspace.DefaultListKeyMap()

	switch {
	case msg.String() == "t":
		return v.trashFocusedCard()
	case msg.String() == "b" || msg.String() == "B":
		return v.boostFocusedCard()

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
	case key.Matches(msg, v.keys.New):
		return v.enterCreateMode()
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

	targetDeferred := v.columns[v.moveTargetCol].Deferred

	cmd := v.pool.Apply(v.session.Hub().ProjectContext(), data.CardMoveMutation{
		CardID:         v.moveSourceCard,
		SourceColIdx:   v.moveSourceCol,
		TargetColIdx:   v.moveTargetCol,
		TargetColumnID: targetColumnID,
		Client:         v.session.AccountClient(),
		ProjectID:      v.session.Scope().ProjectID,
	})

	// Read optimistic state immediately and render
	snap := v.pool.Get()
	if snap.Usable() {
		v.columns = snap.Data
		v.syncKanban()
	}

	// If moved to a deferred column (renders as count-only placeholder),
	// keep kanban focus on the source column's next card.
	if targetDeferred {
		v.kanban.FocusColumn(v.moveSourceCol)
	}

	return cmd
}

func (v *Cards) enterCreateMode() tea.Cmd {
	if v.kanban.FocusedColumn() >= len(v.columns) {
		return nil
	}
	col := v.columns[v.kanban.FocusedColumn()]
	if col.Deferred {
		return workspace.SetStatus("Cannot create in deferred column", false)
	}
	v.creating = true
	v.createInput.Reset()
	v.createInput.Focus()
	return textinput.Blink
}

func (v *Cards) handleCreatingKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		title := strings.TrimSpace(v.createInput.Value())
		if title == "" {
			v.creating = false
			return nil
		}
		v.creating = false
		return v.createCard(title)
	case "esc":
		v.creating = false
		return nil
	default:
		var cmd tea.Cmd
		v.createInput, cmd = v.createInput.Update(msg)
		return cmd
	}
}

func (v *Cards) createCard(title string) tea.Cmd {
	colIdx := v.kanban.FocusedColumn()
	if colIdx >= len(v.columns) {
		return nil
	}
	col := v.columns[colIdx]

	cmd := v.pool.Apply(v.session.Hub().ProjectContext(), &data.CardCreateMutation{
		Title:     title,
		ColumnID:  col.ID,
		ProjectID: v.session.Scope().ProjectID,
		Client:    v.session.AccountClient(),
	})

	// Read optimistic state immediately and render
	snap := v.pool.Get()
	if snap.Usable() {
		v.columns = snap.Data
		v.syncKanban()
	}

	return cmd
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

	board := v.kanban.View()
	if v.creating {
		theme := v.styles.Theme()
		prefix := lipgloss.NewStyle().Foreground(theme.Muted).Render("  + ")
		board += "\n" + prefix + v.createInput.View()
	}
	return board
}

func (v *Cards) renderMoveMode() string {
	theme := v.styles.Theme()

	// Show a header indicating move mode
	var header strings.Builder
	header.WriteString(lipgloss.NewStyle().Bold(true).Foreground(theme.Warning).Render("MOVE"))
	header.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).Render(" > "))
	for i, col := range v.columns {
		style := lipgloss.NewStyle().Foreground(theme.Muted)
		if col.Deferred {
			style = style.Faint(true)
		}
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
			assignees := strings.Join(card.Assignees, ", ")
			var stepsProgress string
			if card.StepsTotal > 0 {
				stepsProgress = fmt.Sprintf("%d/%d", card.StepsDone, card.StepsTotal)
			}
			items = append(items, widget.KanbanCard{
				ID:            fmt.Sprintf("%d", card.ID),
				Title:         card.Title,
				Assignees:     assignees,
				DueOn:         card.DueOn,
				StepsProgress: stepsProgress,
				CommentsCount: card.CommentsCount,
				Completed:     card.Completed,
				Boosts:        card.GetBoosts().Count,
			})
		}
		cols = append(cols, widget.KanbanColumn{
			ID:       fmt.Sprintf("%d", col.ID),
			Title:    col.Title,
			Color:    col.Color,
			Deferred: col.Deferred,
			Count:    col.CardsCount,
			Items:    items,
		})
	}
	v.kanban.SetColumns(cols)
}

func (v *Cards) trashFocusedCard() tea.Cmd {
	card := v.kanban.FocusedCard()
	if card == nil {
		return nil
	}

	var cardID int64
	fmt.Sscanf(card.ID, "%d", &cardID)

	if v.trashPending && v.trashPendingID == card.ID {
		v.trashPending = false
		v.trashPendingID = ""
		scope := v.session.Scope()
		hub := v.session.Hub()
		ctx := hub.ProjectContext()
		itemID := card.ID
		return func() tea.Msg {
			err := hub.TrashRecording(ctx, scope.AccountID, scope.ProjectID, cardID)
			return cardTrashResultMsg{itemID: itemID, err: err}
		}
	}
	v.trashPending = true
	v.trashPendingID = card.ID
	return tea.Batch(
		workspace.SetStatus("Press t again to trash", false),
		tea.Tick(3*time.Second, func(time.Time) tea.Msg { return cardTrashTimeoutMsg{} }),
	)
}

func (v *Cards) boostFocusedCard() tea.Cmd {
	card := v.kanban.FocusedCard()
	if card == nil {
		return nil
	}
	var cardID int64
	fmt.Sscanf(card.ID, "%d", &cardID)
	return func() tea.Msg {
		return workspace.OpenBoostPickerMsg{
			Target: workspace.BoostTarget{
				ProjectID:   v.session.Scope().ProjectID,
				RecordingID: cardID,
				Title:       card.Title,
			},
		}
	}
}
