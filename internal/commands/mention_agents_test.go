package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agentMentionTransport fakes a project whose people include an agent. The
// agent is absent from the pingable set, as it is in Basecamp: an agent can
// never be in a Ping.
type agentMentionTransport struct {
	mu     sync.Mutex
	gets   map[string]int
	posted []byte
}

func (t *agentMentionTransport) getCount(suffix string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for path, c := range t.gets {
		if strings.HasSuffix(path, suffix) {
			n += c
		}
	}
	return n
}

func (t *agentMentionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	respond := func(status int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: header}, nil
	}

	path := req.URL.Path
	if req.Method == http.MethodGet {
		t.mu.Lock()
		if t.gets == nil {
			t.gets = map[string]int{}
		}
		t.gets[path]++
		t.mu.Unlock()

		jane := `{"id": 42000, "name": "Jane Smith", "attachable_sgid": "sgid-jane", "personable_type": "User"}`
		quincy := `{"id": 7, "name": "Quincy", "attachable_sgid": "sgid-quincy", "personable_type": "Agent"}`
		switch {
		case strings.HasSuffix(path, "/circles/people.json"):
			return respond(200, "["+jane+"]")
		case strings.HasSuffix(path, "/projects/123/people.json"):
			return respond(200, "["+jane+","+quincy+"]")
		case strings.HasSuffix(path, "/people/7"), strings.HasSuffix(path, "/people/7.json"):
			return respond(200, quincy)
		case strings.HasSuffix(path, "/projects.json"):
			return respond(200, `[{"id": 123, "name": "Test Project"}]`)
		default:
			return respond(404, `{"error": "not found"}`)
		}
	}

	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		req.Body.Close()
		t.mu.Lock()
		t.posted = body
		t.mu.Unlock()
	}
	return respond(201, `{"id": 999, "content": "ok", "created_at": "2024-01-01T00:00:00Z"}`)
}

func (t *agentMentionTransport) postedContent(tb testing.TB) string {
	tb.Helper()
	require.NotEmpty(tb, t.posted, "nothing was posted")
	var body map[string]any
	require.NoError(tb, json.Unmarshal(t.posted, &body))
	content, _ := body["content"].(string)
	return content
}

func noticeOf(tb testing.TB, buf *bytes.Buffer) string {
	tb.Helper()
	var envelope map[string]any
	require.NoError(tb, json.Unmarshal(buf.Bytes(), &envelope))
	notice, _ := envelope["notice"].(string)
	return notice
}

const agentCardURL = "https://3.basecamp.com/99999/buckets/123/card_tables/cards/789"

func TestCommentsCreateMentionsAgentFromURLProject(t *testing.T) {
	transport := &agentMentionTransport{}
	app, buf := newTestAppWithTransport(t, transport)

	err := executeChatCommand(NewCommentsCmd(), app, "create", agentCardURL, "Hey @Quincy, and @Jane.Smith")
	require.NoError(t, err)

	content := transport.postedContent(t)
	assert.Contains(t, content, `sgid="sgid-quincy"`)
	assert.Contains(t, content, `sgid="sgid-jane"`)
	assert.Empty(t, noticeOf(t, buf))
	assert.Equal(t, 1, transport.getCount("/projects/123/people.json"))
}

func TestCommentsCreateMentionsAgentFromInFlag(t *testing.T) {
	transport := &agentMentionTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	err := executeChatCommand(NewCommentsCmd(), app, "create", "789", "Hey @Quincy and @quincy", "--in", "123")
	require.NoError(t, err)

	content := transport.postedContent(t)
	assert.Equal(t, 2, strings.Count(content, `sgid="sgid-quincy"`))
	assert.Equal(t, 1, transport.getCount("/projects/123/people.json"), "one project fetch per run")
}

func TestCommentsCreateAgentMissWithoutProjectIsNotSilent(t *testing.T) {
	transport := &agentMentionTransport{}
	app, buf := newTestAppWithTransport(t, transport)

	// A bare ID names no project, and the configured default project is not
	// evidence of where the recording lives.
	err := executeChatCommand(NewCommentsCmd(), app, "create", "789", "Hey @Quincy")
	require.NoError(t, err)

	assert.NotContains(t, transport.postedContent(t), "sgid-quincy")
	notice := noticeOf(t, buf)
	assert.Contains(t, notice, "@Quincy")
	assert.Contains(t, notice, "--in", "the miss should say how to reach an agent")
	assert.Zero(t, transport.getCount("/projects/123/people.json"))
}

func TestCommentsCreateMentionsAgentByPersonID(t *testing.T) {
	transport := &agentMentionTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	err := executeChatCommand(NewCommentsCmd(), app, "create", "789", "Hey [@Quincy](person:7)")
	require.NoError(t, err)

	assert.Contains(t, transport.postedContent(t), `sgid="sgid-quincy"`)
}

func TestChatPostMentionsAgentInRoomProject(t *testing.T) {
	transport := &agentMentionTransport{}
	app, buf := newTestAppWithTransport(t, transport)

	err := executeChatCommand(NewChatCmd(), app, "post", "@Quincy please look", "--room", "789", "--in", "123")
	require.NoError(t, err)

	assert.Contains(t, transport.postedContent(t), `sgid="sgid-quincy"`)
	assert.Empty(t, noticeOf(t, buf))
}

func TestCommentsCreateUnknownProjectInMentionScopeFails(t *testing.T) {
	transport := &agentMentionTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	// A project that doesn't resolve is the command's error, not an
	// unresolved mention to post as plain text.
	err := executeChatCommand(NewCommentsCmd(), app, "create", "789", "Hey @Quincy", "--in", "No Such Project")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "No Such Project")
	assert.Empty(t, transport.posted)
}

func TestSharedURLProject(t *testing.T) {
	a := "https://3.basecamp.com/99999/buckets/123/todos/1"
	b := "https://3.basecamp.com/99999/buckets/123/todos/2"
	c := "https://3.basecamp.com/99999/buckets/456/todos/3"
	assert.Equal(t, "123", sharedURLProject(a))
	assert.Equal(t, "123", sharedURLProject(a+","+b))
	assert.Empty(t, sharedURLProject(a+","+c), "URLs in different projects")
	assert.Empty(t, sharedURLProject(a+",2"), "a bare ID's project is unknown")
	assert.Empty(t, sharedURLProject("2"))
}
