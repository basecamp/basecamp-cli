package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

type mockCheckinsAnswersByPersonTransport struct {
	recordedPath string
}

func (m *mockCheckinsAnswersByPersonTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	switch {
	case req.Method == "GET" && strings.Contains(req.URL.Path, "/projects.json"):
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`[{"id":123,"name":"Test Project"}]`)),
			Header:     header,
		}, nil
	case req.Method == "GET" && strings.Contains(req.URL.Path, "/people.json"):
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`[{"id":456,"name":"Alice Smith","email_address":"alice@example.com"}]`)),
			Header:     header,
		}, nil
	case req.Method == "GET" && req.URL.Path == "/99999/questions/789/answers/by/456":
		m.recordedPath = req.URL.Path
		return &http.Response{
			StatusCode: 200,
			Body: io.NopCloser(strings.NewReader(`[{
				"id": 1001,
				"content": "<div>Alice's answer</div>",
				"group_on": "2026-04-21",
				"creator": {"id": 456, "name": "Alice Smith"},
				"parent": {"id": 789, "title": "What did you work on?", "type": "Question", "url": "https://example.test/questions/789", "app_url": "https://example.test/questions/789"},
				"bucket": {"id": 123, "name": "Test Project", "type": "Project"},
				"status": "active",
				"type": "Question::Answer",
				"title": "What did you work on?"
			}]`)),
			Header: header,
		}, nil
	default:
		return &http.Response{
			StatusCode: 404,
			Body:       io.NopCloser(strings.NewReader(`{"error":"Not Found"}`)),
			Header:     header,
		}, nil
	}
}

func TestCheckinsAnswersByPersonFlag(t *testing.T) {
	transport := &mockCheckinsAnswersByPersonTransport{}
	app, _ := newTestAppWithTransport(t, transport)
	app.Config.ProjectID = "123"

	project := ""
	questionnaire := ""
	cmd := newCheckinsAnswersCmd(&project, &questionnaire)

	err := executeCommand(cmd, app, "789", "--by", "Alice Smith")
	require.NoError(t, err)
	assert.Equal(t, "/99999/questions/789/answers/by/456", transport.recordedPath)
}

// TestCheckinsAnswersByBlankValue verifies that an explicitly provided but blank
// --by value is rejected (empty or whitespace), rather than silently falling back
// to the unfiltered endpoint.
func TestCheckinsAnswersByBlankValue(t *testing.T) {
	for _, blank := range []string{"", "   "} {
		t.Run(fmt.Sprintf("%q", blank), func(t *testing.T) {
			transport := &mockCheckinsAnswersByPersonTransport{}
			app, _ := newTestAppWithTransport(t, transport)
			app.Config.ProjectID = "123"

			project := ""
			questionnaire := ""
			cmd := newCheckinsAnswersCmd(&project, &questionnaire)

			err := executeCommand(cmd, app, "789", "--by", blank)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "cannot be blank")
			assert.Empty(t, transport.recordedPath, "must not call the per-person endpoint")
		})
	}
}

type mockCheckinsAnswerCreateTransport struct {
	recordedPath string
	recordedBody map[string]any
}

func (m *mockCheckinsAnswerCreateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	switch {
	case req.Method == "GET" && strings.Contains(req.URL.Path, "/projects.json"):
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`[{"id":123,"name":"Test Project"}]`)),
			Header:     header,
		}, nil
	case req.Method == "POST" && strings.Contains(req.URL.Path, "/questions/456/answers.json"):
		m.recordedPath = req.URL.Path
		if req.Body != nil {
			defer req.Body.Close()
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &m.recordedBody); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: 201,
			Body: io.NopCloser(strings.NewReader(`{
				"id": 789,
				"content": "<p>hello world</p>",
				"group_on": "2026-03-25",
				"creator": {"name": "Rob Zolkos"},
				"parent": {"id": 456, "title": "What did you work on today?", "type": "Question", "url": "https://example.test/questions/456", "app_url": "https://example.test/questions/456"},
				"bucket": {"id": 123, "name": "Test Project", "type": "Project"},
				"status": "active",
				"type": "Question::Answer",
				"title": "Answer"
			}`)),
			Header: header,
		}, nil
	default:
		return &http.Response{
			StatusCode: 404,
			Body:       io.NopCloser(strings.NewReader(`{"error":"Not Found"}`)),
			Header:     header,
		}, nil
	}
}

// mockCheckinsQuestionCreateTransport resolves the questionnaire via the project
// dock and captures the POST body sent to create a question.
type mockCheckinsQuestionCreateTransport struct {
	recordedBody map[string]any
}

func (m *mockCheckinsQuestionCreateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	switch {
	case req.Method == "GET" && strings.Contains(req.URL.Path, "/projects.json"):
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`[{"id":123,"name":"Test Project"}]`)),
			Header:     header,
		}, nil
	case req.Method == "GET" && strings.Contains(req.URL.Path, "/projects/"):
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{"id":123,"dock":[{"name":"questionnaire","id":555,"enabled":true}]}`)),
			Header:     header,
		}, nil
	case req.Method == "POST" && strings.Contains(req.URL.Path, "/questions.json"):
		if req.Body != nil {
			defer req.Body.Close()
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &m.recordedBody); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: 201,
			Body:       io.NopCloser(strings.NewReader(`{"id":789,"title":"How are you?","type":"Question"}`)),
			Header:     header,
		}, nil
	default:
		return &http.Response{
			StatusCode: 404,
			Body:       io.NopCloser(strings.NewReader(`{"error":"Not Found"}`)),
			Header:     header,
		}, nil
	}
}

func runCheckinsQuestionCreate(t *testing.T, args ...string) *mockCheckinsQuestionCreateTransport {
	t.Helper()
	transport := &mockCheckinsQuestionCreateTransport{}
	app, _ := newTestAppWithTransport(t, transport)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newCheckinsQuestionCreateCmd(&project)

	err := executeCommand(cmd, app, args...)
	require.NoError(t, err)
	require.NotNil(t, transport.recordedBody, "expected request body to be captured")
	return transport
}

func TestCheckinsQuestionCreateHasVisibleToClientsFlag(t *testing.T) {
	project := ""
	cmd := newCheckinsQuestionCreateCmd(&project)

	flag := cmd.Flags().Lookup("visible-to-clients")
	require.NotNil(t, flag, "expected --visible-to-clients flag on check-in question create")
}

func TestCheckinsQuestionCreateDefaultOmitsVisibleToClients(t *testing.T) {
	transport := runCheckinsQuestionCreate(t, "How are you?")
	_, ok := transport.recordedBody["visible_to_clients"]
	assert.False(t, ok, "expected visible_to_clients to be omitted when flag is not set")
}

func TestCheckinsQuestionCreateVisibleToClientsTrue(t *testing.T) {
	transport := runCheckinsQuestionCreate(t, "How are you?", "--visible-to-clients")
	assert.Equal(t, true, transport.recordedBody["visible_to_clients"])
}

func TestCheckinsQuestionCreateVisibleToClientsFalse(t *testing.T) {
	transport := runCheckinsQuestionCreate(t, "How are you?", "--visible-to-clients=false")
	val, ok := transport.recordedBody["visible_to_clients"]
	require.True(t, ok, "expected visible_to_clients present for explicit --visible-to-clients=false")
	assert.Equal(t, false, val)
}

func TestCheckinsAnswerCreateDefaultsDateToToday(t *testing.T) {
	originalNow := checkinsNow
	checkinsNow = func() time.Time {
		return time.Date(2026, 3, 25, 9, 30, 0, 0, time.Local)
	}
	t.Cleanup(func() {
		checkinsNow = originalNow
	})

	transport := &mockCheckinsAnswerCreateTransport{}
	app, _ := newTestAppWithTransport(t, transport)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newCheckinsAnswerCreateCmd(&project)

	err := executeCommand(cmd, app, "456", "hello world")
	require.NoError(t, err)
	require.NotNil(t, transport.recordedBody)
	assert.Equal(t, "/99999/questions/456/answers.json", transport.recordedPath)
	assert.Equal(t, "<p>hello world</p>", transport.recordedBody["content"])
	assert.Equal(t, "2026-03-25", transport.recordedBody["group_on"])
}

func TestCheckinsAnswerCreatePreservesExplicitDate(t *testing.T) {
	transport := &mockCheckinsAnswerCreateTransport{}
	app, _ := newTestAppWithTransport(t, transport)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newCheckinsAnswerCreateCmd(&project)

	err := executeCommand(cmd, app, "456", "hello world", "--date", "2026-03-25")
	require.NoError(t, err)
	require.NotNil(t, transport.recordedBody)
	assert.Equal(t, "2026-03-25", transport.recordedBody["group_on"])
}

// Account-wide check-in answers.
//
// `checkins answers` lists the children of one question, so a project alone
// cannot select a listing. Dropping the question ID lists every project's
// answers through the account-wide aggregate instead.

const checkinsAccountWidePath = "/99999/checkins.json"

const checkinsAccountWideBody = `[
  {"id":1,"title":"Monday","type":"Question::Answer","bucket":{"id":123,"name":"Test Project"}},
  {"id":2,"title":"Tuesday","type":"Question::Answer","bucket":{"id":456,"name":"Other Project"}}
]`

func checkinsAccountWideRoute() stubRoute {
	return stubRoute{
		method: http.MethodGet,
		path:   checkinsAccountWidePath,
		status: http.StatusOK,
		body:   checkinsAccountWideBody,
	}
}

func checkinsQuestionAnswersRoute() stubRoute {
	return stubRoute{
		method: http.MethodGet,
		path:   "/99999/questions/789/answers.json",
		status: http.StatusOK,
		body:   `[{"id":11,"content":"<p>done</p>"}]`,
	}
}

// newCheckinsAnswersTestCmd builds the answers command with its own copies of
// the group's persistent flag targets.
func newCheckinsAnswersTestCmd() *cobra.Command {
	project := ""
	questionnaire := ""
	return newCheckinsAnswersCmd(&project, &questionnaire)
}

// runCheckinsAnswersAccountWideCmd runs the answers command against a stub
// serving the account-wide aggregate.
func runCheckinsAnswersAccountWideCmd(t *testing.T, args ...string) (*recordingTransport, error) {
	t.Helper()
	app, transport := setupRecordingTestApp(t, projectsRoute(), checkinsAccountWideRoute())
	return transport, executeRecordingCommand(newCheckinsAnswersTestCmd(), app, args...)
}

func TestCheckinsAnswersQuestionScopedStillHitsQuestionEndpoint(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), checkinsQuestionAnswersRoute())
	app.Config.ProjectID = "123"

	require.NoError(t, executeRecordingCommand(newCheckinsAnswersTestCmd(), app, "789"))
	assert.Equal(t, "/99999/questions/789/answers.json", transport.last(t).Path)
}

func TestCheckinsAnswersWithoutQuestionListsAccountWide(t *testing.T) {
	transport, err := runCheckinsAnswersAccountWideCmd(t)
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, checkinsAccountWidePath, last.Path)
	assert.Empty(t, last.Query, "the default follows every page, which sends no page param")
}

// A configured project cannot scope a question's answers, so it is ignored
// rather than turned into an error or a project lookup.
func TestCheckinsAnswersIgnoresConfiguredProject(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), checkinsAccountWideRoute())
	app.Config.ProjectID = "123"

	require.NoError(t, executeRecordingCommand(newCheckinsAnswersTestCmd(), app))

	for _, req := range transport.recorded() {
		assert.NotEqual(t, "/99999/projects.json", req.Path, "must not resolve the configured project")
	}
	assert.Equal(t, checkinsAccountWidePath, transport.last(t).Path)
}

func TestCheckinsAnswersAllProjectsOverridesConfiguredProject(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), checkinsAccountWideRoute())
	app.Config.ProjectID = "123"

	require.NoError(t, executeRecordingCommand(newCheckinsAnswersTestCmd(), app, "--all-projects"))
	assert.Equal(t, checkinsAccountWidePath, transport.last(t).Path)
}

func TestCheckinsAnswersExplicitProjectWithoutQuestionIsUsage(t *testing.T) {
	assertUsage := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "A project alone cannot select check-in answers")
	}

	t.Run("group --in", func(t *testing.T) {
		app, _ := setupRecordingTestApp(t, projectsRoute(), checkinsAccountWideRoute())
		assertUsage(t, executeRecordingCommand(NewCheckinsCmd(), app, "answers", "--in", "123"))
	})

	t.Run("group --project", func(t *testing.T) {
		app, _ := setupRecordingTestApp(t, projectsRoute(), checkinsAccountWideRoute())
		assertUsage(t, executeRecordingCommand(NewCheckinsCmd(), app, "answers", "--project", "123"))
	})

	// The root-level form lands in app.Flags.Project, not cmd.Flags().Changed.
	t.Run("root --project", func(t *testing.T) {
		app, _ := setupRecordingTestApp(t, projectsRoute(), checkinsAccountWideRoute())
		app.Flags.Project = "123"
		assertUsage(t, executeRecordingCommand(newCheckinsAnswersTestCmd(), app))
	})
}

func TestCheckinsAnswersAllProjectsConflicts(t *testing.T) {
	t.Run("with a question ID", func(t *testing.T) {
		app, _ := setupRecordingTestApp(t, projectsRoute(), checkinsAccountWideRoute())
		err := executeRecordingCommand(newCheckinsAnswersTestCmd(), app, "789", "--all-projects")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--all-projects cannot be combined with a question ID")
	})

	t.Run("with an explicit project", func(t *testing.T) {
		app, _ := setupRecordingTestApp(t, projectsRoute(), checkinsAccountWideRoute())
		err := executeRecordingCommand(NewCheckinsCmd(), app, "answers", "--in", "123", "--all-projects")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--all-projects cannot be combined with --project")
	})

	t.Run("with a root-level project", func(t *testing.T) {
		app, _ := setupRecordingTestApp(t, projectsRoute(), checkinsAccountWideRoute())
		app.Flags.Project = "123"
		err := executeRecordingCommand(newCheckinsAnswersTestCmd(), app, "--all-projects")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--all-projects cannot be combined with --project")
	})
}

func TestCheckinsAnswersAccountWideRejectsBy(t *testing.T) {
	transport, err := runCheckinsAnswersAccountWideCmd(t, "--by", "me")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--by has no account-wide equivalent")
	assert.Empty(t, transport.recorded(), "must not call the aggregate endpoint")
}

// --questionnaire is a persistent flag on the group, so it only reaches the
// answers command through the parent.
func TestCheckinsAnswersAccountWideRejectsQuestionnaire(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), checkinsAccountWideRoute())

	err := executeRecordingCommand(NewCheckinsCmd(), app, "answers", "--questionnaire", "555")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--questionnaire names a check-in container inside one project")
	assert.Empty(t, transport.recorded(), "must not call the aggregate endpoint")
}

func TestCheckinsAnswersAccountWidePagination(t *testing.T) {
	t.Run("--page N asks for that page", func(t *testing.T) {
		transport, err := runCheckinsAnswersAccountWideCmd(t, "--page", "3")
		require.NoError(t, err)
		assert.Equal(t, "page=3", transport.last(t).Query)
	})

	t.Run("--all follows every page", func(t *testing.T) {
		transport, err := runCheckinsAnswersAccountWideCmd(t, "--all")
		require.NoError(t, err)
		assert.Empty(t, transport.last(t).Query)
	})

	t.Run("explicit --page 0 is usage", func(t *testing.T) {
		_, err := runCheckinsAnswersAccountWideCmd(t, "--page", "0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--page 0 is not a page")
	})

	t.Run("negative --page is usage", func(t *testing.T) {
		_, err := runCheckinsAnswersAccountWideCmd(t, "--page", "-1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--page cannot be negative")
	})

	t.Run("negative --limit is usage", func(t *testing.T) {
		_, err := runCheckinsAnswersAccountWideCmd(t, "--limit", "-1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--limit cannot be negative")
	})
}

func TestCheckinsAnswersAccountWideLimitTruncatesWithNotice(t *testing.T) {
	app, _ := setupRecordingTestApp(t, projectsRoute(), checkinsAccountWideRoute())
	buf := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: buf})

	require.NoError(t, executeRecordingCommand(newCheckinsAnswersTestCmd(), app, "--limit", "1"))

	var resp struct {
		Data    []map[string]any `json:"data"`
		Summary string           `json:"summary"`
		Notice  string           `json:"notice"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &resp))
	assert.Len(t, resp.Data, 1)
	assert.Equal(t, "1 check-in answers across all projects", resp.Summary)
	assert.Contains(t, resp.Notice, "Showing 1 of 2 fetched check-in answers")
}

// []Recording is what `recordings list` already hands the styled renderer, so
// the account-wide payload needs no format-dependent flattening.
func TestCheckinsAnswersAccountWideStyledRendersRecordings(t *testing.T) {
	app, _ := setupRecordingTestApp(t, projectsRoute(), checkinsAccountWideRoute())
	buf := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatStyled, Writer: buf})

	require.NoError(t, executeRecordingCommand(newCheckinsAnswersTestCmd(), app))

	rendered := buf.String()
	assert.Contains(t, rendered, "Monday")
	assert.Contains(t, rendered, "Tuesday")
}
