package data

// Data transfer types for Hub pool accessors.
// Migrated from workspace/msg.go to break the workspace→data import direction
// and allow Hub FetchFuncs to return typed data without import cycles.

// ScheduleEntryInfo is a lightweight representation of a schedule entry.
type ScheduleEntryInfo struct {
	ID           int64
	Summary      string
	StartsAt     string
	EndsAt       string
	AllDay       bool
	Participants []string
}

// CheckinQuestionInfo is a lightweight representation of a check-in question.
type CheckinQuestionInfo struct {
	ID           int64
	Title        string
	Paused       bool
	AnswersCount int
	Frequency    string
}

// DocsFilesItemInfo is a lightweight representation of a vault item.
type DocsFilesItemInfo struct {
	ID        int64
	Title     string
	Type      string // "Folder", "Document", "Upload"
	CreatedAt string
	Creator   string
}

// PersonInfo is a lightweight representation of a person for the view.
type PersonInfo struct {
	ID         int64
	Name       string
	Email      string
	Title      string
	Admin      bool
	Owner      bool
	Client     bool
	PersonType string // "User", "Client", etc.
	Company    string
}

// ForwardInfo is a lightweight representation of an email forward.
type ForwardInfo struct {
	ID      int64
	Subject string
	From    string
}

// CampfireLineInfo is a lightweight representation of a campfire line.
type CampfireLineInfo struct {
	ID        int64
	Body      string // HTML content
	Creator   string
	CreatedAt string // formatted time
}

// CampfireLinesResult holds the lines plus pagination metadata from a
// campfire fetch. This compound type is the Pool's data value so that
// views can access TotalCount for pagination without a side-channel.
type CampfireLinesResult struct {
	Lines      []CampfireLineInfo
	TotalCount int
}

// MessageInfo represents a message board post.
type MessageInfo struct {
	ID         int64
	Subject    string
	Creator    string
	CreatedAt  string
	Category   string
	Pinned     bool
	BoostEmbed // embedded boost support
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

// CardColumnInfo represents a kanban column with its cards.
type CardColumnInfo struct {
	ID         int64
	Title      string
	Color      string
	Type       string // "Kanban::Triage", "Kanban::Column", "Kanban::DoneColumn", "Kanban::NotNowColumn"
	CardsCount int    // from column metadata (available without fetching cards)
	Deferred   bool   // true when cards were not fetched (Done/NotNow columns)
	Cards      []CardInfo
}

// CardInfo represents a single card.
type CardInfo struct {
	ID            int64
	Title         string
	Assignees     []string
	DueOn         string
	Position      int
	Completed     bool
	StepsTotal    int
	StepsDone     int
	CommentsCount int
	BoostEmbed    // embedded boost support
}

// TodolistInfo is a lightweight representation of a todolist for the view.
type TodolistInfo struct {
	ID             int64
	Title          string
	CompletedRatio string
	TodosURL       string
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
	BoostEmbed  // embedded boost support
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

// DockToolInfo represents an enabled tool on a project's dock.
type DockToolInfo struct {
	ID      int64
	Name    string // "todoset", "chat", "message_board", etc.
	Title   string
	Enabled bool
}

// ProjectInfo wraps a project with account attribution for multi-account pools.
// basecamp.Project doesn't carry which account it belongs to, so the Hub's
// Projects() FetchFunc annotates each project during fan-out.
type ProjectInfo struct {
	ID          int64
	Name        string
	Description string
	Purpose     string
	Bookmarked  bool
	AccountID   string
	AccountName string
	Dock        []DockToolInfo
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

// TimelineEventInfo is a lightweight representation of a timeline event.
type TimelineEventInfo struct {
	ID             int64
	RecordingID    int64  // parent recording ID for navigation/detail
	CreatedAt      string // formatted time
	CreatedAtTS    int64  // unix timestamp for sorting
	Kind           string // "todo_completed", "message_created", etc.
	Action         string // "completed", "created", "commented on"
	Target         string // "Todo", "Message", "Document"
	Title          string
	SummaryExcerpt string // first ~100 chars of body
	Creator        string
	Project        string
	ProjectID      int64
	Account        string
	AccountID      string
}

// BoostInfo is a lightweight representation of a boost (emoji reaction).
type BoostInfo struct {
	ID        int64
	Content   string // emoji or short text
	Booster   string // name of person who boosted
	BoosterID int64
	CreatedAt string // formatted time
}

// BoostSummary holds boost metadata for display on list items.
// Includes both a count and a preview of who's boosting.
type BoostSummary struct {
	Count   int            // total boost count
	Preview []BoostPreview // compact preview for list display
}

// BoostPreview is a single boost preview for list display.
// Shows either emoji content or avatar indicator depending on context.
type BoostPreview struct {
	Content   string // the boost content (emoji or text)
	BoosterID int64  // for avatar-based display
}

// RecordingWithBoosts extends recording info types to include boost metadata.
type RecordingWithBoosts interface {
	GetBoosts() BoostSummary
}

// GetBoosts returns an empty boost summary - recording types
// should embed BoostEmbed and override this if they have boosts.
func (b BoostSummary) GetBoosts() BoostSummary {
	return b
}

// BoostEmbed can be embedded in recording info types to add boost support.
type BoostEmbed struct {
	BoostsSummary BoostSummary
}

// GetBoosts returns the embedded boost summary.
func (be BoostEmbed) GetBoosts() BoostSummary {
	return be.BoostsSummary
}

// SetBoosts sets the boost summary on the embedded field.
func (be *BoostEmbed) SetBoosts(summary BoostSummary) {
	be.BoostsSummary = summary
}
