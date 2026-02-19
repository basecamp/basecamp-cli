package views

import (
	"context"
	"fmt"
	"io"
	"os"
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
	session *workspace.Session
	store   *data.Store
	styles  *tui.Styles
	keys    todosKeyMap

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

	// Data (local copies for rendering)
	todos map[int64][]todoState // todolistID -> todos with local state
}

// todoDescUpdatedMsg is sent after a todo description is updated.
type todoDescUpdatedMsg struct {
	todoID     int64
	todolistID int64
	err        error
}

// todoState tracks a todo with local optimistic state.
type todoState struct {
	id          int64
	content     string
	description string
	completed   bool
	dueOn       string
	assignees   []string
}

// NewTodos creates the split-pane todos view.
func NewTodos(session *workspace.Session, store *data.Store) *Todos {
	styles := session.Styles()

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
		store:        store,
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
		todos:        make(map[int64][]todoState),
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
	return tea.Batch(v.spinner.Tick, v.fetchTodolists())
}

// Update implements tea.Model.
func (v *Todos) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case workspace.TodolistsLoadedMsg:
		v.loadingLists = false
		if msg.Err != nil {
			return v, workspace.ReportError(msg.Err, "loading todolists")
		}
		v.syncTodolists(msg.Todolists)
		// Auto-select first todolist
		if item := v.listLists.Selected(); item != nil {
			return v, v.selectTodolist(item.ID)
		}
		return v, nil

	case workspace.TodosLoadedMsg:
		v.loadingTodos = false
		if msg.Err != nil {
			return v, workspace.ReportError(msg.Err, "loading todos")
		}
		v.syncTodos(msg.TodolistID, msg.Todos)
		return v, nil

	case workspace.TodoCompletedMsg:
		if msg.Err != nil {
			// Revert optimistic update using the message's TodolistID (not current selection)
			v.revertToggle(msg.TodolistID, msg.TodoID, msg.Completed)
			return v, workspace.ReportError(msg.Err, "toggling todo")
		}
		return v, nil

	case workspace.TodoCreatedMsg:
		if msg.Err != nil {
			return v, workspace.ReportError(msg.Err, "creating todo")
		}
		// Reload the todolist's todos to get the server state
		return v, v.fetchTodos(msg.TodolistID)

	case workspace.RefreshMsg:
		v.loadingLists = true
		return v, tea.Batch(v.spinner.Tick, v.fetchTodolists())

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
		return v, tea.Batch(
			v.fetchTodos(msg.todolistID),
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

	// If we have cached todos, show them immediately
	if cached, ok := v.todos[listID]; ok {
		v.loadingTodos = false
		v.renderTodoItems(cached)
		return nil
	}

	v.loadingTodos = true
	v.listTodos.SetItems(nil)
	return tea.Batch(v.spinner.Tick, v.fetchTodos(listID))
}

func (v *Todos) toggleSelected() tea.Cmd {
	item := v.listTodos.Selected()
	if item == nil {
		return nil
	}

	var todoID int64
	fmt.Sscanf(item.ID, "%d", &todoID)

	// Find current state and toggle optimistically
	todos := v.todos[v.selectedListID]
	var wasCompleted bool
	for i := range todos {
		if todos[i].id == todoID {
			wasCompleted = todos[i].completed
			todos[i].completed = !wasCompleted
			break
		}
	}
	v.renderTodoItems(todos)

	return v.completeTodo(todoID, !wasCompleted)
}

func (v *Todos) revertToggle(todolistID, todoID int64, failedCompleted bool) {
	todos := v.todos[todolistID]
	for i := range todos {
		if todos[i].id == todoID {
			// Revert: set back to opposite of what was attempted
			todos[i].completed = !failedCompleted
			break
		}
	}
	// Only re-render if this is the currently visible list
	if todolistID == v.selectedListID {
		v.renderTodoItems(todos)
	}
}

func (v *Todos) startEditDescription() tea.Cmd {
	item := v.listTodos.Selected()
	if item == nil {
		return nil
	}
	var todoID int64
	fmt.Sscanf(item.ID, "%d", &todoID)

	// Find the todo's current description
	todos := v.todos[v.selectedListID]
	var description string
	for _, t := range todos {
		if t.id == todoID {
			description = t.description
			break
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

	ctx := v.session.Context()
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

func (v *Todos) syncTodolists(todolists []workspace.TodolistInfo) {
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

func (v *Todos) syncTodos(todolistID int64, todos []workspace.TodoInfo) {
	states := make([]todoState, 0, len(todos))
	for _, t := range todos {
		states = append(states, todoState{
			id:          t.ID,
			content:     t.Content,
			description: t.Description,
			completed:   t.Completed,
			dueOn:       t.DueOn,
			assignees:   t.Assignees,
		})
	}
	v.todos[todolistID] = states

	// Only update the right panel if this is still the selected list
	if todolistID == v.selectedListID {
		v.renderTodoItems(states)
	}
}

func (v *Todos) renderTodoItems(todos []todoState) {
	items := make([]widget.ListItem, 0, len(todos))
	for _, t := range todos {
		check := "[ ]"
		if t.completed {
			check = "[x]"
		}

		desc := ""
		if t.dueOn != "" {
			desc = t.dueOn
		}
		if len(t.assignees) > 0 {
			who := strings.Join(t.assignees, ", ")
			if desc != "" {
				desc += " - "
			}
			desc += who
		}

		items = append(items, widget.ListItem{
			ID:          fmt.Sprintf("%d", t.id),
			Title:       check + " " + t.content,
			Description: desc,
		})
	}
	v.listTodos.SetItems(items)
}

// -- Commands (tea.Cmd factories)

func (v *Todos) fetchTodolists() tea.Cmd {
	scope := v.session.Scope()
	ctx := v.session.Context()
	client := v.session.AccountClient()
	return func() tea.Msg {
		result, err := client.Todolists().List(ctx, scope.ProjectID, scope.ToolID, &basecamp.TodolistListOptions{})
		if err != nil {
			return workspace.TodolistsLoadedMsg{Err: err}
		}

		infos := make([]workspace.TodolistInfo, 0, len(result.Todolists))
		for _, tl := range result.Todolists {
			infos = append(infos, workspace.TodolistInfo{
				ID:             tl.ID,
				Title:          tl.Title,
				CompletedRatio: tl.CompletedRatio,
			})
		}
		return workspace.TodolistsLoadedMsg{Todolists: infos}
	}
}

func (v *Todos) fetchTodos(todolistID int64) tea.Cmd {
	scope := v.session.Scope()
	ctx := v.session.Context()
	client := v.session.AccountClient()
	return func() tea.Msg {
		result, err := client.Todos().List(ctx, scope.ProjectID, todolistID, &basecamp.TodoListOptions{})
		if err != nil {
			return workspace.TodosLoadedMsg{TodolistID: todolistID, Err: err}
		}

		infos := make([]workspace.TodoInfo, 0, len(result.Todos))
		for _, t := range result.Todos {
			names := make([]string, 0, len(t.Assignees))
			for _, a := range t.Assignees {
				names = append(names, a.Name)
			}
			infos = append(infos, workspace.TodoInfo{
				ID:          t.ID,
				Content:     t.Content,
				Description: t.Description,
				Completed:   t.Completed,
				DueOn:       t.DueOn,
				Assignees:   names,
				Position:    t.Position,
			})
		}
		return workspace.TodosLoadedMsg{TodolistID: todolistID, Todos: infos}
	}
}

func (v *Todos) completeTodo(todoID int64, completed bool) tea.Cmd {
	scope := v.session.Scope()
	todolistID := v.selectedListID
	ctx := v.session.Context()
	client := v.session.AccountClient()
	return func() tea.Msg {
		var err error
		if completed {
			err = client.Todos().Complete(ctx, scope.ProjectID, todoID)
		} else {
			err = client.Todos().Uncomplete(ctx, scope.ProjectID, todoID)
		}
		return workspace.TodoCompletedMsg{TodolistID: todolistID, TodoID: todoID, Completed: completed, Err: err}
	}
}

func (v *Todos) createTodo(content string) tea.Cmd {
	scope := v.session.Scope()
	todolistID := v.selectedListID
	ctx := v.session.Context()
	client := v.session.AccountClient()
	return func() tea.Msg {
		_, err := client.Todos().Create(ctx, scope.ProjectID, todolistID, &basecamp.CreateTodoRequest{
			Content: content,
		})
		return workspace.TodoCreatedMsg{TodolistID: todolistID, Content: content, Err: err}
	}
}
