// Package workspace provides the persistent TUI application for bcq.
package workspace

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
)

// Navigation messages

// NavigateMsg requests navigation to a new view.
type NavigateMsg struct {
	Target ViewTarget
	Scope  Scope
}

// NavigateBackMsg requests navigation to the previous view.
type NavigateBackMsg struct{}

// NavigateToDepthMsg jumps to a specific breadcrumb depth.
type NavigateToDepthMsg struct {
	Depth int
}

// ViewTarget identifies which view to navigate to.
type ViewTarget int

const (
	ViewProjects ViewTarget = iota
	ViewDock
	ViewTodos
	ViewCampfire
	ViewHey
	ViewCards
	ViewMessages
	ViewSearch
	ViewMyStuff
	ViewPeople
	ViewDetail
	ViewSchedule
	ViewDocsFiles
	ViewCheckins
	ViewForwards
	ViewPulse
	ViewAssignments
	ViewPings
	ViewCompose
	ViewHome
)

// ComposeType identifies what kind of content is being composed.
type ComposeType int

const (
	ComposeMessage ComposeType = iota
)

// MessageCreatedMsg is sent after a message is successfully posted.
type MessageCreatedMsg struct {
	Message MessageInfo
	Err     error
}

// CommentCreatedMsg is sent after a comment is successfully posted.
type CommentCreatedMsg struct {
	RecordingID int64
	Err         error
}

// Scope represents the current position in the Basecamp hierarchy.
type Scope struct {
	AccountID     string
	AccountName   string
	ProjectID     int64
	ProjectName   string
	ToolType      string // "todoset", "chat", "card_table", "message_board", etc.
	ToolID        int64
	RecordingID   int64
	RecordingType string
}

// Data messages

// AccountNameMsg is sent when the account name is resolved.
type AccountNameMsg struct {
	Name string
	Err  error
}

// ProjectsLoadedMsg is sent when projects finish loading.
type ProjectsLoadedMsg struct {
	Projects []basecamp.Project
	Err      error
}

// DockLoadedMsg is sent when a project's dock is loaded.
type DockLoadedMsg struct {
	Project basecamp.Project
	Err     error
}

// TodolistsLoadedMsg is sent when todolists finish loading.
type TodolistsLoadedMsg struct {
	Todolists []TodolistInfo
	Err       error
}

// TodolistInfo is a lightweight representation of a todolist for the view.
type TodolistInfo struct {
	ID             int64
	Title          string
	CompletedRatio string
	TodosURL       string
}

// TodosLoadedMsg is sent when todos for a specific list finish loading.
type TodosLoadedMsg struct {
	TodolistID int64
	Todos      []TodoInfo
	Err        error
}

// TodoInfo is a lightweight representation of a todo for the view.
type TodoInfo struct {
	ID          int64
	Content     string
	Description string
	Completed   bool
	DueOn       string
	Assignees   []string // names
	Position    int
}

// TodoCompletedMsg is sent after a todo completion/uncompletion API call.
type TodoCompletedMsg struct {
	TodolistID int64 // which list the todo belongs to (for safe rollback)
	TodoID     int64
	Completed  bool
	Err        error
}

// TodoCreatedMsg is sent after a todo is created.
type TodoCreatedMsg struct {
	TodolistID int64
	Content    string
	Err        error
}

// Campfire messages

// CampfireLinesLoadedMsg is sent when campfire lines are fetched.
type CampfireLinesLoadedMsg struct {
	Lines      []CampfireLineInfo
	TotalCount int  // total lines available from X-Total-Count
	Prepend    bool // true when loading older messages (prepend to existing)
	Err        error
}

// CampfireLineInfo is a lightweight representation of a campfire line.
type CampfireLineInfo struct {
	ID        int64
	Body      string // HTML content
	Creator   string
	CreatedAt string // formatted time
}

// CampfireLineSentMsg is sent after posting a line.
type CampfireLineSentMsg struct {
	Err error
}

// Hey messages

// HeyEntriesLoadedMsg is sent when inbox entries are loaded.
type HeyEntriesLoadedMsg struct {
	Entries []HeyEntryInfo
	Err     error
}

// HeyEntryInfo is a lightweight representation of an inbox entry.
type HeyEntryInfo struct {
	ID        int64
	Title     string
	Excerpt   string
	Creator   string
	Project   string
	CreatedAt string
	IsRead    bool
}

// Card table messages

// CardColumnsLoadedMsg is sent when card table columns are loaded.
type CardColumnsLoadedMsg struct {
	Columns []CardColumnInfo
	Err     error
}

// CardColumnInfo represents a kanban column with its cards.
type CardColumnInfo struct {
	ID    int64
	Title string
	Color string
	Cards []CardInfo
}

// CardInfo represents a single card.
type CardInfo struct {
	ID        int64
	Title     string
	Assignees []string
	DueOn     string
	Position  int
}

// CardMovedMsg is sent after a card move API call.
type CardMovedMsg struct {
	CardID       int64
	ColumnID     int64 // target column
	SourceColIdx int   // source column index for rollback
	Err          error
}

// Message board messages

// MessagesLoadedMsg is sent when messages are loaded.
type MessagesLoadedMsg struct {
	Messages []MessageInfo
	Err      error
}

// MessageInfo represents a message board post.
type MessageInfo struct {
	ID        int64
	Subject   string
	Creator   string
	CreatedAt string
	Category  string
	Pinned    bool
}

// MessageDetailLoadedMsg is sent when a single message's full content is fetched.
type MessageDetailLoadedMsg struct {
	MessageID int64
	Subject   string
	Creator   string
	CreatedAt string
	Category  string
	Content   string // HTML body
	Err       error
}

// Search messages

// SearchResultsMsg is sent when search results arrive.
type SearchResultsMsg struct {
	Results []SearchResultInfo
	Query   string
	Err     error
}

// SearchResultInfo represents a single search result.
type SearchResultInfo struct {
	ID          int64
	Title       string
	Excerpt     string
	Type        string // "todo", "message", "document", etc.
	Project     string
	ProjectID   int64
	Account     string // account name (populated in multi-account mode)
	AccountID   string // account ID for navigation
	CreatedAt   string
	CreatedAtTS int64 // unix timestamp for sorting
}

// People messages

// PeopleLoadedMsg is sent when the people list finishes loading.
type PeopleLoadedMsg struct {
	People []PersonInfo
	Err    error
}

// PersonInfo is a type alias for data.PersonInfo.
type PersonInfo = data.PersonInfo

// Schedule messages

// ScheduleEntriesLoadedMsg is sent when schedule entries are loaded.
type ScheduleEntriesLoadedMsg struct {
	Entries []ScheduleEntryInfo
	Err     error
}

// ScheduleEntryInfo is a type alias for data.ScheduleEntryInfo.
type ScheduleEntryInfo = data.ScheduleEntryInfo

// Docs & Files messages

// DocsFilesLoadedMsg is sent when vault contents are loaded.
type DocsFilesLoadedMsg struct {
	Items []DocsFilesItemInfo
	Err   error
}

// DocsFilesItemInfo is a type alias for data.DocsFilesItemInfo.
type DocsFilesItemInfo = data.DocsFilesItemInfo

// Check-ins messages

// CheckinQuestionsLoadedMsg is sent when check-in questions are loaded.
type CheckinQuestionsLoadedMsg struct {
	Questions []CheckinQuestionInfo
	Err       error
}

// CheckinQuestionInfo is a type alias for data.CheckinQuestionInfo.
type CheckinQuestionInfo = data.CheckinQuestionInfo

// Multi-account messages

// AccountInfo represents a discovered Basecamp account.
type AccountInfo struct {
	ID   string
	Name string
}

// AccountsDiscoveredMsg is sent when multi-account discovery completes.
type AccountsDiscoveredMsg struct {
	Accounts []AccountInfo
	Err      error
}

// MultiAccountProjectsLoadedMsg is sent when projects from all accounts arrive.
type MultiAccountProjectsLoadedMsg struct {
	AccountProjects []AccountProjectGroup
	Err             error
}

// AccountProjectGroup holds projects for a single account.
type AccountProjectGroup struct {
	Account  AccountInfo
	Projects []basecamp.Project
}

// ActivityEntriesLoadedMsg is sent when cross-account activity entries arrive.
type ActivityEntriesLoadedMsg struct {
	Entries []ActivityEntryInfo
	Err     error
}

// ActivityEntryInfo represents a recording from any account for the activity feed.
type ActivityEntryInfo struct {
	ID          int64
	Title       string
	Type        string // "Todo", "Message", "Document", etc.
	Creator     string
	Account     string
	AccountID   string
	Project     string
	ProjectID   int64
	UpdatedAt   string // formatted time
	UpdatedAtTS int64  // unix timestamp for sorting
}

// AssignmentsLoadedMsg is sent when cross-account todo assignments arrive.
type AssignmentsLoadedMsg struct {
	Assignments []AssignmentInfo
	Err         error
}

// AssignmentInfo represents a todo assigned to the current user.
type AssignmentInfo struct {
	ID        int64
	Content   string
	DueOn     string
	Completed bool
	Account   string
	AccountID string
	Project   string
	ProjectID int64
	Overdue   bool
}

// PingRoomsLoadedMsg is sent when 1:1 campfire rooms are discovered.
type PingRoomsLoadedMsg struct {
	Rooms []PingRoomInfo
	Err   error
}

// PingRoomInfo represents a 1:1 campfire thread.
type PingRoomInfo struct {
	CampfireID  int64
	ProjectID   int64
	PersonName  string
	Account     string
	AccountID   string
	LastMessage string
	LastAt      string
	LastAtTS    int64 // unix timestamp for sorting
}

// Home dashboard messages — separate types to avoid collision with view-specific messages.

// HomeHeyLoadedMsg carries activity entries for the home dashboard.
type HomeHeyLoadedMsg struct {
	Entries []ActivityEntryInfo
	Err     error
}

// HomeAssignmentsLoadedMsg carries assignment data for the home dashboard.
type HomeAssignmentsLoadedMsg struct {
	Assignments []AssignmentInfo
	Err         error
}

// HomeProjectsLoadedMsg carries projects (for bookmarks) for the home dashboard.
type HomeProjectsLoadedMsg struct {
	Projects []basecamp.Project
	Err      error
}

// ProjectBookmarkedMsg is sent after toggling a project bookmark.
type ProjectBookmarkedMsg struct {
	ProjectID  int64
	Bookmarked bool
	Err        error
}

// ErrorMsg wraps an error for display.
type ErrorMsg struct {
	Err     error
	Context string // what was being attempted
}

// StatusMsg sets a temporary status message.
type StatusMsg struct {
	Text    string
	IsError bool
}

// Epoch guard

// EpochMsg wraps an async result with the session epoch at Cmd creation time.
// The workspace drops EpochMsgs whose epoch differs from the current session
// epoch, preventing stale results from a previous account from reaching the
// active view after an account switch.
type EpochMsg struct {
	Epoch uint64
	Inner tea.Msg
}

// Chrome messages

// ToggleHelpMsg toggles the help overlay.
type ToggleHelpMsg struct{}

// TogglePaletteMsg toggles the command palette.
type TogglePaletteMsg struct{}

// RefreshMsg requests a data refresh for the current view.
type RefreshMsg struct{}

// FocusMsg indicates a view gained focus.
type FocusMsg struct{}

// BlurMsg indicates a view lost focus.
type BlurMsg struct{}

// Command factories

// Navigate returns a command that sends a NavigateMsg.
func Navigate(target ViewTarget, scope Scope) tea.Cmd {
	return func() tea.Msg {
		return NavigateMsg{Target: target, Scope: scope}
	}
}

// NavigateBack returns a command that sends a NavigateBackMsg.
func NavigateBack() tea.Cmd {
	return func() tea.Msg {
		return NavigateBackMsg{}
	}
}

// ReportError returns a command that sends an ErrorMsg.
func ReportError(err error, context string) tea.Cmd {
	return func() tea.Msg {
		return ErrorMsg{Err: err, Context: context}
	}
}

// SetStatus returns a command that sets a status message.
func SetStatus(text string, isError bool) tea.Cmd {
	return func() tea.Msg {
		return StatusMsg{Text: text, IsError: isError}
	}
}
