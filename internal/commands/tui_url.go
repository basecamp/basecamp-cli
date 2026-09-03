//go:build dev

package commands

import (
	"regexp"
	"strconv"

	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/urlarg"
)

// bucketOnly matches a project URL with nothing after it. urlarg.Parse wants a
// resource type and answers with no project for these, so they are matched here.
var bucketOnly = regexp.MustCompile(`^https?://[^/]+/(\d+)/buckets/(\d+)/?(?:[?#].*)?$`)

// parseTUITarget reads a Basecamp URL as somewhere for the workspace to open.
//
// The routing is the same one every other command uses for a URL argument, so a
// link that works with `basecamp cards show` works here. What the workspace does
// with the answer is its own business: a tool it has a screen for opens, and one
// it does not lands on the project.
func parseTUITarget(raw string) (workspace.Target, error) {
	if parsed := urlarg.Parse(raw); parsed != nil && parsed.ProjectID != "" {
		project, err := strconv.ParseInt(parsed.ProjectID, 10, 64)
		if err != nil {
			return workspace.Target{}, notALink(raw)
		}

		target := workspace.Target{AccountID: parsed.AccountID, ProjectID: project}
		if parsed.RecordingID != "" {
			id, err := strconv.ParseInt(parsed.RecordingID, 10, 64)
			if err != nil {
				return workspace.Target{}, notALink(raw)
			}
			target.Kind, target.ID = parsed.Type, id
		}
		return target, nil
	}

	if matches := bucketOnly.FindStringSubmatch(raw); matches != nil {
		project, _ := strconv.ParseInt(matches[2], 10, 64)
		return workspace.Target{AccountID: matches[1], ProjectID: project}, nil
	}

	return workspace.Target{}, notALink(raw)
}

func notALink(raw string) error {
	return output.ErrUsage("not a Basecamp URL: " + raw)
}
