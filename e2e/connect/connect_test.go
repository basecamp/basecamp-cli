//go:build linux || darwin

package connect

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/fakebasecamp"
)

// Bystander is someone on the served project whom the agent does not take
// instructions from: the default trust mode trusts only the operator.
const (
	BystanderID   int64 = 26909601
	BystanderName       = "Bystander"
)

// world is the default world with the bystander on the served project.
func world() *fakebasecamp.World {
	w := fakebasecamp.DefaultWorld()
	w.People[BystanderID] = &fakebasecamp.Person{ID: BystanderID, Name: BystanderName}
	p := w.Projects[fakebasecamp.ProjectID]
	p.Members = append(p.Members, BystanderID)
	return w
}

// kickoffID is the message every comment here is on.
const kickoffID int64 = 1_000_000

// comment adds a comment by author on the kickoff message, which it adds
// the first time, and emits the comment's comment.created on lanes.
func comment(h *Harness, author int64, content string, lanes fakebasecamp.Lane) fakebasecamp.Event {
	var id int64
	h.Fake.Update(func(w *fakebasecamp.World) {
		if _, ok := w.Recordings[kickoffID]; !ok {
			w.Recordings[kickoffID] = &fakebasecamp.Recording{
				ID: kickoffID, Type: "Message", BucketID: fakebasecamp.ProjectID, CreatorID: fakebasecamp.OperatorID, Title: "Kickoff",
			}
		}
		id = kickoffID + int64(len(w.Recordings))
		w.Recordings[id] = &fakebasecamp.Recording{
			ID: id, Type: "Comment", BucketID: fakebasecamp.ProjectID, ParentID: kickoffID, CreatorID: author, Content: content,
		}
	})
	return h.Fake.Emit(fakebasecamp.Event{
		EventType: "comment.created", BucketID: fakebasecamp.ProjectID, RecordingID: id, CreatorID: author,
	}, lanes)
}

// askAgent is a comment that asks the agent for something by mentioning it.
func askAgent() string {
	return "<p>Could you review the launch plan, " + fakebasecamp.Mention(fakebasecamp.AgentID) + "? Thanks</p>"
}

// streaming sets the agent up, starts the connector, and waits until an
// event published live reaches it.
func streaming(t *testing.T) (*Harness, *Connector) {
	t.Helper()
	h := NewHarness(t, world())
	h.Setup(t)
	c := h.Connect(t)
	c.WaitStreaming(t, 1)
	return h, c
}

// The setup a person runs, through the CLI: the connection ceremony, then
// connect setup, which writes connect.json owner-only.
func TestSetupThroughTheCLIWritesConnectJSON(t *testing.T) {
	t.Parallel()
	h := NewHarness(t, world())
	h.Setup(t)

	assert.Equal(t, 1, h.Fake.Count(fakebasecamp.RouteAgentConnections), "one connection ceremony")
	info, err := os.Stat(h.ConnectJSON())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	data, err := os.ReadFile(h.ConnectJSON())
	require.NoError(t, err)
	var file struct {
		Profile   string `json:"profile"`
		AccountID string `json:"account_id"`
		Agent     struct {
			PersonID int64  `json:"person_id"`
			Kind     string `json:"kind"`
		} `json:"agent"`
		Trust struct {
			Mode       string `json:"mode"`
			OperatorID int64  `json:"operator_id"`
		} `json:"trust"`
		Projects map[string]json.RawMessage `json:"projects"`
	}
	require.NoError(t, json.Unmarshal(data, &file))
	assert.Equal(t, Agent, file.Profile)
	assert.Equal(t, strconv.FormatInt(fakebasecamp.AccountID, 10), file.AccountID)
	assert.Equal(t, fakebasecamp.AgentID, file.Agent.PersonID)
	assert.Equal(t, "agent", file.Agent.Kind)
	assert.Equal(t, "operator", file.Trust.Mode)
	assert.Equal(t, fakebasecamp.OperatorID, file.Trust.OperatorID)
	assert.Equal(t, []string{strconv.FormatInt(fakebasecamp.ProjectID, 10)}, keys(file.Projects))
}

// The operator mentions the agent: the connector writes the event's pointer,
// admits it, and hands it off in exactly one request line that says what
// was asked, where, by whom, and where to reply, with the agent's own
// mention taken out of the words.
func TestATrustedMentionIsHandedOffInOneRequestLine(t *testing.T) {
	t.Parallel()
	h, c := streaming(t)
	ev := comment(h, fakebasecamp.OperatorID, askAgent(), fakebasecamp.Live)
	c.WaitFor(t, "the request line", func(o Output) bool { return len(o.RequestsFor(ev.ID)) > 0 })
	out := c.Stop(t)

	pointers := out.PointersFor(ev.ID)
	require.Len(t, pointers, 1)
	assert.Equal(t, "live", pointers[0].Lane)
	assert.Equal(t, ev.RecordingID, pointers[0].RecordingID)

	events := out.EventsFor(ev.ID)
	require.Len(t, events, 1)
	assert.Equal(t, "admitted", events[0].State)
	assert.Equal(t, "mentioned", events[0].Trigger)

	requests := out.RequestsFor(ev.ID)
	require.Len(t, requests, 1)
	req := requests[0]
	assert.Equal(t, "request", req.Type)
	assert.Equal(t, "comment.created", req.EventType)
	assert.Equal(t, "mentioned", req.Trigger)
	assert.Equal(t, RequestRecording{
		BucketID:    fakebasecamp.ProjectID,
		ProjectName: fakebasecamp.ProjectName,
		RecordingID: ev.RecordingID,
		Type:        "Comment",
		URL:         h.Fake.URL() + "/" + strconv.FormatInt(fakebasecamp.AccountID, 10) + "/comments/" + strconv.FormatInt(ev.RecordingID, 10),
	}, req.Recording)
	assert.Equal(t, RequestReplyTo{Kind: "comment", RecordingID: kickoffID}, req.ReplyTo)
	assert.Equal(t, fakebasecamp.OperatorID, req.RequesterID)
	assert.Equal(t, fakebasecamp.OperatorName, req.RequesterName)
	assert.True(t, req.Acknowledge, "a person asked for something")
	// The mention leaves a space where it was, so no markup can close over
	// the gap, and nothing else of the words changes.
	assert.Equal(t, strings.Replace(askAgent(), fakebasecamp.Mention(fakebasecamp.AgentID), " ", 1), req.Content)
}

// Basecamp publishes an event on both lanes: live first, and on the poll
// lane the connector walks when it reconnects. It is one event, so it is
// one pointer line and one request line.
func TestAnEventOnBothLanesIsHandedOffOnce(t *testing.T) {
	t.Parallel()
	h, c := streaming(t)
	ev := comment(h, fakebasecamp.OperatorID, askAgent(), fakebasecamp.Both)
	c.WaitFor(t, "the request line", func(o Output) bool { return len(o.RequestsFor(ev.ID)) > 0 })
	before := len(h.Fake.Requests())

	// The reconnect's catch-up walks the poll lane from where the first
	// walk ended, before the event, so the poll lane serves it again.
	require.Equal(t, 1, h.Fake.DropCable())
	c.WaitStreaming(t, 2)
	served := false
	for _, r := range h.Fake.Requests()[before:] {
		if r.Route == fakebasecamp.RouteEvents && r.Status == http.StatusOK && cursorBefore(r.Query.Get("position"), ev.ID) {
			served = true
		}
	}
	require.True(t, served, "the reconnect walked the poll lane from before the event")

	// Something after it, live, to be sure the connector has dealt with all
	// the walk delivered before it stops.
	after := comment(h, fakebasecamp.OperatorID, askAgent(), fakebasecamp.Live)
	c.WaitFor(t, "the request line after it", func(o Output) bool { return len(o.RequestsFor(after.ID)) > 0 })
	out := c.Stop(t)

	assert.Len(t, out.PointersFor(ev.ID), 1)
	assert.Len(t, out.EventsFor(ev.ID), 1)
	assert.Len(t, out.RequestsFor(ev.ID), 1)
}

// Someone the agent does not take instructions from mentions it: the
// connector says so in an event line, and hands nothing off.
func TestAnUntrustedMentionIsDiscardedAndNotHandedOff(t *testing.T) {
	t.Parallel()
	h, c := streaming(t)
	ev := comment(h, BystanderID, askAgent(), fakebasecamp.Live)
	c.WaitFor(t, "the event line", func(o Output) bool { return len(o.EventsFor(ev.ID)) > 0 })
	out := c.Stop(t)

	events := out.EventsFor(ev.ID)
	require.Len(t, events, 1)
	assert.Equal(t, "discarded", events[0].State)
	assert.Equal(t, "untrusted_performer", events[0].Reason)
	assert.Equal(t, BystanderID, events[0].RequesterID)
	assert.Empty(t, out.RequestsFor(ev.ID))
	assert.Empty(t, out.Requests())
}

// Stdout is a protocol a program parses: every line one JSON object of a
// shape the protocol has, and nothing else. What a person reads goes to
// stderr, as slog lines, starting with the connector saying it runs.
func TestStdoutIsOnlyNDJSONAndStderrSaysItRuns(t *testing.T) {
	t.Parallel()
	h, c := streaming(t)
	trusted := comment(h, fakebasecamp.OperatorID, askAgent(), fakebasecamp.Live)
	untrusted := comment(h, BystanderID, askAgent(), fakebasecamp.Live)
	c.WaitFor(t, "both verdicts and the request", func(o Output) bool {
		return len(o.RequestsFor(trusted.ID)) > 0 && len(o.EventsFor(untrusted.ID)) > 0
	})
	out := c.Stop(t)

	require.NotEmpty(t, out.Lines)
	for _, l := range out.Lines {
		assert.True(t, json.Valid([]byte(l.Raw)), "not JSON: %q", l.Raw)
		assert.NoError(t, l.Err, "%q", l.Raw)
	}
	assert.NotEmpty(t, out.Pointers())
	assert.NotEmpty(t, out.Events())
	assert.NotEmpty(t, out.Requests())

	running := out.Logged(MsgRunning)
	require.Len(t, running, 1)
	assert.Equal(t, "INFO", running[0].Level)
	assert.Equal(t, Agent, running[0].Attrs["profile"])
	assert.Equal(t, strconv.FormatInt(fakebasecamp.AccountID, 10), running[0].Attrs["account"])
	assert.Equal(t, strconv.FormatInt(fakebasecamp.AgentID, 10), running[0].Attrs["agent_person_id"])
	for _, l := range out.Logs {
		assert.NotEmpty(t, l.Msg, "stderr line is not a slog record: %q", l.Raw)
	}
}

// cursorBefore reports whether a poll position the fake issued is before
// an event id.
func cursorBefore(position string, id int64) bool {
	n, err := strconv.ParseInt(strings.TrimPrefix(position, "fake-"), 10, 64)
	return strings.HasPrefix(position, "fake-") && err == nil && n < id
}

func keys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
