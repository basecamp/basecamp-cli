package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// mockTodoFilingTransport serves to-dos, lists and groups in one project and
// to-do lists in others to move or copy them into.
//
//   - Project 123 ("Source") has To-dos 300 with list 310 ("Backlog"), which
//     holds group 311 ("Ideas"). To-dos 456, 457 and 459 are in list 310,
//     and 458 in group 311. To-do 460 is loose, on To-dos 300 in no list.
//     To-do 111 was moved and is 457 now, so reading 111 answers 457.
//   - Project 999 ("Dest") has To-dos 900 with lists 901 ("Launch") and 902
//     ("Later"), group 920 in list 901, and an archived list 930. With
//     twoTodosets it also has To-dos 950 with list 951 ("Roadmap"); with
//     duplicateName, list 903 ("launch") in To-dos 900. A disabled To-dos 960
//     is always on its dock.
//   - Project 777 ("Solo") has To-dos 700 with one list, 701 ("Only").
//   - Project 888 ("Bare") has only a disabled To-dos, 880.
//   - A move or copy POST of recording N answers filing N, pending; its first
//     GET completes it as recording N*10+1, read back in the destination it
//     was filed into. postStatus answers every POST with that status,
//     refuse answers the POST for those recordings with 403 and drop with
//     502, a filing in stuck stays processing, and a read of an unreadable
//     new recording answers 404.
type mockTodoFilingTransport struct {
	twoTodosets   bool
	duplicateName bool
	postStatus    int
	refuse        map[string]bool
	drop          map[string]bool
	stuck         map[string]bool
	unreadable    map[string]bool

	mu       sync.Mutex
	requests []string
	posts    []string
	bodies   []map[string]any
	filedTo  map[int64]int64
}

func (m *mockTodoFilingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/99999"), ".json")
	m.requests = append(m.requests, req.Method+" "+path)
	respond := func(status int, body string) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Content-Type", "application/json")
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: header}, nil
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")

	if req.Method == http.MethodPost {
		m.posts = append(m.posts, path)
		body := map[string]any{}
		if req.Body != nil {
			raw, _ := io.ReadAll(req.Body)
			_ = json.Unmarshal(raw, &body)
		}
		m.bodies = append(m.bodies, body)
		if m.postStatus != 0 {
			return respond(m.postStatus, `{"error":"upstream"}`)
		}
		if len(parts) == 3 && parts[0] == "recordings" {
			if m.refuse[parts[1]] {
				return respond(403, `{"error":"forbidden"}`)
			}
			if m.drop[parts[1]] {
				return respond(502, `{"error":"upstream"}`)
			}
			source, _ := strconv.ParseInt(parts[1], 10, 64)
			if m.filedTo == nil {
				m.filedTo = map[int64]int64{}
			}
			dest, _ := body["filing_destination_id"].(float64)
			m.filedTo[source*10+1] = int64(dest)
			return respond(201, fmt.Sprintf(`{"id":%d,"status":"pending","type":"Move","source_recording_id":%d}`, source, source))
		}
	}
	if req.Method == http.MethodPut {
		return respond(204, ``)
	}

	if len(parts) == 2 && parts[0] == "filings" {
		source, _ := strconv.ParseInt(parts[1], 10, 64)
		if m.stuck[parts[1]] {
			return respond(200, fmt.Sprintf(`{"id":%d,"status":"processing","type":"Move","source_recording_id":%d}`, source, source))
		}
		return respond(200, fmt.Sprintf(`{"id":%d,"status":"completed","type":"Move","source_recording_id":%d,"destination_recording":{"id":%d}}`, source, source, source*10+1))
	}

	dock := func(id int, tools ...string) string {
		return fmt.Sprintf(`{"id":%d,"dock":[%s]}`, id, strings.Join(tools, ","))
	}
	todoset := func(id int, enabled bool) string {
		return fmt.Sprintf(`{"name":"todoset","id":%d,"title":"To-dos","enabled":%t}`, id, enabled)
	}
	switch path {
	case "/projects":
		return respond(200, `[{"id":123,"name":"Source"},{"id":999,"name":"Dest"},{"id":777,"name":"Solo"},{"id":888,"name":"Bare"}]`)
	case "/projects/123":
		return respond(200, dock(123, todoset(300, true)))
	case "/projects/999":
		tools := []string{todoset(900, true), todoset(960, false)}
		if m.twoTodosets {
			tools = append(tools, todoset(950, true))
		}
		return respond(200, dock(999, tools...))
	case "/projects/777":
		return respond(200, dock(777, todoset(700, true)))
	case "/projects/888":
		return respond(200, dock(888, todoset(880, false)))
	case "/todosets/300/todolists":
		return respond(200, "["+filingListJSON(310, "Backlog", 123, "Todoset", 300)+"]")
	case "/todosets/900/todolists":
		lists := []string{filingListJSON(901, "Launch", 999, "Todoset", 900), filingListJSON(902, "Later", 999, "Todoset", 900)}
		if m.duplicateName {
			lists = append(lists, filingListJSON(903, "launch", 999, "Todoset", 900))
		}
		return respond(200, "["+strings.Join(lists, ",")+"]")
	case "/todosets/950/todolists":
		return respond(200, "["+filingListJSON(951, "Roadmap", 999, "Todoset", 950)+"]")
	case "/todosets/700/todolists":
		return respond(200, "["+filingListJSON(701, "Only", 777, "Todoset", 700)+"]")
	case "/todosets/300", "/todosets/900", "/todosets/950", "/todosets/700":
		bucket := map[string]int{"300": 123, "900": 999, "950": 999, "700": 777}[parts[1]]
		return respond(200, fmt.Sprintf(`{"id":%s,"title":"To-dos","bucket":{"id":%d,"name":%q}}`, parts[1], bucket, filingProjectNames[bucket]))
	case "/todolists/310":
		return respond(200, filingListJSON(310, "Backlog", 123, "Todoset", 300))
	case "/todolists/311":
		return respond(200, filingListJSON(311, "Ideas", 123, "Todolist", 310))
	case "/todolists/901":
		return respond(200, filingListJSON(901, "Launch", 999, "Todoset", 900))
	case "/todolists/920":
		return respond(200, filingListJSON(920, "Phase 2", 999, "Todolist", 901))
	case "/todolists/930":
		return respond(200, strings.Replace(filingListJSON(930, "Old", 999, "Todoset", 900), `"active"`, `"archived"`, 1))
	case "/todos/456", "/todos/457", "/todos/459":
		return respond(200, filingTodoJSON(parts[1], 123, 310, "Backlog"))
	case "/todos/111":
		return respond(200, filingTodoJSON("457", 123, 310, "Backlog"))
	case "/todos/458":
		return respond(200, filingTodoJSON("458", 123, 311, "Ideas"))
	case "/todos/460":
		return respond(200, strings.Replace(filingTodoJSON("460", 123, 300, "To-dos"), `"type":"Todolist"`, `"type":"Todoset"`, 1))
	}

	if len(parts) == 2 && (parts[0] == "todos" || parts[0] == "todolists") && !m.unreadable[parts[1]] {
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		if into, ok := m.filedTo[id]; ok {
			bucket, title := 999, "Launch"
			switch into {
			case 300:
				bucket, title = 123, "To-dos"
			case 310:
				bucket, title = 123, "Backlog"
			case 701:
				bucket, title = 777, "Only"
			case 900, 950:
				title = "To-dos"
			}
			if parts[0] == "todos" {
				return respond(200, filingTodoJSON(parts[1], bucket, into, title))
			}
			return respond(200, fmt.Sprintf(`{"id":%d,"name":"Filed","type":"Todolist","status":"active","bucket":{"id":%d,"name":%q},"parent":{"id":%d,"title":%q}}`, id, bucket, filingProjectNames[bucket], into, title))
		}
	}
	return respond(404, `{"error":"not found"}`)
}

var filingProjectNames = map[int]string{123: "Source", 999: "Dest", 777: "Solo"}

func filingListJSON(id int, name string, bucket int, parentType string, parent int) string {
	return fmt.Sprintf(`{"id":%d,"name":%q,"title":%q,"type":"Todolist","status":"active","bucket":{"id":%d,"name":%q},"parent":{"id":%d,"title":"Parent","type":%q}}`,
		id, name, name, bucket, filingProjectNames[bucket], parent, parentType)
}

func filingTodoJSON(id string, bucket int, parent int64, parentTitle string) string {
	return fmt.Sprintf(`{"id":%s,"title":"Task %s","type":"Todo","bucket":{"id":%d,"name":%q},"parent":{"id":%d,"title":%q,"type":"Todolist"}}`,
		id, id, bucket, filingProjectNames[bucket], parent, parentTitle)
}

type todoFilingRun struct {
	err      error
	output   string
	summary  string
	data     json.RawMessage
	requests []string
}

func runTodoFilingCommand(t *testing.T, transport *mockTodoFilingTransport, group *cobra.Command, args ...string) todoFilingRun {
	t.Helper()
	shortFilingWait(t)
	app, buf := newTestAppWithTransport(t, transport)
	app.Flags.Hints = true
	run := todoFilingRun{err: executeCommand(group, app, args...), output: buf.String()}
	if run.err == nil {
		var envelope struct {
			Data    json.RawMessage `json:"data"`
			Summary string          `json:"summary"`
		}
		require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
		run.summary, run.data = envelope.Summary, envelope.Data
	}
	run.requests = transport.requests
	return run
}

func requireUsageRefusal(t *testing.T, run todoFilingRun, transport *mockTodoFilingTransport) *output.Error {
	t.Helper()
	e := output.AsError(run.err)
	require.NotNil(t, run.err)
	assert.Empty(t, transport.posts, "nothing is filed")
	for _, r := range transport.requests {
		assert.False(t, strings.HasPrefix(r, "PUT"), "nothing is repositioned: %s", r)
	}
	return e
}

func TestTodosMoveToAListInAnotherProjectByName(t *testing.T) {
	transport := &mockTodoFilingTransport{}
	run := runTodoFilingCommand(t, transport, NewTodosCmd(), "move", "456", "--to-project", "Dest", "--to-list", "launch")
	require.NoError(t, run.err)

	assert.Equal(t, []string{"/recordings/456/moves"}, transport.posts)
	assert.Equal(t, float64(901), transport.bodies[0]["filing_destination_id"])
	var todo map[string]any
	require.NoError(t, json.Unmarshal(run.data, &todo))
	assert.Equal(t, float64(4561), todo["id"])
	assert.Equal(t, float64(456), todo["source_id"])
	assert.Equal(t, "Moved to-do #456 to Dest › Launch as to-do #4561", run.summary)
	assert.Contains(t, run.output, "basecamp todos show 4561")
}

func TestTodosMoveSeveralFilesEachAndReportsEach(t *testing.T) {
	transport := &mockTodoFilingTransport{}
	run := runTodoFilingCommand(t, transport, NewTodosCmd(), "move", "456", "457", "--to-list", "901")
	require.NoError(t, run.err)

	assert.Equal(t, []string{"/recordings/456/moves", "/recordings/457/moves"}, transport.posts)
	var todos []map[string]any
	require.NoError(t, json.Unmarshal(run.data, &todos))
	require.Len(t, todos, 2)
	assert.Equal(t, float64(457), todos[1]["source_id"])
	assert.Equal(t, "Moved 2 to-dos: #456 as #4561 in Dest › Launch, #457 as #4571 in Dest › Launch", run.summary)
}

func TestTodosMoveSeveralFailsWhenOneFailsAndNamesWhatMoved(t *testing.T) {
	transport := &mockTodoFilingTransport{refuse: map[string]bool{"457": true}}
	run := runTodoFilingCommand(t, transport, NewTodosCmd(), "move", "457", "456", "--to-list", "901")
	e := output.AsError(run.err)
	require.NotNil(t, run.err)

	assert.Equal(t, []string{"/recordings/457/moves", "/recordings/456/moves"}, transport.posts, "a failure does not stop the rest")
	assert.Equal(t, output.CodeForbidden, e.Code)
	assert.Contains(t, e.Message, "Moved 1 of 2 to-dos (#456 as #4561 in Dest › Launch)")
	assert.Contains(t, e.Message, "#457 refused")
	assert.Contains(t, e.Hint, "run the command again for #457 alone")
	assert.False(t, e.Retryable)
}

func TestTodosCopySeveralAsksToRunAgainOnlyForTheRefused(t *testing.T) {
	transport := &mockTodoFilingTransport{
		refuse:     map[string]bool{"457": true},
		unreadable: map[string]bool{"4581": true},
		drop:       map[string]bool{"459": true},
	}
	run := runTodoFilingCommand(t, transport, NewTodosCmd(), "copy", "456", "457", "458", "459", "--to-list", "901")
	e := output.AsError(run.err)
	require.NotNil(t, run.err)

	assert.Equal(t, []string{"/recordings/456/copies", "/recordings/457/copies", "/recordings/458/copies", "/recordings/459/copies"},
		transport.posts, "each is copied once and none is retried")
	assert.NotZero(t, output.ExitCodeFor(e.Code))
	assert.NotEqual(t, output.CodeForbidden, e.Code, "a copy that may have started outranks a refusal")
	assert.False(t, e.Retryable)

	assert.Contains(t, e.Message, "Copied 1 of 4 to-dos (#456 as #4561 in Dest › Launch)")
	assert.Contains(t, e.Message, "#457 refused")
	assert.Contains(t, e.Message, "#458 copied as #4581, not read back")
	assert.Contains(t, e.Message, "#459 not settled")

	assert.Contains(t, e.Hint, "Do not copy these again: #458 is #4581 now (basecamp todos show 4581)")
	assert.Contains(t, e.Hint, "Do not copy #459 again yet. Basecamp may have started the copy")
	runAgain := regexp.MustCompile(`(?i)\brun\b.*\bagain\b`)
	offers := 0
	for sentence := range strings.SplitSeq(strings.ReplaceAll(e.Hint, "\n", ". "), ". ") {
		if runAgain.MatchString(sentence) && !strings.HasPrefix(sentence, "Do not") {
			offers++
			assert.Contains(t, sentence, "#457", "only the refused copy is offered to run again: %q", sentence)
			for _, other := range []string{"#456", "#458", "#459"} {
				assert.NotContains(t, sentence, other, "never asks to repeat a copy that was made or may have been: %q", sentence)
			}
		}
	}
	assert.Equal(t, 1, offers, "the refused copy is offered to run again: %q", e.Hint)
}

func TestTodosCopySeveralNeverAsksToRepeatACopyThatWasMadeOrMayHaveBeen(t *testing.T) {
	for name, transport := range map[string]*mockTodoFilingTransport{
		"copied but not read back": {unreadable: map[string]bool{"4571": true}},
		"may have started":         {drop: map[string]bool{"457": true}},
	} {
		t.Run(name, func(t *testing.T) {
			run := runTodoFilingCommand(t, transport, NewTodosCmd(), "copy", "456", "457", "--to-list", "901")
			e := output.AsError(run.err)
			require.NotNil(t, run.err)

			assert.Len(t, transport.posts, 2)
			assert.NotZero(t, output.ExitCodeFor(e.Code))
			assert.Contains(t, e.Message, "Copied 1 of 2 to-dos (#456 as #4561 in Dest › Launch)")
			assert.Contains(t, e.Hint, "Do not copy")
			assert.NotContains(t, strings.ToLower(e.Hint), "run the command again")
			assert.NotContains(t, strings.ToLower(e.Hint), "run it again")
		})
	}
}

func TestTodosMoveRefusesAListInAnotherProjectInsteadOfReportingSuccess(t *testing.T) {
	transport := &mockTodoFilingTransport{}
	run := runTodoFilingCommand(t, transport, NewTodosCmd(), "move", "456", "--to", "1", "--list", "901")
	e := requireUsageRefusal(t, run, transport)

	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Equal(t, "To-do list 901 is in project 999, not in the to-do's project 123", e.Message)
	assert.Contains(t, e.Hint, "basecamp todos move 456 --to-project 999 --to-list 901")
}

func TestTodosMoveRefusesAListURLInAnotherProjectWithTheFilingCommand(t *testing.T) {
	transport := &mockTodoFilingTransport{}
	run := runTodoFilingCommand(t, transport, NewTodosCmd(), "position",
		"https://3.basecamp.com/99999/buckets/123/todos/456", "--to", "1",
		"--list", "https://3.basecamp.com/99999/buckets/999/todolists/901")
	e := requireUsageRefusal(t, run, transport)

	assert.Contains(t, e.Hint, "basecamp todos move 456 --to-project 999 --to-list 901")
	assert.Empty(t, transport.requests, "both URLs name their projects")
}

func TestTodosMoveToAnotherListInItsProjectStillRepositions(t *testing.T) {
	transport := &mockTodoFilingTransport{}
	run := runTodoFilingCommand(t, transport, NewTodosCmd(), "move", "456", "--to", "2", "--list", "310")
	require.NoError(t, run.err)

	assert.Contains(t, transport.requests, "PUT /todos/456/position")
	assert.Empty(t, transport.posts, "a move within the project keeps the to-do's id")
	assert.Equal(t, "Moved todo #456 to list #310 at position 2", run.summary)
}

func TestTodosRepositionRefusesSeveralTodos(t *testing.T) {
	transport := &mockTodoFilingTransport{}
	run := runTodoFilingCommand(t, transport, NewTodosCmd(), "move", "456", "457", "--to", "1")
	e := requireUsageRefusal(t, run, transport)

	assert.Contains(t, e.Hint, "--to-list")
}

func TestTodosMoveRefusesFilingFlagsWithRepositionFlags(t *testing.T) {
	for name, args := range map[string][]string{
		"--to":   {"move", "456", "--to-project", "Dest", "--to-list", "Launch", "--to", "1"},
		"--list": {"move", "456", "--to-project", "Dest", "--list", "Launch"},
	} {
		t.Run(name, func(t *testing.T) {
			transport := &mockTodoFilingTransport{}
			run := runTodoFilingCommand(t, transport, NewTodosCmd(), args...)
			e := requireUsageRefusal(t, run, transport)

			assert.Equal(t, output.CodeUsage, e.Code)
			assert.Contains(t, e.Hint, "use --to-list")
			assert.Empty(t, transport.requests)
		})
	}
}

func TestTodosMoveToAProjectWithSeveralListsIsAmbiguous(t *testing.T) {
	transport := &mockTodoFilingTransport{}
	run := runTodoFilingCommand(t, transport, NewTodosCmd(), "move", "456", "--to-project", "Dest")
	e := requireUsageRefusal(t, run, transport)

	assert.Equal(t, output.CodeAmbiguous, e.Code)
	assert.Contains(t, e.Hint, "--to-list")
	assert.Contains(t, e.Hint, "901  Launch")
	assert.Contains(t, e.Hint, "902  Later")
}

func TestTodosMoveToAProjectWithOneListUsesIt(t *testing.T) {
	transport := &mockTodoFilingTransport{}
	run := runTodoFilingCommand(t, transport, NewTodosCmd(), "move", "456", "--to-project", "Solo")
	require.NoError(t, run.err)

	assert.Equal(t, float64(701), transport.bodies[0]["filing_destination_id"])
	assert.Equal(t, "Moved to-do #456 to Solo › Only as to-do #4561", run.summary)
}

func TestTodosMoveToAListNameTwoListsShareIsAmbiguous(t *testing.T) {
	transport := &mockTodoFilingTransport{duplicateName: true}
	run := runTodoFilingCommand(t, transport, NewTodosCmd(), "move", "456", "--to-project", "Dest", "--to-list", "Launch")
	e := requireUsageRefusal(t, run, transport)

	assert.Equal(t, output.CodeAmbiguous, e.Code)
	assert.Contains(t, e.Hint, "901  Launch")
	assert.Contains(t, e.Hint, "903  launch")
	assert.NotContains(t, e.Hint, "902", "only the lists with that name are candidates")
}

func TestTodosMoveToAProjectWithoutAListNamesTheProject(t *testing.T) {
	transport := &mockTodoFilingTransport{}
	run := runTodoFilingCommand(t, transport, NewTodosCmd(), "move", "456", "--to-project", "Bare")
	e := requireUsageRefusal(t, run, transport)

	assert.Equal(t, output.CodeNotFound, e.Code)
	assert.Equal(t, "Project Bare has no to-do list", e.Message)
}

func TestTodosMoveRefusesADestinationBasecampRefuses(t *testing.T) {
	for name, tc := range map[string]struct{ list, message, hint string }{
		"a group":         {"920", "920 (Phase 2) is a group in to-do list 901", "--to-list 901"},
		"an archived one": {"930", "To-do list 930 (Old) is archived", ""},
	} {
		t.Run(name, func(t *testing.T) {
			transport := &mockTodoFilingTransport{}
			run := runTodoFilingCommand(t, transport, NewTodosCmd(), "move", "456", "--to-list", tc.list)
			e := requireUsageRefusal(t, run, transport)

			assert.Contains(t, e.Message, tc.message)
			assert.Contains(t, e.Hint, tc.hint)
		})
	}
}

func TestTodosMoveRefusesTheListTheTodoIsIn(t *testing.T) {
	for name, todo := range map[string]string{"in the list": "456", "in a group in the list": "458"} {
		t.Run(name, func(t *testing.T) {
			transport := &mockTodoFilingTransport{}
			run := runTodoFilingCommand(t, transport, NewTodosCmd(), "move", todo, "--to-list", "310")
			e := requireUsageRefusal(t, run, transport)

			assert.Contains(t, e.Message, "is already in to-do list 310")
			assert.Contains(t, e.Hint, "basecamp todos move "+todo+" --to <position>")
		})
	}
}

func TestTodosMoveRefusesAListNameWithoutAProject(t *testing.T) {
	transport := &mockTodoFilingTransport{}
	run := runTodoFilingCommand(t, transport, NewTodosCmd(), "move", "456", "--to-list", "Launch")
	e := requireUsageRefusal(t, run, transport)

	assert.Contains(t, e.Hint, "--to-project")
	assert.Empty(t, transport.requests)
}

func TestTodosCopyWithoutDestinationGoesIntoTheTodosOwnList(t *testing.T) {
	for name, tc := range map[string]struct {
		todo string
		into float64
	}{
		"in a list":  {"456", 310},
		"in a group": {"458", 310},
	} {
		t.Run(name, func(t *testing.T) {
			transport := &mockTodoFilingTransport{}
			run := runTodoFilingCommand(t, transport, NewTodosCmd(), "copy", tc.todo, "--include-comments")
			require.NoError(t, run.err)

			assert.Equal(t, []string{"/recordings/" + tc.todo + "/copies"}, transport.posts)
			assert.Equal(t, tc.into, transport.bodies[0]["filing_destination_id"], "Basecamp files into a list, not a group")
			assert.Equal(t, true, transport.bodies[0]["include_comments"])
			assert.Equal(t, "Copied to-do #"+tc.todo+" to Source › Backlog as to-do #"+tc.todo+"1", run.summary)
		})
	}
}

func TestTodosCopySeveralToAnotherProjectNeverRetriesAFailedCopy(t *testing.T) {
	transport := &mockTodoFilingTransport{postStatus: 502}
	run := runTodoFilingCommand(t, transport, NewTodosCmd(), "copy", "456", "457", "--to-project", "Dest", "--to-list", "Later")
	e := output.AsError(run.err)
	require.NotNil(t, run.err)

	assert.Equal(t, []string{"/recordings/456/copies", "/recordings/457/copies"}, transport.posts, "one request per to-do")
	assert.Equal(t, float64(902), transport.bodies[0]["filing_destination_id"])
	assert.False(t, e.Retryable)
	assert.Contains(t, e.Message, "Copied 0 of 2 to-dos")
	assert.Contains(t, e.Hint, "may have started the copy")
	assert.Contains(t, e.Hint, "basecamp recordings list Todo --in 999 --status active --all")
	assert.Contains(t, e.Hint, `look for "Task 456" in Later (#902)`, "a copy keeps its original's created_at, so its title tells it apart")
	assert.Contains(t, e.Hint, `look for "Task 457" in Later (#902)`)
}

func TestTodosCopyIntoTheListItIsInIsAllowed(t *testing.T) {
	transport := &mockTodoFilingTransport{}
	run := runTodoFilingCommand(t, transport, NewTodosCmd(), "copy", "456", "--to-list", "https://3.basecamp.com/99999/buckets/123/todolists/310")
	require.NoError(t, run.err)

	assert.Equal(t, float64(310), transport.bodies[0]["filing_destination_id"])
}

func TestTodosCopyRefusesAListURLThatDisagreesWithToProject(t *testing.T) {
	transport := &mockTodoFilingTransport{}
	run := runTodoFilingCommand(t, transport, NewTodosCmd(), "copy", "456", "--to-project", "Solo",
		"--to-list", "https://3.basecamp.com/99999/buckets/999/todolists/901")
	e := requireUsageRefusal(t, run, transport)

	assert.Contains(t, e.Message, "--to-list URL is in project 999, but --to-project points at project 777")
}

func TestTodosMoveAndCopyALooseTodoIntoAList(t *testing.T) {
	for name, tc := range map[string]struct {
		args    []string
		post    string
		into    float64
		summary string
	}{
		"move to another project":         {[]string{"move", "460", "--to-project", "Dest", "--to-list", "Launch"}, "/recordings/460/moves", 901, "Moved to-do #460 to Dest › Launch as to-do #4601"},
		"move into a list in its project": {[]string{"move", "460", "--to-list", "310"}, "/recordings/460/moves", 310, "Moved to-do #460 to Source › Backlog as to-do #4601"},
		"copy to another project":         {[]string{"copy", "460", "--to-list", "901"}, "/recordings/460/copies", 901, "Copied to-do #460 to Dest › Launch as to-do #4601"},
	} {
		t.Run(name, func(t *testing.T) {
			transport := &mockTodoFilingTransport{}
			run := runTodoFilingCommand(t, transport, NewTodosCmd(), tc.args...)
			require.NoError(t, run.err)

			assert.Equal(t, []string{tc.post}, transport.posts)
			assert.Equal(t, tc.into, transport.bodies[0]["filing_destination_id"])
			assert.Equal(t, tc.summary, run.summary)
		})
	}
}

func TestTodosCopyALooseTodoWithoutADestinationIsRefused(t *testing.T) {
	transport := &mockTodoFilingTransport{}
	run := runTodoFilingCommand(t, transport, NewTodosCmd(), "copy", "460")
	e := requireUsageRefusal(t, run, transport)

	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Contains(t, e.Message, "To-do #460 is loose")
	assert.Contains(t, e.Hint, "basecamp todos copy 460 --to-project 123 --to-list <list>")
}

func TestTodosRefuseATodoNamedTwiceBeforeFilingAny(t *testing.T) {
	for name, tc := range map[string]struct {
		args    []string
		message string
	}{
		"copy by its old id and its new one": {[]string{"copy", "456", "111", "457", "--to-list", "901"}, "111 and 457 are both to-do #457"},
		"move by its old id and its new one": {[]string{"move", "111", "457", "--to-list", "901"}, "111 and 457 are both to-do #457"},
		"the same id twice":                  {[]string{"copy", "456", "456", "--to-list", "901"}, "456 and 456 are both to-do #456"},
	} {
		t.Run(name, func(t *testing.T) {
			transport := &mockTodoFilingTransport{}
			run := runTodoFilingCommand(t, transport, NewTodosCmd(), tc.args...)
			e := requireUsageRefusal(t, run, transport)

			assert.Equal(t, output.CodeUsage, e.Code)
			assert.Equal(t, tc.message, e.Message)
		})
	}
}

// A follow-up a filing prints must reach the same profile and account in a
// fresh shell: a copy's recovery listing that ran against the default account
// would miss the copy and invite a second one.
func TestFilingFollowUpsNameTheProfileAndAnAccountGivenThisRun(t *testing.T) {
	const scope = " --profile work --account 99999"
	todoListing := "basecamp recordings list Todo --in 999 --status active --all" + scope
	for name, tc := range map[string]struct {
		transport http.RoundTripper
		group     func() *cobra.Command
		args      []string
		want      string
	}{
		"a copy that may have started": {
			&mockTodoFilingTransport{drop: map[string]bool{"456": true}}, NewTodosCmd,
			[]string{"copy", "456", "--to-list", "901"}, todoListing,
		},
		"several copies, one may have started": {
			&mockTodoFilingTransport{drop: map[string]bool{"457": true}}, NewTodosCmd,
			[]string{"copy", "456", "457", "--to-list", "901"}, todoListing,
		},
		"a move not read back": {
			&mockTodoFilingTransport{unreadable: map[string]bool{"4561": true}}, NewTodosCmd,
			[]string{"move", "456", "--to-list", "901"}, "basecamp todos show 4561" + scope,
		},
		"several moves, one not read back": {
			&mockTodoFilingTransport{unreadable: map[string]bool{"4571": true}}, NewTodosCmd,
			[]string{"move", "456", "457", "--to-list", "901"}, "basecamp todos show 4571" + scope,
		},
		"a moved to-do's breadcrumb": {
			&mockTodoFilingTransport{}, NewTodosCmd,
			[]string{"move", "456", "--to-list", "901"}, "basecamp todos show 4561" + scope,
		},
		"a list copy that may have started": {
			&mockTodoFilingTransport{postStatus: 502}, NewTodolistsCmd,
			[]string{"copy", "310", "--to-project", "Dest"},
			"basecamp recordings list Todolist --in 999 --status active --all" + scope,
		},
		"a card copy that may have started": {
			&mockFilingTransport{postStatus: 502}, NewCardsCmd,
			[]string{"copy", "456", "--to-project", "Dest"},
			"basecamp recordings list Kanban::Card --in 999 --status active --all" + scope,
		},
		"a card move's breadcrumb": {
			&mockFilingTransport{statuses: []string{"completed"}}, NewCardsCmd,
			[]string{"move", "456", "--to-project", "Dest"}, "basecamp cards show 4560 --in 999" + scope,
		},
		"a copy still running": {
			&mockTodoFilingTransport{stuck: map[string]bool{"456": true}}, NewTodosCmd,
			[]string{"copy", "456", "--to-list", "901"}, "basecamp filings show 456" + scope + ",",
		},
		"a message copy that may have started": {
			func() *familyTransport {
				transport := messagesTransport()
				transport.refuse = map[int64]int{456: 502}
				return transport
			}(), NewMessagesCmd,
			[]string{"copy", "456", "--to-project", "Dest"},
			"basecamp recordings list Message --in 999 --status active --all" + scope + ", then",
		},
		"a message move still running": {
			func() *familyTransport {
				transport := messagesTransport()
				transport.stuck = map[int64]bool{456: true}
				return transport
			}(), NewMessagesCmd,
			[]string{"move", "456", "--to-project", "Dest"}, "basecamp filings show 456" + scope + ",",
		},
	} {
		t.Run(name, func(t *testing.T) {
			shortFilingWait(t)
			app, buf := newTestAppWithTransport(t, tc.transport)
			app.Flags.Hints = true
			app.Config.ActiveProfile = "work"
			app.Config.Sources = map[string]string{"account_id": "flag"}

			err := executeCommand(tc.group(), app, tc.args...)
			shown := buf.String()
			if err != nil {
				shown = output.AsError(err).Hint
			}
			assert.Contains(t, shown, tc.want)
		})
	}
}

// A 401 partway through several filings must keep the remedy that fits the
// credential sent, as a single filing's does: under a profile, a login to the
// default one fixes nothing, and under BASECAMP_TOKEN no login does.
func TestTodosFilingSeveralKeepsTheAuthRemedyForTheCredentialSent(t *testing.T) {
	for name, tc := range map[string]struct {
		envToken string
		want     string
	}{
		"a profile":    {"", "Run: basecamp auth login -P work"},
		"an env token": {"bc_at_env", "BASECAMP_TOKEN is set"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BASECAMP_TOKEN", tc.envToken)
			shortFilingWait(t)
			transport := &mockTodoFilingTransport{postStatus: http.StatusUnauthorized}
			app, buf := newTestAppWithTransport(t, transport)
			app.Config.ActiveProfile = "work"

			err := executeCommand(NewTodosCmd(), app, "copy", "456", "457", "--to-list", "901")
			require.Error(t, err)
			assert.Len(t, transport.posts, 2)
			require.NoError(t, app.Err(err))

			var envelope struct {
				Code string `json:"code"`
				Hint string `json:"hint"`
			}
			require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope), buf.String())
			assert.Equal(t, output.CodeAuth, envelope.Code)
			assert.Contains(t, envelope.Hint, tc.want)
			assert.NotRegexp(t, `basecamp auth login(\n|$)`, envelope.Hint, "the bare login hint is replaced")
			assert.Contains(t, envelope.Hint, "run the command again for #456, #457 alone")
		})
	}
}
