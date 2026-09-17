//go:build unix

package connector

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

// Intake's recovery, with the connector killed around the feed: a filter
// change, a saturated backlog, a live buffer overflow and a stalled repair
// walk. None of it depends on the driver, so these run once, with the first
// registered one.

func intakeHarness(t *testing.T, sc harnessScenario) *harness {
	t.Helper()
	if testing.Short() {
		t.Skip("starts processes")
	}
	raceSubset(t, false)
	require.NotEmpty(t, harnessDrivers)
	return newHarness(t, harnessDrivers[0], sc)
}

// strangerEvent is an event by someone the agent does not trust: intake records
// it and admission discards it without a read, so a test can push thousands.
func strangerEvent(id int64) eventfeed.Event {
	e := todoEvent(id, 9000+id%1000)
	e.CreatorID = 4242
	return e
}

func strangers(from, to int64, tweak func(*feedEntry)) []feedEntry {
	out := make([]feedEntry, 0, to-from+1)
	for id := from; id <= to; id++ {
		e := feedEntry{Event: strangerEvent(id)}
		if tweak != nil {
			tweak(&e)
		}
		out = append(out, e)
	}
	return out
}

func (h *harness) feedKey(filters eventfeed.Filters) eventfeed.CheckpointKey {
	h.t.Helper()
	origin, err := eventfeed.CanonicalOrigin(harnessOrigin)
	require.NoError(h.t, err)
	return eventfeed.CheckpointKey{Origin: origin, AccountID: harnessAccount, ConsumerNamespace: harnessNamespace, FilterKey: filters.FilterKey()}
}

// positionID is the id a stored feed position is after; zero for none.
func (h *harness) positionID(l *Ledger, filters eventfeed.Filters) int64 {
	h.t.Helper()
	position, ok, err := l.Load(context.Background(), h.feedKey(filters))
	require.NoError(h.t, err)
	if !ok {
		return 0
	}
	require.True(h.t, strings.HasPrefix(position, "feed-"), "the feed's checkpoint is a feed position, never a repair walk's: %q", position)
	id, err := strconv.ParseInt(strings.TrimPrefix(position, "feed-"), 10, 64)
	require.NoError(h.t, err)
	return id
}

func (h *harness) feedPolls() []pollLog {
	var out []pollLog
	for _, p := range h.polls() {
		if !p.Repair {
			out = append(out, p)
		}
	}
	return out
}

// A filter change re-enters after the last poll-served id, not at the present,
// and a crash before the new filter set saves a position re-enters there again.
func TestRecoveryAFilterChangeResumesFromTheLastPollServedID(t *testing.T) {
	h := intakeHarness(t, harnessScenario{})
	h.publish(strangers(101, 103, nil)...)
	h.run(harnessRun{})

	h.publish(strangers(104, 105, nil)...)
	narrowed := eventfeed.Filters{Buckets: []int64{harnessBucket}}
	raw, err := json.Marshal(narrowed)
	require.NoError(t, err)
	before := len(h.feedPolls())
	h.run(harnessRun{Filters: string(raw), Kill: "feed-poll", Killed: true})
	l := h.ledger()
	assert.Zero(t, h.positionID(l, narrowed), "killed before the new filter set polled")

	h.run(harnessRun{Filters: string(raw)})
	polls := h.feedPolls()[before:]
	require.NotEmpty(t, polls)
	assert.Equal(t, "103", polls[0].Since, "entered after the last id the poll lane served under the old filters")
	assert.Empty(t, polls[0].Position)
	for _, id := range []int64{104, 105} {
		_, ok, err := l.Get(context.Background(), id)
		require.NoError(t, err)
		assert.True(t, ok, "event %d after the filter change is not skipped", id)
	}
	assert.Equal(t, int64(105), h.positionID(l, narrowed))
}

// A saturated backlog stops intake reading the feed without moving the
// checkpoint past what admission has not taken; a crash there loses nothing.
func TestRecoveryBacklogSaturationPausesTheFeedWithoutMovingTheCheckpoint(t *testing.T) {
	var gate []int64
	var events []feedEntry
	for id := int64(101); id <= 110; id++ {
		recording := 5000 + id
		gate = append(gate, recording)
		events = append(events, feedEntry{Event: todoEvent(id, recording)})
	}
	h := intakeHarness(t, harnessScenario{QueueWarn: 1, QueuePause: 2, ReadGate: gate})
	h.publish(events...)
	h.run(harnessRun{Kill: "paused", Killed: true})

	l := h.ledger()
	assert.Zero(t, h.positionID(l, eventfeed.Filters{}), "the page behind the pause was never checkpointed")
	served, err := l.LastPollServedID(context.Background(), h.feedKey(eventfeed.Filters{}))
	require.NoError(t, err)
	assert.Zero(t, served)
	assert.Empty(t, harnessAttempts(t, l))

	h.sc.ReadGate = nil
	h.writeScenario()
	h.run(harnessRun{})
	for _, e := range events {
		assert.Equal(t, 1, h.handed(e.Event.ID), "event %d", e.Event.ID)
		assert.Equal(t, string(OutcomeSucceeded), outcomeOf(t, l, e.Event.ID), "event %d", e.Event.ID)
	}
	assert.Equal(t, int64(110), h.positionID(l, eventfeed.Filters{}))
}

// A live buffer overflow is on disk before it is accepted, the retained events
// are drained, and the repair walk recovers the dropped ids on its own cursor:
// the feed's checkpoint never moves to a live id, so the unpolled range behind
// it is still served; a crash between the signal and the walk resumes the walk
// on start; the walk repeats through the safety delay, and what it never
// serves is recorded unrecovered once the window closes.
func TestRecoveryABufferOverflowIsReconciledAcrossACrash(t *testing.T) {
	// The buffer reports each drop as it happens: two drops, two losses.
	h := intakeHarness(t, harnessScenario{RepairWindow: 1500 * time.Millisecond, OverflowLosses: 2})
	h.publish(strangers(101, 103, nil)...)
	h.run(harnessRun{})

	const (
		straggler = int64(60_001) // dropped; poll-visible from the third repair poll
		deleted   = int64(60_002) // dropped; never poll-visible
		lastLive  = int64(70_002)
	)
	// Behind the live burst, an unpolled range the feed has not served yet.
	behind := strangers(104, 106, func(e *feedEntry) { e.FromRepairPoll = 1 })
	// Ten thousand and two live events: the buffer holds ten thousand, and
	// drops the two oldest.
	burst := strangers(straggler, lastLive, func(e *feedEntry) {
		e.Live, e.FromRepairPoll = true, 1
		switch e.Event.ID {
		case straggler:
			e.FromRepairPoll = 3
		case deleted:
			e.Never = true
		}
	})
	h.publish(append(behind, burst...)...)
	h.run(harnessRun{Fault: "stall-catch-up", Kill: "repair-poll", Killed: true})

	l := h.ledger()
	losses, err := l.OpenLosses(context.Background())
	require.NoError(t, err)
	require.Len(t, losses, 2, "the overflow was written down before the walk began")
	var missing []int64
	for _, loss := range losses {
		ids, err := l.MissingIDs(context.Background(), loss.ID, LossMissing)
		require.NoError(t, err)
		require.Len(t, ids, 1)
		assert.Equal(t, ids[0]-1, loss.RepairSince, "a walk enters just before its missing id")
		missing = append(missing, ids...)
	}
	slices.Sort(missing)
	assert.Equal(t, []int64{straggler, deleted}, missing)
	assert.LessOrEqual(t, h.positionID(l, eventfeed.Filters{}), int64(103), "no live id moved the checkpoint")

	// Every checkpoint the ledger holds while the walk runs, not only the one
	// it ends with: a walk that wrote its own cursor there would be overwritten
	// by the feed's next page.
	positions := h.watchCheckpoints()
	h.run(harnessRun{Until: "losses-closed"})
	for _, position := range positions() {
		assert.True(t, strings.HasPrefix(position, "feed-"), "the feed's checkpoint only ever holds a feed position, saw %q", position)
	}

	for _, id := range []int64{104, 105, 106} {
		r, ok, err := l.Get(context.Background(), id)
		require.NoError(t, err)
		require.True(t, ok, "event %d behind the live burst is still served", id)
		assert.Equal(t, LanePoll, r.Lane, "event %d came from the feed's own walk", id)
	}
	r, ok, err := l.Get(context.Background(), straggler)
	require.NoError(t, err)
	require.True(t, ok, "the straggler was recovered")
	unrecovered, err := l.UnrecoveredIDs(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []int64{deleted}, unrecovered)
	assert.Equal(t, LaneRepair, r.Lane, "the straggler came from the repair walk")
	// The checkpoint is the feed's own walk, wherever the repair walk got to.
	assert.Equal(t, lastLive, h.positionID(l, eventfeed.Filters{}))

	var walks, servedAt int
	for _, p := range h.polls() {
		if !p.Repair {
			assert.False(t, strings.HasPrefix(p.Position, "repair-"), "the feed never walks from a repair cursor")
			continue
		}
		walks++
		if slices.Contains(p.Served, straggler) && servedAt == 0 {
			servedAt = walks
		}
	}
	assert.Equal(t, 3, servedAt, "the walk repeated through the safety delay until the straggler was served")
	assert.Greater(t, walks, 3, "and kept repeating until the window closed")
	for _, p := range h.feedPolls() {
		if p.Position == "" {
			continue
		}
		id, _ := strconv.ParseInt(strings.TrimPrefix(p.Position, "feed-"), 10, 64)
		assert.False(t, id >= straggler && id < 104, "the feed never jumped a live id ahead of the range behind it")
	}
}

// watchCheckpoints samples the feed's stored position until the returned
// function is called, which returns everything it saw.
func (h *harness) watchCheckpoints() func() []string {
	stop := make(chan struct{})
	done := make(chan []string, 1)
	go func() {
		seen := map[string]bool{}
		for {
			select {
			case <-stop:
				out := make([]string, 0, len(seen))
				for position := range seen {
					out = append(out, position)
				}
				done <- out
				return
			case <-time.After(2 * time.Millisecond):
			}
			l, err := OpenLedgerReadOnly(context.Background(), filepath.Join(h.dir, LedgerFile))
			if err != nil {
				continue
			}
			rows, err := l.db.QueryContext(context.Background(), `SELECT position FROM checkpoints`)
			if err == nil {
				for rows.Next() {
					var position string
					if rows.Scan(&position) == nil {
						seen[position] = true
					}
				}
				_ = rows.Close()
			}
			_ = l.Close()
		}
	}()
	return func() []string {
		close(stop)
		select {
		case out := <-done:
			return out
		case <-time.After(10 * time.Second):
			h.t.Fatal("the checkpoint watcher did not stop")
			return nil
		}
	}
}

// A repair walk that never answers holds up nothing: live events still reach
// the ledger while the loss stays open.
func TestRecoveryAStalledRepairWalkDoesNotStopLiveIntake(t *testing.T) {
	h := intakeHarness(t, harnessScenario{RepairWindow: time.Hour})
	h.publish(strangers(101, 101, nil)...)
	h.run(harnessRun{})

	l := h.ledger()
	_, err := l.RecordLoss(context.Background(), []int64{90_001}, time.Now(), time.Hour, eventfeed.Filters{})
	require.NoError(t, err)
	h.publish(strangers(90_005, 90_005, func(e *feedEntry) { e.Live, e.Never = true, true })...)
	h.publish(strangers(102, 102, nil)...)
	h.run(harnessRun{Fault: "repair-stall", Until: "state:90005,state:102"})

	losses, err := l.OpenLosses(context.Background())
	require.NoError(t, err)
	assert.Len(t, losses, 1, "the loss is still open")
	stalled := false
	for _, p := range h.polls() {
		stalled = stalled || p.Stalled
	}
	assert.True(t, stalled, "the repair walk was running, and stalled")
}
