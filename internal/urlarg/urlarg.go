// Package urlarg provides utilities for parsing Basecamp URLs into IDs.
// This allows users to paste URLs from the browser as command arguments.
package urlarg

import (
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// Parsed represents components extracted from a Basecamp URL.
type Parsed struct {
	AccountID   string
	ProjectID   string // BucketID in Basecamp terminology
	Type        string // e.g., "todos", "messages", "cards"
	RecordingID string
	CommentID   string
}

// router is the shared SDK router instance.
var router = basecamp.DefaultRouter()

// IsURL checks if the input looks like a Basecamp URL.
// Returns true if the URL can be matched (either as an API route or structurally).
func IsURL(input string) bool {
	return router.Match(input) != nil
}

// Parse extracts IDs from a Basecamp URL.
// Returns nil if the input is not a valid Basecamp URL.
//
// Supported URL patterns:
//   - https://3.basecamp.com/{account}/buckets/{bucket}/{type}/{id}
//   - https://3.basecamp.com/{account}/buckets/{bucket}/{type}/{id}#__recording_{comment}
//   - https://3.basecamp.com/{account}/buckets/{bucket}/card_tables/cards/{id}
//   - https://3.basecamp.com/{account}/projects/{project}
func Parse(input string) *Parsed {
	m := router.Match(input)
	if m == nil {
		return nil
	}
	return &Parsed{
		AccountID:   m.AccountID,
		ProjectID:   m.ProjectID,
		Type:        m.PathType,
		RecordingID: m.ResourceID(),
		CommentID:   m.CommentID,
	}
}

// ExtractID extracts the primary ID from an argument.
// If the argument is a Basecamp URL, extracts the recording ID.
// Otherwise, returns the argument as-is (assumed to be an ID).
func ExtractID(arg string) string {
	if parsed := Parse(arg); parsed != nil {
		if parsed.RecordingID != "" {
			return parsed.RecordingID
		}
		if parsed.ProjectID != "" {
			return parsed.ProjectID
		}
		if parsed.AccountID != "" {
			return parsed.AccountID
		}
	}
	return arg
}

// ExtractProjectID extracts the project (bucket) ID from an argument.
// If the argument is a Basecamp URL, extracts the bucket ID.
// Otherwise, returns the argument as-is.
func ExtractProjectID(arg string) string {
	if parsed := Parse(arg); parsed != nil && parsed.ProjectID != "" {
		return parsed.ProjectID
	}
	return arg
}

// ExtractWithProject extracts both the recording ID and project ID from an argument.
// Returns (recordingID, projectID). If projectID is empty, it wasn't in the URL.
func ExtractWithProject(arg string) (recordingID, projectID string) {
	if parsed := Parse(arg); parsed != nil {
		return parsed.RecordingID, parsed.ProjectID
	}
	return arg, ""
}

// ExtractCommentWithProject extracts a comment ID and project ID from an argument.
// For URLs with a fragment (e.g., #__recording_789), returns the comment ID.
// For URLs without a fragment, returns the recording ID (same as ExtractWithProject).
// Returns (commentOrRecordingID, projectID).
func ExtractCommentWithProject(arg string) (id, projectID string) {
	if parsed := Parse(arg); parsed != nil {
		// Prefer comment ID from fragment when present
		if parsed.CommentID != "" {
			return parsed.CommentID, parsed.ProjectID
		}
		return parsed.RecordingID, parsed.ProjectID
	}
	return arg, ""
}

// ExtractIDs extracts IDs from multiple arguments, handling URLs.
func ExtractIDs(args []string) []string {
	result := make([]string, len(args))
	for i, arg := range args {
		result[i] = ExtractID(arg)
	}
	return result
}
