// Package empty provides empty state messages for TUI components.
package empty

// Message represents an empty state message with optional hints.
type Message struct {
	Title   string
	Body    string
	Hints   []string
	Command string // suggested command to run
}

// NoProjects returns the empty state for no projects found.
func NoProjects() Message {
	return Message{
		Title: "No projects found",
		Body:  "You don't have access to any Basecamp projects.",
		Hints: []string{
			"Ask your administrator to add you to a project",
			"Create a new project in Basecamp",
		},
	}
}

// NoTodolists returns the empty state for no todolists found.
func NoTodolists(projectName string) Message {
	return Message{
		Title: "No todolists found",
		Body:  "This project doesn't have any todolists yet.",
		Hints: []string{
			"Create a todolist in Basecamp",
		},
	}
}

// NoTodos returns the empty state for no todos found.
func NoTodos(context string) Message {
	msg := Message{
		Title: "No todos found",
	}
	switch context {
	case "completed":
		msg.Body = "No completed todos."
	case "pending":
		msg.Body = "No pending todos. Everything is done!"
	case "overdue":
		msg.Body = "No overdue todos. You're on track!"
	default:
		msg.Body = "No todos in this project."
		msg.Hints = []string{
			"Create a todo with: basecamp todo --content <text>",
		}
	}
	return msg
}

// NoRecordings returns the empty state for no recordings found.
func NoRecordings(recordingType string) Message {
	typeName := recordingType
	if typeName == "" {
		typeName = "recordings"
	}
	return Message{
		Title: "No " + typeName + " found",
		Body:  "No matching items to display.",
	}
}

// NoPeople returns the empty state for no people found.
func NoPeople() Message {
	return Message{
		Title: "No people found",
		Body:  "No team members in this project.",
	}
}

// NoSearchResults returns the empty state for empty search results.
func NoSearchResults(query string) Message {
	return Message{
		Title: "No results found",
		Body:  "No items match your search.",
		Hints: []string{
			"Try a different search term",
			"Check spelling",
		},
	}
}

// NoComments returns the empty state for no comments found.
func NoComments() Message {
	return Message{
		Title: "No comments",
		Body:  "This item has no comments yet.",
		Hints: []string{
			"Add a comment with: basecamp comment --on <id> --content <text>",
		},
	}
}

// NoRecentItems returns the empty state for no recent items.
func NoRecentItems(itemType string) Message {
	return Message{
		Title: "No recent " + itemType + "s",
		Body:  "Your recently used items will appear here.",
	}
}

// FilterNoMatch returns the empty state when filters yield no results.
func FilterNoMatch() Message {
	return Message{
		Title: "No matches",
		Body:  "No items match your filter.",
		Hints: []string{
			"Press Backspace to clear the filter",
			"Try a different search term",
		},
	}
}

// NetworkError returns an error state for network issues.
func NetworkError() Message {
	return Message{
		Title: "Connection error",
		Body:  "Could not connect to Basecamp.",
		Hints: []string{
			"Check your internet connection",
			"Try again in a few moments",
		},
	}
}

// AuthRequired returns an error state for authentication issues.
func AuthRequired() Message {
	return Message{
		Title: "Authentication required",
		Body:  "You need to log in to Basecamp.",
		Hints: []string{
			"Run: basecamp auth login",
		},
		Command: "basecamp auth login",
	}
}

// NoMessages returns the empty state for no messages.
func NoMessages() Message {
	return Message{
		Title: "No messages",
		Body:  "This project doesn't have any messages yet.",
		Hints: []string{"Press n to write a new message"},
	}
}

// NoScheduleEntries returns the empty state for no schedule entries.
func NoScheduleEntries() Message {
	return Message{
		Title: "No schedule entries",
		Body:  "This project doesn't have any schedule entries yet.",
	}
}

// NoDocsFiles returns the empty state for no documents or files.
func NoDocsFiles() Message {
	return Message{
		Title: "No documents or files",
		Body:  "This project doesn't have any documents or files yet.",
	}
}

// NoPings returns the empty state for no ping threads.
func NoPings() Message {
	return Message{
		Title: "No ping threads",
		Body:  "No active ping conversations.",
	}
}

// NoCheckins returns the empty state for no check-in questions.
func NoCheckins() Message {
	return Message{
		Title: "No check-in questions",
		Body:  "This project doesn't have any automatic check-ins.",
	}
}

// NoForwards returns the empty state for no email forwards.
func NoForwards() Message {
	return Message{
		Title: "No email forwards",
		Body:  "No emails have been forwarded to this project.",
	}
}

// NoAssignments returns the empty state for no assignments.
func NoAssignments() Message {
	return Message{
		Title: "No assignments",
		Body:  "You don't have any assignments right now.",
	}
}

// NoTimeline returns the empty state for no timeline activity.
func NoTimeline() Message {
	return Message{
		Title: "No recent activity",
		Body:  "No recent activity for this project.",
	}
}

// NoColumns returns the empty state for no card table columns.
func NoColumns() Message {
	return Message{
		Title: "No columns",
		Body:  "This card table doesn't have any columns yet.",
	}
}

// NoDockTools returns the empty state for no dock tools.
func NoDockTools() Message {
	return Message{
		Title: "No tools enabled",
		Body:  "This project doesn't have any tools enabled.",
	}
}
