package admission

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testNow = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

// fakeLedger holds the ledger's contract as the intake adapter must: one
// verdict per event, and admitted-or-queued decided in the commit itself.
type fakeLedger struct {
	mu        sync.Mutex
	live      map[string]bool
	decided   map[int64]State
	revisions map[int64]int64
	commits   []Verdict
	failure   error

	// inFlight counts commits running per key, to catch two at once.
	inFlight map[string]*atomic.Int32
	overlap  atomic.Bool
	hold     time.Duration
}

func newFakeLedger() *fakeLedger {
	return &fakeLedger{live: map[string]bool{}, decided: map[int64]State{}, revisions: map[int64]int64{}, inFlight: map[string]*atomic.Int32{}}
}

func (l *fakeLedger) Commit(_ context.Context, v Verdict) (State, error) {
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
	if l.failure != nil {
		return "", l.failure
	}
	if l.revisions[v.EventID] != v.Revision {
		return "", ErrAlreadyDecided
	}
	state := v.State
	if state == StateAdmitted {
		if l.live[v.ConversationKey] {
			state = StateQueued
		}
		// An admitted record not yet dispatched already makes the
		// conversation live: it is about to become its task.
		l.live[v.ConversationKey] = true
	}
	if state != StateAdmitted && state != StateQueued && v.Snapshot != nil {
		return "", errors.New("content on a verdict that is not admitted")
	}
	l.decided[v.EventID] = state
	l.revisions[v.EventID]++
	v.State = state
	l.commits = append(l.commits, v)
	return state, nil
}

func TestCommitReportsTheStateTheLedgerWrote(t *testing.T) {
	ledger := newFakeLedger()
	ledger.live["recording:9000"] = true
	c := NewCommitter(ledger)

	got, err := c.Commit(context.Background(), Verdict{EventID: 1, State: StateAdmitted, ConversationKey: "recording:9000"})
	require.NoError(t, err)
	assert.Equal(t, StateQueued, got.State)

	got, err = c.Commit(context.Background(), Verdict{EventID: 2, State: StateAdmitted, ConversationKey: "recording:1"})
	require.NoError(t, err)
	assert.Equal(t, StateAdmitted, got.State)
}

type contraryLedger struct{ write State }

func (l contraryLedger) Commit(context.Context, Verdict) (State, error) { return l.write, nil }

func TestCommitRefusesALedgerThatWroteSomethingElse(t *testing.T) {
	// Queued for admitted is the one substitution the ledger may make.
	_, err := NewCommitter(contraryLedger{write: StateAdmitted}).Commit(context.Background(), Verdict{EventID: 1, State: StateBlocked, Reason: ReasonReadFailed})
	require.ErrorContains(t, err, "ledger wrote admitted for a blocked verdict")

	_, err = NewCommitter(contraryLedger{write: StateQueued}).Commit(context.Background(), Verdict{EventID: 1, State: StateDiscarded, Reason: ReasonStale})
	require.Error(t, err)
}

func TestOneVerdictPerEvent(t *testing.T) {
	ledger := newFakeLedger()
	c := NewCommitter(ledger)
	v := Verdict{EventID: 7, State: StateAdmitted, ConversationKey: "recording:1"}

	_, err := c.Commit(context.Background(), v)
	require.NoError(t, err)
	_, err = c.Commit(context.Background(), v)
	require.ErrorIs(t, err, ErrAlreadyDecided, "a second decision from the same load")

	// A blocked record is decided again from its new revision.
	_, err = c.Commit(context.Background(), Verdict{EventID: 8, State: StateBlocked, Reason: ReasonNoRoute})
	require.NoError(t, err)
	_, err = c.Commit(context.Background(), Verdict{EventID: 8, Revision: 1, State: StateAdmitted, ConversationKey: "recording:2"})
	require.NoError(t, err)
}

func TestAnOlderDecisionNeverOverwritesANewerBlockedOne(t *testing.T) {
	ledger := newFakeLedger()
	c := NewCommitter(ledger)

	// Two decisions loaded the record at the same revision; the one that
	// finished second decided on an older read.
	_, err := c.Commit(context.Background(), Verdict{EventID: 9, State: StateBlocked, Reason: ReasonNoRoute})
	require.NoError(t, err)
	_, err = c.Commit(context.Background(), Verdict{EventID: 9, State: StateDiscarded, Reason: ReasonNotAddressed})
	require.ErrorIs(t, err, ErrAlreadyDecided)
	assert.Equal(t, StateBlocked, ledger.decided[9])
}

func TestAnInterruptedDecisionIsNotWritten(t *testing.T) {
	ledger := newFakeLedger()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewCommitter(ledger).Commit(ctx, Verdict{EventID: 1, State: StateAdmitted})
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, ledger.decided)
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
	// A free lock is not taken by a caller already canceled, however select
	// picks between its ready cases.
	ctx0, cancel0 := context.WithCancel(context.Background())
	cancel0()
	for range 100 {
		if release, err := k.lock(ctx0, "free"); err == nil {
			release()
			t.Fatal("a canceled caller took a free lock")
		}
	}

	unlock, err := k.lock(context.Background(), "k")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := make(chan error, 1)
	go func() {
		_, err := k.lock(ctx, "k")
		result <- err
	}()
	select {
	case err = <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("a canceled wait for a held lock did not return")
	}

	unlock()
	k.mu.Lock()
	assert.Empty(t, k.locks)
	k.mu.Unlock()
}

func TestNextBlockedRetry(t *testing.T) {
	blockedAt := testNow

	next, ok := NextBlockedRetry(ReasonReadFailed, blockedAt, blockedAt, time.Time{})
	require.True(t, ok)

	_, ok = NextBlockedRetry(ReasonBucketMismatch, blockedAt, blockedAt, time.Time{})
	assert.False(t, ok, "the pointer's bucket never changes; only a redispatch re-runs it")
	_, ok = NextBlockedRetry(ReasonUnroutable, blockedAt, blockedAt, time.Time{})
	assert.False(t, ok, "no timer can give a type a read")
	assert.Equal(t, blockedAt.Add(10*time.Minute), next)

	next, ok = NextBlockedRetry(ReasonDeltaUnverified, blockedAt, blockedAt.Add(23*time.Hour+50*time.Minute), time.Time{})
	require.True(t, ok, "the last retry inside the day")
	assert.Equal(t, blockedAt.Add(24*time.Hour), next)

	_, ok = NextBlockedRetry(ReasonReadUnresolved, blockedAt, blockedAt.Add(24*time.Hour), time.Time{})
	assert.False(t, ok, "after a day only a redispatch re-runs it")

	next, ok = NextBlockedRetry(ReasonThrottled, blockedAt, blockedAt, blockedAt.Add(3*time.Minute))
	require.True(t, ok)
	assert.Equal(t, blockedAt.Add(10*time.Minute), next, "a short throttle keeps the ordinary interval")
	next, ok = NextBlockedRetry(ReasonThrottled, blockedAt, blockedAt, blockedAt.Add(45*time.Minute))
	require.True(t, ok)
	assert.Equal(t, blockedAt.Add(45*time.Minute), next, "a long one is waited out")

	_, ok = NextBlockedRetry(ReasonNoRoute, blockedAt, blockedAt, time.Time{})
	assert.False(t, ok, "no_route waits for connect.json, not for time")
}

type sliceSource struct {
	mu      sync.Mutex
	ids     []int64
	waiting bool
}

// drained reports whether every id was taken and the taker came back for more,
// so the last id's work is done.
func (s *sliceSource) drained() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ids) == 0 && s.waiting
}

func (s *sliceSource) Take(ctx context.Context) (int64, error) {
	s.mu.Lock()
	if len(s.ids) > 0 {
		id := s.ids[0]
		s.ids = s.ids[1:]
		s.mu.Unlock()
		return id, nil
	}
	s.waiting = true
	s.mu.Unlock()
	<-ctx.Done()
	return 0, ctx.Err()
}

type mapRecords map[int64]Event

func (m mapRecords) LoadUndecided(_ context.Context, id int64) (Event, bool, error) {
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
	source := &sliceSource{ids: []int64{3, 1, 2, 1}}

	records := mapRecords{
		1: {ID: 1, EventType: "card.created", BucketID: routedProj, RecordingID: recordingID, CreatorID: operatorID},
		2: {ID: 2, EventType: "card.moved", BucketID: routedProj, RecordingID: recordingID, CreatorID: operatorID},
		// 3 is no longer seen: skipped.
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		// One worker, and the record no longer seen first: were it decided,
		// its line would be one of the two this test waits for.
		done <- Run(ctx, RunOptions{
			// 1 again at the end: decided once, reported once.
			Source:    source,
			Records:   records,
			Admitter:  newAdmitter(t, basePolicy(), f),
			Committer: NewCommitter(ledger),
			Workers:   1,
			Lines:     out,
		})
	}()
	require.Eventually(t, func() bool {
		ledger.mu.Lock()
		defer ledger.mu.Unlock()
		return len(ledger.decided) == 2 && source.drained()
	}, 2*time.Second, 5*time.Millisecond)
	cancel()
	require.NoError(t, <-done)

	assert.Equal(t, 2, strings.Count(out.String(), "\n"), "one line per decided event")
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
	assert.NotContains(t, lines, int64(3))
	ledger.mu.Lock()
	assert.Len(t, ledger.commits, 2)
	ledger.mu.Unlock()
}

type failingRecords struct{}

func (failingRecords) LoadUndecided(context.Context, int64) (Event, bool, error) {
	return Event{}, false, errors.New("database is locked")
}

func TestRunDistinguishesShutdownFromADeadline(t *testing.T) {
	opts := func() RunOptions {
		return RunOptions{
			Source:    &sliceSource{},
			Records:   mapRecords{},
			Admitter:  newAdmitter(t, basePolicy(), newFakeReads()),
			Committer: NewCommitter(newFakeLedger()),
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	require.NoError(t, Run(ctx, opts()), "a shutdown is not an error")

	dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer dcancel()
	require.ErrorIs(t, Run(dctx, opts()), context.DeadlineExceeded)
}

type chunkWriter struct {
	b     strings.Builder
	chunk int
}

func (w *chunkWriter) Write(p []byte) (int, error) {
	n := min(w.chunk, len(p))
	w.b.Write(p[:n])
	return n, nil
}

type stuckWriter struct{}

func (stuckWriter) Write([]byte) (int, error) { return 0, nil }

func TestALineIsWrittenWholeOrNotAtAll(t *testing.T) {
	w := &chunkWriter{chunk: 7}
	require.NoError(t, newLineWriter(nil, w).write(Verdict{EventID: 1, EventType: "card.created", State: StateDiscarded, Reason: ReasonNotInMatrix}))
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSuffix(w.b.String(), "\n")), &m), "a writer that takes a line in pieces still gets all of it")
	assert.True(t, strings.HasSuffix(w.b.String(), "}\n"))

	// Bounded: a writer loop that forgot this case would spin, and should fail
	// this test by name rather than hang the suite.
	done := make(chan error, 1)
	go func() { done <- newLineWriter(nil, stuckWriter{}).write(Verdict{EventID: 1, State: StateDiscarded}) }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, io.ErrShortWrite)
	case <-time.After(2 * time.Second):
		t.Fatal("a writer that takes nothing was retried forever")
	}
}

func TestLinesCannotCarryTerminalControls(t *testing.T) {
	esc, csi, bel := string(rune(0x1b)), string(rune(0x9b)), string(rune(0x07))
	var b strings.Builder
	require.NoError(t, newLineWriter(nil, &b).write(Verdict{
		EventID:      1,
		EventType:    "card.created" + esc + "]0;owned" + bel,
		RecordingURL: "https://app.basecamp.com/x" + csi + "31m" + esc + "[2J",
		Route:        "/work" + esc + "[1m",
		State:        StateDiscarded,
		Reason:       ReasonNotInMatrix,
	}))
	for _, control := range []string{esc, csi, bel} {
		assert.NotContains(t, b.String(), control)
	}
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(b.String()), &m))
	assert.Equal(t, "card.created", m["event_type"], "the whole OSC sequence goes, payload included")
}

func TestRunStopsOnACommitFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ledger := newFakeLedger()
	ledger.failure = errors.New("disk I/O error")
	err := Run(ctx, RunOptions{
		Source:    &sliceSource{ids: []int64{1}},
		Records:   mapRecords{1: {ID: 1, EventType: "card.moved", BucketID: routedProj, RecordingID: recordingID, CreatorID: operatorID}},
		Admitter:  newAdmitter(t, basePolicy(), newFakeReads()),
		Committer: NewCommitter(ledger),
	})
	require.ErrorContains(t, err, "disk I/O error")
}

func TestRunStopsOnALoadFailure(t *testing.T) {
	// Bounded, so a Run that swallowed the failure fails this test by name
	// rather than hanging the suite.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := Run(ctx, RunOptions{
		Source:    &sliceSource{ids: []int64{1}},
		Records:   failingRecords{},
		Admitter:  newAdmitter(t, basePolicy(), newFakeReads()),
		Committer: NewCommitter(newFakeLedger()),
	})
	require.ErrorContains(t, err, "database is locked")
}

func TestLinesKeepLocalPathsAsWritten(t *testing.T) {
	var b strings.Builder
	require.NoError(t, newLineWriter(nil, &b).write(Verdict{EventID: 1, State: StateAdmitted, Route: " /work/My  Projects\tA", Class: "in ternal"}))
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(b.String()), &m))
	assert.Equal(t, " /work/My  Projects\tA", m["route"], "whitespace in a path is not a terminal control")
	assert.Equal(t, "in ternal", m["class"])
}

// A sink that is safe for concurrent use, so the only race a run can report is
// one of admission's own making.
type lockedSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *lockedSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

// The verdict writer is built before any worker exists. Built on first use, a
// pool of workers reaching their first lines together would read and assign it
// at once — a data race on the ordinary path, not an edge case.
func TestTheVerdictWriterIsBuiltBeforeAnyWorkerWrites(t *testing.T) {
	sink := &lockedSink{}
	lines := newLineWriter(nil, sink)

	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Go(func() {
			for i := range 25 {
				require.NoError(t, lines.write(Verdict{
					EventID:   int64(worker*100 + i),
					EventType: "comment.created",
					State:     StateAdmitted,
				}))
			}
		})
	}
	wg.Wait()

	scanner := bufio.NewScanner(bytes.NewReader(sink.buf.Bytes()))
	count := 0
	for scanner.Scan() {
		var line map[string]any
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &line), "torn line %d: %q", count, scanner.Text())
		count++
	}
	require.NoError(t, scanner.Err())
	assert.Equal(t, 200, count)
}

// One sink has one writer, whoever asks for it: the lock that keeps lines
// whole is only one lock if there is only one of it.
func TestOneSinkHasOneWriter(t *testing.T) {
	sink := &lockedSink{}

	first := newLineWriter(nil, sink)
	second := newLineWriter(nil, sink)

	assert.Same(t, first.out, second.out)
}
