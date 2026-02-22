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

	"github.com/basecamp/basecamp-cli/internal/dateparse"
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
	DueDate   key.Binding
	Assign    key.Binding
	Unassign  key.Binding
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
		DueDate: key.NewBinding(
			key.WithKeys("D"),
			key.WithHelp("D", "due date"),
		),
		Assign: key.NewBinding(
			key.WithKeys("a"),
			key.WithHelp("a", "assign"),
		),
		Unassign: key.NewBinding(
			key.WithKeys("A"),
			key.WithHelp("A", "unassign"),
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

	// Due date setting
	settingDue bool
	dueInput   textinput.Model

	// Assigning
	assigning   bool
	assignInput textinput.Model
}

// todoDescUpdatedMsg is sent after a todo description is updated.
type todoDescUpdatedMsg struct {
	todoID     int64
	todolistID int64
	err        error
}

// todoDueUpdatedMsg is sent after a todo due date is set or cleared.
type todoDueUpdatedMsg struct {
	todolistID int64
	err        error
}

// todoAssignResultMsg is sent after a todo assignee is set or cleared.
type todoAssignResultMsg struct {
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
	return v.creating || v.editingDesc || v.settingDue || v.assigning || v.listLists.Filtering() || v.listTodos.Filtering()
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
	return v.editingDesc || v.settingDue || v.assigning
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
		v.keys.DueDate,
		v.keys.Assign,
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
		// Re-sync the view and show the error.
		if v.selectedListID != 0 {
			todosPool := v.session.Hub().Todos(v.session.Scope().ProjectID, v.selectedListID)
			if snap := todosPool.Get(); snap.Usable() {
				v.syncTodos(v.selectedListID, snap.Data)
			}
		}
		return v, workspace.ReportError(msg.Err, "updating todo")

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

	case todoDueUpdatedMsg:
		if msg.err != nil {
			return v, workspace.ReportError(msg.err, "updating due date")
		}
		todosPool := v.session.Hub().Todos(v.session.Scope().ProjectID, msg.todolistID)
		todosPool.Invalidate()
		return v, tea.Batch(
			todosPool.Fetch(v.session.Hub().ProjectContext()),
			workspace.SetStatus("Due date updated", false),
		)

	case todoAssignResultMsg:
		if msg.err != nil {
			return v, workspace.ReportError(msg.err, "updating assignee")
		}
		todosPool := v.session.Hub().Todos(v.session.Scope().ProjectID, msg.todolistID)
		todosPool.Invalidate()
		return v, tea.Batch(
			todosPool.Fetch(v.session.Hub().ProjectContext()),
			workspace.SetStatus("Assignee updated", false),
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
		if v.settingDue {
			return v, v.handleSettingDueKey(msg)
		}
		if v.assigning {
			return v, v.handleAssigningKey(msg)
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

	case key.Matches(msg, v.keys.DueDate):
		if v.focus == todosPaneRight {
			return v.startSettingDue()
		}

	case key.Matches(msg, v.keys.Assign):
		if v.focus == todosPaneRight && v.selectedListID != 0 {
			return v.startAssigning()
		}

	case key.Matches(msg, v.keys.Unassign):
		if v.focus == todosPaneRight {
			return v.clearAssignees()
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

	if v.settingDue {
		b.WriteString("\n")
		theme := v.styles.Theme()
		prefix := lipgloss.NewStyle().Foreground(theme.Muted).Render("  Due: ")
		b.WriteString(prefix + v.dueInput.View())
	}

	if v.assigning {
		b.WriteString("\n")
		theme := v.styles.Theme()
		prefix := lipgloss.NewStyle().Foreground(theme.Muted).Render("  Assign: ")
		b.WriteString(prefix + v.assignInput.View())
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

	todosPool := v.session.Hub().Todos(scope.ProjectID, todolistID)
	cmd := todosPool.Apply(v.session.Hub().ProjectContext(), &data.TodoCreateMutation{
		Content:    content,
		TodolistID: todolistID,
		ProjectID:  scope.ProjectID,
		Client:     v.session.AccountClient(),
	})

	// Read optimistic state immediately and render
	snap := todosPool.Get()
	if snap.Usable() {
		v.syncTodos(todolistID, snap.Data)
	}

	return cmd
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

// -- Due date --

func (v *Todos) startSettingDue() tea.Cmd {
	item := v.listTodos.Selected()
	if item == nil {
		return nil
	}
	v.settingDue = true
	v.dueInput = textinput.New()
	v.dueInput.Placeholder = "due date (tomorrow, fri, 2026-03-15)..."
	v.dueInput.CharLimit = 64
	v.dueInput.Focus()
	return textinput.Blink
}

func (v *Todos) handleSettingDueKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		input := strings.TrimSpace(v.dueInput.Value())
		v.settingDue = false
		if input == "" {
			return v.clearDueDate()
		}
		parsed := dateparse.Parse(input)
		if !dateparse.IsValid(input) {
			return workspace.SetStatus("Unrecognized date: "+input, true)
		}
		return v.setDueDate(parsed)
	case "esc":
		v.settingDue = false
		return nil
	default:
		var cmd tea.Cmd
		v.dueInput, cmd = v.dueInput.Update(msg)
		return cmd
	}
}

func (v *Todos) setDueDate(dueOn string) tea.Cmd {
	item := v.listTodos.Selected()
	if item == nil {
		return nil
	}
	var todoID int64
	fmt.Sscanf(item.ID, "%d", &todoID)

	scope := v.session.Scope()
	hub := v.session.Hub()
	ctx := hub.ProjectContext()
	todolistID := v.selectedListID
	return func() tea.Msg {
		err := hub.UpdateTodo(ctx, scope.AccountID, scope.ProjectID, todoID,
			&basecamp.UpdateTodoRequest{DueOn: dueOn})
		return todoDueUpdatedMsg{todolistID: todolistID, err: err}
	}
}

func (v *Todos) clearDueDate() tea.Cmd {
	item := v.listTodos.Selected()
	if item == nil {
		return nil
	}
	var todoID int64
	fmt.Sscanf(item.ID, "%d", &todoID)

	scope := v.session.Scope()
	hub := v.session.Hub()
	ctx := hub.ProjectContext()
	todolistID := v.selectedListID
	return func() tea.Msg {
		err := hub.ClearTodoDueOn(ctx, scope.AccountID, scope.ProjectID, todoID)
		return todoDueUpdatedMsg{todolistID: todolistID, err: err}
	}
}

// -- Assign --

func (v *Todos) startAssigning() tea.Cmd {
	item := v.listTodos.Selected()
	if item == nil {
		return nil
	}
	v.assigning = true
	v.assignInput = textinput.New()
	v.assignInput.Placeholder = "assign to (name)..."
	v.assignInput.CharLimit = 128
	v.assignInput.Focus()
	return textinput.Blink
}

func (v *Todos) handleAssigningKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		input := strings.TrimSpace(v.assignInput.Value())
		v.assigning = false
		if input == "" {
			return nil
		}
		return v.assignTodo(input)
	case "esc":
		v.assigning = false
		return nil
	default:
		var cmd tea.Cmd
		v.assignInput, cmd = v.assignInput.Update(msg)
		return cmd
	}
}

func (v *Todos) assignTodo(nameQuery string) tea.Cmd {
	item := v.listTodos.Selected()
	if item == nil {
		return nil
	}
	var todoID int64
	fmt.Sscanf(item.ID, "%d", &todoID)

	// Resolve name from People pool
	peoplePool := v.session.Hub().People()
	snap := peoplePool.Get()
	if !snap.Usable() {
		return workspace.SetStatus("People not loaded yet — try again", true)
	}

	q := strings.ToLower(nameQuery)
	var matches []data.PersonInfo
	for _, p := range snap.Data {
		if strings.Contains(strings.ToLower(p.Name), q) {
			matches = append(matches, p)
		}
	}

	switch len(matches) {
	case 0:
		return workspace.SetStatus("No one found matching \""+nameQuery+"\"", true)
	case 1:
		// exact match
	default:
		names := make([]string, 0, len(matches))
		for _, m := range matches {
			names = append(names, m.Name)
		}
		if len(names) > 4 {
			names = append(names[:4], "...")
		}
		return workspace.SetStatus("Multiple matches: "+strings.Join(names, ", ")+" — be more specific", true)
	}

	matched := matches[0]
	scope := v.session.Scope()
	hub := v.session.Hub()
	ctx := hub.ProjectContext()
	todolistID := v.selectedListID
	return func() tea.Msg {
		err := hub.UpdateTodo(ctx, scope.AccountID, scope.ProjectID, todoID,
			&basecamp.UpdateTodoRequest{AssigneeIDs: []int64{matched.ID}})
		return todoAssignResultMsg{todolistID: todolistID, err: err}
	}
}

func (v *Todos) clearAssignees() tea.Cmd {
	item := v.listTodos.Selected()
	if item == nil {
		return nil
	}
	var todoID int64
	fmt.Sscanf(item.ID, "%d", &todoID)

	scope := v.session.Scope()
	hub := v.session.Hub()
	ctx := hub.ProjectContext()
	todolistID := v.selectedListID
	return func() tea.Msg {
		err := hub.ClearTodoAssignees(ctx, scope.AccountID, scope.ProjectID, todoID)
		return todoAssignResultMsg{todolistID: todolistID, err: err}
	}
}
