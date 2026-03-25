package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestCheckinsAnswerCreateDefaultsDateToToday(t *testing.T) {
	expectedDate := time.Now().Format("2006-01-02")
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
	assert.Equal(t, expectedDate, transport.recordedBody["group_on"])
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
