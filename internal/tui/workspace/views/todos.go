package views

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/recents"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// todosKeyMap defines todo-specific keybindings.
type todosKeyMap struct {
	Toggle    key.Binding
	New       key.Binding
	SwitchTab key.Binding
	EditDesc  key.Binding
	Boost     key.Binding
}

func defaultTodosKeyMap() todosKeyMap {
	return todosKeyMap{
		Toggle: key.NewBinding(
			key.WithKeys("x"),
			key.WithHelp("x", "complete/uncomplete"),
		),
		New: key.NewBinding(
			key.WithKeys("n"),
			key.WithHelp("n", "new todo"),
		),
		SwitchTab: key.NewBinding(
			key.WithKeys("tab", "shift+tab"),
			key.WithHelp("tab", "switch pane"),
		),
		Boost: key.NewBinding(key.WithKeys("b", "B"), key.WithHelp("b", "boost")),
		EditDesc: key.NewBinding(
			key.WithKeys("d"),
			key.WithHelp("d", "edit description"),
		),
	}
}

// todosPane tracks which panel has focus.
type todosPane int

const (
	todosPaneLeft  todosPane = iota // todolist list
	todosPaneRight                  // todo list
)

// Todos is the split-pane view for todolists and their todos.
type Todos struct {
	session      *workspace.Session
	todolistPool *data.Pool[[]data.TodolistInfo]
	styles       *tui.Styles
	keys         todosKeyMap

	// Layout
	split         *widget.SplitPane
	listLists     *widget.List // left: todolists
	listTodos     *widget.List // right: todos
	focus         todosPane
	width, height int

	// Loading
	spinner        spinner.Model
	loadingLists   bool
	loadingTodos   bool
	selectedListID int64

	// Inline creation
	creating  bool
	textInput textinput.Model

	// Description editing
	editingDesc  bool
	descComposer *widget.Composer
	descTodoID   int64
}

// todoDescUpdatedMsg is sent after a todo description is updated.
type todoDescUpdatedMsg struct {
	todoID     int64
	todolistID int64
	err        error
}

// NewTodos creates the split-pane todos view.
func NewTodos(session *workspace.Session) *Todos {
	styles := session.Styles()
	scope := session.Scope()

	todolistPool := session.Hub().Todolists(scope.ProjectID, scope.ToolID)

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	listLists := widget.NewList(styles)
	listLists.SetEmptyText("No todolists found.")
	listLists.SetFocused(true)

	listTodos := widget.NewList(styles)
	listTodos.SetEmptyText("Select a todolist to view todos.")
	listTodos.SetFocused(false)

	ti := textinput.New()
	ti.Placeholder = "New todo..."
	ti.CharLimit = 256

	split := widget.NewSplitPane(styles, 0.35)

	client := session.AccountClient()
	uploadFn := func(ctx context.Context, path, filename, contentType string) (string, error) {
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		resp, err := client.Attachments().Create(ctx, filename, contentType, io.Reader(f))
		if err != nil {
			return "", err
		}
		return resp.AttachableSGID, nil
	}

	descComp := widget.NewComposer(styles,
		widget.WithMode(widget.ComposerRich),
		widget.WithAutoExpand(false),
		widget.WithUploadFn(uploadFn),
		widget.WithContext(session.Context()),
		widget.WithPlaceholder("Todo description (Markdown)..."),
	)

	return &Todos{
		session:      session,
		todolistPool: todolistPool,
		styles:       styles,
		keys:         defaultTodosKeyMap(),
		split:        split,
		listLists:    listLists,
		listTodos:    listTodos,
		focus:        todosPaneLeft,
		spinner:      s,
		loadingLists: true,
		textInput:    ti,
		descComposer: descComp,
	}
}

// Title implements View.
func (v *Todos) Title() string {
	return "Todos"
}

// InputActive implements workspace.InputCapturer.
func (v *Todos) InputActive() bool {
	return v.creating || v.editingDesc || v.listLists.Filtering() || v.listTodos.Filtering()
}

// StartFilter implements workspace.Filterable.
func (v *Todos) StartFilter() {
	if v.focus == todosPaneLeft {
		v.listLists.StartFilter()
	} else {
		v.listTodos.StartFilter()
	}
}

// IsModal implements workspace.ModalActive.
func (v *Todos) IsModal() bool {
	return v.editingDesc
}

// ShortHelp implements View.
func (v *Todos) ShortHelp() []key.Binding {
	if v.listLists.Filtering() || v.listTodos.Filtering() {
		return filterHints()
	}
	if v.editingDesc {
		return []key.Binding{
			key.NewBinding(key.WithKeys("ctrl+enter"), key.WithHelp("ctrl+enter", "save")),
			key.NewBinding(key.WithKeys("ctrl+e"), key.WithHelp("ctrl+e", "$EDITOR")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		}
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		v.keys.SwitchTab,
		v.keys.Toggle,
		v.keys.New,
		v.keys.EditDesc,
		v.keys.Boost,
	}
}

// FullHelp implements View.
func (v *Todos) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

// SetSize implements View.
func (v *Todos) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.split.SetSize(w, h)
	v.listLists.SetSize(v.split.LeftWidth(), h)
	v.listTodos.SetSize(v.split.RightWidth(), h)
}

// Init implements tea.Model.
func (v *Todos) Init() tea.Cmd {
	snap := v.todolistPool.Get()
	if snap.Usable() {
		v.syncTodolists(snap.Data)
		v.loadingLists = false
		if snap.Fresh() {
			if item := v.listLists.Selected(); item != nil {
				return v.selectTodolist(item.ID)
			}
			return nil
		}
	}
	return tea.Batch(v.spinner.Tick, v.todolistPool.FetchIfStale(v.session.Hub().ProjectContext()))
}

// Update implements tea.Model.
func (v *Todos) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case data.PoolUpdatedMsg:
		if msg.Key == v.todolistPool.Key() {
			snap := v.todolistPool.Get()
			if snap.Usable() {
				v.loadingLists = false
				v.syncTodolists(snap.Data)
				if v.selectedListID == 0 {
					if item := v.listLists.Selected(); item != nil {
						return v, v.selectTodolist(item.ID)
					}
				}
			}
			if snap.State == data.StateError {
				v.loadingLists = false
				return v, workspace.ReportError(snap.Err, "loading todolists")
			}
		} else {
			// Check if this is a todos pool update for the currently selected list
			if v.selectedListID != 0 {
				todosPool := v.session.Hub().Todos(v.session.Scope().ProjectID, v.selectedListID)
				if msg.Key == todosPool.Key() {
					snap := todosPool.Get()
					if snap.Usable() {
						v.loadingTodos = false
						v.syncTodos(v.selectedListID, snap.Data)
					}
					if snap.State == data.StateError {
						v.loadingTodos = false
						return v, workspace.ReportError(snap.Err, "loading todos")
					}
				}
			}
		}
		return v, nil

	case data.MutationErrorMsg:
		// Mutation failed — pool already rolled back optimistic state.
		// Just show the error.
		return v, workspace.ReportError(msg.Err, "toggling todo")

	case workspace.TodoCreatedMsg:
		if msg.Err != nil {
			return v, workspace.ReportError(msg.Err, "creating todo")
		}
		todosPool := v.session.Hub().Todos(v.session.Scope().ProjectID, msg.TodolistID)
		todosPool.Invalidate()
		return v, todosPool.Fetch(v.session.Hub().ProjectContext())

	case workspace.RefreshMsg:
		v.todolistPool.Invalidate()
		v.loadingLists = true
		return v, tea.Batch(v.spinner.Tick, v.todolistPool.Fetch(v.session.Hub().ProjectContext()))

	case spinner.TickMsg:
		if v.loadingLists || v.loadingTodos {
			var cmd tea.Cmd
			v.spinner, cmd = v.spinner.Update(msg)
			return v, cmd
		}

	case todoDescUpdatedMsg:
		if msg.err != nil {
			return v, workspace.ReportError(msg.err, "updating description")
		}
		// Only close the editor on success
		v.editingDesc = false
		v.descComposer.Reset()
		todosPool := v.session.Hub().Todos(v.session.Scope().ProjectID, msg.todolistID)
		todosPool.Invalidate()
		return v, tea.Batch(
			todosPool.Fetch(v.session.Hub().ProjectContext()),
			workspace.SetStatus("Description updated", false),
		)

	case widget.ComposerSubmitMsg:
		if msg.Err != nil {
			return v, workspace.ReportError(msg.Err, "composing description")
		}
		// Keep editing state until API call succeeds (todoDescUpdatedMsg handles cleanup)
		return v, v.updateTodoDescription(msg.Content)

	case widget.EditorReturnMsg:
		if v.editingDesc {
			return v, v.descComposer.HandleEditorReturn(msg)
		}

	case workspace.BoostCreatedMsg:
		// Optimistically update the boost count in the todo list
		if msg.Target.RecordingID != 0 {
			items := v.listTodos.Items()
			for i, item := range items {
				var itemID int64
				fmt.Sscanf(item.ID, "%d", &itemID)
				if itemID == msg.Target.RecordingID {
					item.Boosts++
					items[i] = item
					break
				}
			}
			v.listTodos.SetItems(items)
		}
		return v, nil

	case tea.KeyMsg:
		if v.editingDesc {
			return v, v.handleEditDescKey(msg)
		}
		if v.creating {
			return v, v.handleCreatingKey(msg)
		}
		if v.loadingLists {
			return v, nil
		}
		return v, v.handleKey(msg)
	}

	// Forward to desc composer for upload results
	if v.editingDesc {
		if cmd := v.descComposer.Update(msg); cmd != nil {
			return v, cmd
		}
	}

	return v, nil
}

func (v *Todos) handleKey(msg tea.KeyMsg) tea.Cmd {
	listKeys := workspace.DefaultListKeyMap()

	switch {
	case key.Matches(msg, v.keys.SwitchTab):
		v.toggleFocus()
		return nil

	case key.Matches(msg, v.keys.Toggle):
		if v.focus == todosPaneRight {
			return v.toggleSelected()
		}

	case key.Matches(msg, v.keys.New):
		if v.focus == todosPaneRight && v.selectedListID != 0 {
			v.creating = true
			v.textInput.Reset()
			v.textInput.Focus()
			return textinput.Blink
		}

	case key.Matches(msg, v.keys.EditDesc):
		if v.focus == todosPaneRight {
			return v.startEditDescription()
		}

	case key.Matches(msg, v.keys.Boost):
		if v.focus == todosPaneRight {
			return v.boostSelectedTodo()
		}

	case key.Matches(msg, listKeys.Open):
		if v.focus == todosPaneRight {
			return v.openSelectedTodo()
		}
		return v.updateFocusedList(msg)

	default:
		return v.updateFocusedList(msg)
	}
	return nil
}

func (v *Todos) openSelectedTodo() tea.Cmd {
	item := v.listTodos.Selected()
	if item == nil {
		return nil
	}
	var todoID int64
	fmt.Sscanf(item.ID, "%d", &todoID)

	// Record in recents
	if r := v.session.Recents(); r != nil {
		r.Add(recents.Item{
			ID:          item.ID,
			Title:       item.Title,
			Description: "Todo",
			Type:        recents.TypeRecording,
			AccountID:   v.session.Scope().AccountID,
			ProjectID:   fmt.Sprintf("%d", v.session.Scope().ProjectID),
		})
	}

	scope := v.session.Scope()
	scope.RecordingID = todoID
	scope.RecordingType = "Todo"
	return workspace.Navigate(workspace.ViewDetail, scope)
}

func (v *Todos) handleCreatingKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		content := strings.TrimSpace(v.textInput.Value())
		if content == "" {
			v.creating = false
			return nil
		}
		v.creating = false
		return v.createTodo(content)

	case "esc":
		v.creating = false
		return nil

	default:
		var cmd tea.Cmd
		v.textInput, cmd = v.textInput.Update(msg)
		return cmd
	}
}

func (v *Todos) toggleFocus() {
	if v.focus == todosPaneLeft {
		v.focus = todosPaneRight
	} else {
		v.focus = todosPaneLeft
	}
	v.listLists.SetFocused(v.focus == todosPaneLeft)
	v.listTodos.SetFocused(v.focus == todosPaneRight)
}

func (v *Todos) updateFocusedList(msg tea.KeyMsg) tea.Cmd {
	if v.focus == todosPaneLeft {
		prevIdx := v.listLists.SelectedIndex()
		cmd := v.listLists.Update(msg)

		// If cursor moved, load the newly selected todolist's todos
		if v.listLists.SelectedIndex() != prevIdx {
			if item := v.listLists.Selected(); item != nil {
				return tea.Batch(cmd, v.selectTodolist(item.ID))
			}
		}
		return cmd
	}
	return v.listTodos.Update(msg)
}

func (v *Todos) selectTodolist(id string) tea.Cmd {
	var listID int64
	fmt.Sscanf(id, "%d", &listID)
	if listID == v.selectedListID {
		return nil
	}
	v.selectedListID = listID

	todosPool := v.session.Hub().Todos(v.session.Scope().ProjectID, listID)
	snap := todosPool.Get()
	if snap.Usable() {
		v.loadingTodos = false
		v.syncTodos(listID, snap.Data)
		if snap.Fresh() {
			return nil
		}
	}

	v.loadingTodos = true
	v.listTodos.SetItems(nil)
	return tea.Batch(v.spinner.Tick, todosPool.FetchIfStale(v.session.Hub().ProjectContext()))
}

func (v *Todos) toggleSelected() tea.Cmd {
	item := v.listTodos.Selected()
	if item == nil {
		return nil
	}

	var todoID int64
	fmt.Sscanf(item.ID, "%d", &todoID)

	// Find current completion state from the MutatingPool
	todosPool := v.session.Hub().Todos(v.session.Scope().ProjectID, v.selectedListID)
	snap := todosPool.Get()
	if !snap.Usable() {
		return nil
	}
	var wasCompleted bool
	for _, t := range snap.Data {
		if t.ID == todoID {
			wasCompleted = t.Completed
			break
		}
	}

	cmd := todosPool.Apply(v.session.Hub().ProjectContext(), data.TodoCompleteMutation{
		TodoID:    todoID,
		Completed: !wasCompleted,
		Client:    v.session.AccountClient(),
		ProjectID: v.session.Scope().ProjectID,
	})

	// Read optimistic state immediately and render
	snap = todosPool.Get()
	if snap.Usable() {
		v.syncTodos(v.selectedListID, snap.Data)
	}

	return cmd
}

func (v *Todos) startEditDescription() tea.Cmd {
	item := v.listTodos.Selected()
	if item == nil {
		return nil
	}
	var todoID int64
	fmt.Sscanf(item.ID, "%d", &todoID)

	// Find the todo's current description from the MutatingPool
	todosPool := v.session.Hub().Todos(v.session.Scope().ProjectID, v.selectedListID)
	snap := todosPool.Get()
	var description string
	if snap.Usable() {
		for _, t := range snap.Data {
			if t.ID == todoID {
				description = t.Description
				break
			}
		}
	}

	v.editingDesc = true
	v.descTodoID = todoID
	v.descComposer.Reset()

	// Convert existing HTML description to Markdown for editing
	if description != "" {
		md := richtext.HTMLToMarkdown(description)
		v.descComposer.SetValue(md)
	}

	v.descComposer.SetSize(v.split.RightWidth(), 6)
	return v.descComposer.Focus()
}

func (v *Todos) handleEditDescKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		v.editingDesc = false
		v.descComposer.Blur()
		v.descComposer.Reset()
		return nil
	default:
		return v.descComposer.Update(msg)
	}
}

func (v *Todos) updateTodoDescription(content widget.ComposerContent) tea.Cmd {
	scope := v.session.Scope()
	todoID := v.descTodoID
	todolistID := v.selectedListID

	html := richtext.MarkdownToHTML(content.Markdown)
	if len(content.Attachments) > 0 {
		refs := make([]richtext.AttachmentRef, 0, len(content.Attachments))
		for _, att := range content.Attachments {
			if att.Status == widget.AttachUploaded {
				refs = append(refs, richtext.AttachmentRef{
					SGID:        att.SGID,
					Filename:    att.Filename,
					ContentType: att.ContentType,
				})
			}
		}
		html = richtext.EmbedAttachments(html, refs)
	}

	ctx := v.session.Hub().ProjectContext()
	client := v.session.AccountClient()
	return func() tea.Msg {
		_, err := client.Todos().Update(ctx, scope.ProjectID, todoID, &basecamp.UpdateTodoRequest{
			Description: html,
		})
		return todoDescUpdatedMsg{todoID: todoID, todolistID: todolistID, err: err}
	}
}

// View implements tea.Model.
func (v *Todos) View() string {
	if v.loadingLists {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Loading todolists...")
	}

	// Left panel: todolist list
	left := v.listLists.View()

	// Right panel: todos or loading
	var right string
	if v.loadingTodos {
		right = lipgloss.NewStyle().
			Padding(0, 1).
			Render(v.spinner.View() + " Loading todos...")
	} else {
		right = v.renderRightPanel()
	}

	v.split.SetContent(left, right)
	return v.split.View()
}

func (v *Todos) renderRightPanel() string {
	var b strings.Builder

	b.WriteString(v.listTodos.View())

	if v.creating {
		b.WriteString("\n")
		theme := v.styles.Theme()
		prefix := lipgloss.NewStyle().Foreground(theme.Muted).Render("  + ")
		b.WriteString(prefix + v.textInput.View())
	}

	if v.editingDesc {
		b.WriteString("\n")
		theme := v.styles.Theme()
		sep := lipgloss.NewStyle().Foreground(theme.Border).Render("─ Description ─")
		b.WriteString(sep + "\n")
		b.WriteString(v.descComposer.View())
	}

	return b.String()
}

// -- Data sync

func (v *Todos) syncTodolists(todolists []data.TodolistInfo) {
	items := make([]widget.ListItem, 0, len(todolists))
	for _, tl := range todolists {
		items = append(items, widget.ListItem{
			ID:    fmt.Sprintf("%d", tl.ID),
			Title: tl.Title,
			Extra: tl.CompletedRatio,
		})
	}
	v.listLists.SetItems(items)
}

func (v *Todos) syncTodos(todolistID int64, todos []data.TodoInfo) {
	// Only update the right panel if this is still the selected list
	if todolistID == v.selectedListID {
		v.renderTodoItems(todos)
	}
}

func (v *Todos) renderTodoItems(todos []data.TodoInfo) {
	items := make([]widget.ListItem, 0, len(todos))
	for _, t := range todos {
		check := "[ ]"
		if t.Completed {
			check = "[x]"
		}

		desc := ""
		if t.DueOn != "" {
			desc = t.DueOn
		}
		if len(t.Assignees) > 0 {
			who := strings.Join(t.Assignees, ", ")
			if desc != "" {
				desc += " - "
			}
			desc += who
		}

		items = append(items, widget.ListItem{
			ID:          fmt.Sprintf("%d", t.ID),
			Title:       check + " " + t.Content,
			Description: desc,
			Boosts:      t.GetBoosts().Count,
		})
	}
	v.listTodos.SetItems(items)
}

// -- Commands (tea.Cmd factories)

func (v *Todos) createTodo(content string) tea.Cmd {
	scope := v.session.Scope()
	todolistID := v.selectedListID
	ctx := v.session.Hub().ProjectContext()
	client := v.session.AccountClient()
	return func() tea.Msg {
		_, err := client.Todos().Create(ctx, scope.ProjectID, todolistID, &basecamp.CreateTodoRequest{
			Content: content,
		})
		return workspace.TodoCreatedMsg{TodolistID: todolistID, Content: content, Err: err}
	}
}

func (v *Todos) boostSelectedTodo() tea.Cmd {
	item := v.listTodos.Selected()
	if item == nil {
		return nil
	}
	id, err := strconv.ParseInt(item.ID, 10, 64)
	if err != nil {
		return nil
	}
	return func() tea.Msg {
		return workspace.OpenBoostPickerMsg{
			Target: workspace.BoostTarget{
				ProjectID:   v.session.Scope().ProjectID,
				RecordingID: id,
				Title:       item.Title,
			},
		}
	}
}
