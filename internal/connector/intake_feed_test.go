package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed/feedtest"
)

// End-to-end through the real eventfeed run loop, on the package's own
// deterministic fakes: subscribe, catch up, drain, stream — and then a restart
// over the same ledger.

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func pushFrame(t *testing.T, identifier string, event eventfeed.Event) []byte {
	t.Helper()
	payload := map[string]any{
		"id":                 event.ID,
		"kind":               event.Kind,
		"event_type":         event.EventType,
		"action":             event.Action,
		"created_at":         event.CreatedAt.UTC().Format(time.RFC3339),
		"bucket_id":          event.BucketID,
		"creator_id":         event.CreatorID,
		"performed_by_id":    nil,
		"actor_type":         "person",
		"recording_id":       event.RecordingID,
		"visible_to_clients": true,
	}
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	frame, err := json.Marshal(map[string]json.RawMessage{
		"identifier": mustJSON(t, identifier),
		"message":    raw,
	})
	require.NoError(t, err)
	return frame
}

func confirmFrame(t *testing.T, identifier string) []byte {
	t.Helper()
	frame, err := json.Marshal(map[string]any{
		"type":       "confirm_subscription",
		"identifier": identifier,
	})
	require.NoError(t, err)
	return frame
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return raw
}

// subscribedConn waits for the connector's subscribe command and answers it,
// returning the connection and the identifier the connector chose.
func subscribedConn(t *testing.T, transport *feedtest.Transport) (*feedtest.Conn, string) {
	t.Helper()
	var (
		conn       *feedtest.Conn
		identifier string
	)
	require.Eventually(t, func() bool {
		conn = transport.LastConn()
		return conn != nil
	}, 5*time.Second, 5*time.Millisecond, "the connector should dial the cable URL the mint served")

	// Action Cable greets first; the subscribe follows the welcome.
	conn.Serve([]byte(`{"type":"welcome"}`))

	require.Eventually(t, func() bool {
		writes := conn.Writes()
		if len(writes) == 0 {
			return false
		}
		var command struct {
			Command    string `json:"command"`
			Identifier string `json:"identifier"`
		}
		if err := json.Unmarshal(writes[0], &command); err != nil {
			return false
		}
		identifier = command.Identifier
		return command.Command == "subscribe" && identifier != ""
	}, 5*time.Second, 5*time.Millisecond, "the connector should subscribe before it takes a position")

	conn.Serve(confirmFrame(t, identifier))
	return conn, identifier
}

func TestIntakeRunsTheFeedThroughCatchUpAndStreaming(t *testing.T) {
	ledger := newTestLedger(t)
	queue, err := NewQueue(DefaultBacklogWarn, DefaultBacklogPause)
	require.NoError(t, err)

	transport := feedtest.NewTransport()
	minter := feedtest.NewMinter()
	minter.ScriptTicket(eventfeed.StreamTicket{Ticket: "t", ExpiresIn: 120, URL: "wss://cable.basecamp.com/cable?ticket=t"})
	polls := feedtest.NewPolls()
	polls.ScriptPage(eventfeed.PollPage{
		Events:   []eventfeed.Event{testEvent(17099838500)},
		Position: "caught-up-position",
	})

	var pointers safeBuffer
	intake, err := New(Options{
		Origin:            "https://3.basecampapi.com",
		AccountID:         "2914079",
		ConsumerNamespace: "connector-test",
		Ledger:            ledger,
		Queue:             queue,
		Minter:            minter,
		Polls:             polls,
		Transport:         transport,
		Pointers:          &pointers,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- intake.Run(ctx) }()

	conn, identifier := subscribedConn(t, transport)

	// The catch-up page lands first; then a live event on the socket.
	require.Eventually(t, func() bool {
		_, ok, err := ledger.Get(ctx, 17099838500)
		return err == nil && ok
	}, 5*time.Second, 5*time.Millisecond, "the catch-up walk's events reach the ledger")

	conn.Serve(pushFrame(t, identifier, testEvent(17099838600)))
	require.Eventually(t, func() bool {
		_, ok, err := ledger.Get(ctx, 17099838600)
		return err == nil && ok
	}, 5*time.Second, 5*time.Millisecond, "a live event reaches the ledger")

	// Only the poll lane advances the durable position.
	position, ok, err := ledger.Load(ctx, intake.CheckpointKey())
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "caught-up-position", position)

	live, _, err := ledger.Get(ctx, 17099838600)
	require.NoError(t, err)
	assert.Equal(t, LaneLive, live.Lane)
	polled, _, err := ledger.Get(ctx, 17099838500)
	require.NoError(t, err)
	assert.Equal(t, LanePoll, polled.Lane)

	assert.Equal(t, 2, countLines(pointers.String()))
	assert.Equal(t, 2, queue.Depth())

	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("Run should return when its context is canceled")
	}
}

// Kill and restart: no duplicate, and the walk resumes from the stored
// position rather than the present.
func TestIntakeSurvivesARestartWithoutDuplicating(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/connector.db"

	firstLedger, err := OpenLedger(path)
	require.NoError(t, err)
	_, err = firstLedger.RecordSeen(context.Background(), testEvent(17099838500), LanePoll)
	require.NoError(t, err)
	require.NoError(t, firstLedger.Save(context.Background(), eventfeed.CheckpointKey{
		Origin:            "https://3.basecampapi.com",
		AccountID:         "2914079",
		ConsumerNamespace: "connector-test",
		FilterKey:         eventfeed.Filters{}.FilterKey(),
	}, "position-from-the-previous-run"))
	require.NoError(t, firstLedger.Close())

	ledger, err := OpenLedger(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ledger.Close() })

	queue, err := NewQueue(DefaultBacklogWarn, DefaultBacklogPause)
	require.NoError(t, err)

	transport := feedtest.NewTransport()
	minter := feedtest.NewMinter()
	minter.ScriptTicket(eventfeed.StreamTicket{Ticket: "t", ExpiresIn: 120, URL: "wss://cable.basecamp.com/cable?ticket=t"})
	polls := feedtest.NewPolls()
	// The same event the previous run already saw, plus one it did not.
	polls.ScriptPage(eventfeed.PollPage{
		Events:   []eventfeed.Event{testEvent(17099838500), testEvent(17099838501)},
		Position: "position-after-the-restart",
	})

	var pointers safeBuffer
	intake, err := New(Options{
		Origin:            "https://3.basecampapi.com",
		AccountID:         "2914079",
		ConsumerNamespace: "connector-test",
		Ledger:            ledger,
		Queue:             queue,
		Minter:            minter,
		Polls:             polls,
		Transport:         transport,
		Pointers:          &pointers,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- intake.Run(ctx) }()

	subscribedConn(t, transport)

	require.Eventually(t, func() bool {
		_, ok, err := ledger.Get(ctx, 17099838501)
		return err == nil && ok
	}, 5*time.Second, 5*time.Millisecond)

	calls := polls.Calls()
	require.NotEmpty(t, calls)
	assert.Equal(t, "position-from-the-previous-run", calls[0].Cursor.Position,
		"a restart resumes from the stored position, never at the head")

	assert.Equal(t, 1, countLines(pointers.String()),
		"the event the previous run already saw is not a second unit of work")
	assert.Equal(t, 1, queue.Depth())

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run should return when its context is canceled")
	}
}

func TestIntakeRecordsTheLastPollServedIDFromTheWalk(t *testing.T) {
	ledger := newTestLedger(t)
	queue, err := NewQueue(DefaultBacklogWarn, DefaultBacklogPause)
	require.NoError(t, err)

	transport := feedtest.NewTransport()
	minter := feedtest.NewMinter()
	minter.ScriptTicket(eventfeed.StreamTicket{Ticket: "t", ExpiresIn: 120, URL: "wss://cable.basecamp.com/cable?ticket=t"})
	polls := feedtest.NewPolls()
	polls.ScriptPage(eventfeed.PollPage{
		Events:   []eventfeed.Event{testEvent(17099838500), testEvent(17099838501)},
		Position: "p1",
	})

	intake, err := New(Options{
		Origin:            "https://3.basecampapi.com",
		AccountID:         "2914079",
		ConsumerNamespace: "connector-test",
		Ledger:            ledger,
		Queue:             queue,
		Minter:            minter,
		Polls:             polls,
		Transport:         transport,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = intake.Run(ctx) }()

	conn, identifier := subscribedConn(t, transport)

	require.Eventually(t, func() bool {
		served, err := ledger.LastPollServedID(ctx, intake.CheckpointKey())
		return err == nil && served == 17099838501
	}, 5*time.Second, 5*time.Millisecond, "the poll lane's highest served id is durable")

	// A live event far ahead of the poll lane must not move it.
	conn.Serve(pushFrame(t, identifier, testEvent(17099999999)))
	require.Eventually(t, func() bool {
		_, ok, err := ledger.Get(ctx, 17099999999)
		return err == nil && ok
	}, 5*time.Second, 5*time.Millisecond)

	served, err := ledger.LastPollServedID(ctx, intake.CheckpointKey())
	require.NoError(t, err)
	assert.Equal(t, int64(17099838501), served,
		fmt.Sprintf("a live id is not a poll-served id; re-entering at %d would skip the safety delay", 17099999999))
}

// A repair walk must not strand the process on the way out. It is bound to
// Run's lifetime, and an unfinished walk is a delay: the loss is still open on
// disk and the next start resumes it.
func TestShutdownDoesNotWaitOutAnOpenRepairWalk(t *testing.T) {
	ledger := newTestLedger(t)
	queue, err := NewQueue(DefaultBacklogWarn, DefaultBacklogPause)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err = ledger.RecordLoss(ctx, []int64{17099838509}, time.Now(), time.Hour)
	require.NoError(t, err)

	transport := feedtest.NewTransport()
	minter := feedtest.NewMinter()
	minter.ScriptTicket(eventfeed.StreamTicket{Ticket: "t", ExpiresIn: 120, URL: "wss://cable.basecamp.com/cable?ticket=t"})
	polls := feedtest.NewPolls()

	intake, err := New(Options{
		Origin:            "https://3.basecampapi.com",
		AccountID:         "2914079",
		ConsumerNamespace: "connector-test",
		Ledger:            ledger,
		Queue:             queue,
		Minter:            minter,
		Polls:             polls,
		Transport:         transport,
		// A repair cadence far longer than the test: the walk is asleep when
		// the shutdown arrives, which is the case that used to hang.
		RepairInterval: time.Hour,
	})
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() { done <- intake.Run(ctx) }()

	subscribedConn(t, transport)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run should return on shutdown rather than wait out the repair cadence")
	}

	open, err := ledger.OpenLosses(context.Background())
	require.NoError(t, err)
	assert.Len(t, open, 1, "the loss stays on disk for the next start to resume")
}
