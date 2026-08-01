package commands

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

const draftsListPath = "/99999/my/drafts.json"

// draftsFeedBody builds n drafts. The first is bucket-rooted and unscheduled
// (both nil-able fields absent), the rest carry a parent and a scheduled time,
// so one fixture exercises both display states.
func draftsFeedBody(n int) string {
	items := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		parent := "null"
		scheduled := "null"
		if i > 1 {
			parent = `{"id": 555, "title": "Kickoff", "app_url": "https://3.basecamp.com/x"}`
			scheduled = `"2026-08-15T09:00:00.000Z"`
		}
		items = append(items, fmt.Sprintf(`{
			"id": %d,
			"app_url": "https://3.basecamp.com/draft/%d",
			"title": "Draft %d",
			"type": "message",
			"bucket": {"id": 977190, "name": "JD test proj", "app_url": "https://3.basecamp.com/p"},
			"parent": %s,
			"excerpt": "",
			"created_at": "2026-07-01T10:00:00.000Z",
			"updated_at": "2026-07-02T10:00:00.000Z",
			"scheduled_posting_at": %s
		}`, i, i, i, parent, scheduled))
	}
	return "[" + strings.Join(items, ",") + "]"
}

func draftsListRoute(n int) stubRoute {
	return stubRoute{
		method: http.MethodGet,
		path:   draftsListPath,
		status: http.StatusOK,
		body:   draftsFeedBody(n),
		pages:  []string{draftsFeedBody(n)},
	}
}

// The default must walk positive pages rather than reaching page 0, which is
// the SDK's "fetch every page" spelling.
func TestDraftsListDefaultWalksPositivePages(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, draftsListRoute(2))

	require.NoError(t, executeRecordingCommand(NewDraftsCmd(), app, "list"))

	queries := transport.queriesFor(draftsListPath)
	require.NotEmpty(t, queries)
	for _, q := range queries {
		assert.Contains(t, q, "page=", "no default request may omit page=")
		assert.NotContains(t, q, "page=0")
	}
	assert.Equal(t, "page=1", queries[0])
}

func TestDraftsListAllFetchesEveryPage(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, draftsListRoute(2))

	require.NoError(t, executeRecordingCommand(NewDraftsCmd(), app, "list", "--all"))

	queries := transport.queriesFor(draftsListPath)
	require.Len(t, queries, 1)
	assert.Empty(t, queries[0], "--all omits page= entirely")
}

func TestDraftsListPageIsExactlyOneRequest(t *testing.T) {
	app, transport, _ := setupPersonalFeedApp(t, draftsListRoute(2))

	require.NoError(t, executeRecordingCommand(NewDraftsCmd(), app, "list", "--page", "2"))

	assert.Equal(t, []string{"page=2"}, transport.queriesFor(draftsListPath))
}

func TestDraftsListLimitTrimsExactly(t *testing.T) {
	app, _, out := setupPersonalFeedApp(t, draftsListRoute(5))

	require.NoError(t, executeRecordingCommand(NewDraftsCmd(), app, "list", "--limit", "2"))

	envelope := decodePersonalFeedEnvelope(t, out)
	assert.Len(t, envelope.Data, 2)
	assert.Equal(t, "2 drafts across all projects", envelope.Summary)
}

func TestDraftsListRejectsUnusablePaging(t *testing.T) {
	assertRejected := func(t *testing.T, args ...string) {
		t.Helper()
		app, transport, _ := setupPersonalFeedApp(t, draftsListRoute(2))

		err := executeRecordingCommand(NewDraftsCmd(), app, args...)

		requireBookmarksUsageError(t, err)
		assert.Empty(t, transport.recorded(), "a rejected listing must not reach the server")
	}

	t.Run("explicit --page 0", func(t *testing.T) { assertRejected(t, "list", "--page", "0") })
	t.Run("negative --limit", func(t *testing.T) { assertRejected(t, "list", "--limit=-1") })
	t.Run("--all with --limit", func(t *testing.T) { assertRejected(t, "list", "--all", "--limit", "5") })
	t.Run("--page with --all", func(t *testing.T) { assertRejected(t, "list", "--page", "2", "--all") })
}

// A Draft nests its project, and the generic renderer skips nested objects — so
// a generic render drops the one column that makes a cross-project row
// actionable. Both nil-able fields are display states rather than gaps.
func TestFlattenDraftsRendersBothNilStates(t *testing.T) {
	scheduled := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	rows := flattenDrafts([]basecamp.Draft{
		{
			ID:     1,
			Title:  "Bucket-rooted draft",
			Type:   "message",
			Bucket: basecamp.DraftBucket{ID: 977190, Name: "JD test proj"},
		},
		{
			ID:                 2,
			Title:              "Filed and scheduled",
			Type:               "document",
			Bucket:             basecamp.DraftBucket{ID: 977190, Name: "JD test proj"},
			Parent:             &basecamp.DraftParent{ID: 555, Title: "Kickoff"},
			ScheduledPostingAt: &scheduled,
		},
	})

	require.Len(t, rows, 2)

	assert.Equal(t, "JD test proj", rows[0]["project"], "the project is what makes the row attributable")
	assert.Equal(t, "project root", rows[0]["filed_under"], "no parent is a state, not a blank")
	assert.Equal(t, "not scheduled", rows[0]["scheduled"], "unscheduled is a state, not a blank")

	assert.Equal(t, "Kickoff", rows[1]["filed_under"])
	assert.Equal(t, scheduled, rows[1]["scheduled"])
}
