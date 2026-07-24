package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	cmd := newCheckinsAnswersCmd(&project)

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
			cmd := newCheckinsAnswersCmd(&project)

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
