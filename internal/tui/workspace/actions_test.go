package workspace

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRegistry(t *testing.T) {
	r := NewRegistry()
	assert.Empty(t, r.All())
}

func TestRegistry_Register(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{Name: ":test", Description: "test action"})

	all := r.All()
	require.Len(t, all, 1)
	assert.Equal(t, ":test", all[0].Name)
}

func TestRegistry_AllReturnsCopy(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{Name: ":a"})

	all := r.All()
	all[0].Name = "mutated"
	assert.Equal(t, ":a", r.All()[0].Name, "mutation should not affect registry")
}

func TestRegistry_SearchEmptyQuery(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{Name: ":a"})
	r.Register(Action{Name: ":b"})

	assert.Len(t, r.Search(""), 2, "empty query returns all")
}

func TestRegistry_SearchByName(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{Name: ":todos", Description: "Navigate to todos"})
	r.Register(Action{Name: ":projects", Description: "Navigate to projects"})

	results := r.Search("todo")
	require.Len(t, results, 1)
	assert.Equal(t, ":todos", results[0].Name)
}

func TestRegistry_SearchByAlias(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{Name: ":chat", Aliases: []string{"campfire", "fire"}})
	r.Register(Action{Name: ":todos"})

	results := r.Search("campfire")
	require.Len(t, results, 1)
	assert.Equal(t, ":chat", results[0].Name)
}

func TestRegistry_SearchByDescription(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{Name: ":hey", Description: "Open Hey! inbox"})
	r.Register(Action{Name: ":quit", Description: "Quit Basecamp"})

	results := r.Search("inbox")
	require.Len(t, results, 1)
	assert.Equal(t, ":hey", results[0].Name)
}

func TestRegistry_SearchCaseInsensitive(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{Name: ":Projects", Aliases: []string{"HOME"}})

	assert.Len(t, r.Search("project"), 1)
	assert.Len(t, r.Search("home"), 1)
	assert.Len(t, r.Search("HOME"), 1)
}

func TestRegistry_SearchNoMatch(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{Name: ":todos"})

	assert.Empty(t, r.Search("zzz"))
}

func TestRegistry_ForScope_Any(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{Name: ":a", Scope: ScopeAny})
	r.Register(Action{Name: ":b", Scope: ScopeProject})

	results := r.ForScope(Scope{})
	require.Len(t, results, 1)
	assert.Equal(t, ":a", results[0].Name)
}

func TestRegistry_ForScope_Account(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{Name: ":a", Scope: ScopeAny})
	r.Register(Action{Name: ":b", Scope: ScopeAccount})
	r.Register(Action{Name: ":c", Scope: ScopeProject})

	results := r.ForScope(Scope{AccountID: "123"})
	require.Len(t, results, 2)
	assert.Equal(t, ":a", results[0].Name)
	assert.Equal(t, ":b", results[1].Name)
}

func TestRegistry_ForScope_Project(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{Name: ":a", Scope: ScopeAny})
	r.Register(Action{Name: ":b", Scope: ScopeAccount})
	r.Register(Action{Name: ":c", Scope: ScopeProject})

	results := r.ForScope(Scope{AccountID: "123", ProjectID: 42})
	assert.Len(t, results, 3)
}

func TestDefaultActions(t *testing.T) {
	r := DefaultActions()
	all := r.All()

	// Verify expected count
	assert.GreaterOrEqual(t, len(all), 10)

	// Verify all have names and descriptions
	for _, a := range all {
		assert.NotEmpty(t, a.Name, "action should have a name")
		assert.NotEmpty(t, a.Description, "action %s should have a description", a.Name)
		assert.NotEmpty(t, a.Category, "action %s should have a category", a.Name)
		assert.NotNil(t, a.Execute, "action %s should have an Execute func", a.Name)
	}
}

func TestDefaultActions_QuitReturnsQuit(t *testing.T) {
	r := DefaultActions()
	results := r.Search("quit")
	require.Len(t, results, 1)

	cmd := results[0].Execute(nil)
	msg := cmd()
	_, isQuit := msg.(tea.QuitMsg)
	assert.True(t, isQuit, "quit action should produce tea.QuitMsg")
}

func TestDefaultActions_ProjectScopeActions(t *testing.T) {
	r := DefaultActions()

	// Without a project, these should be filtered out
	noProject := r.ForScope(Scope{AccountID: "1"})
	for _, a := range noProject {
		assert.NotEqual(t, ScopeProject, a.Scope,
			"action %s requires project but was returned for account-only scope", a.Name)
	}

	// With a project, they should appear
	withProject := r.ForScope(Scope{AccountID: "1", ProjectID: 42})
	names := make(map[string]bool)
	for _, a := range withProject {
		names[a.Name] = true
	}
	assert.True(t, names[":todos"])
	assert.True(t, names[":chat"])
	assert.True(t, names[":messages"])
	assert.True(t, names[":cards"])
	assert.True(t, names[":schedule"])
	assert.True(t, names[":timeline"])
	assert.True(t, names[":checkins"])
	assert.True(t, names[":docs"])
	assert.True(t, names[":forwards"])
	assert.True(t, names[":compose"])
}

func TestDefaultActions_NewNavigationActions(t *testing.T) {
	r := DefaultActions()

	expected := []struct {
		name     string
		category string
	}{
		{":schedule", "project"},
		{":timeline", "project"},
		{":checkins", "project"},
		{":docs", "project"},
		{":forwards", "project"},
		{":compose", "mutation"},
	}

	all := r.All()
	nameMap := make(map[string]Action, len(all))
	for _, a := range all {
		nameMap[a.Name] = a
	}

	for _, exp := range expected {
		a, ok := nameMap[exp.name]
		require.True(t, ok, "action %s should exist in DefaultActions", exp.name)
		assert.Equal(t, exp.category, a.Category, "action %s category", exp.name)
		assert.Equal(t, ScopeProject, a.Scope, "action %s should require ScopeProject", exp.name)
		assert.NotEmpty(t, a.Description, "action %s should have a description", exp.name)
		assert.NotEmpty(t, a.Aliases, "action %s should have aliases", exp.name)
		assert.NotNil(t, a.Execute, "action %s should have Execute", exp.name)
	}
}

// -- :complete action tests --

func TestAction_Complete_AvailableForTodo(t *testing.T) {
	r := DefaultActions()
	scope := Scope{
		AccountID:     "1",
		ProjectID:     42,
		RecordingID:   100,
		RecordingType: "Todo",
	}
	names := actionNames(r.ForScope(scope))
	assert.True(t, names[":complete"], ":complete should be available for a Todo recording")
}

func TestAction_Complete_UnavailableForMessage(t *testing.T) {
	r := DefaultActions()
	scope := Scope{
		AccountID:     "1",
		ProjectID:     42,
		RecordingID:   100,
		RecordingType: "Message",
	}
	names := actionNames(r.ForScope(scope))
	assert.False(t, names[":complete"], ":complete should not be available for a Message")
}

func TestAction_Complete_UnavailableWithoutRecording(t *testing.T) {
	r := DefaultActions()
	scope := Scope{AccountID: "1", ProjectID: 42}
	names := actionNames(r.ForScope(scope))
	assert.False(t, names[":complete"], ":complete should not be available without RecordingID")
}

func TestAction_Complete_UnavailableWithoutProject(t *testing.T) {
	r := DefaultActions()
	scope := Scope{AccountID: "1", RecordingID: 100, RecordingType: "Todo"}
	names := actionNames(r.ForScope(scope))
	assert.False(t, names[":complete"], ":complete should not be available without ProjectID")
}

// -- :trash action tests --

func TestAction_Trash_AvailableForAnyRecording(t *testing.T) {
	r := DefaultActions()
	scope := Scope{
		AccountID:     "1",
		ProjectID:     42,
		RecordingID:   100,
		RecordingType: "Message",
	}
	names := actionNames(r.ForScope(scope))
	assert.True(t, names[":trash"], ":trash should be available for any recording")
}

func TestAction_Trash_UnavailableWithoutRecording(t *testing.T) {
	r := DefaultActions()
	scope := Scope{AccountID: "1", ProjectID: 42}
	names := actionNames(r.ForScope(scope))
	assert.False(t, names[":trash"], ":trash should not be available without RecordingID")
}

func TestAction_Available_RefinesScope_DoesNotBypass(t *testing.T) {
	r := NewRegistry()
	r.Register(Action{
		Name:  ":test",
		Scope: ScopeProject,
		Available: func(s Scope) bool {
			return true // always says yes
		},
		Execute: func(_ *Session) tea.Cmd { return nil },
	})

	// ProjectID=0 means scope check fails even though Available returns true
	results := r.ForScope(Scope{AccountID: "1"})
	assert.Empty(t, results, "Available should not bypass scope check")
}

func TestCompleteCmd_Success(t *testing.T) {
	cmd := completeCmd(func() error { return nil })
	msg := cmd()
	status, ok := msg.(StatusMsg)
	require.True(t, ok, "success should produce StatusMsg")
	assert.Equal(t, "Completed", status.Text)
}

func TestCompleteCmd_Error(t *testing.T) {
	cmd := completeCmd(func() error { return fmt.Errorf("boom") })
	msg := cmd()
	errMsg, ok := msg.(ErrorMsg)
	require.True(t, ok, "error should produce ErrorMsg")
	assert.Equal(t, "completing todo", errMsg.Context)
}

func TestTrashCmd_Success_ProducesStatusAndNavigateBack(t *testing.T) {
	cmd := trashCmd(func() error { return nil })
	msg := cmd()

	batch, ok := msg.(tea.BatchMsg)
	require.True(t, ok, "success should produce BatchMsg")
	require.Len(t, batch, 2)

	msg0 := batch[0]()
	status, ok := msg0.(StatusMsg)
	require.True(t, ok, "first msg should be StatusMsg")
	assert.Equal(t, "Trashed", status.Text)

	msg1 := batch[1]()
	_, ok = msg1.(NavigateBackMsg)
	assert.True(t, ok, "second msg should be NavigateBackMsg")
}

func TestTrashCmd_Error(t *testing.T) {
	cmd := trashCmd(func() error { return fmt.Errorf("boom") })
	msg := cmd()
	errMsg, ok := msg.(ErrorMsg)
	require.True(t, ok, "error should produce ErrorMsg")
	assert.Equal(t, "trashing recording", errMsg.Context)
}

// pingLineJSON is a line in a Ping as bc3's recording JSON has it: its parent
// is the Ping's Campfire, and its bucket is the Ping, of type Circle.
const pingLineJSON = `{"id":111,"status":"active","type":"Chat::Lines::Text","title":"Lunch?",` +
	`"parent":{"id":222,"title":"Ping","type":"Chat::Transcript"},` +
	`"bucket":{"id":333,"name":"Jason Fried","type":"Circle"}}`

// The palette's :trash never sends a Campfire line to the generic trash route,
// which bc3 refuses for anything in a Ping (bc3#14171) and which in a project
// would skip the line's own delete checks. Nor does it delete the line, which
// can't be undone and which the palette would do without asking: it says to
// press t twice in the line's view, which asks first. The scope's type is only
// a label, so the line is read to learn what it is: a deep link to a Ping line
// labels it Campfire::Line, and a quick jump not at all.
func TestAction_Trash_RefusesACampfireLine(t *testing.T) {
	for _, label := range []string{"Chat::Lines::Text", "Campfire::Line", ""} {
		t.Run("label "+label, func(t *testing.T) {
			transport := &actionTransport{routes: map[string]actionReply{
				"GET /99999/buckets/333/recordings/111.json": {http.StatusOK, pingLineJSON},
			}}
			session := actionSession(transport, Scope{AccountID: "99999", ProjectID: 333, RecordingID: 111, RecordingType: label})

			msg := trashAction(t).Execute(session)()

			assert.Empty(t, transport.writes())
			errMsg, ok := msg.(ErrorMsg)
			require.True(t, ok, "expected an ErrorMsg, got %T", msg)
			assert.ErrorIs(t, errMsg.Err, errCampfireLineTrash)
		})
	}
}

// When the recording can't be read, :trash writes nothing rather than guess.
func TestAction_Trash_WritesNothingWhenItCantTell(t *testing.T) {
	transport := &actionTransport{routes: map[string]actionReply{}}
	session := actionSession(transport, Scope{AccountID: "99999", ProjectID: 333, RecordingID: 111, RecordingType: "Message"})

	msg := trashAction(t).Execute(session)()

	assert.Empty(t, transport.writes())
	_, ok := msg.(ErrorMsg)
	assert.True(t, ok, "expected an ErrorMsg, got %T", msg)
}

// Anything else is trashed as before, a card included: its type is read
// through its bucket, since bc3's bucket-less /recordings/<id>.json answers
// 404 for one.
func TestAction_Trash_OtherRecordingTrashes(t *testing.T) {
	transport := &actionTransport{routes: map[string]actionReply{
		"GET /99999/buckets/333/recordings/100.json":    {http.StatusOK, `{"id":100,"type":"Kanban::Card","title":"Ship it"}`},
		"PUT /99999/recordings/100/status/trashed.json": {http.StatusNoContent, ""},
	}}
	session := actionSession(transport, Scope{AccountID: "99999", ProjectID: 333, RecordingID: 100, RecordingType: "Card"})

	msg := trashAction(t).Execute(session)()

	assert.Equal(t, []string{"PUT /99999/recordings/100/status/trashed.json"}, transport.writes())
	assert.Equal(t, "Trashed", firstStatus(t, msg))
}

// -- helpers --

type actionReply struct {
	status int
	body   string
}

// actionTransport answers from a table keyed by "METHOD path" and records
// every request; anything without a route answers 404.
type actionTransport struct {
	mu       sync.Mutex
	routes   map[string]actionReply
	requests []string
}

func (at *actionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	call := req.Method + " " + req.URL.Path
	at.mu.Lock()
	at.requests = append(at.requests, call)
	at.mu.Unlock()
	reply, ok := at.routes[call]
	if !ok {
		reply = actionReply{http.StatusNotFound, `{"error":"Not Found"}`}
	}
	return &http.Response{
		StatusCode: reply.status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(reply.body)),
		Request:    req,
	}, nil
}

// writes returns the requests that change something, in order.
func (at *actionTransport) writes() []string {
	at.mu.Lock()
	defer at.mu.Unlock()
	var writes []string
	for _, call := range at.requests {
		if !strings.HasPrefix(call, http.MethodGet+" ") {
			writes = append(writes, call)
		}
	}
	return writes
}

type actionTestToken struct{}

func (actionTestToken) AccessToken(context.Context) (string, error) { return "test-token", nil }

func actionSession(transport http.RoundTripper, scope Scope) *Session {
	sdk := basecamp.NewClient(&basecamp.Config{BaseURL: "https://3.basecampapi.com"}, actionTestToken{},
		basecamp.WithTransport(transport), basecamp.WithMaxRetries(1))
	return NewTestSessionWithClient(sdk, scope)
}

func trashAction(t *testing.T) Action {
	t.Helper()
	for _, a := range DefaultActions().All() {
		if a.Name == ":trash" {
			return a
		}
	}
	require.FailNow(t, "no :trash action")
	return Action{}
}

// firstStatus returns the text of the StatusMsg a result batch starts with,
// failing on an ErrorMsg.
func firstStatus(t *testing.T, msg tea.Msg) string {
	t.Helper()
	if errMsg, ok := msg.(ErrorMsg); ok {
		require.FailNow(t, "unexpected error", "%s: %v", errMsg.Context, errMsg.Err)
	}
	batch, ok := msg.(tea.BatchMsg)
	require.True(t, ok, "expected a BatchMsg, got %T", msg)
	status, ok := batch[0]().(StatusMsg)
	require.True(t, ok)
	return status.Text
}

func actionNames(actions []Action) map[string]bool {
	m := make(map[string]bool, len(actions))
	for _, a := range actions {
		m[a.Name] = true
	}
	return m
}
