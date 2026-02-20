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
	ViewActivity
)

// IsGlobal returns true for view targets that aggregate across all accounts.
func (t ViewTarget) IsGlobal() bool {
	switch t {
	case ViewHome, ViewHey, ViewPulse, ViewAssignments,
		ViewPings, ViewProjects, ViewSearch, ViewActivity:
		return true
	default:
		return false
	}
}

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
	AccountID string // which account this name belongs to
	Name      string
	Err       error
}

// DockLoadedMsg is sent when a project's dock is loaded.
type DockLoadedMsg struct {
	Project basecamp.Project
	Err     error
}

// TodolistInfo is a type alias for data.TodolistInfo.
type TodolistInfo = data.TodolistInfo

// TodoInfo is a type alias for data.TodoInfo.
type TodoInfo = data.TodoInfo

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

// CampfireLineInfo is a type alias for data.CampfireLineInfo.
type CampfireLineInfo = data.CampfireLineInfo

// CampfireLineSentMsg is sent after posting a line.
type CampfireLineSentMsg struct {
	Err error
}

// HeyEntryInfo is a type alias for data.HeyEntryInfo.
type HeyEntryInfo = data.HeyEntryInfo

// Card table messages

// CardColumnInfo is a type alias for data.CardColumnInfo.
type CardColumnInfo = data.CardColumnInfo

// CardInfo is a type alias for data.CardInfo.
type CardInfo = data.CardInfo

// MessageInfo is a type alias for data.MessageInfo.
type MessageInfo = data.MessageInfo

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

// SearchResultInfo is a type alias for data.SearchResultInfo.
type SearchResultInfo = data.SearchResultInfo

// PersonInfo is a type alias for data.PersonInfo.
type PersonInfo = data.PersonInfo

// ScheduleEntryInfo is a type alias for data.ScheduleEntryInfo.
type ScheduleEntryInfo = data.ScheduleEntryInfo

// DocsFilesItemInfo is a type alias for data.DocsFilesItemInfo.
type DocsFilesItemInfo = data.DocsFilesItemInfo

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

// ActivityEntryInfo is a type alias for data.ActivityEntryInfo.
type ActivityEntryInfo = data.ActivityEntryInfo

// AssignmentInfo is a type alias for data.AssignmentInfo.
type AssignmentInfo = data.AssignmentInfo

// PingRoomInfo is a type alias for data.PingRoomInfo.
type PingRoomInfo = data.PingRoomInfo

// TimelineEventInfo is a type alias for data.TimelineEventInfo.
type TimelineEventInfo = data.TimelineEventInfo

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
