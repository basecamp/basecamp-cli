package admission

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testNow = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

type fakeLedger struct {
	mu      sync.Mutex
	live    map[string]bool
	commits []Verdict

	// inFlight counts commits running per key, to catch two at once.
	inFlight map[string]*atomic.Int32
	overlap  atomic.Bool
	hold     time.Duration
}

func newFakeLedger() *fakeLedger {
	return &fakeLedger{live: map[string]bool{}, inFlight: map[string]*atomic.Int32{}}
}

func (l *fakeLedger) LiveTask(_ context.Context, key string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.live[key], nil
}

func (l *fakeLedger) Commit(_ context.Context, v Verdict) error {
	l.mu.Lock()
	counter := l.inFlight[v.ConversationKey]
	if counter == nil {
		counter = &atomic.Int32{}
		l.inFlight[v.ConversationKey] = counter
	}
	l.mu.Unlock()

	if counter.Add(1) > 1 {
		l.overlap.Store(true)
	}
	time.Sleep(l.hold)
	counter.Add(-1)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.commits = append(l.commits, v)
	// Committing an admitted record starts the conversation's task, as
	// dispatch will; a later event on the key must see it.
	if v.State == StateAdmitted {
		l.live[v.ConversationKey] = true
	}
	return nil
}

func TestCommitQueuesBehindALiveTask(t *testing.T) {
	ledger := newFakeLedger()
	ledger.live["recording:9000"] = true
	c := NewCommitter(ledger)

	got, err := c.Commit(context.Background(), Verdict{EventID: 1, State: StateAdmitted, ConversationKey: "recording:9000"})
	require.NoError(t, err)
	assert.Equal(t, StateQueued, got.State)
	assert.Equal(t, StateQueued, ledger.commits[0].State)

	got, err = c.Commit(context.Background(), Verdict{EventID: 2, State: StateAdmitted, ConversationKey: "recording:1"})
	require.NoError(t, err)
	assert.Equal(t, StateAdmitted, got.State)

	got, err = c.Commit(context.Background(), Verdict{EventID: 3, State: StateBlocked, Reason: ReasonReadFailed, ConversationKey: "recording:9000"})
	require.NoError(t, err)
	assert.Equal(t, StateBlocked, got.State, "only an admitted verdict joins a live task")
}

func TestCommitsAreSerialisedPerConversation(t *testing.T) {
	ledger := newFakeLedger()
	ledger.hold = 5 * time.Millisecond
	c := NewCommitter(ledger)

	const n = 8
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			_, err := c.Commit(context.Background(), Verdict{EventID: int64(i + 1), State: StateAdmitted, ConversationKey: "campfire:8000"})
			assert.NoError(t, err)
		})
	}
	wg.Wait()

	assert.False(t, ledger.overlap.Load(), "two commits on one conversation ran at once")
	var admitted, queued int
	for _, v := range ledger.commits {
		switch v.State {
		case StateAdmitted:
			admitted++
		case StateQueued:
			queued++
		case StateBlocked, StateDiscarded:
			t.Errorf("unexpected %s commit", v.State)
		}
	}
	assert.Equal(t, 1, admitted, "exactly one event starts the conversation's task")
	assert.Equal(t, n-1, queued)

	c.locks.mu.Lock()
	assert.Empty(t, c.locks.locks, "a lock nobody holds is dropped")
	c.locks.mu.Unlock()
}

func TestCommitLockHonoursCancellation(t *testing.T) {
	var k keyedMutex
	unlock, err := k.lock(context.Background(), "k")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = k.lock(ctx, "k")
	require.ErrorIs(t, err, context.Canceled)

	unlock()
	k.mu.Lock()
	assert.Empty(t, k.locks)
	k.mu.Unlock()
}

func TestNextBlockedRetry(t *testing.T) {
	blockedAt := testNow

	next, ok := NextBlockedRetry(ReasonReadFailed, blockedAt, blockedAt)
	require.True(t, ok)
	assert.Equal(t, blockedAt.Add(10*time.Minute), next)

	next, ok = NextBlockedRetry(ReasonDeltaUnverified, blockedAt, blockedAt.Add(23*time.Hour+50*time.Minute))
	require.True(t, ok, "the last retry inside the day")
	assert.Equal(t, blockedAt.Add(24*time.Hour), next)

	_, ok = NextBlockedRetry(ReasonReadUnresolved, blockedAt, blockedAt.Add(24*time.Hour))
	assert.False(t, ok, "after a day only a redispatch re-runs it")

	_, ok = NextBlockedRetry(ReasonNoRoute, blockedAt, blockedAt)
	assert.False(t, ok, "no_route waits for connect.json, not for time")
}

type sliceSource struct {
	mu  sync.Mutex
	ids []int64
}

func (s *sliceSource) Take(ctx context.Context) (int64, error) {
	s.mu.Lock()
	if len(s.ids) > 0 {
		id := s.ids[0]
		s.ids = s.ids[1:]
		s.mu.Unlock()
		return id, nil
	}
	s.mu.Unlock()
	<-ctx.Done()
	return 0, ctx.Err()
}

type mapRecords map[int64]Event

func (m mapRecords) LoadSeen(_ context.Context, id int64) (Event, bool, error) {
	ev, ok := m[id]
	return ev, ok, nil
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestRunDecidesCommitsAndReportsWithoutContent(t *testing.T) {
	f := newFakeReads()
	const secret = "the body of the instruction"
	f.summaries[recordingID] = summaryWith(recordingID, routedProj, "Kanban::Card", operatorID, "<div>"+secret+" "+mentionOf(t, agentID)+"</div>")
	ledger := newFakeLedger()
	out := &syncBuffer{}

	records := mapRecords{
		1: {ID: 1, EventType: "card.created", BucketID: routedProj, RecordingID: recordingID, CreatorID: operatorID},
		2: {ID: 2, EventType: "card.moved", BucketID: routedProj, RecordingID: recordingID, CreatorID: operatorID},
		// 3 is no longer seen: skipped.
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, RunOptions{
			Source:    &sliceSource{ids: []int64{1, 2, 3}},
			Records:   records,
			Admitter:  newAdmitter(t, basePolicy(), f),
			Committer: NewCommitter(ledger),
			Workers:   2,
			Lines:     out,
		})
	}()
	require.Eventually(t, func() bool { return strings.Count(out.String(), "\n") == 2 }, 2*time.Second, 5*time.Millisecond)
	cancel()
	require.NoError(t, <-done)

	assert.NotContains(t, out.String(), secret, "no line may carry content")
	lines := map[int64]map[string]any{}
	for raw := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &m))
		lines[int64(m["event_id"].(float64))] = m
	}
	assert.Equal(t, map[string]any{
		"type": "event", "event_id": float64(1), "event_type": "card.created", "trigger": "mentioned",
		"class": "internal", "route": "/work/connector", "bucket_id": float64(routedProj), "recording_id": float64(recordingID),
		"recording_url": "https://app.basecamp.com/2914079/buckets/1/recordings/1", "requester_id": float64(operatorID), "state": "admitted",
	}, lines[1])
	assert.Equal(t, "discarded", lines[2]["state"])
	assert.Equal(t, "not_in_matrix", lines[2]["reason"])
	assert.Len(t, ledger.commits, 2)
}

type failingRecords struct{}

func (failingRecords) LoadSeen(context.Context, int64) (Event, bool, error) {
	return Event{}, false, errors.New("database is locked")
}

func TestRunStopsOnALedgerFailure(t *testing.T) {
	err := Run(context.Background(), RunOptions{
		Source:    &sliceSource{ids: []int64{1}},
		Records:   failingRecords{},
		Admitter:  newAdmitter(t, basePolicy(), newFakeReads()),
		Committer: NewCommitter(newFakeLedger()),
	})
	require.ErrorContains(t, err, "database is locked")
}
