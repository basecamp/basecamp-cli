package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

const pingsTestAPI = "https://3.basecampapi.com/99999"

func pingPersonJSON(id int, name, email string) string {
	return fmt.Sprintf(`{"id":%d,"name":%q,"email_address":%q,"attachable_sgid":"sgid-%d","personable_type":"User","can_ping":true}`, id, name, email, id)
}

// pingJSON is a Ping whose Campfire is campfireID. people are the other people.
func pingJSON(id, campfireID int, title string, named bool, people ...string) string {
	return fmt.Sprintf(`{"id":%d,"created_at":"2026-10-19T15:15:25.688Z","updated_at":"2026-10-19T15:16:25.688Z",`+
		`"title":%q,"named":%t,"avatar_url":"https://x/avatar","url":"%s/circles/%d.json",`+
		`"app_url":"https://3.basecamp.com/99999/circles/%d","bookmark_url":"https://x/bm",`+
		`"room_url":"%s/buckets/%d/chats/%d.json",`+
		`"links":{"lines":"%s/buckets/%d/chats/%d/lines.json","files":"%s/buckets/%d/chats/%d/uploads.json"},`+
		`"people":[%s]}`,
		id, title, named, pingsTestAPI, id, id, pingsTestAPI, id, campfireID,
		pingsTestAPI, id, campfireID, pingsTestAPI, id, campfireID, strings.Join(people, ","))
}

type pingsReply struct {
	status int
	body   string
}

// pingsFake is a Basecamp that answers by "METHOD path" and records what it
// was asked, bodies included.
type pingsFake struct {
	mu       sync.Mutex
	replies  map[string]pingsReply
	requests []string
	bodies   map[string][]byte
}

func newPingsFake() *pingsFake {
	return &pingsFake{replies: map[string]pingsReply{}, bodies: map[string][]byte{}}
}

func (f *pingsFake) on(method, path string, status int, body string) {
	f.replies[method+" "+path] = pingsReply{status, body}
}

func (f *pingsFake) RoundTrip(req *http.Request) (*http.Response, error) {
	key := req.Method + " " + req.URL.Path
	f.mu.Lock()
	f.requests = append(f.requests, key)
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		req.Body.Close()
		f.bodies[key] = b
	}
	reply, ok := f.replies[key]
	f.mu.Unlock()
	if !ok {
		reply = pingsReply{404, `{"error":"no fake for ` + key + `"}`}
	}
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: reply.status,
		Body:       io.NopCloser(strings.NewReader(reply.body)),
		Header:     header,
		Request:    req,
	}, nil
}

func (f *pingsFake) asked(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.requests {
		if r == key {
			return true
		}
	}
	return false
}

func (f *pingsFake) body(key string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var m map[string]any
	_ = json.Unmarshal(f.bodies[key], &m)
	return m
}

// pingable is who the caller can ping in the tests.
const pingablePeople = `[` +
	`{"id":1001,"name":"Jason Fried","email_address":"jason@example.com","attachable_sgid":"sgid-1001"},` +
	`{"id":1002,"name":"Jane Smith","email_address":"jane@example.com","attachable_sgid":"sgid-1002"},` +
	`{"id":1003,"name":"John Doe","email_address":"john@example.com","attachable_sgid":"sgid-1003"},` +
	`{"id":1004,"name":"Jason Zimdars","email_address":"jz@example.com","attachable_sgid":"sgid-1004"}]`

func newPingsFakeWithPeople() *pingsFake {
	f := newPingsFake()
	f.on("GET", "/99999/circles/people.json", 200, pingablePeople)
	f.on("GET", "/99999/projects.json", 200, `[]`)
	return f
}

type pingsEnvelope struct {
	Summary     string `json:"summary"`
	Notice      string `json:"notice"`
	Breadcrumbs []struct {
		Cmd string `json:"cmd"`
	} `json:"breadcrumbs"`
	Data json.RawMessage `json:"data"`
}

func (e pingsEnvelope) crumbs() []string {
	cmds := make([]string, 0, len(e.Breadcrumbs))
	for _, b := range e.Breadcrumbs {
		cmds = append(cmds, b.Cmd)
	}
	return cmds
}

func runPings(t *testing.T, f *pingsFake, newCmd func() *cobra.Command, args ...string) (pingsEnvelope, error) {
	t.Helper()
	app, buf := newTestAppWithTransport(t, f)
	app.Flags.Hints = true
	cmd := newCmd()
	cmd.SetArgs(args)
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	var env pingsEnvelope
	if buf.Len() > 0 {
		require.NoError(t, json.Unmarshal(buf.Bytes(), &env), buf.String())
	}
	return env, err
}

func runPingsCmd(t *testing.T, f *pingsFake, args ...string) (pingsEnvelope, error) {
	t.Helper()
	return runPings(t, f, NewPingsCmd, args...)
}

func dataMap(t *testing.T, e pingsEnvelope) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(e.Data, &m))
	return m
}

func TestPingsListShowsPingsWithTheirCampfire(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("GET", "/99999/circles.json", 200, "["+
		pingJSON(500, 700, "Jason Fried", false, pingPersonJSON(1001, "Jason Fried", "jason@example.com"))+","+
		pingJSON(501, 701, "Jane, John", false, pingPersonJSON(1002, "Jane Smith", "jane@example.com"), pingPersonJSON(1003, "John Doe", "john@example.com"))+"]")

	env, err := runPingsCmd(t, f, "list")
	require.NoError(t, err)

	assert.Equal(t, "2 pings", env.Summary)
	var data []map[string]any
	require.NoError(t, json.Unmarshal(env.Data, &data))
	require.Len(t, data, 2)
	assert.EqualValues(t, 500, data[0]["id"])
	assert.EqualValues(t, 700, data[0]["campfire_id"])
	assert.Equal(t, "Jane, John", data[1]["title"])
	assert.Contains(t, env.crumbs(), "basecamp pings messages 500")
}

func TestPingsListEmptyExplainsWhenAPingAppears(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("GET", "/99999/circles.json", 200, `[]`)

	env, err := runPingsCmd(t, f, "list")
	require.NoError(t, err)

	assert.Equal(t, "0 pings", env.Summary)
	assert.Contains(t, env.Notice, "appears here once it has a message")
	assert.Contains(t, env.crumbs(), "basecamp pings start <person>")
}

func TestPingsDisabledForTheCompanySaysSo(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("GET", "/99999/circles.json", 403, `{"error":"Pings have been disabled"}`)

	_, err := runPingsCmd(t, f, "list")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Pings have been disabled")
	assert.Contains(t, pingsErrorHint(t, err), "turned Pings off")
}

func TestPingsShowByIDAndByURL(t *testing.T) {
	for _, arg := range []string{
		"500",
		"https://app.basecamp.com/99999/circles/500",
		"https://3.basecamp.com/99999/circles/500",
	} {
		t.Run(arg, func(t *testing.T) {
			f := newPingsFakeWithPeople()
			f.on("GET", "/99999/circles/500", 200, pingJSON(500, 700, "Jason Fried", false, pingPersonJSON(1001, "Jason Fried", "jason@example.com")))

			env, err := runPingsCmd(t, f, "show", arg)
			require.NoError(t, err)

			assert.Equal(t, "Ping #500: Jason Fried", env.Summary)
			assert.EqualValues(t, 700, dataMap(t, env)["campfire_id"])
			assert.Equal(t, []string{"basecamp pings messages 500", `basecamp pings post 500 "message"`}, env.crumbs())
		})
	}
}

func TestPingsShowUnknownIDExplainsBareNumbersArePings(t *testing.T) {
	f := newPingsFakeWithPeople()

	_, err := runPingsCmd(t, f, "show", "123")
	require.Error(t, err)
	assert.Contains(t, pingsErrorHint(t, err), "not a person ID")
}

func TestPingsRejectURLsThatAreNotPings(t *testing.T) {
	f := newPingsFakeWithPeople()

	_, err := runPingsCmd(t, f, "show", "https://3.basecamp.com/99999/buckets/456/todos/789")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected a ping URL")

	_, err = runPingsCmd(t, f, "show", "https://evil.example.com/99999/circles/500")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "untrusted host")

	_, err = runPingsCmd(t, f, "show", "https://3.basecamp.com/11111/circles/500")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match the configured account")

	assert.Empty(t, f.requests, "a bad reference must not reach the network")
}

func TestPingsStartDirectPingByName(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("POST", "/99999/circles.json", 201, pingJSON(500, 700, "Jane Smith", false, pingPersonJSON(1002, "Jane Smith", "jane@example.com")))

	env, err := runPingsCmd(t, f, "start", "Jane")
	require.NoError(t, err)

	body := f.body("POST /99999/circles.json")
	assert.Equal(t, []any{float64(1002)}, body["user_ids"])
	assert.NotContains(t, body, "new_group")
	assert.Equal(t, "Ping #500: Jane Smith", env.Summary)
	assert.EqualValues(t, 700, dataMap(t, env)["campfire_id"])
	assert.Contains(t, env.crumbs(), "basecamp pings messages 500")
}

func TestPingsStartTwiceReturnsTheSamePing(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("POST", "/99999/circles.json", 201, pingJSON(500, 700, "Jane Smith", false, pingPersonJSON(1002, "Jane Smith", "jane@example.com")))

	first, err := runPingsCmd(t, f, "start", "jane@example.com")
	require.NoError(t, err)
	second, err := runPingsCmd(t, f, "with", "1002")
	require.NoError(t, err)

	assert.Equal(t, dataMap(t, first)["id"], dataMap(t, second)["id"])
}

func TestPingsStartGroupAndNewGroup(t *testing.T) {
	people := []string{pingPersonJSON(1002, "Jane Smith", "jane@example.com"), pingPersonJSON(1003, "John Doe", "john@example.com")}

	f := newPingsFakeWithPeople()
	f.on("POST", "/99999/circles.json", 201, pingJSON(501, 701, "Jane Smith, John Doe", false, people...))
	_, err := runPingsCmd(t, f, "start", "Jane", "john@example.com")
	require.NoError(t, err)
	body := f.body("POST /99999/circles.json")
	assert.Equal(t, []any{float64(1002), float64(1003)}, body["user_ids"])
	assert.NotContains(t, body, "new_group")

	f = newPingsFakeWithPeople()
	f.on("POST", "/99999/circles.json", 201, pingJSON(502, 702, "Jane Smith, John Doe", false, people...))
	_, err = runPingsCmd(t, f, "start", "Jane", "John", "--new-group")
	require.NoError(t, err)
	assert.Equal(t, true, f.body("POST /99999/circles.json")["new_group"])
}

func TestPingsStartDeduplicatesPeople(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("POST", "/99999/circles.json", 201, pingJSON(500, 700, "Jane Smith", false, pingPersonJSON(1002, "Jane Smith", "jane@example.com")))

	_, err := runPingsCmd(t, f, "start", "Jane", "jane@example.com", "1002")
	require.NoError(t, err)
	assert.Equal(t, []any{float64(1002)}, f.body("POST /99999/circles.json")["user_ids"])
}

func TestPingsStartNewGroupNeedsTwoPeople(t *testing.T) {
	f := newPingsFakeWithPeople()

	_, err := runPingsCmd(t, f, "start", "Jane", "--new-group")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--new-group needs two or more people")
	assert.False(t, f.asked("POST /99999/circles.json"))
}

func TestPingsStartAmbiguousNameAsksWhichPerson(t *testing.T) {
	f := newPingsFakeWithPeople()

	_, err := runPingsCmd(t, f, "start", "Jason")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Ambiguous")
	assert.False(t, f.asked("POST /99999/circles.json"))
}

func TestPingsStartUnknownPersonIsNotFound(t *testing.T) {
	f := newPingsFakeWithPeople()

	_, err := runPingsCmd(t, f, "start", "Nobody Atall")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	assert.False(t, f.asked("POST /99999/circles.json"))
}

func TestPingsStartReportsPeopleBasecampLeftOut(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("POST", "/99999/circles.json", 201, pingJSON(500, 700, "Jane Smith", false, pingPersonJSON(1002, "Jane Smith", "jane@example.com")))

	env, err := runPingsCmd(t, f, "start", "Jane", "John")
	require.NoError(t, err)
	assert.Contains(t, env.Notice, "Left out because they cannot be pinged: John Doe")
}

func TestPingsStartWithNobodyReachableFails(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("POST", "/99999/circles.json", 201, pingJSON(500, 700, "Me", false))

	_, err := runPingsCmd(t, f, "start", "Jane")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "None of those people can be pinged")
}

func TestPingsStartRefusedWith422ExplainsAgentRule(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("POST", "/99999/circles.json", 422, ``)

	_, err := runPingsCmd(t, f, "start", "Jane")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "would not start this ping")
	assert.Contains(t, pingsErrorHint(t, err), "someone who can use Pings")
}

func TestPingsStartDisabledWith403(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("POST", "/99999/circles.json", 403, `{"error":"Pings have been disabled"}`)

	_, err := runPingsCmd(t, f, "start", "Jane")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Pings have been disabled")
}

func TestPingsStartMeIsRefused(t *testing.T) {
	f := newPingsFakeWithPeople()

	_, err := runPingsCmd(t, f, "start", "me")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "always in your own pings")
}

func TestPingsMessagesReadsTheCampfireOfThePing(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("GET", "/99999/circles/500", 200, pingJSON(500, 700, "Jason Fried", false, pingPersonJSON(1001, "Jason Fried", "jason@example.com")))
	f.on("GET", "/99999/chats/700/lines.json", 200, `[{"id":9,"content":"hi","created_at":"2026-10-19T15:15:25.688Z"}]`)

	env, err := runPingsCmd(t, f, "messages", "https://app.basecamp.com/99999/circles/500")
	require.NoError(t, err)

	assert.Equal(t, "1 messages", env.Summary)
	assert.Equal(t, []string{`basecamp pings post 500 "message"`, "basecamp pings messages 500 --limit 50"}, env.crumbs())
}

func TestPingsMessagesByPersonUsesTheDirectPingFromTheList(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("GET", "/99999/circles.json", 200, "["+
		pingJSON(501, 701, "Jane, John", false, pingPersonJSON(1002, "Jane Smith", "jane@example.com"), pingPersonJSON(1003, "John Doe", "john@example.com"))+","+
		pingJSON(502, 702, "Named with Jane", true, pingPersonJSON(1002, "Jane Smith", "jane@example.com"))+","+
		pingJSON(500, 700, "Jane Smith", false, pingPersonJSON(1002, "Jane Smith", "jane@example.com"))+"]")
	f.on("GET", "/99999/chats/700/lines.json", 200, `[]`)

	env, err := runPingsCmd(t, f, "messages", "Jane")
	require.NoError(t, err)

	assert.Equal(t, "0 messages", env.Summary)
	assert.False(t, f.asked("POST /99999/circles.json"), "reading a ping must not start one")
}

func TestPingsMessagesByPersonWithNoPingYetSaysHowToStartOne(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("GET", "/99999/circles.json", 200, `[]`)

	_, err := runPingsCmd(t, f, "messages", "Jane")
	require.Error(t, err)
	assert.Contains(t, pingsErrorHint(t, err), "basecamp pings start Jane")
	assert.False(t, f.asked("POST /99999/circles.json"), "reading a ping must not start one")
}

func TestPingsPostWritesToThePingsCampfire(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("GET", "/99999/circles/500", 200, pingJSON(500, 700, "Jason Fried", false, pingPersonJSON(1001, "Jason Fried", "jason@example.com")))
	f.on("POST", "/99999/chats/700/lines.json", 201, `{"id":9,"content":"Lunch?","created_at":"2026-10-19T15:15:25.688Z"}`)

	env, err := runPingsCmd(t, f, "post", "500", "Lunch?")
	require.NoError(t, err)

	assert.Equal(t, "Posted message #9", env.Summary)
	assert.Equal(t, "Lunch?", f.body("POST /99999/chats/700/lines.json")["content"])
	assert.Equal(t, []string{"basecamp pings messages 500", `basecamp pings post 500 "reply"`}, env.crumbs())
}

func TestPingsPostToAPersonStartsOrReusesTheDirectPing(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("POST", "/99999/circles.json", 201, pingJSON(500, 700, "Jane Smith", false, pingPersonJSON(1002, "Jane Smith", "jane@example.com")))
	f.on("POST", "/99999/chats/700/lines.json", 201, `{"id":9,"content":"hi","created_at":"2026-10-19T15:15:25.688Z"}`)

	_, err := runPingsCmd(t, f, "post", "Jane", "hi")
	require.NoError(t, err)

	assert.Equal(t, []any{float64(1002)}, f.body("POST /99999/circles.json")["user_ids"])
	assert.True(t, f.asked("POST /99999/chats/700/lines.json"))
}

func TestPingsPostMentionsResolveAmongThePingsPeople(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("GET", "/99999/circles/501", 200, pingJSON(501, 701, "Jane, John", false,
		pingPersonJSON(1002, "Jane Smith", "jane@example.com"), pingPersonJSON(1003, "John Doe", "john@example.com")))
	f.on("POST", "/99999/chats/701/lines.json", 201, `{"id":9,"content":"x","created_at":"2026-10-19T15:15:25.688Z"}`)

	env, err := runPingsCmd(t, f, "post", "501", "Hey @John, and @Jason Fried too")
	require.NoError(t, err)

	body := f.body("POST /99999/chats/701/lines.json")
	assert.Equal(t, "text/html", body["content_type"])
	content := body["content"].(string)
	assert.Contains(t, content, "sgid-1003", "John is in the ping")
	assert.NotContains(t, content, "sgid-1001", "Jason Fried is pingable but not in this ping")
	assert.Contains(t, env.Notice, "@Jason")
	assert.Contains(t, env.Notice, "only people in this ping")
}

func TestPingsPostPersonIDMentionOutsideThePingFails(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("GET", "/99999/circles/501", 200, pingJSON(501, 701, "Jane Smith", false, pingPersonJSON(1002, "Jane Smith", "jane@example.com")))

	_, err := runPingsCmd(t, f, "post", "501", "ask [@Jason](person:1001)")
	require.Error(t, err)
	assert.Contains(t, pingsErrorHint(t, err), "Not in this ping")
	assert.False(t, f.asked("POST /99999/chats/701/lines.json"))
}

func TestPingsPostPlainTextSkipsMentions(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("GET", "/99999/circles/501", 200, pingJSON(501, 701, "Jane Smith", false, pingPersonJSON(1002, "Jane Smith", "jane@example.com")))
	f.on("POST", "/99999/chats/701/lines.json", 201, `{"id":9,"content":"x","created_at":"2026-10-19T15:15:25.688Z"}`)

	_, err := runPingsCmd(t, f, "post", "501", "hi @Jane", "--content-type", "text/plain")
	require.NoError(t, err)

	body := f.body("POST /99999/chats/701/lines.json")
	assert.Equal(t, "text/plain", body["content_type"])
	assert.Equal(t, "hi @Jane", body["content"])
}

func TestPingsPostWithoutMessageAsksForIt(t *testing.T) {
	f := newPingsFakeWithPeople()

	_, err := runPingsCmd(t, f, "post", "500")
	require.NoError(t, err, "a missing message shows help, as chat post does")
	assert.Empty(t, f.requests)
}

func TestPingsUploadRejectsMissingFileBeforeTheNetwork(t *testing.T) {
	f := newPingsFakeWithPeople()

	_, err := runPingsCmd(t, f, "upload", "500", "/nonexistent/file.png")
	require.Error(t, err)
	assert.Empty(t, f.requests)
}

func TestShowPingURL(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("GET", "/99999/circles/500", 200, pingJSON(500, 700, "Jason Fried", false, pingPersonJSON(1001, "Jason Fried", "jason@example.com")))

	env, err := runPings(t, f, NewShowCmd, "https://app.basecamp.com/99999/circles/500")
	require.NoError(t, err)

	assert.Equal(t, "Ping #500: Jason Fried", env.Summary)
	assert.EqualValues(t, 700, dataMap(t, env)["campfire_id"])
}

func TestShowPingLineURLReadsTheLineRecording(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("GET", "/99999/recordings/111.json", 200, `{"id":111,"type":"Chat::Lines::Text","title":"hi","parent":{"id":700}}`)
	f.on("GET", "/99999/chats/700/lines/111", 200, `{"id":111,"type":"Chat::Lines::Text","content":"hi"}`)

	_, err := runPings(t, f, NewShowCmd, "https://app.basecamp.com/99999/circles/500@111")
	require.NoError(t, err)

	assert.True(t, f.asked("GET /99999/recordings/111.json"))
}

func TestChatLineAcceptsAPingLineURL(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("GET", "/99999/circles/500", 200, pingJSON(500, 700, "Jason Fried", false, pingPersonJSON(1001, "Jason Fried", "jason@example.com")))
	f.on("GET", "/99999/chats/700/lines/111", 200, `{"id":111,"content":"hi","creator":{"name":"Jason Fried"},"created_at":"2026-10-19T15:15:25.688Z"}`)
	f.on("GET", "/99999/recordings/111/comments.json", 200, `[]`)

	env, err := runPings(t, f, NewChatCmd, "line", "https://app.basecamp.com/99999/circles/500@111")
	require.NoError(t, err)

	assert.Equal(t, "Line #111 by Jason Fried", env.Summary)
}

func TestChatLineRejectsAPingURLWithoutALineAndPointsToPings(t *testing.T) {
	f := newPingsFakeWithPeople()

	_, err := runPings(t, f, NewChatCmd, "line", "https://app.basecamp.com/99999/circles/500")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a ping URL names a ping, not a message")
	assert.Contains(t, pingsErrorHint(t, err), "basecamp pings show 500")
}

func TestChatMessagesInAPingFindsItsCampfire(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("GET", "/99999/circles/500", 200, pingJSON(500, 700, "Jason Fried", false, pingPersonJSON(1001, "Jason Fried", "jason@example.com")))
	f.on("GET", "/99999/chats/700/lines.json", 200, `[]`)

	_, err := runPings(t, f, NewChatCmd, "messages", "--in", "500")
	require.NoError(t, err)

	assert.True(t, f.asked("GET /99999/chats/700/lines.json"))
}

func TestChatMessagesInAMissingProjectKeepsTheProjectError(t *testing.T) {
	f := newPingsFakeWithPeople()

	_, err := runPings(t, f, NewChatCmd, "messages", "--in", "777")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func pingsErrorHint(t *testing.T, err error) string {
	t.Helper()
	var e *output.Error
	require.ErrorAs(t, err, &e)
	return e.Hint
}

func (f *pingsFake) askedAbout(fragment string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.requests {
		if strings.Contains(r, fragment) {
			return true
		}
	}
	return false
}

func TestChatLineCommandsTakeThePingLineURLsRoomOverRoomAndIn(t *testing.T) {
	const url = "https://app.basecamp.com/99999/circles/500@111"
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"line", []string{"line", url, "--room", "456", "--in", "777"}, "GET /99999/chats/700/lines/111"},
		{"update", []string{"update", url, "edited", "--room", "456", "--in", "777"}, "PUT /99999/chats/700/lines/111"},
		{"delete", []string{"delete", url, "--force", "--room", "456", "--in", "777"}, "DELETE /99999/chats/700/lines/111"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPingsFakeWithPeople()
			f.on("GET", "/99999/circles/500", 200, pingJSON(500, 700, "Jason Fried", false, pingPersonJSON(1001, "Jason Fried", "jason@example.com")))
			f.on("GET", "/99999/chats/700/lines/111", 200, `{"id":111,"content":"hi","creator":{"name":"Jason Fried"},"created_at":"2026-10-19T15:15:25.688Z"}`)
			f.on("PUT", "/99999/chats/700/lines/111", 204, ``)
			f.on("DELETE", "/99999/chats/700/lines/111", 204, ``)
			f.on("GET", "/99999/recordings/111/comments.json", 200, `[]`)

			env, err := runPings(t, f, NewChatCmd, tc.args...)
			require.NoError(t, err)

			assert.True(t, f.asked(tc.want), f.requests)
			assert.False(t, f.askedAbout("/chats/456"), "the stale --room must not be used")
			assert.False(t, f.askedAbout("/projects/777"), "--in must not replace the ping")
			if tc.name != "update" {
				assert.Contains(t, env.crumbs(), "basecamp chat messages --room 700 --in 500")
			}
		})
	}
}

func TestChatLinePingURLReportsAFailedPingLookup(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("GET", "/99999/circles/500", 403, `{"error":"Pings have been disabled"}`)

	_, err := runPings(t, f, NewChatCmd, "line", "https://app.basecamp.com/99999/circles/500@111", "--room", "456")
	require.Error(t, err)
	assert.Contains(t, pingsErrorHint(t, err), "turned Pings off")
	assert.False(t, f.askedAbout("/chats/456"))
}

func TestChatMessagesInAPingReportsAFailedPingLookup(t *testing.T) {
	f := newPingsFakeWithPeople()
	f.on("GET", "/99999/circles/500", 403, `{"error":"Pings have been disabled"}`)

	_, err := runPings(t, f, NewChatCmd, "messages", "--in", "500")
	require.Error(t, err)
	assert.Contains(t, pingsErrorHint(t, err), "turned Pings off")
}

func TestPingsStartNewGroupNeedsTwoDistinctPeople(t *testing.T) {
	f := newPingsFakeWithPeople()

	_, err := runPingsCmd(t, f, "start", "Jane", "jane@example.com", "--new-group")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--new-group needs two or more people")
	assert.False(t, f.asked("POST /99999/circles.json"))
}

func attachmentFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "report.txt")
	require.NoError(t, os.WriteFile(path, []byte("report"), 0o600))
	return path
}

func TestPostsAnAttachmentWithoutAMessage(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  func() *cobra.Command
		args []string
	}{
		{"pings post", NewPingsCmd, []string{"post", "500"}},
		{"chat post", NewChatCmd, []string{"post", "--room", "700"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPingsFakeWithPeople()
			f.on("GET", "/99999/circles/500", 200, pingJSON(500, 700, "Jason Fried", false, pingPersonJSON(1001, "Jason Fried", "jason@example.com")))
			f.on("POST", "/99999/chats/700/uploads.json", 201, `{"id":888,"content":"","created_at":"2026-10-19T15:15:25.688Z"}`)

			env, err := runPings(t, f, tc.cmd, append(tc.args, "--attach", attachmentFile(t))...)
			require.NoError(t, err)

			assert.True(t, f.asked("POST /99999/chats/700/uploads.json"), f.requests)
			assert.False(t, f.asked("POST /99999/chats/700/lines.json"), "there is no text to post")
			assert.Equal(t, "Posted 1 attachment(s)", env.Summary)
		})
	}
}

func TestPingsTargetKeepsTheURLAccountForTheResolvedOne(t *testing.T) {
	f := newPingsFakeWithPeople()
	app, _ := newTestAppWithTransport(t, f)
	app.Config.AccountID = ""

	target, err := parsePingTarget(app, "https://app.basecamp.com/11111/circles/500")
	require.NoError(t, err, "with no account configured the URL cannot be compared yet")

	app.Config.AccountID = "99999" // what ensureAccount settles on
	_, err = resolvePing(context.Background(), app, target, pingLookupOnly)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "URL account 11111 does not match the configured account 99999")
	assert.Empty(t, f.requests)
}

func TestPingsHintQuotesThePersonForTheShell(t *testing.T) {
	f := newPingsFake()
	f.on("GET", "/99999/circles/people.json", 200, `[{"id":1005,"name":"Sam $HOME","email_address":"sam@example.com","attachable_sgid":"sgid-1005"}]`)
	f.on("GET", "/99999/circles.json", 200, `[]`)

	_, err := runPingsCmd(t, f, "messages", "Sam $HOME")
	require.Error(t, err)
	assert.Contains(t, pingsErrorHint(t, err), "basecamp pings start 'Sam $HOME'")
}

// Editing a Ping line resolves @mentions among the Ping's people, as posting
// there does: a name nobody in the Ping has stays text, and the account's
// people are not asked about, since the Ping is no project.
func TestChatUpdateOnAPingLineMentionsOnlyThePingsPeople(t *testing.T) {
	const url = "https://app.basecamp.com/99999/circles/500@111"
	const put = "PUT /99999/chats/700/lines/111"
	for _, tc := range []struct {
		name        string
		message     string
		wantSGID    string
		wantNotice  string
		wantNoSGIDs []string
	}{
		{"a name in the Ping", "thanks @Jason", "sgid-1001", "", []string{"sgid-1004"}},
		{"a name nobody has", "thanks @Nobody", "", "@Nobody", nil},
		{"a namesake outside the Ping", "thanks @Jane.Smith", "", "@Jane.Smith", []string{"sgid-1002"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPingsFakeWithPeople()
			f.on("GET", "/99999/circles/500", 200, pingJSON(500, 700, "Jason Fried", false, pingPersonJSON(1001, "Jason Fried", "jason@example.com")))
			f.on("PUT", "/99999/chats/700/lines/111", 204, ``)
			f.on("GET", "/99999/chats/700/lines/111", 200, `{"id":111,"content":"edited","creator":{"name":"Jason Fried"},"created_at":"2026-10-19T15:15:25.688Z"}`)

			env, err := runPings(t, f, NewChatCmd, "update", url, tc.message)
			require.NoError(t, err)

			require.True(t, f.asked(put), f.requests)
			content, _ := f.body(put)["content"].(string)
			if tc.wantSGID != "" {
				assert.Contains(t, content, tc.wantSGID)
			}
			for _, sgid := range tc.wantNoSGIDs {
				assert.NotContains(t, content, sgid, "only the Ping's people can be mentioned")
			}
			if tc.wantNotice != "" {
				assert.Contains(t, content, tc.wantNotice, "an unresolved mention stays text")
				assert.Contains(t, env.Notice, tc.wantNotice)
				assert.Contains(t, env.Notice, "only people in this ping")
			}
			assert.False(t, f.askedAbout("/projects/500"), "a Ping is no project")
			assert.False(t, f.askedAbout("people.json"), "the account's people are not asked about")
		})
	}
}

// pings post refuses HTML without a content type as chat post does, and
// posts it as rich text when asked.
func TestPingsPostRefusesHTMLWithoutAContentType(t *testing.T) {
	const post = "POST /99999/chats/700/lines.json"
	newFake := func() *pingsFake {
		f := newPingsFakeWithPeople()
		f.on("GET", "/99999/circles/500", 200, pingJSON(500, 700, "Jason Fried", false, pingPersonJSON(1001, "Jason Fried", "jason@example.com")))
		f.on("POST", "/99999/chats/700/lines.json", 201, `{"id":9,"content":"x","created_at":"2026-10-19T15:15:25.688Z"}`)
		return f
	}

	f := newFake()
	_, err := runPingsCmd(t, f, "post", "500", "<p>Build is green</p><ul><li>#913</li></ul>")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "this message is HTML")
	assert.False(t, f.asked(post), "nothing is posted")

	f = newFake()
	_, err = runPingsCmd(t, f, "post", "500", "<p>Build is green</p>", "--content-type", "text/html")
	require.NoError(t, err)
	assert.Equal(t, "text/html", f.body(post)["content_type"])
	assert.Equal(t, "<p>Build is green</p>", f.body(post)["content"])
}
