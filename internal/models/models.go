// Package models provides canonical type definitions for Basecamp API entities.
// These types are used throughout the SDK and CLI for API responses.
package models

// Person represents a Basecamp person reference.
// Used for assignees, creators, and other user references.
type Person struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Todo represents a Basecamp todo item.
type Todo struct {
	ID          int64    `json:"id"`
	Content     string   `json:"content"`
	Description string   `json:"description,omitempty"`
	DueOn       string   `json:"due_on,omitempty"`
	Completed   bool     `json:"completed"`
	Assignees   []Person `json:"assignees,omitempty"`
}

// Todolist represents a Basecamp todolist container.
type Todolist struct {
	ID                  int64  `json:"id"`
	Name                string `json:"name"`
	Description         string `json:"description,omitempty"`
	TodosRemainingCount int    `json:"todos_remaining_count"`
	CompletedRatio      string `json:"completed_ratio,omitempty"`
}

// Project represents a Basecamp project (bucket).
type Project struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status,omitempty"`
	Purpose     string `json:"purpose,omitempty"`
	Bookmarked  bool   `json:"bookmarked,omitempty"`
	URL         string `json:"url,omitempty"`
	AppURL      string `json:"app_url,omitempty"`
}

// Message represents a Basecamp message board message.
type Message struct {
	ID        int64  `json:"id"`
	Subject   string `json:"subject"`
	Content   string `json:"content,omitempty"`
	CreatedAt string `json:"created_at"`
	Creator   Person `json:"creator,omitempty"`
}

// Card represents a Basecamp card table card.
type Card struct {
	ID        int64    `json:"id"`
	Title     string   `json:"title"`
	Content   string   `json:"content,omitempty"`
	DueOn     string   `json:"due_on,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
	Parent    *Parent  `json:"parent,omitempty"`
	Assignees []Person `json:"assignees,omitempty"`
}

// Parent represents a parent container (column, list, etc.).
type Parent struct {
	ID    int64  `json:"id,omitempty"`
	Title string `json:"title,omitempty"`
}

// CardColumn represents a card table column.
type CardColumn struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	CardsCount int    `json:"cards_count"`
}

// Comment represents a comment on a Basecamp recording.
type Comment struct {
	ID        int64  `json:"id"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
	Creator   Person `json:"creator,omitempty"`
}

// Recording represents a generic Basecamp recording.
// Recordings are the base type for messages, todos, comments, etc.
type Recording struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title,omitempty"`
	Content   string `json:"content,omitempty"`
	CreatedAt string `json:"created_at"`
	Creator   Person `json:"creator,omitempty"`
}

// CampfireLine represents a campfire chat line.
type CampfireLine struct {
	ID        int64  `json:"id"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
	Creator   Person `json:"creator,omitempty"`
}

// ScheduleEntry represents a schedule entry.
type ScheduleEntry struct {
	ID          int64  `json:"id"`
	Summary     string `json:"summary"`
	Description string `json:"description,omitempty"`
	StartsAt    string `json:"starts_at"`
	EndsAt      string `json:"ends_at,omitempty"`
	AllDay      bool   `json:"all_day"`
}

// SearchResult represents a search result item.
type SearchResult struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Type      string `json:"type"`
	URL       string `json:"url,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}
