package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type chatBotRequest struct {
	method, path, query string
	body                map[string]any
}

// chatBotTransport answers the project lookup with a project whose dock has
// one chat (789), and every other request with respond's status and body. It
// records every request that isn't the project lookup.
type chatBotTransport struct {
	respond  func(req *http.Request) (int, string)
	requests []chatBotRequest
}

func (t *chatBotTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	reply := func(status int, body string) *http.Response {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: header}
	}

	if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/projects.json") {
		return reply(200, `[{"id": 123, "name": "Test Project"}]`), nil
	}
	if req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/projects/") {
		return reply(200, `{"id": 123, "dock": [{"name": "chat", "id": 789, "enabled": true}]}`), nil
	}

	recorded := chatBotRequest{method: req.Method, path: req.URL.Path, query: req.URL.RawQuery}
	if req.Body != nil {
		data, _ := io.ReadAll(req.Body)
		req.Body.Close()
		if len(data) > 0 {
			_ = json.Unmarshal(data, &recorded.body)
		}
	}
	t.requests = append(t.requests, recorded)
	status, body := t.respond(req)
	return reply(status, body), nil
}

const chatBotJSON = `{"id": 42, "service_name": "deploys", "command_url": "https://example.com/hook",
	"created_at": "2024-01-01T00:00:00Z", "updated_at": "2024-01-01T00:00:00Z",
	"url": "https://3.basecampapi.com/99999/buckets/123/chats/789/integrations/42.json",
	"app_url": "https://3.basecamp.com/99999/buckets/123/chats/789/integrations/42",
	"lines_url": "https://3.basecampapi.com/99999/integrations/KEY/buckets/123/chats/789/lines.json"}`

func chatBotOK(req *http.Request) (int, string) {
	switch {
	case req.Method == http.MethodDelete:
		return 204, ""
	case req.Method == http.MethodPost:
		return 201, chatBotJSON
	case strings.HasSuffix(req.URL.Path, "/integrations.json"):
		return 200, "[" + chatBotJSON + "]"
	case strings.HasSuffix(req.URL.Path, "/uploads.json"):
		return 200, `[{"id": 5, "content": "", "created_at": "2024-01-01T00:00:00Z",
			"attachments": [{"filename": "plan.pdf", "byte_size": 2048}]}]`
	default:
		return 200, chatBotJSON
	}
}

func runChatBotCommand(t *testing.T, respond func(*http.Request) (int, string), args ...string) (*chatBotTransport, map[string]any, error) {
	t.Helper()
	return runChatBotCommandAs(t, "", respond, args...)
}

// runChatBotCommandAs runs the command under the named profile ("" for none).
func runChatBotCommandAs(t *testing.T, profile string, respond func(*http.Request) (int, string), args ...string) (*chatBotTransport, map[string]any, error) {
	t.Helper()
	transport := &chatBotTransport{respond: respond}
	app, buf := newTestAppWithTransport(t, transport)
	app.Flags.Hints = true
	app.Config.ActiveProfile = profile
	err := executeChatCommand(NewChatCmd(), app, args...)
	var envelope map[string]any
	if buf.Len() > 0 {
		require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope), buf.String())
	}
	return transport, envelope, err
}

func TestChatUploadsListsTheRoomsFiles(t *testing.T) {
	transport, envelope, err := runChatBotCommand(t, chatBotOK, "uploads")
	require.NoError(t, err)

	require.Len(t, transport.requests, 1)
	assert.Equal(t, http.MethodGet, transport.requests[0].method)
	assert.Equal(t, "/99999/chats/789/uploads.json", transport.requests[0].path)

	data := envelope["data"].([]any)
	require.Len(t, data, 1)
	assert.Equal(t, float64(5), data[0].(map[string]any)["id"])
	assert.Equal(t, "1 files posted in chat", envelope["summary"])
	assert.Contains(t, envelopeBreadcrumbCmds(envelope), "basecamp chat upload <file> --room 789 --in 123")
}

func TestChatUploadsPageAsksForThatPage(t *testing.T) {
	transport, _, err := runChatBotCommand(t, chatBotOK, "uploads", "--page", "2", "--room", "789")
	require.NoError(t, err)
	require.Len(t, transport.requests, 1)
	assert.Equal(t, "/99999/chats/789/uploads.json", transport.requests[0].path)
	assert.Contains(t, transport.requests[0].query, "page=2")
}

// Newest first, asked for explicitly, so a --limit keeps the latest files
// whatever the server's default order.
func TestChatUploadsAsksForNewestFirst(t *testing.T) {
	transport, _, err := runChatBotCommand(t, chatBotOK, "uploads")
	require.NoError(t, err)
	require.Len(t, transport.requests, 1)
	assert.Contains(t, transport.requests[0].query, "sort=created_at")
	assert.Contains(t, transport.requests[0].query, "direction=desc")
}

// A follow-up runs as a fresh process, so it carries the profile the run used.
func TestChatBotFollowUpsCarryTheProfile(t *testing.T) {
	for name, args := range map[string][]string{
		"uploads": {"uploads"},
		"list":    {"bots", "list"},
		"show":    {"bots", "show", "42"},
		"create":  {"bots", "create", "deploys"},
		"update":  {"bots", "update", "42", "--name", "ship"},
		"delete":  {"bots", "delete", "42", "--force"},
	} {
		t.Run(name, func(t *testing.T) {
			_, envelope, err := runChatBotCommandAs(t, "staging", chatBotOK, args...)
			require.NoError(t, err)
			cmds := envelopeBreadcrumbCmds(envelope)
			require.NotEmpty(t, cmds)
			for _, cmd := range cmds {
				assert.Contains(t, cmd, "--profile staging")
			}
		})
	}
}

// Contradictory flags are the caller's error, refused before any request.
func TestChatUploadsRefusesConflictingFlags(t *testing.T) {
	for name, args := range map[string][]string{
		"all and limit":  {"uploads", "--all", "--limit", "5"},
		"page and all":   {"uploads", "--page", "2", "--all"},
		"page and limit": {"uploads", "--page", "2", "--limit", "5"},
		"negative limit": {"uploads", "--limit", "-1"},
	} {
		t.Run(name, func(t *testing.T) {
			transport, _, err := runChatBotCommand(t, chatBotOK, args...)
			require.Error(t, err)
			assert.Empty(t, transport.requests)
		})
	}
}

func TestChatBotsListUsesTheProjectAndRoom(t *testing.T) {
	transport, envelope, err := runChatBotCommand(t, chatBotOK, "bots", "list")
	require.NoError(t, err)

	require.Len(t, transport.requests, 1)
	assert.Equal(t, http.MethodGet, transport.requests[0].method)
	assert.Equal(t, "/99999/buckets/123/chats/789/integrations.json", transport.requests[0].path)
	assert.Equal(t, "1 chatbots", envelope["summary"])
	bot := envelope["data"].([]any)[0].(map[string]any)
	assert.Equal(t, "deploys", bot["service_name"])
	assert.Contains(t, bot["lines_url"], "/lines.json", "the lines_url is the reason to list them")
}

func TestChatBotsShow(t *testing.T) {
	transport, envelope, err := runChatBotCommand(t, chatBotOK, "bots", "show", "42", "--room", "789")
	require.NoError(t, err)

	require.Len(t, transport.requests, 1)
	assert.Equal(t, "/99999/buckets/123/chats/789/integrations/42", transport.requests[0].path)
	assert.Equal(t, "Chatbot #42: deploys", envelope["summary"])
	assert.Contains(t, envelopeBreadcrumbCmds(envelope), "basecamp chat bots update 42 --name <name> --room 789 --in 123")
}

func TestChatBotsCreateSendsNameAndCommandURL(t *testing.T) {
	transport, envelope, err := runChatBotCommand(t, chatBotOK,
		"bots", "create", "deploys", "--command-url", "https://example.com/hook")
	require.NoError(t, err)

	require.Len(t, transport.requests, 1)
	req := transport.requests[0]
	assert.Equal(t, http.MethodPost, req.method)
	assert.Equal(t, "/99999/buckets/123/chats/789/integrations.json", req.path)
	assert.Equal(t, map[string]any{"service_name": "deploys", "command_url": "https://example.com/hook"}, req.body)
	assert.Equal(t, "Created chatbot #42: deploys", envelope["summary"])
}

func TestChatBotsCreateWithoutCommandURLSendsOnlyTheName(t *testing.T) {
	transport, _, err := runChatBotCommand(t, chatBotOK, "bots", "create", "deploys")
	require.NoError(t, err)
	require.Len(t, transport.requests, 1)
	assert.Equal(t, map[string]any{"service_name": "deploys"}, transport.requests[0].body)
}

func TestChatBotsCreateRefusesBeforeTheWire(t *testing.T) {
	for name, args := range map[string][]string{
		"empty name": {"bots", "create", ""},
		"two names":  {"bots", "create", "deploy", "bot"},
	} {
		t.Run(name, func(t *testing.T) {
			transport, _, err := runChatBotCommand(t, chatBotOK, args...)
			require.Error(t, err)
			assert.Empty(t, transport.requests)
		})
	}
}

// Basecamp takes the name on every update, so update reads the chatbot and
// sends back what the caller didn't change: renaming keeps the command URL,
// and a new command URL keeps the name.
func TestChatBotsUpdateKeepsWhatIsLeftOut(t *testing.T) {
	cases := map[string]struct {
		args []string
		want map[string]any
	}{
		"rename":      {[]string{"--name", "deploy_bot"}, map[string]any{"service_name": "deploy_bot", "command_url": "https://example.com/hook"}},
		"command URL": {[]string{"--command-url", "https://example.com/v2"}, map[string]any{"service_name": "deploys", "command_url": "https://example.com/v2"}},
		"both": {[]string{"--name", "deploy_bot", "--command-url", "https://example.com/v2"},
			map[string]any{"service_name": "deploy_bot", "command_url": "https://example.com/v2"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			transport, envelope, err := runChatBotCommand(t, chatBotOK, append([]string{"bots", "update", "42"}, tc.args...)...)
			require.NoError(t, err)

			require.Len(t, transport.requests, 2)
			assert.Equal(t, http.MethodGet, transport.requests[0].method)
			put := transport.requests[1]
			assert.Equal(t, http.MethodPut, put.method)
			assert.Equal(t, "/99999/buckets/123/chats/789/integrations/42", put.path)
			assert.Equal(t, tc.want, put.body)
			assert.Equal(t, "Updated chatbot #42: deploys", envelope["summary"])
		})
	}
}

func TestChatBotsUpdateRefusesBeforeTheWire(t *testing.T) {
	for name, args := range map[string][]string{
		"nothing to change":  {"bots", "update", "42"},
		"empty name":         {"bots", "update", "42", "--name", ""},
		"empty command URL":  {"bots", "update", "42", "--command-url", ""},
		"non-numeric ID":     {"bots", "update", "deploys", "--name", "x"},
		"zero ID":            {"bots", "update", "0", "--name", "x"},
		"show non-numeric":   {"bots", "show", "deploys"},
		"delete non-numeric": {"bots", "delete", "deploys", "--force"},
	} {
		t.Run(name, func(t *testing.T) {
			transport, _, err := runChatBotCommand(t, chatBotOK, args...)
			require.Error(t, err)
			assert.Empty(t, transport.requests)
		})
	}
}

// A chatbot delete is permanent and account-wide, so in a machine-output mode
// it needs --force, and without it nothing is sent.
func TestChatBotsDeleteNeedsForceWhereNothingCanConfirm(t *testing.T) {
	transport := &chatBotTransport{respond: chatBotOK}
	app, _ := newTestAppWithTransport(t, transport)
	cmd := NewChatCmd()
	cmd.PersistentFlags().Bool("json", true, "")

	err := executeChatCommand(cmd, app, "bots", "delete", "42")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--force")
	assert.Empty(t, transport.requests)
}

func TestChatBotsDeleteWithForce(t *testing.T) {
	transport, envelope, err := runChatBotCommand(t, chatBotOK, "bots", "delete", "42", "--force")
	require.NoError(t, err)

	require.Len(t, transport.requests, 1)
	assert.Equal(t, http.MethodDelete, transport.requests[0].method)
	assert.Equal(t, "/99999/buckets/123/chats/789/integrations/42", transport.requests[0].path)
	assert.Equal(t, "Deleted chatbot #42", envelope["summary"])
	assert.Equal(t, true, envelope["data"].(map[string]any)["deleted"])
}

// Basecamp's refusal (a member who isn't an administrator) is the command's
// error, with nothing printed as if the chatbot had been made.
func TestChatBotsCreateReportsARefusal(t *testing.T) {
	refuse := func(*http.Request) (int, string) { return 403, `{"error": "forbidden"}` }
	transport, envelope, err := runChatBotCommand(t, refuse, "bots", "create", "deploys")
	require.Error(t, err)
	assert.Nil(t, envelope)
	assert.Len(t, transport.requests, 1, "a refused create is not retried")
}

func envelopeBreadcrumbCmds(envelope map[string]any) []string {
	var cmds []string
	crumbs, _ := envelope["breadcrumbs"].([]any)
	for _, c := range crumbs {
		if m, ok := c.(map[string]any); ok {
			if s, ok := m["cmd"].(string); ok {
				cmds = append(cmds, s)
			}
		}
	}
	return cmds
}
