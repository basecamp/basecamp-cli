package commands

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/urlarg"
)

// urlPathToRecordingType canonicalizes URL path segments to the recording type
// names that Detail view expects (singular, cased to match API responses).
var urlPathToRecordingType = map[string]string{
	"todos":            "Todo",
	"messages":         "Message",
	"cards":            "Card",
	"uploads":          "Upload",
	"forwards":         "Forward",
	"questions":        "Question",
	"question_answers": "Question::Answer",
	"schedule_entries": "Schedule::Entry",
	"comments":         "Comment",
	"documents":        "Document",
	"columns":          "Kanban::Column",
	"lines":            "Campfire::Line",
}

// bucketOnlyPattern matches project-level bucket URLs without a recording path.
// urlarg.Parse doesn't match these since the SDK router expects a resource type.
// Only accepts /buckets/{id} with optional trailing slash, query string, or fragment.
var bucketOnlyPattern = regexp.MustCompile(
	`^https?://(?:3\.)?basecamp\.com/(\d+)/buckets/(\d+)/?(?:\?[^/]*)?(?:#.*)?$`,
)

// parseBasecampURL extracts a ViewTarget and Scope from a Basecamp URL.
// Recording URLs use the shared urlarg.Parse router for consistent matching;
// project-only bucket URLs fall back to regex.
func parseBasecampURL(raw string) (workspace.ViewTarget, workspace.Scope, error) {
	// Try the full SDK router first — handles recordings, cards, project URLs.
	// Skip results without a ProjectID (e.g. bare /buckets/{id} without a resource).
	if parsed := urlarg.Parse(raw); parsed != nil && parsed.ProjectID != "" {
		projectID, _ := strconv.ParseInt(parsed.ProjectID, 10, 64)
		scope := workspace.Scope{
			AccountID: parsed.AccountID,
			ProjectID: projectID,
		}

		if parsed.RecordingID != "" {
			recordingID, _ := strconv.ParseInt(parsed.RecordingID, 10, 64)
			scope.RecordingID = recordingID

			// Campfire chat URLs → ViewCampfire (not ViewDetail).
			// Handles both /chats/{id} and /chats/{chatID}/lines/{lineID}
			// (for the latter, urlarg returns Type="lines" with the line as
			// RecordingID; we walk up the raw path to find the parent chat).
			if parsed.Type == "chats" {
				scope.ToolType = "chat"
				scope.ToolID = recordingID
				scope.RecordingID = 0 // chat ID is ToolID, not a recording
				return workspace.ViewCampfire, scope, nil
			}
			if parsed.Type == "lines" {
				if chatID := extractParentChatID(raw); chatID != 0 {
					scope.ToolType = "chat"
					scope.ToolID = chatID
					scope.RecordingID = 0
					return workspace.ViewCampfire, scope, nil
				}
			}

			if canonical, ok := urlPathToRecordingType[parsed.Type]; ok {
				scope.RecordingType = canonical
			} else {
				scope.RecordingType = parsed.Type
			}
			return workspace.ViewDetail, scope, nil
		}

		// Project URL (/projects/{id}) — but only if the router didn't extract
		// an unrecognized type segment (e.g. /buckets/42/foobar).
		if parsed.Type == "" || parsed.Type == "project" {
			return workspace.ViewDock, scope, nil
		}
	}

	// Fall back to bucket-only pattern (/buckets/{id} without recording)
	if matches := bucketOnlyPattern.FindStringSubmatch(raw); matches != nil {
		projectID, _ := strconv.ParseInt(matches[2], 10, 64)
		return workspace.ViewDock, workspace.Scope{
			AccountID: matches[1],
			ProjectID: projectID,
		}, nil
	}

	return 0, workspace.Scope{}, fmt.Errorf("not a valid Basecamp URL: %s", raw)
}

// extractParentChatID walks the URL path segments to find /chats/{id} above
// a nested resource like /lines/{id}. Returns 0 if not found.
// No host-hardcoded regex — works with any Basecamp host.
func extractParentChatID(raw string) int64 {
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "chats" {
			id, err := strconv.ParseInt(parts[i+1], 10, 64)
			if err == nil {
				return id
			}
		}
	}
	return 0
}
