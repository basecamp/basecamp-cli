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
	"github.com/basecamp/basecamp-cli/internal/tui/empty"
	"github.com/basecamp/basecamp-cli/internal/tui/recents"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// docCreatedMsg is sent after creating a document.
type docCreatedMsg struct {
	vaultID int64
	err     error
}

// folderCreatedMsg is sent after creating a folder.
type folderCreatedMsg struct {
	vaultID int64
	err     error
}

// docsFilesTrashResultMsg is sent after trashing a docs/files item.
type docsFilesTrashResultMsg struct {
	vaultID int64
	itemID  string
	err     error
}

// docsFilesTrashTimeoutMsg resets the double-press trash confirmation.
type docsFilesTrashTimeoutMsg struct{}

// folderEntry tracks the state needed to restore cursor when navigating back.
type folderEntry struct {
	vaultID      int64
	title        string
	cursorIdx    int
	cursorItemID string
}

// DocsFiles is the list view for a project's vault (Docs & Files).
type DocsFiles struct {
	session *workspace.Session
	pool    *data.Pool[[]data.DocsFilesItemInfo]
	styles  *tui.Styles

	// Layout
	list          *widget.List
	spinner       spinner.Model
	loading       bool
	width, height int

	// Data
	items []data.DocsFilesItemInfo

	// Folder navigation
	folderStack    []folderEntry
	currentVaultID int64
	currentTitle   string

	// Create
	creatingDoc    bool
	creatingFolder bool
	createInput    textinput.Model

	// Trash (double-press)
	trashPending   bool
	trashPendingID string
}

// NewDocsFiles creates the docs & files view.
func NewDocsFiles(session *workspace.Session) *DocsFiles {
	styles := session.Styles()
	scope := session.Scope()
	pool := session.Hub().DocsFiles(scope.ProjectID, scope.ToolID)

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyMessage(empty.NoDocsFiles())
	list.SetFocused(true)

	return &DocsFiles{
		session:        session,
		pool:           pool,
		styles:         styles,
		list:           list,
		spinner:        s,
		loading:        true,
		currentVaultID: scope.ToolID,
		currentTitle:   "Docs & Files",
	}
}

// Title implements View.
func (v *DocsFiles) Title() string {
	return v.currentTitle
}

// IsModal implements workspace.ModalActive.
// Modal when inside a sub-folder (Esc should pop the folder, not navigate back).
func (v *DocsFiles) IsModal() bool {
	return len(v.folderStack) > 0
}

// ShortHelp implements View.
func (v *DocsFiles) ShortHelp() []key.Binding {
	if v.list.Filtering() {
		return filterHints()
	}
	if v.creatingDoc || v.creatingFolder {
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "create")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		}
	}
	hints := []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new doc")),
		key.NewBinding(key.WithKeys("N"), key.WithHelp("N", "new folder")),
		key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "trash")),
	}
	return hints
}

// FullHelp implements View.
func (v *DocsFiles) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

// StartFilter implements workspace.Filterable.
func (v *DocsFiles) StartFilter() { v.list.StartFilter() }

// InputActive implements workspace.InputCapturer.
func (v *DocsFiles) InputActive() bool {
	return v.list.Filtering() || v.creatingDoc || v.creatingFolder
}

// SetSize implements View.
func (v *DocsFiles) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.list.SetSize(w, h)
}

// Init implements tea.Model.
func (v *DocsFiles) Init() tea.Cmd {
	snap := v.pool.Get()
	if snap.Usable() {
		v.items = snap.Data
		v.syncList()
		v.loading = false
		if snap.Fresh() {
			return nil
		}
	}
	return tea.Batch(v.spinner.Tick, v.pool.FetchIfStale(v.session.Hub().ProjectContext()))
}

// Update implements tea.Model.
func (v *DocsFiles) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case workspace.FocusMsg:
		return v, v.pool.FetchIfStale(v.session.Hub().ProjectContext())

	case data.PoolUpdatedMsg:
		if msg.Key == v.pool.Key() {
			snap := v.pool.Get()
			if snap.Usable() {
				v.items = snap.Data
				v.syncList()
				v.loading = false
			}
			if snap.State == data.StateError {
				v.loading = false
				return v, workspace.ReportError(snap.Err, "loading docs & files")
			}
			if snap.Loading() && !snap.HasData {
				v.loading = true
			}
		}
		return v, nil

	case docCreatedMsg:
		if msg.err != nil {
			return v, workspace.ReportError(msg.err, "creating document")
		}
		pool := v.session.Hub().DocsFiles(v.session.Scope().ProjectID, msg.vaultID)
		pool.Invalidate()
		if msg.vaultID == v.currentVaultID {
			return v, tea.Batch(
				workspace.SetStatus("Document created", false),
				pool.Fetch(v.session.Hub().ProjectContext()),
			)
		}
		return v, workspace.SetStatus("Document created", false)

	case folderCreatedMsg:
		if msg.err != nil {
			return v, workspace.ReportError(msg.err, "creating folder")
		}
		pool := v.session.Hub().DocsFiles(v.session.Scope().ProjectID, msg.vaultID)
		pool.Invalidate()
		if msg.vaultID == v.currentVaultID {
			return v, tea.Batch(
				workspace.SetStatus("Folder created", false),
				pool.Fetch(v.session.Hub().ProjectContext()),
			)
		}
		return v, workspace.SetStatus("Folder created", false)

	case docsFilesTrashResultMsg:
		if msg.err != nil {
			return v, workspace.ReportError(msg.err, "trashing item")
		}
		pool := v.session.Hub().DocsFiles(v.session.Scope().ProjectID, msg.vaultID)
		pool.Invalidate()
		if msg.vaultID == v.currentVaultID {
			return v, tea.Batch(
				workspace.SetStatus("Trashed", false),
				pool.Fetch(v.session.Hub().ProjectContext()),
			)
		}
		return v, workspace.SetStatus("Trashed", false)

	case docsFilesTrashTimeoutMsg:
		v.trashPending = false
		v.trashPendingID = ""
		return v, nil

	case workspace.RefreshMsg:
		v.pool.Invalidate()
		v.loading = true
		return v, tea.Batch(v.spinner.Tick, v.pool.Fetch(v.session.Hub().ProjectContext()))

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
		if v.creatingDoc || v.creatingFolder {
			return v, v.handleCreateKey(msg)
		}
		return v, v.handleKey(msg)
	}
	return v, nil
}

func (v *DocsFiles) handleKey(msg tea.KeyMsg) tea.Cmd {
	if v.list.Filtering() {
		v.trashPending = false
		v.trashPendingID = ""
		return v.list.Update(msg)
	}

	// Reset trash confirmation on non-t keys
	if msg.String() != "t" {
		v.trashPending = false
		v.trashPendingID = ""
	}

	keys := workspace.DefaultListKeyMap()

	switch {
	case msg.String() == "t":
		return v.trashSelected()
	case msg.String() == "n":
		return v.startCreateDoc()
	case msg.String() == "N":
		return v.startCreateFolder()
	case key.Matches(msg, keys.Open):
		return v.openSelectedItem()
	case msg.Type == tea.KeyEscape || msg.Type == tea.KeyBackspace:
		return v.goBackFolder()
	default:
		return v.list.Update(msg)
	}
}

func (v *DocsFiles) openSelectedItem() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}
	var itemID int64
	fmt.Sscanf(item.ID, "%d", &itemID)

	// Find the item's type from our data
	recordType := "Document"
	var itemTitle string
	for _, it := range v.items {
		if it.ID == itemID {
			recordType = it.Type
			itemTitle = it.Title
			break
		}
	}

	// Folders: push folder stack and swap pool
	if recordType == "Folder" {
		return v.enterFolder(itemID, itemTitle)
	}

	// Record in recents
	if r := v.session.Recents(); r != nil {
		r.Add(recents.Item{
			ID:          item.ID,
			Title:       item.Title,
			Description: recordType,
			Type:        recents.TypeRecording,
			AccountID:   v.session.Scope().AccountID,
			ProjectID:   fmt.Sprintf("%d", v.session.Scope().ProjectID),
		})
	}

	scope := v.session.Scope()
	scope.RecordingID = itemID
	scope.RecordingType = recordType
	return workspace.Navigate(workspace.ViewDetail, scope)
}

func (v *DocsFiles) enterFolder(vaultID int64, title string) tea.Cmd {
	// Save current state
	cursorItemID := ""
	if sel := v.list.Selected(); sel != nil {
		cursorItemID = sel.ID
	}
	v.folderStack = append(v.folderStack, folderEntry{
		vaultID:      v.currentVaultID,
		title:        v.currentTitle,
		cursorIdx:    v.list.SelectedIndex(),
		cursorItemID: cursorItemID,
	})

	// Swap pool to the new vault
	v.currentVaultID = vaultID
	v.currentTitle = title
	v.pool = v.session.Hub().DocsFiles(v.session.Scope().ProjectID, vaultID)

	snap := v.pool.Get()
	if snap.Usable() {
		v.items = snap.Data
		v.syncList()
		v.loading = false
		if snap.Fresh() {
			return func() tea.Msg { return workspace.ChromeSyncMsg{} }
		}
		// Stale but usable — show cached data and refresh in background
		return tea.Batch(
			v.pool.FetchIfStale(v.session.Hub().ProjectContext()),
			func() tea.Msg { return workspace.ChromeSyncMsg{} },
		)
	}

	v.loading = true
	return tea.Batch(
		v.spinner.Tick,
		v.pool.FetchIfStale(v.session.Hub().ProjectContext()),
		func() tea.Msg { return workspace.ChromeSyncMsg{} },
	)
}

func (v *DocsFiles) goBackFolder() tea.Cmd {
	if len(v.folderStack) == 0 {
		return nil
	}

	entry := v.folderStack[len(v.folderStack)-1]
	v.folderStack = v.folderStack[:len(v.folderStack)-1]

	v.currentVaultID = entry.vaultID
	v.currentTitle = entry.title
	v.pool = v.session.Hub().DocsFiles(v.session.Scope().ProjectID, entry.vaultID)

	snap := v.pool.Get()
	if snap.Usable() {
		v.items = snap.Data
		v.syncList()
		v.loading = false
		// Restore cursor: try by ID first, fall back to index
		if entry.cursorItemID != "" {
			if !v.list.SelectByID(entry.cursorItemID) {
				v.list.SelectIndex(entry.cursorIdx)
			}
		} else {
			v.list.SelectIndex(entry.cursorIdx)
		}
	} else {
		v.loading = true
	}

	cmds := []tea.Cmd{
		v.pool.FetchIfStale(v.session.Hub().ProjectContext()),
		func() tea.Msg { return workspace.ChromeSyncMsg{} },
	}
	if v.loading {
		cmds = append(cmds, v.spinner.Tick)
	}
	return tea.Batch(cmds...)
}

// -- Create

func (v *DocsFiles) startCreateDoc() tea.Cmd {
	v.creatingDoc = true
	v.createInput = textinput.New()
	v.createInput.Placeholder = "New document title..."
	v.createInput.Focus()
	return v.createInput.Cursor.BlinkCmd()
}

func (v *DocsFiles) startCreateFolder() tea.Cmd {
	v.creatingFolder = true
	v.createInput = textinput.New()
	v.createInput.Placeholder = "New folder title..."
	v.createInput.Focus()
	return v.createInput.Cursor.BlinkCmd()
}

func (v *DocsFiles) handleCreateKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyEscape:
		v.creatingDoc = false
		v.creatingFolder = false
		return nil
	case tea.KeyEnter:
		title := strings.TrimSpace(v.createInput.Value())
		if title == "" {
			v.creatingDoc = false
			v.creatingFolder = false
			return nil
		}
		if v.creatingDoc {
			v.creatingDoc = false
			return v.createDocument(title)
		}
		v.creatingFolder = false
		return v.createFolder(title)
	default:
		var cmd tea.Cmd
		v.createInput, cmd = v.createInput.Update(msg)
		return cmd
	}
}

func (v *DocsFiles) createDocument(title string) tea.Cmd {
	scope := v.session.Scope()
	hub := v.session.Hub()
	ctx := hub.ProjectContext()
	vaultID := v.currentVaultID
	return func() tea.Msg {
		err := hub.CreateDocument(ctx, scope.AccountID, scope.ProjectID, vaultID, title)
		return docCreatedMsg{vaultID: vaultID, err: err}
	}
}

func (v *DocsFiles) createFolder(title string) tea.Cmd {
	scope := v.session.Scope()
	hub := v.session.Hub()
	ctx := hub.ProjectContext()
	vaultID := v.currentVaultID
	return func() tea.Msg {
		err := hub.CreateVault(ctx, scope.AccountID, scope.ProjectID, vaultID, title)
		return folderCreatedMsg{vaultID: vaultID, err: err}
	}
}

// -- Trash (double-press)

func (v *DocsFiles) trashSelected() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}

	var itemID int64
	fmt.Sscanf(item.ID, "%d", &itemID)

	if v.trashPending && v.trashPendingID == item.ID {
		v.trashPending = false
		v.trashPendingID = ""
		scope := v.session.Scope()
		hub := v.session.Hub()
		ctx := hub.ProjectContext()
		listItemID := item.ID
		vaultID := v.currentVaultID
		return func() tea.Msg {
			err := hub.TrashRecording(ctx, scope.AccountID, scope.ProjectID, itemID)
			return docsFilesTrashResultMsg{vaultID: vaultID, itemID: listItemID, err: err}
		}
	}
	v.trashPending = true
	v.trashPendingID = item.ID
	return tea.Batch(
		workspace.SetStatus("Press t again to trash", false),
		tea.Tick(3*time.Second, func(time.Time) tea.Msg { return docsFilesTrashTimeoutMsg{} }),
	)
}

// View implements tea.Model.
func (v *DocsFiles) View() string {
	if v.loading {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Loading docs & files…")
	}

	if v.creatingDoc || v.creatingFolder {
		label := "New document: "
		if v.creatingFolder {
			label = "New folder: "
		}
		inputLine := lipgloss.NewStyle().
			Foreground(v.styles.Theme().Primary).Bold(true).
			Render(label) + v.createInput.View()
		// Crop the list output to leave room for the input line
		listView := lipgloss.NewStyle().
			MaxHeight(max(1, v.height-1)).
			Render(v.list.View())
		return inputLine + "\n" + listView
	}

	return v.list.View()
}

// -- Data sync

func (v *DocsFiles) syncList() {
	items := make([]widget.ListItem, 0, len(v.items))
	for _, it := range v.items {
		desc := it.Type
		if it.Creator != "" {
			desc += " - " + it.Creator
		}
		if it.CreatedAt != "" {
			desc += " - " + it.CreatedAt
		}

		if it.Type == "Folder" {
			var parts []string
			if it.VaultsCount > 0 {
				parts = append(parts, fmt.Sprintf("%d folders", it.VaultsCount))
			}
			if it.DocsCount > 0 {
				parts = append(parts, fmt.Sprintf("%d docs", it.DocsCount))
			}
			if it.UploadsCount > 0 {
				parts = append(parts, fmt.Sprintf("%d uploads", it.UploadsCount))
			}
			if len(parts) > 0 {
				desc += " (" + strings.Join(parts, ", ") + ")"
			}
		}

		items = append(items, widget.ListItem{
			ID:          fmt.Sprintf("%d", it.ID),
			Title:       it.Title,
			Description: desc,
		})
	}
	v.list.SetItems(items)
}
