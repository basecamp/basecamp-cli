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
