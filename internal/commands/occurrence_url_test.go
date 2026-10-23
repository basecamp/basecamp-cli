package commands

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

const (
	occurrenceURL     = "https://3.basecamp.com/99999/buckets/123/schedule_entries/789/occurrences/20261022"
	occurrenceEditURL = occurrenceURL + "/edit"
	entryURL          = "https://3.basecamp.com/99999/buckets/123/schedule_entries/789"
)

// occurrenceTransport records every request and answers each with entry 789,
// which is enough for a command to reach the API.
type occurrenceTransport struct {
	mu       sync.Mutex
	requests []string
}

func (t *occurrenceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.requests = append(t.requests, req.Method+" "+req.URL.Path)
	t.mu.Unlock()
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"id": 789}`)),
		Header:     header,
		Request:    req,
	}, nil
}

type occurrenceCase struct {
	name string
	cmd  func() *cobra.Command
	args string // %s is the target
}

// Between them, these commands reach the entry through each shared extractor.
var occurrenceCases = []occurrenceCase{
	{"schedule update", NewScheduleCmd, "update %s --summary X"},
	{"recordings trash", NewRecordingsCmd, "trash %s"},
	{"recordings archive", NewRecordingsCmd, "archive %s"},
	{"comments create", NewCommentsCmd, "create %s hello"},
	{"comments list", NewCommentsCmd, "list %s"},
	{"boost create", NewBoostsCmd, "create %s yes"},
	{"subscriptions subscribe", NewSubscriptionsCmd, "subscribe %s"},
	{"attachments list", NewAttachmentsCmd, "list %s"},
}

func runOccurrenceCase(t *testing.T, c occurrenceCase, target string) ([]string, error) {
	t.Helper()
	transport := &occurrenceTransport{}
	app, _ := setupMessagesMockApp(t, transport)
	err := executeMessagesCommand(c.cmd(), app, strings.Fields(fmt.Sprintf(c.args, target))...)
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.requests, err
}

func TestOccurrenceURLIsRefused(t *testing.T) {
	for _, c := range occurrenceCases {
		for _, target := range []string{occurrenceURL, occurrenceEditURL} {
			t.Run(c.name+" "+target, func(t *testing.T) {
				requests, err := runOccurrenceCase(t, c, target)

				var outErr *output.Error
				require.True(t, errors.As(err, &outErr), "want a usage error, got %v", err)
				assert.Equal(t, output.CodeUsage, outErr.Code)
				assert.Contains(t, outErr.Message, "occurrence")
				assert.Contains(t, outErr.Hint, "789", "the hint names the event's ID")
				assert.Empty(t, requests)
			})
		}
	}
}

func TestEntryURLAndIDStillReachTheEntry(t *testing.T) {
	for _, c := range occurrenceCases {
		for _, target := range []string{entryURL, "789"} {
			t.Run(c.name+" "+target, func(t *testing.T) {
				requests, err := runOccurrenceCase(t, c, target)

				var outErr *output.Error
				if errors.As(err, &outErr) {
					assert.NotContains(t, outErr.Message, "occurrence")
				}
				assert.True(t, slicesContainSubstring(requests, "/789"), "no request for entry 789: %v", requests)
			})
		}
	}
}

func TestCommentFragmentOnOccurrenceURLNamesTheComment(t *testing.T) {
	transport := &occurrenceTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	err := executeMessagesCommand(NewCommentsCmd(), app, "update", occurrenceURL+"#__recording_555", "edited")
	require.NoError(t, err)
	assert.True(t, slicesContainSubstring(transport.requests, "/comments/555"), "requests: %v", transport.requests)
}

func TestScheduleShowOccurrenceEditURL(t *testing.T) {
	transport := &mockScheduleShowTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	err := executeMessagesCommand(NewScheduleCmd(), app, "show", occurrenceEditURL)
	require.NoError(t, err)
	assert.True(t, slicesContainSubstring(transport.requests, "/schedule_entries/789/occurrences/20261022"),
		"requests: %v", transport.requests)
}

func TestShowOccurrenceEditURL(t *testing.T) {
	reqs, err := runShowCmd(t, &showTrackingTransport{}, occurrenceEditURL)
	require.NoError(t, err)
	require.NotEmpty(t, reqs)
	assert.Contains(t, reqs[0], "/schedule_entries/789/occurrences/20261022.json")
}

func slicesContainSubstring(items []string, sub string) bool {
	for _, item := range items {
		if strings.Contains(item, sub) {
			return true
		}
	}
	return false
}
