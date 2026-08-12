package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/names"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// forwardsAccountWidePath is the account-wide aggregate feed for the harness
// account; forwardsInboxPath is the project-scoped inbox listing.
const (
	forwardsAccountWidePath = "/99999/forwards.json"
	forwardsInboxPath       = "/99999/inboxes/555/inbox_forwards.json"
)

// forwardsFeedBody renders n forwards as the account-wide feed serves them.
func forwardsFeedBody(n int) string {
	items := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		items = append(items, `{"id":`+strconv.Itoa(i)+`,"type":"Inbox::Forward","title":"Forward","subject":"Subject","status":"active","bucket":{"id":123,"name":"Test Project","type":"Project"},"creator":{"id":1,"name":"A"}}`)
	}
	return "[" + strings.Join(items, ",") + "]"
}

func forwardsAccountWideRoute(n int) stubRoute {
	return stubRoute{
		method: http.MethodGet,
		path:   forwardsAccountWidePath,
		status: http.StatusOK,
		body:   forwardsFeedBody(n),
		// The bounded default walks positive pages until one comes back empty.
		pages: []string{forwardsFeedBody(n)},
	}
}

func forwardsInboxRoute() stubRoute {
	return stubRoute{
		method: http.MethodGet,
		path:   forwardsInboxPath,
		status: http.StatusOK,
		body:   forwardsFeedBody(1),
	}
}

// requireForwardsUsageError asserts err is an actionable usage error rather
// than a silently accepted flag.
func requireForwardsUsageError(t *testing.T, err error) *output.Error {
	t.Helper()
	require.Error(t, err)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Equal(t, output.CodeUsage, outErr.Code)
	return outErr
}

// Scope selection.

func TestForwardsListProjectScopedStillUsesInbox(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), forwardsInboxRoute())

	err := executeRecordingCommand(NewForwardsCmd(), app, "list", "--in", "123", "--inbox", "555")

	require.NoError(t, err)
	assert.Equal(t, forwardsInboxPath, transport.last(t).Path)
}

func TestForwardsListConfiguredProjectStaysProjectScoped(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), forwardsInboxRoute())
	app.Config.ProjectID = "123"

	err := executeRecordingCommand(NewForwardsCmd(), app, "list", "--inbox", "555")

	require.NoError(t, err)
	assert.Equal(t, forwardsInboxPath, transport.last(t).Path)
}

func TestForwardsListWithoutProjectListsAccountWide(t *testing.T) {
	app, transport := setupRecordingTestApp(t, forwardsAccountWideRoute(2))

	err := executeRecordingCommand(NewForwardsCmd(), app, "list")

	require.NoError(t, err)
	// The default is bounded now: one project's inbox and every project's
	// inboxes are different amounts of work, so only --all asks for the latter.
	assert.Equal(t, []string{"page=1", "page=2"}, transport.queriesFor(forwardsAccountWidePath))
}

func TestForwardsListAllProjectsOverridesConfiguredProject(t *testing.T) {
	app, transport := setupRecordingTestApp(t, forwardsAccountWideRoute(2))
	app.Config.ProjectID = "123"

	err := executeRecordingCommand(NewForwardsCmd(), app, "list", "--all-projects")

	require.NoError(t, err)
	assert.Equal(t, forwardsAccountWidePath, transport.last(t).Path)
}

func TestForwardsListAllProjectsConflictsWithExplicitProject(t *testing.T) {
	assertConflict := func(t *testing.T, configure func(*appctx.App), args ...string) {
		t.Helper()
		app, transport := setupRecordingTestApp(t, forwardsAccountWideRoute(2), forwardsInboxRoute())
		configure(app)

		err := executeRecordingCommand(NewForwardsCmd(), app, args...)

		outErr := requireForwardsUsageError(t, err)
		assert.Contains(t, outErr.Message, "--all-projects")
		assert.Empty(t, transport.recorded(), "a conflicting scope must not reach the API")
	}

	t.Run("--in", func(t *testing.T) {
		assertConflict(t, func(*appctx.App) {}, "list", "--all-projects", "--in", "123")
	})
	t.Run("-p", func(t *testing.T) {
		assertConflict(t, func(*appctx.App) {}, "list", "--all-projects", "-p", "123")
	})
	t.Run("root --project", func(t *testing.T) {
		assertConflict(t, func(app *appctx.App) { app.Flags.Project = "123" }, "list", "--all-projects")
	})
}

// Rejected flags. Every one of these would otherwise be silently ignored.

func TestForwardsListAccountWideRejectsInbox(t *testing.T) {
	assertRejected := func(t *testing.T, args ...string) {
		t.Helper()
		app, transport := setupRecordingTestApp(t, forwardsAccountWideRoute(2))

		err := executeRecordingCommand(NewForwardsCmd(), app, args...)

		outErr := requireForwardsUsageError(t, err)
		assert.Contains(t, outErr.Message, "--inbox")
		assert.Contains(t, outErr.Hint, "--project")
		assert.Empty(t, transport.recorded())
	}

	t.Run("after the subcommand", func(t *testing.T) {
		assertRejected(t, "list", "--inbox", "555")
	})
	t.Run("before the subcommand", func(t *testing.T) {
		assertRejected(t, "--inbox", "555", "list")
	})
	t.Run("with --all-projects", func(t *testing.T) {
		assertRejected(t, "list", "--all-projects", "--inbox", "555")
	})
}

func TestForwardsListAccountWideRejectsUnusablePaging(t *testing.T) {
	assertRejected := func(t *testing.T, wantFragment string, args ...string) {
		t.Helper()
		app, transport := setupRecordingTestApp(t, forwardsAccountWideRoute(2))

		err := executeRecordingCommand(NewForwardsCmd(), app, args...)

		outErr := requireForwardsUsageError(t, err)
		assert.Contains(t, outErr.Message, wantFragment)
		assert.Empty(t, transport.recorded())
	}

	t.Run("explicit --page 0", func(t *testing.T) {
		assertRejected(t, "--page", "list", "--page", "0")
	})
	t.Run("negative --page", func(t *testing.T) {
		assertRejected(t, "--page", "list", "--page=-1")
	})
	t.Run("negative --limit", func(t *testing.T) {
		assertRejected(t, "--limit", "list", "--limit=-1")
	})
	t.Run("--all with --limit", func(t *testing.T) {
		assertRejected(t, "--limit", "list", "--all", "--limit", "5")
	})
	t.Run("--page with --limit", func(t *testing.T) {
		assertRejected(t, "--page", "list", "--page", "2", "--limit", "5")
	})
}

// Pagination contract: the default is every page, and any positive page is
// addressable.

func TestForwardsListAccountWidePageContract(t *testing.T) {
	assertQuery := func(t *testing.T, wantQuery string, args ...string) {
		t.Helper()
		app, transport := setupRecordingTestApp(t, forwardsAccountWideRoute(2))

		err := executeRecordingCommand(NewForwardsCmd(), app, args...)

		require.NoError(t, err)
		last := transport.last(t)
		assert.Equal(t, forwardsAccountWidePath, last.Path)
		assert.Equal(t, wantQuery, last.Query)
	}

	t.Run("--all follows every page", func(t *testing.T) {
		assertQuery(t, "", "list", "--all")
	})
	t.Run("--page 1", func(t *testing.T) {
		assertQuery(t, "page=1", "list", "--page", "1")
	})
	t.Run("--page beyond the first", func(t *testing.T) {
		assertQuery(t, "page=7", "list", "--page", "7")
	})
}

// Output: []Recording goes to the renderer as-is, and --limit trims it with a
// notice that names the full total.

func TestForwardsListAccountWideLimitTruncatesWithNotice(t *testing.T) {
	app, out := setupForwardsCountingApp(t, forwardsFeedBody(3), 3)

	err := executeRecordingCommand(NewForwardsCmd(), app, "list", "--limit", "2")

	require.NoError(t, err)
	envelope := decodeForwardsEnvelope(t, out)
	assert.Len(t, envelope.Data, 2)
	assert.Equal(t, "2 forwards across all projects", envelope.Summary)
	assert.Contains(t, envelope.Notice, "Showing 2 of 3 results")
}

func TestForwardsListAccountWideUntruncatedHasNoNotice(t *testing.T) {
	app, out := setupForwardsCountingApp(t, forwardsFeedBody(3), 3)

	err := executeRecordingCommand(NewForwardsCmd(), app, "list")

	require.NoError(t, err)
	envelope := decodeForwardsEnvelope(t, out)
	assert.Len(t, envelope.Data, 3)
	assert.Equal(t, "3 forwards across all projects", envelope.Summary)
	assert.Empty(t, envelope.Notice)
	require.NotEmpty(t, envelope.Data)
	assert.Equal(t, "Test Project", envelope.Data[0].Bucket.Name, "each row carries its project")
}

// forwardsCountingTransport serves the account-wide feed with an X-Total-Count
// header, which the shared stub route table cannot express.
type forwardsCountingTransport struct {
	body       string
	totalCount string
}

func (t *forwardsCountingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":  []string{"application/json"},
			"X-Total-Count": []string{t.totalCount},
		},
		Body:    io.NopCloser(strings.NewReader(t.body)),
		Request: req,
	}, nil
}

// setupForwardsCountingApp returns an app whose SDK sees a feed with a total
// count, plus the buffer its JSON envelope is written to.
func setupForwardsCountingApp(t *testing.T, body string, totalCount int) (*appctx.App, *bytes.Buffer) {
	t.Helper()

	app, _ := setupRecordingTestApp(t)
	transport := &forwardsCountingTransport{body: body, totalCount: strconv.Itoa(totalCount)}
	app.SDK = basecamp.NewClient(
		&basecamp.Config{BaseURL: "https://3.basecampapi.com"},
		recordingTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	app.Names = names.NewResolver(app.SDK, app.Auth, app.Config.AccountID)

	out := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: out})
	return app, out
}

// forwardsEnvelope is the slice of the JSON success envelope these tests read.
type forwardsEnvelope struct {
	Data []struct {
		ID     int64 `json:"id"`
		Bucket struct {
			Name string `json:"name"`
		} `json:"bucket"`
	} `json:"data"`
	Summary string `json:"summary"`
	Notice  string `json:"notice"`
}

func decodeForwardsEnvelope(t *testing.T, out *bytes.Buffer) forwardsEnvelope {
	t.Helper()
	var envelope forwardsEnvelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope))
	return envelope
}
