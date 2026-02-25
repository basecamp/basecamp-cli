package views

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/dateparse"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/empty"
	"github.com/basecamp/basecamp-cli/internal/tui/recents"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// todosKeyMap defines todo-specific keybindings.
type todosKeyMap struct {
	Toggle        key.Binding
	New           key.Binding
	SwitchTab     key.Binding
	EditDesc      key.Binding
	Boost         key.Binding
	DueDate       key.Binding
	Assign        key.Binding
	Unassign      key.Binding
	NewList       key.Binding
	RenameList    key.Binding
	TrashList     key.Binding
	ShowCompleted key.Binding
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
		NewList: key.NewBinding(
			key.WithKeys("N"),
			key.WithHelp("N", "new list"),
		),
		RenameList: key.NewBinding(
			key.WithKeys("R"),
			key.WithHelp("R", "rename list"),
		),
		TrashList: key.NewBinding(
			key.WithKeys("T"),
			key.WithHelp("T", "trash list"),
		),
		ShowCompleted: key.NewBinding(
			key.WithKeys("c"),
			key.WithHelp("c", "completed"),
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

	// Todolist management
	creatingList       bool
	renamingList       bool
	listInput          textinput.Model
	trashListPending   bool
	trashListPendingID string

	// Completed filter
	showCompleted bool
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

type todolistCreatedMsg struct {
	todosetID int64
	err       error
}

type todolistRenamedMsg struct {
	todolistID int64
	err        error
}

type todolistTrashResultMsg struct {
	todolistID int64
	err        error
}

type todolistTrashTimeoutMsg struct{}

// todoUncompletedMsg is sent after un-completing a todo from completed mode.
type todoUncompletedMsg struct {
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
	listLists.SetEmptyMessage(empty.NoTodolists(""))
	listLists.SetFocused(true)

	listTodos := widget.NewList(styles)
	listTodos.SetEmptyMessage(empty.NoTodos(""))
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

// HasSplitPane implements workspace.SplitPaneFocuser.
func (v *Todos) HasSplitPane() bool { return true }

// InputActive implements workspace.InputCapturer.
func (v *Todos) InputActive() bool {
	return v.creating || v.editingDesc || v.settingDue || v.assigning ||
		v.creatingList || v.renamingList ||
		v.listLists.Filtering() || v.listTodos.Filtering()
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
	return v.editingDesc || v.settingDue || v.assigning || v.creatingList || v.renamingList
}

// FocusedItem implements workspace.FocusedRecording.
func (v *Todos) FocusedItem() workspace.FocusedItemScope {
	if v.focus != todosPaneRight {
		return workspace.FocusedItemScope{}
	}
	item := v.listTodos.Selected()
	if item == nil {
		return workspace.FocusedItemScope{}
	}
	var id int64
	fmt.Sscanf(item.ID, "%d", &id)
	return workspace.FocusedItemScope{RecordingID: id}
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
	if v.focus == todosPaneLeft {
		completedHint := key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "completed"))
		if v.showCompleted {
			completedHint = key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "pending"))
		}
		return []key.Binding{
			key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
			v.keys.SwitchTab,
			completedHint,
			v.keys.NewList,
			v.keys.RenameList,
			v.keys.TrashList,
		}
	}
	if v.showCompleted {
		return []key.Binding{
			key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
			v.keys.SwitchTab,
			key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "uncomplete")),
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
		v.keys.Unassign,
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
			// Check if this is a todos pool update for the currently selected list.
			// Route to the active pool based on showCompleted mode.
			if v.selectedListID != 0 {
				if v.showCompleted {
					completedPool := v.session.Hub().CompletedTodos(v.session.Scope().ProjectID, v.selectedListID)
					if msg.Key == completedPool.Key() {
						snap := completedPool.Get()
						if snap.Usable() {
							v.loadingTodos = false
							v.syncTodos(v.selectedListID, snap.Data)
						}
						if snap.State == data.StateError {
							v.loadingTodos = false
							return v, workspace.ReportError(snap.Err, "loading completed todos")
						}
					}
				} else {
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

	case workspace.FocusMsg:
		ctx := v.session.Hub().ProjectContext()
		cmds := []tea.Cmd{v.todolistPool.FetchIfStale(ctx)}
		if v.selectedListID != 0 {
			if v.showCompleted {
				pool := v.session.Hub().CompletedTodos(v.session.Scope().ProjectID, v.selectedListID)
				cmds = append(cmds, pool.FetchIfStale(ctx))
			} else {
				pool := v.session.Hub().Todos(v.session.Scope().ProjectID, v.selectedListID)
				cmds = append(cmds, pool.FetchIfStale(ctx))
			}
		}
		return v, tea.Batch(cmds...)

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

	case todolistCreatedMsg:
		if msg.err != nil {
			return v, workspace.ReportError(msg.err, "creating todolist")
		}
		v.todolistPool.Invalidate()
		v.loadingLists = true
		return v, tea.Batch(
			v.spinner.Tick,
			v.todolistPool.Fetch(v.session.Hub().ProjectContext()),
			workspace.SetStatus("Todolist created", false),
		)

	case todolistRenamedMsg:
		if msg.err != nil {
			return v, workspace.ReportError(msg.err, "renaming todolist")
		}
		v.todolistPool.Invalidate()
		v.loadingLists = true
		return v, tea.Batch(
			v.spinner.Tick,
			v.todolistPool.Fetch(v.session.Hub().ProjectContext()),
			workspace.SetStatus("Todolist renamed", false),
		)

	case todolistTrashResultMsg:
		if msg.err != nil {
			return v, workspace.ReportError(msg.err, "trashing todolist")
		}
		if msg.todolistID == v.selectedListID {
			v.selectedListID = 0
			v.listTodos.SetItems(nil)
		}
		v.todolistPool.Invalidate()
		v.loadingLists = true
		return v, tea.Batch(
			v.spinner.Tick,
			v.todolistPool.Fetch(v.session.Hub().ProjectContext()),
			workspace.SetStatus("Todolist trashed", false),
		)

	case todolistTrashTimeoutMsg:
		v.trashListPending = false
		v.trashListPendingID = ""
		return v, nil

	case todoUncompletedMsg:
		if msg.err != nil {
			return v, workspace.ReportError(msg.err, "uncompleting todo")
		}
		// Invalidate both pools — the item moves between them
		completedPool := v.session.Hub().CompletedTodos(v.session.Scope().ProjectID, msg.todolistID)
		completedPool.Invalidate()
		pendingPool := v.session.Hub().Todos(v.session.Scope().ProjectID, msg.todolistID)
		pendingPool.Invalidate()
		// Refetch whichever pool is currently active (user may have toggled mid-flight)
		ctx := v.session.Hub().ProjectContext()
		var fetchCmd tea.Cmd
		if v.showCompleted {
			fetchCmd = completedPool.Fetch(ctx)
		} else {
			fetchCmd = pendingPool.Fetch(ctx)
		}
		return v, tea.Batch(fetchCmd, workspace.SetStatus("Todo uncompleted", false))

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
		if v.creatingList || v.renamingList {
			return v, v.handleListInputKey(msg)
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
	if v.listLists.Filtering() || v.listTodos.Filtering() {
		v.trashListPending = false
		v.trashListPendingID = ""
		return v.updateFocusedList(msg)
	}

	// Reset trash list confirmation on non-T keys (when left pane focused)
	if v.focus == todosPaneLeft && msg.String() != "T" {
		v.trashListPending = false
		v.trashListPendingID = ""
	}

	listKeys := workspace.DefaultListKeyMap()

	switch {
	case key.Matches(msg, v.keys.NewList):
		if v.focus == todosPaneLeft {
			v.creatingList = true
			v.listInput = textinput.New()
			v.listInput.Placeholder = "New todolist name..."
			v.listInput.CharLimit = 256
			v.listInput.Focus()
			return textinput.Blink
		}

	case key.Matches(msg, v.keys.RenameList):
		if v.focus == todosPaneLeft {
			item := v.listLists.Selected()
			if item == nil {
				return nil
			}
			v.renamingList = true
			v.listInput = textinput.New()
			v.listInput.SetValue(item.Title)
			v.listInput.CharLimit = 256
			v.listInput.Focus()
			return textinput.Blink
		}

	case key.Matches(msg, v.keys.TrashList):
		if v.focus == todosPaneLeft {
			return v.trashSelectedList()
		}

	case key.Matches(msg, v.keys.ShowCompleted):
		if v.focus == todosPaneLeft {
			return v.toggleShowCompleted()
		}

	case key.Matches(msg, v.keys.SwitchTab):
		v.toggleFocus()
		return nil

	case key.Matches(msg, v.keys.Toggle):
		if v.focus == todosPaneRight {
			if v.showCompleted {
				return v.uncompleteSelected()
			}
			return v.toggleSelected()
		}

	case key.Matches(msg, v.keys.New):
		if v.focus == todosPaneRight && v.selectedListID != 0 && !v.showCompleted {
			v.creating = true
			v.textInput.Reset()
			v.textInput.Focus()
			return textinput.Blink
		}

	case key.Matches(msg, v.keys.EditDesc):
		if v.focus == todosPaneRight && !v.showCompleted {
			return v.startEditDescription()
		}

	case key.Matches(msg, v.keys.DueDate):
		if v.focus == todosPaneRight && !v.showCompleted {
			return v.startSettingDue()
		}

	case key.Matches(msg, v.keys.Assign):
		if v.focus == todosPaneRight && v.selectedListID != 0 && !v.showCompleted {
			return v.startAssigning()
		}

	case key.Matches(msg, v.keys.Unassign):
		if v.focus == todosPaneRight && !v.showCompleted {
			return v.clearAssignees()
		}

	case key.Matches(msg, v.keys.Boost):
		if v.focus == todosPaneRight && !v.showCompleted {
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

func (v *Todos) handleListInputKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		val := strings.TrimSpace(v.listInput.Value())
		if val == "" {
			v.creatingList = false
			v.renamingList = false
			return nil
		}
		if v.creatingList {
			v.creatingList = false
			return v.createTodolist(val)
		}
		if v.renamingList {
			v.renamingList = false
			return v.renameTodolist(val)
		}
		return nil
	case "esc":
		v.creatingList = false
		v.renamingList = false
		return nil
	default:
		var cmd tea.Cmd
		v.listInput, cmd = v.listInput.Update(msg)
		return cmd
	}
}

func (v *Todos) createTodolist(name string) tea.Cmd {
	scope := v.session.Scope()
	hub := v.session.Hub()
	ctx := hub.ProjectContext()
	todosetID := scope.ToolID
	return func() tea.Msg {
		err := hub.CreateTodolist(ctx, scope.AccountID, scope.ProjectID, todosetID, name)
		return todolistCreatedMsg{todosetID: todosetID, err: err}
	}
}

func (v *Todos) renameTodolist(name string) tea.Cmd {
	item := v.listLists.Selected()
	if item == nil {
		return nil
	}
	var todolistID int64
	fmt.Sscanf(item.ID, "%d", &todolistID)
	scope := v.session.Scope()
	hub := v.session.Hub()
	ctx := hub.ProjectContext()
	return func() tea.Msg {
		err := hub.UpdateTodolist(ctx, scope.AccountID, scope.ProjectID, todolistID, name)
		return todolistRenamedMsg{todolistID: todolistID, err: err}
	}
}

func (v *Todos) trashSelectedList() tea.Cmd {
	item := v.listLists.Selected()
	if item == nil {
		return nil
	}
	var todolistID int64
	fmt.Sscanf(item.ID, "%d", &todolistID)

	if v.trashListPending && v.trashListPendingID == item.ID {
		v.trashListPending = false
		v.trashListPendingID = ""
		scope := v.session.Scope()
		hub := v.session.Hub()
		ctx := hub.ProjectContext()
		listID := todolistID
		return func() tea.Msg {
			err := hub.TrashTodolist(ctx, scope.AccountID, scope.ProjectID, listID)
			return todolistTrashResultMsg{todolistID: listID, err: err}
		}
	}
	v.trashListPending = true
	v.trashListPendingID = item.ID
	return tea.Batch(
		workspace.SetStatus("Press T again to trash list", false),
		tea.Tick(3*time.Second, func(time.Time) tea.Msg { return todolistTrashTimeoutMsg{} }),
	)
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

	if v.showCompleted {
		return v.loadCompletedTodos(listID)
	}
	return v.loadPendingTodos(listID)
}

func (v *Todos) loadPendingTodos(listID int64) tea.Cmd {
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

func (v *Todos) loadCompletedTodos(listID int64) tea.Cmd {
	completedPool := v.session.Hub().CompletedTodos(v.session.Scope().ProjectID, listID)
	snap := completedPool.Get()
	if snap.Usable() {
		v.loadingTodos = false
		v.syncTodos(listID, snap.Data)
		if snap.Fresh() {
			return nil
		}
	}

	v.loadingTodos = true
	v.listTodos.SetItems(nil)
	return tea.Batch(v.spinner.Tick, completedPool.FetchIfStale(v.session.Hub().ProjectContext()))
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

func (v *Todos) toggleShowCompleted() tea.Cmd {
	v.showCompleted = !v.showCompleted
	if v.selectedListID != 0 {
		// Reload the right pane from the appropriate pool.
		// selectedListID stays stable — only the data source changes.
		if v.showCompleted {
			return v.loadCompletedTodos(v.selectedListID)
		}
		return v.loadPendingTodos(v.selectedListID)
	}
	return nil
}

func (v *Todos) uncompleteSelected() tea.Cmd {
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
		err := hub.UncompleteTodo(ctx, scope.AccountID, scope.ProjectID, todoID)
		return todoUncompletedMsg{todolistID: todolistID, err: err}
	}
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
	if v.creatingList || v.renamingList {
		theme := v.styles.Theme()
		prefix := "  + "
		if v.renamingList {
			prefix = "  ~ "
		}
		left += "\n" + lipgloss.NewStyle().Foreground(theme.Muted).Render(prefix) + v.listInput.View()
	}

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
			desc = formatDueDate(t.DueOn)
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
				AccountID:   v.session.Scope().AccountID,
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
