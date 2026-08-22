package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

const bookmarksListPath = "/99999/my/bookmarks.json"

func bookmarkRecordingPath(id int64) string {
	return fmt.Sprintf("/99999/recordings/%d/bookmark.json", id)
}

// bookmarksFeedBody builds n bookmarks, each wrapping a recording that carries
// a bucket — the nested field the display rows exist to surface.
func bookmarksFeedBody(n int) string {
	items := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		items = append(items, fmt.Sprintf(`{
			"id": %d,
			"created_at": "2026-07-01T10:00:00.000Z",
			"updated_at": "2026-07-01T10:00:00.000Z",
			"recording": {
				"id": %d,
				"title": "Bookmarked item %d",
				"type": "Todo",
				"status": "active",
				"created_at": "2026-06-01T10:00:00.000Z",
				"updated_at": "2026-06-01T10:00:00.000Z",
				"bucket": {"id": 977190, "name": "JD test proj", "type": "Project"}
			}
		}`, i, 1000+i, i))
	}
	return "[" + strings.Join(items, ",") + "]"
}

func bookmarksListRoute(n int) stubRoute {
	return stubRoute{
		method: http.MethodGet,
		path:   bookmarksListPath,
		status: http.StatusOK,
		body:   bookmarksFeedBody(n),
		// The bounded walk stops on the first empty page, so a page-aware route
		// is what distinguishes it from a full-account crawl.
		pages: []string{bookmarksFeedBody(n)},
	}
}

// setupPersonalFeedApp is the recording test app with its output swapped for a
// JSON envelope writer, so a test can assert on the transport and the rendered
// payload at once.
func setupPersonalFeedApp(t *testing.T, routes ...stubRoute) (*appctx.App, *recordingTransport, *bytes.Buffer) {
	t.Helper()
	app, transport := setupRecordingTestApp(t, routes...)
	out := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: out})
	return app, transport, out
}

// personalFeedEnvelope is the slice of the JSON success envelope these tests read.
type personalFeedEnvelope struct {
	Data    []json.RawMessage `json:"data"`
	Summary string            `json:"summary"`
	Notice  string            `json:"notice"`
}

func decodePersonalFeedEnvelope(t *testing.T, out *bytes.Buffer) personalFeedEnvelope {
	t.Helper()
	var envelope personalFeedEnvelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope))
	return envelope
}

func requireBookmarksUsageError(t *testing.T, err error) *output.Error {
	t.Helper()
	require.Error(t, err)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Equal(t, output.CodeUsage, outErr.Code)
	return outErr
}

// The default must walk positive pages. Page 0 is the SDK's "fetch every page"
// spelling, and reaching it by default is the fetch-everything-then-truncate
// defect the account-wide listings were rewritten to remove.
func TestBookmarksListDefaultWalksPositivePages(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, bookmarksListRoute(2))

	require.NoError(t, executeRecordingCommand(NewBookmarksCmd(), app, "list"))

	queries := transport.queriesFor(bookmarksListPath)
	require.NotEmpty(t, queries)
	for _, q := range queries {
		assert.Contains(t, q, "page=", "no default request may omit page=")
		assert.NotContains(t, q, "page=0")
	}
	assert.Equal(t, "page=1", queries[0])
}

func TestBookmarksListAllFetchesEveryPage(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, bookmarksListRoute(2))

	require.NoError(t, executeRecordingCommand(NewBookmarksCmd(), app, "list", "--all"))

	queries := transport.queriesFor(bookmarksListPath)
	require.Len(t, queries, 1, "--all is a single call into the SDK's own traversal")
	assert.Empty(t, queries[0], "--all omits page= entirely")
}

func TestBookmarksListPageIsExactlyOneRequest(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, bookmarksListRoute(2))

	require.NoError(t, executeRecordingCommand(NewBookmarksCmd(), app, "list", "--page", "3"))

	assert.Equal(t, []string{"page=3"}, transport.queriesFor(bookmarksListPath))
}

func TestBookmarksListLimitTrimsExactly(t *testing.T) {
	app, _, out := setupPersonalFeedApp(t, bookmarksListRoute(5))

	require.NoError(t, executeRecordingCommand(NewBookmarksCmd(), app, "list", "--limit", "2"))

	envelope := decodePersonalFeedEnvelope(t, out)
	assert.Len(t, envelope.Data, 2)
	assert.Equal(t, "2 bookmarks across all projects", envelope.Summary)
}

func TestBookmarksListRejectsUnusablePaging(t *testing.T) {
	assertRejected := func(t *testing.T, wantFragment string, args ...string) {
		t.Helper()
		app, transport, _ := setupPersonalFeedApp(t, bookmarksListRoute(2))

		err := executeRecordingCommand(NewBookmarksCmd(), app, args...)

		outErr := requireBookmarksUsageError(t, err)
		assert.Contains(t, outErr.Message, wantFragment)
		assert.Empty(t, transport.recorded(), "a rejected listing must not reach the server")
	}

	t.Run("explicit --page 0", func(t *testing.T) {
		assertRejected(t, "--page", "list", "--page", "0")
	})
	t.Run("negative --limit", func(t *testing.T) {
		assertRejected(t, "--limit", "list", "--limit=-1")
	})
	t.Run("--all with --limit", func(t *testing.T) {
		assertRejected(t, "--limit", "list", "--all", "--limit", "5")
	})
	t.Run("--page with --all", func(t *testing.T) {
		assertRejected(t, "--page", "list", "--page", "2", "--all")
	})
}

// The generic renderer skips nested objects, so a generic render of a Bookmark
// shows its own id and timestamp and drops the recording entirely — the one
// thing the row is about. These rows are built by hand for exactly that reason.
func TestFlattenBookmarksCarriesTheRecording(t *testing.T) {
	rows := flattenBookmarks([]basecamp.Bookmark{{
		ID:        7,
		CreatedAt: time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC),
		Recording: basecamp.Recording{
			ID:     1001,
			Title:  "Bookmarked item 1",
			Type:   "Todo",
			Bucket: &basecamp.Bucket{ID: 977190, Name: "JD test proj"},
		},
	}})

	require.Len(t, rows, 1)
	assert.Equal(t, int64(1001), rows[0]["id"], "the row's id is the recording's, not the bookmark's")
	assert.Equal(t, "Bookmarked item 1", rows[0]["title"])
	assert.Equal(t, "Todo", rows[0]["type"])
	assert.Equal(t, "JD test proj", rows[0]["project"])
	assert.Contains(t, rows[0], "bookmarked_at")
}

// check answers a question. Both answers are successes, so neither may be
// signaled through the exit code — that space belongs to real failures.
func TestBookmarksCheckReportsBothAnswersAsSuccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"bookmarked", `{"bookmarked": true}`, true},
		{"not bookmarked", `{"bookmarked": false}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, _, out := setupPersonalFeedApp(t, stubRoute{
				method: http.MethodGet,
				path:   bookmarkRecordingPath(42),
				status: http.StatusOK,
				body:   tc.body,
			})

			err := executeRecordingCommand(NewBookmarksCmd(), app, "check", "42")

			require.NoError(t, err, "a false answer is still a successful call")
			var envelope struct {
				Data struct {
					Bookmarked bool `json:"bookmarked"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(out.Bytes(), &envelope))
			assert.Equal(t, tc.want, envelope.Data.Bookmarked)
		})
	}
}

func TestBookmarksCheckAcceptsAURL(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, stubRoute{
		method: http.MethodGet,
		path:   bookmarkRecordingPath(42),
		status: http.StatusOK,
		body:   `{"bookmarked": true}`,
	})

	require.NoError(t, executeRecordingCommand(NewBookmarksCmd(), app,
		"check", "https://3.basecamp.com/1234567/buckets/89/todos/42"))

	assert.Equal(t, bookmarkRecordingPath(42), transport.last(t).Path)
}

func TestBookmarksVerbsRejectANonID(t *testing.T) {
	for _, verb := range []string{"add", "remove", "check"} {
		t.Run(verb, func(t *testing.T) {
			app, transport, _ := setupPersonalFeedApp(t)

			err := executeRecordingCommand(NewBookmarksCmd(), app, verb, "not-an-id")

			outErr := requireBookmarksUsageError(t, err)
			assert.Contains(t, outErr.Hint, "recording id")
			assert.Empty(t, transport.recorded())
		})
	}
}

func TestBookmarksAddPostsToTheRecording(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, stubRoute{
		method: http.MethodPost,
		path:   bookmarkRecordingPath(42),
		status: http.StatusCreated,
		body: `{
			"id": 7,
			"created_at": "2026-07-01T10:00:00.000Z",
			"updated_at": "2026-07-01T10:00:00.000Z",
			"recording": {
				"id": 42, "title": "Ship it", "type": "Todo", "status": "active",
				"created_at": "2026-06-01T10:00:00.000Z",
				"updated_at": "2026-06-01T10:00:00.000Z"
			}
		}`,
	})

	require.NoError(t, executeRecordingCommand(NewBookmarksCmd(), app, "add", "42"))

	call := transport.last(t)
	assert.Equal(t, http.MethodPost, call.Method)
	assert.Equal(t, bookmarkRecordingPath(42), call.Path)
}

func TestBookmarksRemoveDeletesTheBookmark(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, stubRoute{
		method: http.MethodDelete,
		path:   bookmarkRecordingPath(42),
		status: http.StatusNoContent,
		body:   "",
	})

	require.NoError(t, executeRecordingCommand(NewBookmarksCmd(), app, "remove", "42"))

	call := transport.last(t)
	assert.Equal(t, http.MethodDelete, call.Method)
	assert.Equal(t, bookmarkRecordingPath(42), call.Path)
}
