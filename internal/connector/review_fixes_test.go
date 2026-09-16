package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed/feedtest"
)

func TestALateArrivalClearsAnUnrecoveredID(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()

	loss, err := ledger.RecordLoss(ctx, []int64{17099838509}, time.Now(), time.Minute)
	require.NoError(t, err)
	_, err = ledger.CloseLoss(ctx, loss.ID, time.Now())
	require.NoError(t, err)

	_, err = ledger.RecordSeen(ctx, testEvent(17099838509), LanePoll)
	require.NoError(t, err)

	unrecovered, err := ledger.UnrecoveredIDs(ctx)
	require.NoError(t, err)
	assert.Empty(t, unrecovered, "an id intake received is not unrecovered, however late it came")
}

func TestPointerLineReportsTheLaneIntakeWasGiven(t *testing.T) {
	var pointers bytes.Buffer
	intake, _, _ := newTestIntake(t, nil, &pointers)
	require.NoError(t, intake.ingest(context.Background(), testEvent(1), LaneRepair))

	var pointer Pointer
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(pointers.String())), &pointer))
	assert.Equal(t, LaneRepair, pointer.Lane, "the pointer line and the ledger must agree on the lane")
}

func TestMembershipBaselineIsTakenFromTheFirstSuccessfulRead(t *testing.T) {
	intake, _, _ := newTestIntake(t, nil, nil)

	// The snapshot at subscribe failed, so there is none.
	assert.False(t, intake.membershipChanged([]int64{1, 2}), "the first successful read is a baseline, not a change")
	assert.True(t, intake.membershipChanged([]int64{1, 2, 3}), "and a later grant is then seen")
}

type flakyMembership struct {
	mu      sync.Mutex
	buckets []int64
	err     error
}

func (m *flakyMembership) Buckets(context.Context) ([]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buckets, m.err
}

func newFeedIntake(t *testing.T, ledger *Ledger, opts Options) (*Intake, *feedtest.Transport, *feedtest.Minter, *feedtest.Polls) {
	t.Helper()
	queue, err := NewQueue(DefaultBacklogWarn, DefaultBacklogPause)
	require.NoError(t, err)
	transport := feedtest.NewTransport()
	minter := feedtest.NewMinter()
	polls := feedtest.NewPolls()
	opts.Origin = "https://3.basecampapi.com"
	opts.AccountID = "2914079"
	opts.ConsumerNamespace = "connector-test"
	opts.Ledger = ledger
	opts.Queue = queue
	opts.Minter = minter
	opts.Polls = polls
	opts.Transport = transport
	intake, err := New(opts)
	require.NoError(t, err)
	return intake, transport, minter, polls
}

func ticket() eventfeed.StreamTicket {
	return eventfeed.StreamTicket{Ticket: "t", ExpiresIn: 120, URL: "wss://cable.basecamp.com/cable?ticket=t"}
}

func runInBackground(ctx context.Context, t *testing.T, intake *Intake) chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- intake.Run(ctx) }()
	return done
}

func awaitReturn(t *testing.T, done chan error, why string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal(why)
		return nil
	}
}

func TestATerminalFeedIsNotHeldOpenByTheMembershipWatcher(t *testing.T) {
	ledger := newTestLedger(t)
	intake, _, minter, _ := newFeedIntake(t, ledger, Options{
		Membership:         &flakyMembership{buckets: []int64{48699913}},
		MembershipInterval: time.Hour,
	})
	minter.ScriptError(&eventfeed.MintError{Kind: eventfeed.MintUnrecoverable, Err: errors.New("gone")})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := awaitReturn(t, runInBackground(ctx, t, intake),
		"a feed that terminated must return its error, not wait out the membership ticker")
	assert.Error(t, err)
}

func TestATerminalFeedIsNotHeldOpenByARepairWalk(t *testing.T) {
	ledger := newTestLedger(t)
	_, err := ledger.RecordLoss(context.Background(), []int64{17099838509}, time.Now(), time.Hour)
	require.NoError(t, err)

	intake, _, minter, _ := newFeedIntake(t, ledger, Options{RepairInterval: time.Hour})
	minter.ScriptError(&eventfeed.MintError{Kind: eventfeed.MintUnrecoverable, Err: errors.New("gone")})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = awaitReturn(t, runInBackground(ctx, t, intake),
		"a repair walk is off the delivery path and must not delay the supervisor by its window")
	assert.Error(t, err)

	open, err := ledger.OpenLosses(context.Background())
	require.NoError(t, err)
	assert.Len(t, open, 1, "the loss stays open for the next start")
}

func TestAnEventFromAnUnsnapshottedProjectReconnectsPromptly(t *testing.T) {
	ledger := newTestLedger(t)
	intake, transport, minter, polls := newFeedIntake(t, ledger, Options{
		Membership:         &flakyMembership{buckets: []int64{1}},
		MembershipInterval: time.Hour,
	})
	minter.ScriptTicket(ticket())
	minter.ScriptTicket(ticket())
	polls.ScriptPage(eventfeed.PollPage{Events: []eventfeed.Event{testEvent(17099838500)}, Position: "p1"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runInBackground(ctx, t, intake)
	subscribedConn(t, transport)

	require.Eventually(t, func() bool { return len(transport.Dials()) >= 2 }, 5*time.Second, 10*time.Millisecond,
		"the poll lane served project 48699913, which the live subscription does not hold; the reconnect is due now, not at the next membership tick")

	cancel()
	awaitReturn(t, done, "Run should return on shutdown")
}

// After a restart the SDK's own reset cursor starts at zero, so a rejected
// stored position would re-enter at the present. The ledger holds the safe
// re-entry; intake must use it.
func TestARejectedStoredPositionReentersAtThePersistedPollServedID(t *testing.T) {
	ledger := newTestLedger(t)
	intake, transport, minter, polls := newFeedIntake(t, ledger, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := intake.CheckpointKey()
	require.NoError(t, ledger.Save(ctx, key, "position-the-server-will-refuse"))
	require.NoError(t, ledger.NotePollServed(ctx, key, 17099838500))

	minter.ScriptTicket(ticket())
	minter.ScriptTicket(ticket())
	polls.ScriptError(&eventfeed.PollError{Kind: eventfeed.PollPositionInvalid})
	polls.ScriptPage(eventfeed.PollPage{Position: "p2"})
	polls.ScriptPage(eventfeed.PollPage{Position: "p3"})

	done := runInBackground(ctx, t, intake)
	subscribedConn(t, transport)

	// Whatever the connector does next — re-enter on this connection or make
	// a new one — the second poll is the re-entry. A new connection is
	// answered as it appears.
	answered := 1
	var reentry eventfeed.Cursor
	require.Eventually(t, func() bool {
		if conns := transport.Conns(); len(conns) > answered {
			answerSubscription(conns[len(conns)-1])
			answered = len(conns)
		}
		calls := polls.Calls()
		if len(calls) < 2 {
			return false
		}
		reentry = calls[1].Cursor
		return true
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, "17099838500", reentry.Since,
		"re-entering at the present skips everything between the refused position and now")

	cancel()
	awaitReturn(t, done, "Run should return on shutdown")
}

// A filter change re-enters under the new digest at the last poll-served id,
// which is tracked apart from any one digest's position.
func TestAFilterChangeReentersAtTheLastPollServedID(t *testing.T) {
	ledger := newTestLedger(t)
	intake, transport, minter, polls := newFeedIntake(t, ledger, Options{
		Filters: eventfeed.Filters{Types: []string{"comment.created"}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	old := intake.CheckpointKey()
	old.FilterKey = eventfeed.Filters{}.FilterKey()
	require.NoError(t, ledger.Save(ctx, old, "old-digest-position"))
	require.NoError(t, ledger.NotePollServed(ctx, old, 17099838500))

	minter.ScriptTicket(ticket())
	polls.ScriptPage(eventfeed.PollPage{Position: "p1"})

	done := runInBackground(ctx, t, intake)
	subscribedConn(t, transport)

	require.Eventually(t, func() bool { return polls.CallCount() > 0 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, "17099838500", polls.Calls()[0].Cursor.Since,
		"a new filter set with no position of its own must not enter at the present")

	cancel()
	awaitReturn(t, done, "Run should return on shutdown")
}

// answerSubscription greets a connection and confirms its subscription without
// failing the test, so it can run inside a polling condition.
func answerSubscription(conn *feedtest.Conn) {
	conn.Serve([]byte(`{"type":"welcome"}`))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if writes := conn.Writes(); len(writes) > 0 {
			var command struct {
				Identifier string `json:"identifier"`
			}
			if json.Unmarshal(writes[0], &command) == nil && command.Identifier != "" {
				frame, _ := json.Marshal(map[string]string{"type": "confirm_subscription", "identifier": command.Identifier})
				conn.Serve(frame)
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A retention 410 reaching the repair walk is the same foreign loss it is on
// the feed: recorded as its own class and not retried as if it were transient.
func TestARetention410InTheRepairWalkIsRecordedNotRetried(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	loss, err := ledger.RecordLoss(ctx, []int64{17099838509}, clock.at, 10*time.Minute)
	require.NoError(t, err)

	adapter := newTestAdapter(t, &fakeFeedClient{err: retentionGone()})
	walker, _ := newTestWalker(t, ledger, adapter, clock)
	require.NoError(t, walker.reconcile(ctx, loss))

	gaps, err := ledger.Gaps(ctx)
	require.NoError(t, err)
	require.Len(t, gaps, 1, "the retention 410 is a fact about the feed, recorded once")
	assert.Equal(t, GapRetention, gaps[0].Class)
	assert.Nil(t, gaps[0].EpochAfterID)
}

// A 410 in the walk fences off the ids below the epoch. Ids above it are still
// servable from the resume, and giving up on them too would lose them for
// nothing.
func TestAnEpoch410InTheRepairWalkOnlyCondemnsTheIDsBehindIt(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	loss, err := ledger.RecordLoss(ctx, []int64{100, 200}, clock.at, 10*time.Minute)
	require.NoError(t, err)

	polls := &scriptedPolls{
		errs: []error{&eventfeed.PollError{
			Kind:         eventfeed.PollGone,
			EpochAfterID: 150,
			ResumeURL:    "https://3.basecampapi.com/2914079/events.json?since=150",
		}},
		pages: []eventfeed.PollPage{{}, {Events: []eventfeed.Event{testEvent(200)}, Position: "after-epoch"}},
	}
	walker, _ := newTestWalker(t, ledger, polls, clock)
	require.NoError(t, walker.reconcile(ctx, loss))

	require.GreaterOrEqual(t, len(polls.cursors), 2)
	assert.Equal(t, "https://3.basecampapi.com/2914079/events.json?since=150", polls.cursors[1].PageURL,
		"the resume is followed as served")

	unrecovered, err := ledger.UnrecoveredIDs(ctx)
	require.NoError(t, err)
	assert.Equal(t, []int64{100}, unrecovered)
	recovered, err := ledger.MissingIDs(ctx, loss.ID, LossRecovered)
	require.NoError(t, err)
	assert.Equal(t, []int64{200}, recovered)
}

// The walk's own cursor can be refused — minted under another filter set, or
// by a rotated secret. The walk re-enters from its explicit id rather than
// retrying the refused cursor for the whole window.
func TestARefusedRepairCursorReentersFromTheExplicitID(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	loss, err := ledger.RecordLoss(ctx, []int64{17099838509}, clock.at, 10*time.Minute)
	require.NoError(t, err)
	require.NoError(t, ledger.SaveRepairCursor(ctx, loss.ID, "cursor-under-old-filters"))
	loss.RepairCursor = "cursor-under-old-filters"

	polls := &scriptedPolls{
		errs:  []error{&eventfeed.PollError{Kind: eventfeed.PollFilterChanged}},
		pages: []eventfeed.PollPage{{}, {Events: []eventfeed.Event{testEvent(17099838509)}, Position: "fresh"}},
	}
	walker, ingested := newTestWalker(t, ledger, polls, clock)
	require.NoError(t, walker.reconcile(ctx, loss))

	require.GreaterOrEqual(t, len(polls.cursors), 2)
	assert.Equal(t, "17099838508", polls.cursors[1].Since)
	assert.Equal(t, []int64{17099838509}, *ingested)
}

// A failure no repeat will fix ends this start's walk but leaves the loss open,
// so the next start — perhaps after the cause is fixed — tries again rather
// than finding the ids already condemned.
func TestAnUnrecoverableRepairPollLeavesTheLossOpen(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	clock := &walkClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	loss, err := ledger.RecordLoss(ctx, []int64{17099838509}, clock.at, 10*time.Minute)
	require.NoError(t, err)

	polls := &scriptedPolls{errs: []error{&eventfeed.PollError{Kind: eventfeed.PollRedirectRefused}}}
	walker, _ := newTestWalker(t, ledger, polls, clock)
	require.NoError(t, walker.reconcile(ctx, loss))

	assert.Equal(t, 1, polls.calls, "a refused redirect is not worth ten minutes of retries")
	open, err := ledger.OpenLosses(ctx)
	require.NoError(t, err)
	assert.Len(t, open, 1)
}

func retentionGone() error {
	return &basecamp.FeedPositionGoneError{
		Err:    &basecamp.Error{Code: basecamp.CodeAPI, HTTPStatus: 410, Message: "outside retention"},
		Resume: "https://3.basecampapi.com/2914079/my/inbox.json?since=0",
	}
}
