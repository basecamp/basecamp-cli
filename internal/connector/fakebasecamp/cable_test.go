package fakebasecamp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/fakebasecamp"
)

// cableClient speaks Action Cable to the fake by hand, to hold it to the
// frames themselves.
type cableClient struct {
	t  *testing.T
	ws *websocket.Conn
}

func dialCable(t *testing.T, s *fakebasecamp.Server, bearer string) *cableClient {
	t.Helper()
	status, _, body := do(t, s, http.MethodPost, "/999/events/stream_ticket.json", bearer, "")
	require.Equal(t, http.StatusOK, status, body)
	var ticket struct {
		Ticket    string `json:"ticket"`
		ExpiresIn int    `json:"expires_in"`
		URL       string `json:"url"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &ticket))
	assert.Positive(t, ticket.ExpiresIn)
	assert.Equal(t, "ws"+s.URL()[len("http"):]+"/cable?ticket="+ticket.Ticket, ticket.URL)

	ws, resp, err := websocket.Dial(bounded(t), ticket.URL, &websocket.DialOptions{Subprotocols: []string{"actioncable-v1-json"}})
	require.NoError(t, err)
	if resp.Body != nil {
		_ = resp.Body.Close()
	}
	assert.Equal(t, "actioncable-v1-json", ws.Subprotocol())
	t.Cleanup(func() { _ = ws.CloseNow() })
	return &cableClient{t: t, ws: ws}
}

// frame reads the next frame, within d.
func (c *cableClient) frame(d time.Duration) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(c.t.Context(), d)
	defer cancel()
	_, data, err := c.ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	var frame map[string]any
	require.NoError(c.t, json.Unmarshal(data, &frame), "%s", data)
	return frame, nil
}

// next is the next frame that is not a ping.
func (c *cableClient) next() map[string]any {
	c.t.Helper()
	for {
		frame, err := c.frame(waitFor)
		require.NoError(c.t, err)
		if frame["type"] != "ping" {
			return frame
		}
	}
}

func (c *cableClient) send(v any) {
	c.t.Helper()
	data, err := json.Marshal(v)
	require.NoError(c.t, err)
	require.NoError(c.t, c.ws.Write(bounded(c.t), websocket.MessageText, data))
}

// The cable welcomes, confirms the subscription it is sent, and pushes the
// events the subscription's filters and the caller's projects let through,
// in the push shape: the poll row plus actor_type and visible_to_clients.
func TestCableSpeaksActionCable(t *testing.T) {
	s, bearer := start(t)
	c := dialCable(t, s, bearer)
	assert.Equal(t, map[string]any{"type": "welcome"}, c.next())

	identifier := `{"channel":"EventsChannel","buckets":"48699913","exclude_performers":"52007412"}`
	c.send(map[string]string{"command": "subscribe", "identifier": identifier})
	assert.Equal(t, map[string]any{"type": "confirm_subscription", "identifier": identifier}, c.next())
	assert.Equal(t, 1, s.Subscribers())

	s.Update(func(w *fakebasecamp.World) {
		w.Projects[777] = &fakebasecamp.Project{ID: 777, Name: "Elsewhere", Members: []int64{fakebasecamp.OperatorID}}
	})
	s.Emit(commentBy(fakebasecamp.OperatorID, otherProject), fakebasecamp.Live)           // another bucket
	s.Emit(commentBy(fakebasecamp.AgentID, fakebasecamp.ProjectID), fakebasecamp.Live)    // excluded performer
	s.Emit(commentBy(fakebasecamp.OperatorID, 777), fakebasecamp.Live)                    // a project the agent is not on
	s.Emit(commentBy(fakebasecamp.OperatorID, fakebasecamp.ProjectID), fakebasecamp.Poll) // the poll lane only
	want := s.Emit(fakebasecamp.Event{
		EventType: "todo.assignment_changed", BucketID: fakebasecamp.ProjectID, RecordingID: 9002,
		CreatorID: fakebasecamp.OperatorID, Details: json.RawMessage(`{"column_id":1}`),
	}, fakebasecamp.Live)

	frame := c.next()
	assert.Equal(t, identifier, frame["identifier"])
	message, ok := frame["message"].(map[string]any)
	require.True(t, ok, "%v", frame)
	assert.Equal(t, map[string]any{
		"id":                 float64(want.ID),
		"kind":               "todo_assignment_changed",
		"action":             "assignment_changed",
		"event_type":         "todo.assignment_changed",
		"bucket_id":          float64(fakebasecamp.ProjectID),
		"creator_id":         float64(fakebasecamp.OperatorID),
		"performed_by_id":    nil,
		"recording_id":       float64(9002),
		"created_at":         want.CreatedAt.Format(time.RFC3339Nano),
		"details":            map[string]any{"column_id": float64(1)},
		"actor_type":         "person",
		"visible_to_clients": false,
	}, message)

	// A different subscription on the same connection is rejected; the same
	// one again is absorbed.
	c.send(map[string]string{"command": "subscribe", "identifier": `{"channel":"EventsChannel"}`})
	assert.Equal(t, "reject_subscription", c.next()["type"])
	c.send(map[string]string{"command": "unsubscribe", "identifier": identifier})
	await(t, s, "the unsubscribe", func() bool { return s.Subscribers() == 0 })
}

// The cable pings on its interval until it is silenced; then nothing more
// arrives but what is published.
func TestCablePingsUntilSilenced(t *testing.T) {
	s, bearer := start(t, fakebasecamp.WithPingInterval(20*time.Millisecond))
	c := dialCable(t, s, bearer)
	assert.Equal(t, "welcome", c.next()["type"])
	frame, err := c.frame(waitFor)
	require.NoError(t, err)
	assert.Equal(t, "ping", frame["type"])
	assert.IsType(t, float64(0), frame["message"], "the ping carries the server's epoch")

	identifier := `{"channel":"EventsChannel"}`
	c.send(map[string]string{"command": "subscribe", "identifier": identifier})
	assert.Equal(t, "confirm_subscription", c.next()["type"])
	s.StopPings()
	// Every frame after this one was queued after the silence.
	marker := s.Emit(commentBy(fakebasecamp.OperatorID, fakebasecamp.ProjectID), fakebasecamp.Live)
	for {
		frame, err := c.frame(waitFor)
		require.NoError(t, err)
		if frame["type"] == "ping" {
			continue
		}
		require.Equal(t, float64(marker.ID), frame["message"].(map[string]any)["id"])
		break
	}
	assert.Equal(t, 1, s.Subscribers(), "silenced, not dropped")
	// Ten intervals of nothing. The read gives up by closing the socket, so
	// it comes last.
	_, err = c.frame(10 * 20 * time.Millisecond)
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "a silenced connection pinged: %v", err)
}

// A cable URL whose ticket the fake never minted is refused before the
// upgrade, and a dropped connection is gone at once.
func TestCableRefusesAnUnknownTicketAndDrops(t *testing.T) {
	s, bearer := start(t)
	_, resp, err := websocket.Dial(bounded(t), "ws"+s.URL()[len("http"):]+"/cable?ticket=forged",
		&websocket.DialOptions{Subprotocols: []string{"actioncable-v1-json"}})
	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	c := dialCable(t, s, bearer)
	c.next()
	c.send(map[string]string{"command": "subscribe", "identifier": `{"channel":"EventsChannel"}`})
	c.next()
	assert.Equal(t, 1, s.DropCable())
	assert.Zero(t, s.Subscribers())
	_, err = c.frame(waitFor)
	assert.Error(t, err)
	assert.Equal(t, 2, s.Count(fakebasecamp.RouteCable))
}
