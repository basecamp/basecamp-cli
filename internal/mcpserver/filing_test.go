package mcpserver

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/basecamp/mcp/mcptest"
)

const (
	fakeSourceID  = 101
	fakeNewID     = 202
	fakeProjectID = 77
	fakeFilingID  = 9
	fakeListID    = 600
)

// fakeBasecamp stands in for Basecamp behind move and copy: one recording of
// a given type and parent, a destination project whose dock the test sets,
// and a filing that runs through the statuses the test lists.
type fakeBasecamp struct {
	t          *testing.T
	recordType string
	parentType string
	dock       []basecamp.DockItem
	lists      []basecamp.Todolist
	// recurring answers each read with an occurrence of the event read, as
	// Basecamp does for a recurring event.
	recurring bool
	// movedTo answers a read of the source with the recording at this id,
	// as Basecamp's redirect from a moved recording's old id does.
	movedTo int
	// unreadable answers reads of these paths with these statuses.
	unreadable map[string]int
	// listsIn overrides lists for the To-dos tool with that id.
	listsIn map[string][]basecamp.Todolist
	// start answers the POST that starts the filing; nil starts it.
	start http.HandlerFunc
	// statuses are what GetFiling reports, one per poll, the last repeated.
	statuses []basecamp.FilingStatus

	mu      sync.Mutex
	started map[string]any
	starts  int
	polls   int
}

var trailingID = regexp.MustCompile(`/(\d+)(\.json)?$`)

func (f *fakeBasecamp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.Path
	if status, ok := f.unreadable[strings.TrimSuffix(path, ".json")]; ok {
		f.json(w, status, map[string]any{"error": "no"})
		return
	}
	switch {
	case r.Method == http.MethodPost && (strings.HasSuffix(path, "/moves.json") || strings.HasSuffix(path, "/copies.json")):
		f.starts++
		body, _ := io.ReadAll(r.Body)
		f.started = map[string]any{}
		require.NoError(f.t, json.Unmarshal(body, &f.started))
		f.started["path"] = path
		if f.start != nil {
			f.start(w, r)
			return
		}
		f.json(w, http.StatusCreated, f.filing(basecamp.FilingPending))
	case strings.HasPrefix(path, "/999/filings/"):
		status := f.statuses[min(f.polls, len(f.statuses)-1)]
		f.polls++
		f.json(w, http.StatusOK, f.filing(status))
	case strings.TrimSuffix(path, ".json") == fmt.Sprintf("/999/projects/%d", fakeProjectID):
		f.json(w, http.StatusOK, map[string]any{"id": fakeProjectID, "name": "Dest", "dock": f.dock})
	case strings.HasSuffix(path, "/todolists.json"):
		todoset := strings.Split(path, "/")[3]
		if lists, ok := f.listsIn[todoset]; ok {
			f.json(w, http.StatusOK, lists)
			return
		}
		f.json(w, http.StatusOK, f.lists)
	case r.Method == http.MethodGet && trailingID.MatchString(path):
		id, _ := strconv.Atoi(trailingID.FindStringSubmatch(path)[1])
		if id != fakeSourceID && id != fakeNewID {
			f.t.Errorf("unexpected read: %s", path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if id == fakeSourceID && f.movedTo != 0 {
			id = f.movedTo
		}
		if f.recurring {
			f.json(w, http.StatusOK, map[string]any{
				"id":     id + 1,
				"type":   "Schedule::Entry::Occurrence",
				"parent": map[string]any{"id": id, "type": "Schedule::Entry", "title": "Weekly"},
				"bucket": map[string]any{"id": fakeProjectID, "name": "Dest", "type": "Project"},
			})
			return
		}
		f.json(w, http.StatusOK, map[string]any{
			"id":     id,
			"type":   f.recordType,
			"title":  "Thing",
			"parent": map[string]any{"id": 55, "type": f.parentType, "title": "Old home"},
			"bucket": map[string]any{"id": fakeProjectID, "name": "Dest", "type": "Project"},
		})
	default:
		f.t.Errorf("unexpected upstream request: %s %s", r.Method, path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeBasecamp) filing(status basecamp.FilingStatus) map[string]any {
	filing := map[string]any{"id": fakeFilingID, "status": status, "type": "Move", "source_recording_id": fakeSourceID, "url": "x"}
	if status == basecamp.FilingCompleted {
		filing["destination_recording"] = map[string]any{"id": fakeNewID, "type": f.recordType, "url": "x", "app_url": "https://3.basecamp.com/999/x/202"}
	}
	return filing
}

func (f *fakeBasecamp) json(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	require.NoError(f.t, json.NewEncoder(w).Encode(body))
}

// dockFor is a destination project's dock with one enabled tool of every
// kind, each with its own id.
func dockFor() []basecamp.DockItem {
	names := []basecamp.FilingContainer{
		basecamp.FilingIntoCardTable, basecamp.FilingIntoMessageBoard, basecamp.FilingIntoVault, basecamp.FilingIntoSchedule,
		basecamp.FilingIntoQuestionnaire, basecamp.FilingIntoInbox, basecamp.FilingIntoTodoset,
	}
	dock := make([]basecamp.DockItem, len(names))
	for i, name := range names {
		dock[i] = basecamp.DockItem{ID: int64(500 + i), Name: string(name), Title: string(name), Enabled: true}
	}
	return dock
}

func newFake(t *testing.T, recordType, parentType string) *fakeBasecamp {
	return &fakeBasecamp{
		t:          t,
		recordType: recordType,
		parentType: parentType,
		dock:       dockFor(),
		lists:      []basecamp.Todolist{{ID: fakeListID, Title: "Only list"}},
		statuses:   []basecamp.FilingStatus{basecamp.FilingProcessing, basecamp.FilingCompleted},
	}
}

func filingSession(t *testing.T, fake *fakeBasecamp, cfg Config) *mcp.ClientSession {
	t.Helper()
	upstream := httptest.NewServer(fake)
	t.Cleanup(upstream.Close)
	srv, err := New(newTestAPI(upstream), cfg)
	require.NoError(t, err)
	return mcptest.Connect(t, srv.BuildMCPServer(slog.New(slog.DiscardHandler)))
}

func file(t *testing.T, session *mcp.ClientSession, action string, params map[string]any) (map[string]any, bool) {
	t.Helper()
	text, isError := mcptest.CallText(t, session, "basecamp_recordings", map[string]any{"action": action, "params": params})
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		return map[string]any{"text": text}, isError
	}
	return decoded, isError
}

func failureOf(t *testing.T, answer map[string]any) map[string]any {
	t.Helper()
	failure, ok := answer["error"].(map[string]any)
	require.True(t, ok, "not a filing failure: %v", answer)
	return failure
}

func dockID(name basecamp.FilingContainer) float64 {
	for _, tool := range dockFor() {
		if tool.Name == string(name) {
			return float64(tool.ID)
		}
	}
	return 0
}

// recordableTypes is every recording type a candidate for filing could be,
// as Basecamp spells it in JSON: bc3's recordable models (app/models, each
// including Recordable), the SDK's RecordingType constants, and every
// type-shaped string in the vendored model. FilingContainerFor is asked
// about each of them, with each as a parent, so a type it starts accepting
// shows up here without anyone listing it.
func recordableTypes(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	for _, typ := range []string{
		"Attachment", "AutolinkedImage", "Chat::Lines::Code", "Chat::Lines::RichText", "Chat::Lines::Text",
		"Chat::Lines::Upload", "Chat::Transcript", "Client::Approval", "Client::Approval::Response", "Client::Board",
		"Client::Correspondence", "Client::Forward", "Client::Introduction", "Client::Reply", "CloudFile", "Comment",
		"Dock", "Document", "Door", "Embed", "Gallery", "Gauge", "Gauge::Needle", "GoogleDocument", "Hill::Dot",
		"Hill::Version", "Inbox", "Inbox::Forward", "Inbox::Reply", "Journal", "Journal::Entry", "Kanban::Board",
		"Kanban::Boardset", "Kanban::Card", "Kanban::Column", "Kanban::DoneColumn", "Kanban::NotNowColumn",
		"Kanban::OnHold", "Kanban::Step", "Kanban::Triage", "Kanban::Wormhole", "Message", "Message::Board",
		"Notebook::Note", "Printout", "Question", "Question::Answer", "Questionnaire", "Relay", "Schedule",
		"Schedule::Entry", "Schedule::Entry::Occurrence", "Subtask", "Timeline", "Timesheet", "Timesheet::Entry",
		"Todo", "Todolist", "Todoset", "Upload", "Vault",
		string(basecamp.RecordingTypeComment), string(basecamp.RecordingTypeDocument), string(basecamp.RecordingTypeDoor),
		string(basecamp.RecordingTypeKanbanCard), string(basecamp.RecordingTypeKanbanStep), string(basecamp.RecordingTypeMessage),
		string(basecamp.RecordingTypeQuestionAnswer), string(basecamp.RecordingTypeScheduleEntry), string(basecamp.RecordingTypeTodo),
		string(basecamp.RecordingTypeTodolist), string(basecamp.RecordingTypeUpload), string(basecamp.RecordingTypeVault),
	} {
		seen[typ] = true
	}
	model, err := os.ReadFile("model/openapi.json")
	require.NoError(t, err)
	for _, match := range regexp.MustCompile(`"([A-Z][A-Za-z]+(?:::[A-Z][A-Za-z]+)*)"`).FindAllSubmatch(model, -1) {
		seen[string(match[1])] = true
	}
	types := make([]string, 0, len(seen))
	for typ := range seen {
		types = append(types, typ)
	}
	slices.Sort(types)
	return types
}

// TestFilingServesEveryTypeTheSDKFiles is the parity check: every recording
// type basecamp.FilingContainerFor accepts, under every parent it accepts it
// with, moves and copies through basecamp_recordings into a project's
// container of the kind the SDK names, and answers with the new recording.
// A type the SDK starts filing that this server cannot read, or a container
// kind it cannot resolve from a project, fails here.
func TestFilingServesEveryTypeTheSDKFiles(t *testing.T) {
	type route struct {
		typ       string
		container basecamp.FilingContainer
	}
	types := recordableTypes(t)
	routes := map[route]string{}
	for _, typ := range types {
		for _, parent := range append([]string{""}, types...) {
			if container, ok := basecamp.FilingContainerFor(typ, parent); ok {
				if _, seen := routes[route{typ, container}]; !seen {
					routes[route{typ, container}] = parent
				}
			}
		}
	}
	require.NotEmpty(t, routes)
	t.Logf("%d routes from %d candidate types", len(routes), len(types))

	served := map[string]bool{}
	for r, parent := range routes {
		served[r.typ] = true
		for _, action := range []string{moveAction, copyAction} {
			t.Run(fmt.Sprintf("%s %s under %s", action, r.typ, parent), func(t *testing.T) {
				t.Parallel()
				fake := newFake(t, r.typ, parent)
				fake.statuses = []basecamp.FilingStatus{basecamp.FilingCompleted}
				session := filingSession(t, fake, Config{})

				answer, isError := file(t, session, action, map[string]any{
					"recording_id":   fakeSourceID,
					"recording_type": r.typ,
					"project_id":     fakeProjectID,
				})
				require.False(t, isError, "%v", answer)
				assert.Equal(t, float64(fakeNewID), answer["id"])
				assert.Equal(t, float64(fakeSourceID), answer["source_id"])
				assert.Equal(t, r.typ, answer["type"])

				want := dockID(r.container)
				if r.container == basecamp.FilingIntoTodolist {
					want = fakeListID
				}
				assert.Equal(t, want, fake.started["filing_destination_id"], "filed into the project's %s", r.container)
				assert.Equal(t, 1, fake.starts, "started once")
			})
		}
	}
	for typ := range filingReads {
		assert.True(t, served[typ], "recording_type advertises %s, which FilingContainerFor never accepts", typ)
	}
}

func TestFilingIntoANamedContainerSkipsTheProject(t *testing.T) {
	fake := newFake(t, "Document", "Vault")
	session := filingSession(t, fake, Config{})

	answer, isError := file(t, session, copyAction, map[string]any{
		"recording_id":     fakeSourceID,
		"recording_type":   "Document",
		"container_id":     "4100",
		"folder_id":        4200,
		"include_comments": true,
	})
	require.False(t, isError, "%v", answer)
	assert.Equal(t, float64(4100), fake.started["filing_destination_id"])
	assert.Equal(t, float64(4200), fake.started["parent_id"])
	assert.Equal(t, true, fake.started["include_comments"])
	assert.Contains(t, fake.started["path"], "/copies.json")
}

func TestFilingRefusesADestinationItCannotPick(t *testing.T) {
	t.Run("turned off", func(t *testing.T) {
		fake := newFake(t, "Message", "Message::Board")
		fake.dock = []basecamp.DockItem{{ID: 900, Name: "message_board", Title: "Message Board", Enabled: false}}
		failure := failureOf(t, mustFail(t, filingSession(t, fake, Config{}), moveAction, map[string]any{
			"recording_id": fakeSourceID, "recording_type": "Message", "project_id": fakeProjectID,
		}))
		assert.Equal(t, "destination_disabled", failure["type"])
		assert.Equal(t, float64(900), failure["tool_id"])
		assert.Equal(t, true, failure["retry_safe"])
		assert.Contains(t, failure["message"], "enable_tool")
		assert.Zero(t, fake.starts)
	})
	t.Run("several", func(t *testing.T) {
		fake := newFake(t, "Kanban::Card", "Kanban::Triage")
		fake.dock = []basecamp.DockItem{
			{ID: 901, Name: "kanban_board", Title: "Bugs", Enabled: true},
			{ID: 902, Name: "kanban_board", Title: "Ideas", Enabled: true},
		}
		failure := failureOf(t, mustFail(t, filingSession(t, fake, Config{}), copyAction, map[string]any{
			"recording_id": fakeSourceID, "recording_type": "Kanban::Card", "project_id": fakeProjectID,
		}))
		assert.Equal(t, "destination_ambiguous", failure["type"])
		assert.Equal(t, []any{
			map[string]any{"id": float64(901), "title": "Bugs"},
			map[string]any{"id": float64(902), "title": "Ideas"},
		}, failure["candidates"])
		assert.Zero(t, fake.starts)
	})
	t.Run("several to-do lists", func(t *testing.T) {
		fake := newFake(t, "Todo", "Todolist")
		fake.lists = []basecamp.Todolist{{ID: 601, Title: "A"}, {ID: 602, Title: "B"}}
		failure := failureOf(t, mustFail(t, filingSession(t, fake, Config{}), moveAction, map[string]any{
			"recording_id": fakeSourceID, "recording_type": "Todo", "project_id": fakeProjectID,
		}))
		assert.Equal(t, "destination_ambiguous", failure["type"])
		assert.Len(t, failure["candidates"], 2)
		assert.Zero(t, fake.starts)
	})
	t.Run("to-do lists across several To-dos tools", func(t *testing.T) {
		fake := newFake(t, "Todo", "Todolist")
		fake.dock = append(fake.dock, basecamp.DockItem{ID: 599, Name: "todoset", Title: "Second", Enabled: true})
		fake.listsIn = map[string][]basecamp.Todolist{"599": {{ID: 601, Title: "Second list"}}}
		failure := failureOf(t, mustFail(t, filingSession(t, fake, Config{}), copyAction, map[string]any{
			"recording_id": fakeSourceID, "recording_type": "Todo", "project_id": fakeProjectID,
		}))
		assert.Equal(t, "destination_ambiguous", failure["type"])
		assert.Equal(t, []any{
			map[string]any{"id": float64(fakeListID), "title": "Only list"},
			map[string]any{"id": float64(601), "title": "Second list"},
		}, failure["candidates"], "the candidates are lists, which container_id takes, not To-dos tools")
		assert.Zero(t, fake.starts)
	})
	t.Run("none", func(t *testing.T) {
		fake := newFake(t, "Inbox::Forward", "Inbox")
		fake.dock = nil
		failure := failureOf(t, mustFail(t, filingSession(t, fake, Config{}), moveAction, map[string]any{
			"recording_id": fakeSourceID, "recording_type": "Inbox::Forward", "project_id": fakeProjectID,
		}))
		assert.Equal(t, "destination_missing", failure["type"])
		assert.Zero(t, fake.starts)
	})
}

func mustFail(t *testing.T, session *mcp.ClientSession, action string, params map[string]any) map[string]any {
	t.Helper()
	answer, isError := file(t, session, action, params)
	require.True(t, isError, "%v", answer)
	return answer
}

func TestFilingRefusesWhatItCannotFile(t *testing.T) {
	cases := map[string]struct {
		parentType string
		params     map[string]any
		want       string
	}{
		"a whole Docs & Files tool":     {"Project", map[string]any{"recording_type": "Vault", "project_id": fakeProjectID}, "not filed on its own"},
		"a folder outside Docs & Files": {"Message::Board", map[string]any{"recording_type": "Message", "container_id": 5, "folder_id": 6}, "folder_id applies only"},
		"both destinations":             {"Message::Board", map[string]any{"recording_type": "Message", "container_id": 5, "project_id": fakeProjectID}, "exactly one"},
		"no destination":                {"Message::Board", map[string]any{"recording_type": "Message"}, "exactly one"},
		"a type that is not filed":      {"Message", map[string]any{"recording_type": "Comment", "container_id": 5}, "recording_type"},
		"comments on a move":            {"Message::Board", map[string]any{"recording_type": "Message", "container_id": 5, "include_comments": true}, "unknown parameter"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			typ, _ := c.params["recording_type"].(string)
			fake := newFake(t, typ, c.parentType)
			params := map[string]any{"recording_id": fakeSourceID}
			for k, v := range c.params {
				params[k] = v
			}
			answer := mustFail(t, filingSession(t, fake, Config{}), moveAction, params)
			assert.Contains(t, fmt.Sprint(answer), c.want)
			assert.Zero(t, fake.starts, "nothing filed")
		})
	}
}

// TestFilingSaysWhetherTryingAgainIsSafe pins how each way a filing can end
// short of a new recording reads to the caller.
func TestFilingSaysWhetherTryingAgainIsSafe(t *testing.T) {
	defer func(saved basecamp.FilingWaitOptions) { filingWait = saved }(filingWait)
	filingWait.Timeout = 300 * time.Millisecond

	params := map[string]any{"recording_id": fakeSourceID, "recording_type": "Message", "container_id": 5}

	t.Run("refused", func(t *testing.T) {
		fake := newFake(t, "Message", "Message::Board")
		fake.start = func(w http.ResponseWriter, _ *http.Request) {
			fake.json(w, http.StatusUnprocessableEntity, map[string]any{"error": "nope"})
		}
		failure := failureOf(t, mustFail(t, filingSession(t, fake, Config{}), copyAction, params))
		assert.Equal(t, "filing_refused", failure["type"])
		assert.Equal(t, true, failure["retry_safe"])
	})
	t.Run("refused as not found", func(t *testing.T) {
		fake := newFake(t, "Message", "Message::Board")
		fake.start = func(w http.ResponseWriter, _ *http.Request) {
			fake.json(w, http.StatusNotFound, map[string]any{"error": "not found"})
		}
		failure := failureOf(t, mustFail(t, filingSession(t, fake, Config{}), moveAction, params))
		assert.Equal(t, "filing_refused", failure["type"])
		assert.Contains(t, failure["message"], "not a message board this recording can go into, or is the one it is in now")
	})
	t.Run("copy may have started", func(t *testing.T) {
		fake := newFake(t, "Message", "Message::Board")
		fake.start = func(w http.ResponseWriter, _ *http.Request) {
			fake.json(w, http.StatusInternalServerError, map[string]any{"error": "boom"})
		}
		failure := failureOf(t, mustFail(t, filingSession(t, fake, Config{}), copyAction, params))
		assert.Equal(t, "filing_may_have_started", failure["type"])
		assert.Equal(t, false, failure["retry_safe"])
		assert.Equal(t, 1, fake.starts, "a copy is never sent twice")
	})
	t.Run("failed", func(t *testing.T) {
		fake := newFake(t, "Message", "Message::Board")
		fake.statuses = []basecamp.FilingStatus{basecamp.FilingFailed}
		failure := failureOf(t, mustFail(t, filingSession(t, fake, Config{}), moveAction, params))
		assert.Equal(t, "filing_failed", failure["type"])
		assert.Equal(t, float64(fakeFilingID), failure["filing_id"])
		assert.Equal(t, false, failure["retry_safe"])
	})
	t.Run("unsettled", func(t *testing.T) {
		fake := newFake(t, "Message", "Message::Board")
		fake.statuses = []basecamp.FilingStatus{basecamp.FilingProcessing}
		failure := failureOf(t, mustFail(t, filingSession(t, fake, Config{}), copyAction, params))
		assert.Equal(t, "filing_unsettled", failure["type"])
		assert.Equal(t, float64(fakeFilingID), failure["filing_id"])
		assert.Equal(t, "processing", failure["filing_status"])
		assert.Equal(t, false, failure["retry_safe"])
		assert.Contains(t, failure["message"], "get_filing")
	})
}

func TestFilingIsAWriteTheReadOnlyServerDrops(t *testing.T) {
	fake := newFake(t, "Message", "Message::Board")
	session := filingSession(t, fake, Config{ReadOnly: true})

	answer := mustFail(t, session, moveAction, map[string]any{"recording_id": fakeSourceID, "recording_type": "Message", "container_id": 5})
	assert.NotContains(t, fmt.Sprint(answer), "source_id")
	assert.Zero(t, fake.starts)

	text, isError := mcptest.CallText(t, session, "basecamp_recordings", map[string]any{"action": "describe", "params": map[string]any{"action": "get_filing"}})
	assert.False(t, isError, "get_filing stays served read-only: %s", text)
}

func TestFilingFilesARecurringEventAsItsSeries(t *testing.T) {
	fake := newFake(t, "Schedule::Entry", "Schedule")
	fake.recurring = true
	session := filingSession(t, fake, Config{})

	answer, isError := file(t, session, moveAction, map[string]any{
		"recording_id": fakeSourceID, "recording_type": "Schedule::Entry", "project_id": fakeProjectID,
	})
	require.False(t, isError, "%v", answer)
	assert.Equal(t, fmt.Sprintf("/999/recordings/%d/moves.json", fakeSourceID), fake.started["path"], "the series is what moves")
	assert.Equal(t, float64(fakeNewID), answer["id"])
	assert.Equal(t, float64(fakeNewID+1), answer["occurrence_id"])
	assert.Equal(t, "Schedule::Entry", answer["type"])
	assert.Equal(t, float64(fakeSourceID), answer["source_id"])
	assert.NotContains(t, answer, "parent")
}

func TestFilingTypesAFailedReadBeforeFiling(t *testing.T) {
	t.Run("the recording", func(t *testing.T) {
		fake := newFake(t, "Message", "Message::Board")
		fake.unreadable = map[string]int{fmt.Sprintf("/999/messages/%d", fakeSourceID): http.StatusNotFound}
		failure := failureOf(t, mustFail(t, filingSession(t, fake, Config{}), moveAction, map[string]any{
			"recording_id": fakeSourceID, "recording_type": "Message", "project_id": fakeProjectID,
		}))
		assert.Equal(t, "recording_unreadable", failure["type"])
		assert.Equal(t, true, failure["retry_safe"])
		assert.Zero(t, fake.starts)
	})
	t.Run("the destination project", func(t *testing.T) {
		fake := newFake(t, "Message", "Message::Board")
		fake.unreadable = map[string]int{fmt.Sprintf("/999/projects/%d", fakeProjectID): http.StatusForbidden}
		failure := failureOf(t, mustFail(t, filingSession(t, fake, Config{}), copyAction, map[string]any{
			"recording_id": fakeSourceID, "recording_type": "Message", "project_id": fakeProjectID,
		}))
		assert.Equal(t, "destination_unreadable", failure["type"])
		assert.Equal(t, true, failure["retry_safe"])
		assert.Zero(t, fake.starts)
	})
}

func TestFilingRefusesAnIDThatNowRedirects(t *testing.T) {
	fake := newFake(t, "Document", "Vault")
	fake.movedTo = fakeNewID
	failure := failureOf(t, mustFail(t, filingSession(t, fake, Config{}), moveAction, map[string]any{
		"recording_id": fakeSourceID, "recording_type": "Document", "container_id": 5,
	}))
	assert.Equal(t, "recording_moved", failure["type"])
	assert.Equal(t, float64(fakeNewID), failure["recording_id"])
	assert.Equal(t, true, failure["retry_safe"])
	assert.Zero(t, fake.starts, "a retried move never files the recording again")
}
