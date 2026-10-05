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
		// The bounded default walks positive pages until one comes back empty,
		// so the fixture has to have an end. Serving the same body for every
		// page would walk to the cap instead.
		pages: []string{checkinsAccountWideBody},
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

	// The default used to send no page param at all, which asked the server to
	// crawl the whole account — the shape that timed out in production. It now
	// walks positive pages and stops at the first empty one.
	assert.Equal(t, []string{"page=1", "page=2"}, transport.queriesFor(checkinsAccountWidePath))
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
	assert.Equal(t, "1 check-in answer across all projects", resp.Summary)
	// "of 2 fetched" was an artifact of fetching everything and then trimming.
	// The walk now stops at the cap, so what it can honestly report is that
	// more may exist — not a total it never saw.
	assert.Contains(t, resp.Notice, "Showing the first 1 check-in answers; more may exist")
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

// BC3 reads a question's time of day from schedule.time_of_day; hour and minute
// are response-only, and a create that sends them is refused with 422.
func TestCheckinsQuestionCreateSendsTimeOfDay(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"How are you?"}, "17:00"},
		{[]string{"How are you?", "--time", "4:30pm"}, "16:30"},
		{[]string{"How are you?", "--time", "09:05"}, "09:05"},
	} {
		transport := runCheckinsQuestionCreate(t, tc.args...)
		schedule, ok := transport.recordedBody["schedule"].(map[string]any)
		require.True(t, ok, "expected a schedule object, got %v", transport.recordedBody["schedule"])
		assert.Equal(t, tc.want, schedule["time_of_day"], "args %v", tc.args)
		assert.NotContains(t, schedule, "hour")
		assert.NotContains(t, schedule, "minute")
	}
}

func TestQuestionTimeOfDay(t *testing.T) {
	for in, want := range map[string]string{"5:00pm": "17:00", "5pm": "17:00", "12:00am": "00:00", "12:30pm": "12:30", "17:00": "17:00", " 9:05AM ": "09:05"} {
		got, err := questionTimeOfDay(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{"25:00", "17:60", "13pm", "13am", "0pm", "0am", "5:00:30pm", "5:60pm", "noon", "", "5:00:00"} {
		_, err := questionTimeOfDay(in)
		assert.Error(t, err, "expected %q to be rejected", in)
	}
}

// mockCheckinsQuestionUpdateTransport serves an every-other-week, Monday/Wednesday,
// 9:30 question anchored on 2026-09-28 and records the PUT that updates it.
type mockCheckinsQuestionUpdateTransport struct {
	recordedBody map[string]any
	gets         int
	question     string // overrides the served question when set
}

func (m *mockCheckinsQuestionUpdateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	question := `{"id":789,"title":"How are you?","type":"Question","schedule":{"frequency":"every_other_week","days":[1,3],"hour":9,"minute":30,"week_instance":2,"start_date":"2026-09-28"}}`
	if m.question != "" {
		question = m.question
	}

	switch {
	case req.Method == "GET" && strings.Contains(req.URL.Path, "/projects.json"):
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`[{"id":123,"name":"Test Project"}]`)), Header: header}, nil
	case req.Method == "GET" && strings.Contains(req.URL.Path, "/questions/789"):
		m.gets++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(question)), Header: header}, nil
	case req.Method == "PUT" && strings.Contains(req.URL.Path, "/questions/789"):
		defer req.Body.Close()
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &m.recordedBody); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(question)), Header: header}, nil
	default:
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(`{"error":"Not Found"}`)), Header: header}, nil
	}
}

func runCheckinsQuestionUpdate(t *testing.T, args ...string) *mockCheckinsQuestionUpdateTransport {
	t.Helper()
	transport := &mockCheckinsQuestionUpdateTransport{}
	app, _ := newTestAppWithTransport(t, transport)
	app.Config.ProjectID = "123"

	project := ""
	require.NoError(t, executeCommand(newCheckinsQuestionUpdateCmd(&project), app, args...))
	require.NotNil(t, transport.recordedBody, "expected the update to be sent")
	return transport
}

// BC3 replaces a question's whole schedule on update and refuses one missing its
// frequency or time, so a flag that changes one part must carry the rest over —
// the start date too, or an every-other-week question re-anchors on today.
func TestCheckinsQuestionUpdateSendsTheWholeSchedule(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want map[string]any
	}{
		{[]string{"789", "--time", "4:15pm"}, map[string]any{"frequency": "every_other_week", "days": []any{1.0, 3.0}, "time_of_day": "16:15", "week_instance": 2.0, "start_date": "2026-09-28"}},
		{[]string{"789", "--frequency", "every_day", "--days", "1,2,3,4,5"}, map[string]any{"frequency": "every_day", "days": []any{1.0, 2.0, 3.0, 4.0, 5.0}, "time_of_day": "09:30", "week_instance": 2.0, "start_date": "2026-09-28"}},
	} {
		transport := runCheckinsQuestionUpdate(t, tc.args...)
		assert.Equal(t, tc.want, transport.recordedBody["schedule"], "args %v", tc.args)
	}
}

func TestCheckinsQuestionUpdateTitleOnlyLeavesTheScheduleAlone(t *testing.T) {
	transport := runCheckinsQuestionUpdate(t, "789", "New title")
	assert.Equal(t, "New title", transport.recordedBody["title"])
	assert.NotContains(t, transport.recordedBody, "schedule")
	assert.Zero(t, transport.gets, "a title-only update needs no read")
}

// A question Basecamp returns without a schedule leaves nothing to carry over,
// so an update that changes only part of one is refused here with a usage error
// naming the flags to pass, rather than sent for Basecamp to refuse.
func TestCheckinsQuestionUpdateRefusesAScheduleItCannotComplete(t *testing.T) {
	transport := &mockCheckinsQuestionUpdateTransport{question: `{"id":789,"title":"How are you?","type":"Question"}`}
	app, _ := newTestAppWithTransport(t, transport)
	app.Config.ProjectID = "123"

	project := ""
	err := executeCommand(newCheckinsQuestionUpdateCmd(&project), app, "789", "--time", "4:15pm")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--frequency")
	assert.Nil(t, transport.recordedBody, "an incomplete schedule must not be sent")
}
