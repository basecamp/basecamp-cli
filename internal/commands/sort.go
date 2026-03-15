package commands

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// validateSortField returns a usage error if field is not in the allowed set.
func validateSortField(field string, allowed []string) error {
	for _, a := range allowed {
		if field == a {
			return nil
		}
	}
	return output.ErrUsage(fmt.Sprintf("invalid sort field %q; valid values: %s", field, strings.Join(allowed, ", ")))
}

// compareDueOn compares two due-date strings (YYYY-MM-DD or empty).
// Empty values sort last in the default (ascending) direction.
func compareDueOn(a, b string) bool {
	if a == "" && b == "" {
		return false
	}
	if a == "" {
		return false // a goes last
	}
	if b == "" {
		return true // b goes last
	}
	return a < b
}

// sortTodos sorts a slice of todos by field with default direction, then reverses if requested.
// Default directions: title/position ascending, created/updated descending, due ascending (empties last).
func sortTodos(todos []basecamp.Todo, field string, reverse bool) {
	sort.SliceStable(todos, func(i, j int) bool {
		switch field {
		case "title":
			return strings.ToLower(todos[i].Title) < strings.ToLower(todos[j].Title)
		case "created":
			return todos[i].CreatedAt.After(todos[j].CreatedAt)
		case "updated":
			return todos[i].UpdatedAt.After(todos[j].UpdatedAt)
		case "position":
			return todos[i].Position < todos[j].Position
		case "due":
			return compareDueOn(todos[i].DueOn, todos[j].DueOn)
		}
		return false
	})
	if reverse {
		slices.Reverse(todos)
	}
}

// sortCards sorts a slice of cards by field with default direction, then reverses if requested.
func sortCards(cards []basecamp.Card, field string, reverse bool) {
	sort.SliceStable(cards, func(i, j int) bool {
		switch field {
		case "title":
			return strings.ToLower(cards[i].Title) < strings.ToLower(cards[j].Title)
		case "created":
			return cards[i].CreatedAt.After(cards[j].CreatedAt)
		case "updated":
			return cards[i].UpdatedAt.After(cards[j].UpdatedAt)
		case "position":
			return cards[i].Position < cards[j].Position
		case "due":
			return compareDueOn(cards[i].DueOn, cards[j].DueOn)
		}
		return false
	})
	if reverse {
		slices.Reverse(cards)
	}
}

// sortMessages sorts a slice of messages by field with default direction, then reverses if requested.
// "title" maps to the Subject field on messages.
func sortMessages(messages []basecamp.Message, field string, reverse bool) {
	sort.SliceStable(messages, func(i, j int) bool {
		switch field {
		case "title":
			return strings.ToLower(messages[i].Subject) < strings.ToLower(messages[j].Subject)
		case "created":
			return messages[i].CreatedAt.After(messages[j].CreatedAt)
		case "updated":
			return messages[i].UpdatedAt.After(messages[j].UpdatedAt)
		}
		return false
	})
	if reverse {
		slices.Reverse(messages)
	}
}

// sortTodolists sorts a slice of todolists by field with default direction, then reverses if requested.
// "title" maps to the Name field on todolists.
func sortTodolists(todolists []basecamp.Todolist, field string, reverse bool) {
	sort.SliceStable(todolists, func(i, j int) bool {
		switch field {
		case "title":
			return strings.ToLower(todolists[i].Name) < strings.ToLower(todolists[j].Name)
		case "created":
			return todolists[i].CreatedAt.After(todolists[j].CreatedAt)
		case "updated":
			return todolists[i].UpdatedAt.After(todolists[j].UpdatedAt)
		case "position":
			return todolists[i].Position < todolists[j].Position
		}
		return false
	})
	if reverse {
		slices.Reverse(todolists)
	}
}

// sortProjects sorts a slice of projects by field with default direction, then reverses if requested.
// "title" maps to the Name field on projects.
func sortProjects(projects []basecamp.Project, field string, reverse bool) {
	sort.SliceStable(projects, func(i, j int) bool {
		switch field {
		case "title":
			return strings.ToLower(projects[i].Name) < strings.ToLower(projects[j].Name)
		case "created":
			return projects[i].CreatedAt.After(projects[j].CreatedAt)
		case "updated":
			return projects[i].UpdatedAt.After(projects[j].UpdatedAt)
		}
		return false
	})
	if reverse {
		slices.Reverse(projects)
	}
}

// sortPeople sorts a slice of people by name, then reverses if requested.
func sortPeople(people []basecamp.Person, field string, reverse bool) {
	sort.SliceStable(people, func(i, j int) bool {
		switch field {
		case "name":
			return strings.ToLower(people[i].Name) < strings.ToLower(people[j].Name)
		}
		return false
	})
	if reverse {
		slices.Reverse(people)
	}
}

// scheduleEntryTitle returns the display title for a schedule entry,
// preferring Summary (the user-facing name) with Title as fallback.
func scheduleEntryTitle(e basecamp.ScheduleEntry) string {
	if e.Summary != "" {
		return e.Summary
	}
	return e.Title
}

// sortScheduleEntries sorts a slice of schedule entries by field with default direction, then reverses if requested.
// "title" uses Summary (the user-visible name) with Title as fallback, matching display behavior.
func sortScheduleEntries(entries []basecamp.ScheduleEntry, field string, reverse bool) {
	sort.SliceStable(entries, func(i, j int) bool {
		switch field {
		case "title":
			return strings.ToLower(scheduleEntryTitle(entries[i])) < strings.ToLower(scheduleEntryTitle(entries[j]))
		case "created":
			return entries[i].CreatedAt.After(entries[j].CreatedAt)
		case "updated":
			return entries[i].UpdatedAt.After(entries[j].UpdatedAt)
		}
		return false
	})
	if reverse {
		slices.Reverse(entries)
	}
}
