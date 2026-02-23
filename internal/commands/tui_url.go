package commands

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
)

// basecampURLPattern matches Basecamp 3/4 URLs.
// Examples:
//
//	https://3.basecamp.com/12345/buckets/67890
//	https://3.basecamp.com/12345/buckets/67890/todos/11111
//	https://3.basecamp.com/12345/buckets/67890/messages/22222
var basecampURLPattern = regexp.MustCompile(
	`^https?://(?:3\.)?basecamp\.com/(\d+)/buckets/(\d+)(?:/(\w+)/(\d+))?`,
)

// parseBasecampURL extracts a ViewTarget and Scope from a Basecamp URL.
// Project-only URLs resolve to ViewDock; URLs with a recording resolve to ViewDetail.
func parseBasecampURL(raw string) (workspace.ViewTarget, workspace.Scope, error) {
	matches := basecampURLPattern.FindStringSubmatch(raw)
	if matches == nil {
		return 0, workspace.Scope{}, fmt.Errorf("not a valid Basecamp URL: %s", raw)
	}

	accountID := matches[1]
	projectID, _ := strconv.ParseInt(matches[2], 10, 64)
	scope := workspace.Scope{
		AccountID: accountID,
		ProjectID: projectID,
	}

	if matches[3] != "" && matches[4] != "" {
		recordingID, _ := strconv.ParseInt(matches[4], 10, 64)
		scope.RecordingID = recordingID
		scope.RecordingType = matches[3]
		return workspace.ViewDetail, scope, nil
	}

	return workspace.ViewDock, scope, nil
}
