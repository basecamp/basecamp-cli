package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeRecordingTransport plays Basecamp as assigneeEchoTransport does and
// keeps every POST and PUT it was sent, so a test can read the request body.
type writeRecordingTransport struct {
	assigneeEchoTransport
	writes []recordedWrite
}

type recordedWrite struct {
	method string
	path   string
	body   map[string]any
}

func (m *writeRecordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost || req.Method == http.MethodPut {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, err
		}
		m.writes = append(m.writes, recordedWrite{req.Method, req.URL.Path, body})
		req.Body = io.NopCloser(bytes.NewReader(raw))
	}
	return m.assigneeEchoTransport.RoundTrip(req)
}

// Every command that assigns a to-do or card, where --notify asks Basecamp to
// tell the person.
var notifyingAssigneeWrites = []assigneeWrite{
	{"todos create", NewTodosCmd, []string{"create", "Thing", "--list", "555", "--assignee", "42"}},
	{"todos update", NewTodosCmd, []string{"update", "999", "--assignee", "42"}},
	{"cards create", NewCardsCmd, []string{"create", "Thing", "--column", "111", "--assignee", "42"}},
	{"cards update", NewCardsCmd, []string{"update", "999", "--assignee", "42"}},
	{"assign", NewAssignCmd, []string{"999", "--to", "42"}},
	{"assign --card", NewAssignCmd, []string{"999", "--card", "--to", "42"}},
}

func runRecordingWrite(t *testing.T, cmd *cobra.Command, args ...string) []recordedWrite {
	t.Helper()
	transport := &writeRecordingTransport{}
	app := setupCardsMockApp(t, transport)
	require.NoError(t, executeCommand(cmd, app, args...))
	require.Len(t, transport.writes, 1, "want exactly one write")
	return transport.writes
}

func TestAssigneeWritesSendNotifyOnlyWithTheFlag(t *testing.T) {
	for _, w := range notifyingAssigneeWrites {
		t.Run(w.name+" --notify", func(t *testing.T) {
			writes := runRecordingWrite(t, w.cmd(), append(w.args, "--notify")...)

			assert.Equal(t, true, writes[0].body["notify"])
			assert.Contains(t, writes[0].body["assignee_ids"], float64(42))
		})
		t.Run(w.name, func(t *testing.T) {
			writes := runRecordingWrite(t, w.cmd(), w.args...)

			assert.NotEqual(t, true, writes[0].body["notify"], "notify must not be sent as true without --notify")
		})
	}
}

// A card is created already assigned, in one request, rather than created and
// then assigned with a second.
func TestCardsCreateAssignsAndNotifiesInOneRequest(t *testing.T) {
	writes := runRecordingWrite(t, NewCardsCmd(), "create", "Thing", "--column", "111", "--assignee", "42", "--notify")

	assert.Equal(t, http.MethodPost, writes[0].method)
	assert.Equal(t, "/99999/card_tables/lists/111/cards.json", writes[0].path)
	assert.Equal(t, []any{float64(42)}, writes[0].body["assignee_ids"])
	assert.Equal(t, true, writes[0].body["notify"])
}

func TestCardsCreateWithoutAssigneeSendsNoAssignees(t *testing.T) {
	writes := runRecordingWrite(t, NewCardsCmd(), "create", "Thing", "--column", "111")

	_, sent := writes[0].body["assignee_ids"]
	assert.False(t, sent)
}

// --notify without an assignee change is accepted and sent, as todos update
// has always done: Basecamp notifies only people a request newly assigns, so
// it tells nobody.
func TestNotifyWithoutAssigneeIsAccepted(t *testing.T) {
	cases := []assigneeWrite{
		{"todos update", NewTodosCmd, []string{"update", "999", "--notify"}},
		{"cards create", NewCardsCmd, []string{"create", "Thing", "--column", "111", "--notify"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writes := runRecordingWrite(t, c.cmd(), c.args...)

			assert.Equal(t, true, writes[0].body["notify"])
			if c.name != "todos update" { // it writes back the assignees it read
				_, sent := writes[0].body["assignee_ids"]
				assert.False(t, sent)
			}
		})
	}
}

// Basecamp always notifies a person assigned to a card step, so assign --step
// accepts --notify and the step write needs no flag to carry it.
func TestAssignStepAcceptsNotify(t *testing.T) {
	writes := runRecordingWrite(t, NewAssignCmd(), "999", "--step", "--to", "42", "--notify")

	assert.Equal(t, http.MethodPut, writes[0].method)
	assert.Contains(t, writes[0].body["assignee_ids"], float64(42))
}

// A person already assigned is not assigned again, so nothing is written and
// nobody is notified.
func TestAssignNotifyLeavesAnExistingAssigneeAlone(t *testing.T) {
	transport := &writeRecordingTransport{}
	transport.existing = `{"id": 42, "name": "Annie Bryan"}`
	app := setupCardsMockApp(t, transport)

	require.NoError(t, executeCommand(NewAssignCmd(), app, "999", "--card", "--to", "42", "--notify"))
	assert.Empty(t, transport.writes)
}

// cards update --notify alone succeeds like todos update --notify, but reads
// the card: BC3 answers a card update with no card field with a 400.
func TestCardsUpdateNotifyAloneReadsTheCard(t *testing.T) {
	transport := &writeRecordingTransport{}
	app := setupCardsMockApp(t, transport)

	require.NoError(t, executeCommand(NewCardsCmd(), app, "update", "999", "--notify"))
	assert.Empty(t, transport.writes)
}

func TestCardsUpdateNotifyWithAnotherFieldSendsIt(t *testing.T) {
	writes := runRecordingWrite(t, NewCardsCmd(), "update", "999", "--title", "New", "--notify")

	assert.Equal(t, "New", writes[0].body["title"])
	assert.Equal(t, true, writes[0].body["notify"])
	_, sent := writes[0].body["assignee_ids"]
	assert.False(t, sent)
}
