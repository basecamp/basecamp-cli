package connector

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/ndjson"
)

// The join between intake's ledger and admission. Admission's own tests hold
// its contract against a fake; these hold the real ledger to the same one.

const (
	adapterAgentID    int64 = 52007412
	adapterOperatorID int64 = 26909558
	adapterBucketID   int64 = 48699913
)

func seenRecord(t *testing.T, ledger *Ledger, id int64) Record {
	t.Helper()
	ctx := context.Background()
	_, err := ledger.RecordSeen(ctx, testEvent(id), LanePoll)
	require.NoError(t, err)
	record, ok, err := ledger.Get(ctx, id)
	require.NoError(t, err)
	require.True(t, ok)
	return record
}

func getRecord(t *testing.T, ledger *Ledger, id int64) Record {
	t.Helper()
	record, ok, err := ledger.Get(context.Background(), id)
	require.NoError(t, err)
	require.True(t, ok)
	return record
}

func admittedVerdict(id, revision int64, key string) admission.Verdict {
	return admission.Verdict{
		EventID:         id,
		EventType:       "comment.created",
		BucketID:        adapterBucketID,
		RecordingID:     10304028972,
		Revision:        revision,
		RequesterID:     adapterOperatorID,
		State:           admission.StateAdmitted,
		Trigger:         admission.TriggerMentioned,
		Acknowledge:     true,
		ConversationKey: key,
		Reply:           &admission.ReplyDestination{Kind: admission.ReplyComment, RecordingID: 10304028989},
		Routed:          true,
		Route:           "/work/connector",
		Class:           "internal",
		RecordingURL:    "https://app.basecamp.com/2914079/buckets/48699913/recordings/10304028972",
		Snapshot: &admission.Snapshot{
			Type:      "Comment",
			Title:     "A comment",
			AppURL:    "https://app.basecamp.com/2914079/buckets/48699913/recordings/10304028972",
			Content:   "<div>please look</div>",
			UpdatedAt: time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC),
		},
	}
}

func blockedVerdict(id, revision int64, reason admission.Reason) admission.Verdict {
	return admission.Verdict{
		EventID: id, EventType: "comment.created", BucketID: adapterBucketID,
		RecordingID: 10304028972, Revision: revision, RequesterID: adapterOperatorID,
		State: admission.StateBlocked, Reason: reason,
	}
}

func TestAdmissionLoadsAnUndecidedRecordAtItsRevision(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	seen := seenRecord(t, ledger, 1)
	store := ledger.Admission()

	ev, ok, err := store.LoadUndecided(ctx, 1)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, admission.Event{
		ID: 1, EventType: "comment.created", BucketID: adapterBucketID, RecordingID: 10304028972,
		CreatorID: adapterOperatorID, Revision: 0, SeenAt: seen.SeenAt,
	}, ev)
	assert.False(t, ev.SeenAt.IsZero(), "admission dates membership refusals from SeenAt")

	// A blocked record is decided again, at the revision its verdict left.
	_, err = store.Commit(ctx, blockedVerdict(1, 0, admission.ReasonReadFailed))
	require.NoError(t, err)
	ev, ok, err = store.LoadUndecided(ctx, 1)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, int64(1), ev.Revision)

	_, ok, err = store.LoadUndecided(ctx, 404)
	require.NoError(t, err)
	assert.False(t, ok, "an unknown id is skipped")
}

func TestAdmissionSkipsARecordPastDeciding(t *testing.T) {
	for _, state := range []RecordState{StateAdmitted, StateQueued, StateDispatched, StateCompleted, StateDiscarded} {
		t.Run(string(state), func(t *testing.T) {
			ledger := newTestLedger(t)
			ctx := context.Background()
			seenRecord(t, ledger, 1)
			switch state {
			case StateDiscarded:
				require.NoError(t, ledger.SetState(ctx, 1, StateDiscarded, "untrusted_author"))
			case StateCompleted:
				require.NoError(t, reachTerminal(t, ledger, 7, StateCompleted))
				require.NoError(t, ledger.SetState(ctx, 1, StateAdmitted, ""))
				require.NoError(t, ledger.SetState(ctx, 1, StateDispatched, ""))
				require.NoError(t, ledger.SetState(ctx, 1, StateCompleted, ""))
			case StateDispatched:
				require.NoError(t, ledger.SetState(ctx, 1, StateAdmitted, ""))
				require.NoError(t, ledger.SetState(ctx, 1, StateDispatched, ""))
			default:
				require.NoError(t, ledger.SetState(ctx, 1, state, ""))
			}

			_, ok, err := ledger.Admission().LoadUndecided(ctx, 1)
			require.NoError(t, err)
			assert.False(t, ok)
		})
	}
}

func TestAdmissionCommitWritesTheVerdictOntoTheRecord(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC)
	ledger.now = func() time.Time { return at }
	seenRecord(t, ledger, 1)
	v := admittedVerdict(1, 0, "recording:10304028989")

	written, err := ledger.Admission().Commit(ctx, v)
	require.NoError(t, err)
	assert.Equal(t, admission.StateAdmitted, written)

	record := getRecord(t, ledger, 1)
	assert.Equal(t, StateAdmitted, record.State)
	assert.Empty(t, record.Reason)
	assert.Equal(t, int64(1), record.Revision)
	d := record.Decision
	require.NotNil(t, d.DecidedAt)
	assert.True(t, at.Equal(*d.DecidedAt))
	assert.Nil(t, d.BlockedAt)
	assert.Nil(t, d.RetryAt)
	assert.Equal(t, "mentioned", d.Trigger)
	assert.True(t, d.Acknowledge)
	assert.Equal(t, "recording:10304028989", d.ConversationKey)
	assert.Equal(t, "comment", d.ReplyKind)
	assert.Equal(t, int64(10304028989), d.ReplyRecordingID)
	assert.True(t, d.Routed)
	assert.Equal(t, "/work/connector", d.Route)
	assert.Equal(t, "internal", d.Class)
	assert.Equal(t, v.RecordingURL, d.RecordingURL)
	assert.Equal(t, adapterOperatorID, d.RequesterID)

	var snapshot admission.Snapshot
	require.NoError(t, json.Unmarshal(d.Snapshot, &snapshot))
	assert.Equal(t, *v.Snapshot, snapshot)
}

// Invariant 5 of admission: one verdict per event. The same decision arriving
// twice — two fetchers, a restart — is written once.
func TestAdmissionCommitIsOneVerdictPerEvent(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	seenRecord(t, ledger, 1)
	store := ledger.Admission()

	_, err := store.Commit(ctx, admittedVerdict(1, 0, "recording:9"))
	require.NoError(t, err)
	_, err = store.Commit(ctx, admittedVerdict(1, 0, "recording:9"))

	require.ErrorIs(t, err, admission.ErrAlreadyDecided)
	record := getRecord(t, ledger, 1)
	assert.Equal(t, StateAdmitted, record.State, "not re-queued behind itself")
	assert.Equal(t, int64(1), record.Revision)
}

func TestAdmissionAnOlderDecisionNeverOverwritesANewer(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	seenRecord(t, ledger, 1)
	store := ledger.Admission()

	// Two decisions loaded the record at revision 0; the blocked one lands
	// first.
	_, err := store.Commit(ctx, blockedVerdict(1, 0, admission.ReasonReadFailed))
	require.NoError(t, err)

	_, err = store.Commit(ctx, admittedVerdict(1, 0, "recording:9"))
	require.ErrorIs(t, err, admission.ErrAlreadyDecided)
	record := getRecord(t, ledger, 1)
	assert.Equal(t, StateBlocked, record.State, "a blocked verdict stands against an older decision too")
	assert.Equal(t, "read_failed", record.Reason)
	assert.Empty(t, record.Decision.Snapshot)

	// A decision loaded after it applies.
	written, err := store.Commit(ctx, admittedVerdict(1, 1, "recording:9"))
	require.NoError(t, err)
	assert.Equal(t, admission.StateAdmitted, written)
	assert.Empty(t, getRecord(t, ledger, 1).Reason)
}

// Every state change bumps the revision, not only admission's: a decision
// loaded before anything else moved the record is stale.
func TestAdmissionAnyMoveStalesALoadedDecision(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	seenRecord(t, ledger, 1)
	store := ledger.Admission()
	ev, ok, err := store.LoadUndecided(ctx, 1)
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, ledger.SetState(ctx, 1, StateBlocked, "no_route"))

	_, err = store.Commit(ctx, admittedVerdict(1, ev.Revision, "recording:9"))
	require.ErrorIs(t, err, admission.ErrAlreadyDecided)
	assert.Equal(t, StateBlocked, getRecord(t, ledger, 1).State)
}

// A record admission already decided, or dispatch has moved on, is never
// decided again — even by a verdict carrying its current revision.
func TestAdmissionNeverDecidesARecordPastDeciding(t *testing.T) {
	cases := map[string]func(*testing.T, *Ledger){
		"admitted": func(t *testing.T, l *Ledger) {
			require.NoError(t, l.SetState(context.Background(), 1, StateAdmitted, ""))
		},
		"dispatched": func(t *testing.T, l *Ledger) {
			require.NoError(t, l.SetState(context.Background(), 1, StateAdmitted, ""))
			require.NoError(t, l.SetState(context.Background(), 1, StateDispatched, ""))
		},
		"discarded": func(t *testing.T, l *Ledger) {
			require.NoError(t, l.SetState(context.Background(), 1, StateDiscarded, "by_operator"))
		},
	}
	for name, reach := range cases {
		t.Run(name, func(t *testing.T) {
			ledger := newTestLedger(t)
			ctx := context.Background()
			seenRecord(t, ledger, 1)
			reach(t, ledger)
			before := getRecord(t, ledger, 1)

			for _, v := range []admission.Verdict{
				admittedVerdict(1, before.Revision, "recording:9"),
				blockedVerdict(1, before.Revision, admission.ReasonReadFailed),
			} {
				_, err := ledger.Admission().Commit(ctx, v)
				require.ErrorIs(t, err, admission.ErrAlreadyDecided)
			}
			after := getRecord(t, ledger, 1)
			assert.Equal(t, before.State, after.State)
			assert.Equal(t, before.Revision, after.Revision)
		})
	}
}

func TestAdmissionCommitOnAMissingRecord(t *testing.T) {
	_, err := newTestLedger(t).Admission().Commit(context.Background(), blockedVerdict(404, 0, admission.ReasonReadFailed))
	assert.ErrorIs(t, err, ErrNoSuchRecord)
	assert.NotErrorIs(t, err, admission.ErrAlreadyDecided)
}

// Admitted or queued is the ledger's decision, taken against the conversation
// as it stands in the verdict's own transaction.
func TestAdmissionQueuesBehindALiveConversation(t *testing.T) {
	const key = "recording:10304028989"
	cases := []struct {
		name  string
		other func(*testing.T, *Ledger)
		want  admission.State
	}{
		{"nothing else on the key", nil, admission.StateAdmitted},
		{"an admitted record becomes the task", func(t *testing.T, l *Ledger) {
			_, err := l.Admission().Commit(context.Background(), admittedVerdict(2, 0, key))
			require.NoError(t, err)
		}, admission.StateQueued},
		{"a dispatched record is a running task", func(t *testing.T, l *Ledger) {
			_, err := l.Admission().Commit(context.Background(), admittedVerdict(2, 0, key))
			require.NoError(t, err)
			require.NoError(t, l.SetState(context.Background(), 2, StateDispatched, ""))
		}, admission.StateQueued},
		{"a queued record waits on a live task", func(t *testing.T, l *Ledger) {
			_, err := l.Admission().Commit(context.Background(), admittedVerdict(2, 0, key))
			require.NoError(t, err)
			_, err = l.Admission().Commit(context.Background(), admittedVerdict(3, 0, key))
			require.NoError(t, err)
			require.NoError(t, l.SetState(context.Background(), 2, StateDispatched, ""))
			require.NoError(t, l.SetState(context.Background(), 2, StateCompleted, ""))
		}, admission.StateQueued},
		{"a completed task is not live", func(t *testing.T, l *Ledger) {
			_, err := l.Admission().Commit(context.Background(), admittedVerdict(2, 0, key))
			require.NoError(t, err)
			require.NoError(t, l.SetState(context.Background(), 2, StateDispatched, ""))
			require.NoError(t, l.SetState(context.Background(), 2, StateCompleted, ""))
		}, admission.StateAdmitted},
		{"a blocked record is not a task", func(t *testing.T, l *Ledger) {
			v := admittedVerdict(2, 0, key)
			v.State, v.Reason, v.Snapshot = admission.StateBlocked, admission.ReasonNoRoute, nil
			_, err := l.Admission().Commit(context.Background(), v)
			require.NoError(t, err)
		}, admission.StateAdmitted},
		{"a live task on another conversation", func(t *testing.T, l *Ledger) {
			_, err := l.Admission().Commit(context.Background(), admittedVerdict(2, 0, "recording:1"))
			require.NoError(t, err)
		}, admission.StateAdmitted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ledger := newTestLedger(t)
			seenRecord(t, ledger, 1)
			seenRecord(t, ledger, 2)
			seenRecord(t, ledger, 3)
			if tc.other != nil {
				tc.other(t, ledger)
			}

			written, err := ledger.Admission().Commit(context.Background(), admittedVerdict(1, 0, key))
			require.NoError(t, err)
			assert.Equal(t, tc.want, written)
			assert.Equal(t, RecordState(tc.want), getRecord(t, ledger, 1).State)

			// Through the committer too, which reports what the ledger wrote.
		})
	}
}

// Only an admitted verdict carries content, and a verdict the ledger refuses
// writes nothing at all.
func TestAdmissionRefusesAMalformedVerdictWithoutWriting(t *testing.T) {
	blockedWithContent := blockedVerdict(1, 0, admission.ReasonNoRoute)
	blockedWithContent.Snapshot = admittedVerdict(1, 0, "k").Snapshot
	discardedWithContent := blockedWithContent
	discardedWithContent.State, discardedWithContent.Reason = admission.StateDiscarded, admission.ReasonStale
	admittedWithout := admittedVerdict(1, 0, "recording:9")
	admittedWithout.Snapshot = nil
	admittedNoKey := admittedVerdict(1, 0, "")
	queued := admittedVerdict(1, 0, "recording:9")
	queued.State, queued.Snapshot = admission.StateQueued, nil
	blockedNoReason := blockedVerdict(1, 0, "")

	for name, v := range map[string]admission.Verdict{
		"blocked with content":     blockedWithContent,
		"discarded with content":   discardedWithContent,
		"admitted without content": admittedWithout,
		"admitted without a key":   admittedNoKey,
		"queued is not a verdict":  queued,
		"blocked without a reason": blockedNoReason,
	} {
		t.Run(name, func(t *testing.T) {
			ledger := newTestLedger(t)
			seenRecord(t, ledger, 1)

			_, err := ledger.Admission().Commit(context.Background(), v)

			require.Error(t, err)
			assert.NotErrorIs(t, err, admission.ErrAlreadyDecided)
			record := getRecord(t, ledger, 1)
			assert.Equal(t, StateSeen, record.State)
			assert.Equal(t, int64(0), record.Revision)
			assert.Nil(t, record.Decision.DecidedAt)
			assert.Empty(t, record.Decision.Snapshot)
		})
	}
}

// A throttled verdict keeps the server's deadline, and the retry window counts
// from the first of a run of blocked verdicts.
func TestAdmissionKeepsTheBlockedScheduleInputs(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	store := ledger.Admission()
	t0 := time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC)
	ledger.now = func() time.Time { return t0 }
	seenRecord(t, ledger, 1)

	throttled := blockedVerdict(1, 0, admission.ReasonThrottled)
	throttled.RetryAt = t0.Add(7 * time.Minute)
	_, err := store.Commit(ctx, throttled)
	require.NoError(t, err)
	d := getRecord(t, ledger, 1).Decision
	require.NotNil(t, d.RetryAt)
	assert.True(t, throttled.RetryAt.Equal(*d.RetryAt))
	require.NotNil(t, d.BlockedAt)
	assert.True(t, t0.Equal(*d.BlockedAt))

	// Re-run ten minutes later and blocked again, on a read this time.
	t1 := t0.Add(10 * time.Minute)
	ledger.now = func() time.Time { return t1 }
	_, err = store.Commit(ctx, blockedVerdict(1, 1, admission.ReasonReadFailed))
	require.NoError(t, err)
	d = getRecord(t, ledger, 1).Decision
	assert.Nil(t, d.RetryAt, "only a throttled verdict has a deadline")
	require.NotNil(t, d.BlockedAt)
	assert.True(t, t0.Equal(*d.BlockedAt), "the window still counts from the first block")
	require.NotNil(t, d.DecidedAt)
	assert.True(t, t1.Equal(*d.DecidedAt), "the last attempt is the latest verdict")

	// NextBlockedRetry reads what the ledger kept.
	next, ok := admission.NextBlockedRetry(admission.Reason(getRecord(t, ledger, 1).Reason), *d.BlockedAt, *d.DecidedAt, time.Time{})
	require.True(t, ok)
	assert.True(t, t1.Add(admission.BlockedRetryInterval).Equal(next))

	// Admitted at last: no longer blocked, and a deadline is a blocked
	// record's alone.
	admitted := admittedVerdict(1, 2, "recording:9")
	admitted.RetryAt = t1.Add(time.Hour)
	_, err = store.Commit(ctx, admitted)
	require.NoError(t, err)
	d = getRecord(t, ledger, 1).Decision
	assert.Nil(t, d.BlockedAt)
	assert.Nil(t, d.RetryAt)
}

// Two fetchers deciding one event at once: exactly one verdict is written.
func TestAdmissionConcurrentDecisionsWriteOne(t *testing.T) {
	ledger := newTestLedger(t)
	seenRecord(t, ledger, 1)
	store := ledger.Admission()

	const racers = 8
	results := make([]error, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Go(func() {
			v := admittedVerdict(1, 0, "recording:9")
			if i%2 == 1 {
				v = blockedVerdict(1, 0, admission.ReasonReadFailed)
			}
			_, results[i] = store.Commit(context.Background(), v)
		})
	}
	wg.Wait()

	applied := 0
	for _, err := range results {
		if err == nil {
			applied++
			continue
		}
		require.ErrorIs(t, err, admission.ErrAlreadyDecided)
	}
	assert.Equal(t, 1, applied)
	assert.Equal(t, int64(1), getRecord(t, ledger, 1).Revision)
}

// Retention takes the verdict's content with the pointer's.
func TestDropContentTakesTheVerdictToo(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC)
	ledger.now = func() time.Time { return at }
	seenRecord(t, ledger, 1)
	_, err := ledger.Admission().Commit(ctx, admittedVerdict(1, 0, "recording:9"))
	require.NoError(t, err)
	require.NoError(t, ledger.SetState(ctx, 1, StateDispatched, ""))
	require.NoError(t, ledger.SetState(ctx, 1, StateCompleted, ""))

	dropped, err := ledger.DropContent(ctx, at.Add(time.Hour), at.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, dropped)

	record := getRecord(t, ledger, 1)
	assert.Equal(t, StateCompleted, record.State)
	assert.Equal(t, Decision{DecidedAt: record.Decision.DecidedAt}, record.Decision,
		"only the tombstone's timestamps survive")
}

// Migration 4 carries a ledger written before it: its records load at
// revision 0 and are decided like any other.
func TestMigrationFourCarriesAnEarlierLedger(t *testing.T) {
	path := t.TempDir() + "/state/connector.db"
	all := migrations
	migrations = all[:3]
	old, err := OpenLedger(path)
	migrations = all
	require.NoError(t, err)
	_, err = old.RecordSeen(context.Background(), testEvent(1), LanePoll)
	require.NoError(t, err)
	require.NoError(t, old.Close())

	ledger, err := OpenLedger(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ledger.Close() })
	version, err := ledger.SchemaVersion(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 4, version)

	ev, ok, err := ledger.Admission().LoadUndecided(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, int64(0), ev.Revision)
	_, err = ledger.Admission().Commit(context.Background(), admittedVerdict(1, ev.Revision, "recording:9"))
	require.NoError(t, err)
	assert.Equal(t, StateAdmitted, getRecord(t, ledger, 1).State)
}

// The handoff end to end: intake records and offers, admission takes from the
// same queue, decides against the real ledger, and reports on the shared
// writer without content.
func TestRunAdmissionDecidesWhatIntakeHandsOver(t *testing.T) {
	ledger := newTestLedger(t)
	queue, err := NewQueue(10, 100)
	require.NoError(t, err)
	var out bytes.Buffer
	lines := ndjson.NewWriter(&out)

	const secret = "the instruction itself"
	reads := &adapterReads{summaries: map[int64]*basecamp.RecordingSummary{}}
	for _, id := range []int64{501, 502} {
		reads.summaries[id] = &basecamp.RecordingSummary{
			ID: id, Status: "active", Type: "Todo", Title: "A to-do",
			AppURL:             "https://app.basecamp.com/2914079/buckets/48699913/todos/" + strconv.FormatInt(id, 10),
			Bucket:             &basecamp.Bucket{ID: adapterBucketID},
			Creator:            &basecamp.Person{ID: adapterOperatorID},
			Content:            mentionMarkup(adapterAgentID) + secret,
			MentionedPersonIDs: []int64{adapterAgentID},
			UpdatedAt:          time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC),
		}
	}
	admitter, err := admission.NewAdmitter(admission.Policy{
		AgentID:  adapterAgentID,
		Trust:    admission.Trust{Mode: admission.TrustOperator, OperatorID: adapterOperatorID},
		Projects: map[int64]admission.Route{adapterBucketID: {Path: "/work/connector", Class: "internal"}},
	}, admission.Reads{Summaries: reads, Subscriptions: reads, Assignments: reads})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunAdmission(ctx, AdmissionOptions{Ledger: ledger, Queue: queue, Admitter: admitter, Workers: 2, Lines: lines})
	}()

	// Two to-dos, then a third event on the first one's conversation: a
	// to-do's conversation is itself, so the third queues behind the first.
	for i, recording := range []int64{501, 502, 501} {
		ev := testEvent(int64(i + 1))
		ev.EventType, ev.Kind, ev.RecordingID = "todo.created", "todo_created", recording
		_, err := ledger.RecordSeen(ctx, ev, LanePoll)
		require.NoError(t, err)
		require.NoError(t, queue.Offer(ctx, ev.ID))
		require.Eventually(t, func() bool {
			return getRecord(t, ledger, ev.ID).State != StateSeen
		}, 5*time.Second, 10*time.Millisecond)
	}
	cancel()
	require.NoError(t, <-done)

	assert.Equal(t, StateAdmitted, getRecord(t, ledger, 1).State)
	assert.Equal(t, StateAdmitted, getRecord(t, ledger, 2).State)
	third := getRecord(t, ledger, 3)
	assert.Equal(t, StateQueued, third.State)
	assert.Contains(t, string(third.Decision.Snapshot), secret, "the queued follow-up keeps its instruction")

	got := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, got, 3)
	assert.NotContains(t, out.String(), secret, "no content on the wire")
	var last admission.Line
	require.NoError(t, json.Unmarshal([]byte(got[2]), &last))
	assert.Equal(t, int64(3), last.EventID)
	assert.Equal(t, admission.StateQueued, last.State, "the line reports what the ledger wrote")
}

func TestRunAdmissionNeedsTheLedgerAndQueue(t *testing.T) {
	admitter, err := admission.NewAdmitter(admission.Policy{
		AgentID: adapterAgentID,
		Trust:   admission.Trust{Mode: admission.TrustOperator, OperatorID: adapterOperatorID},
	}, admission.Reads{Summaries: &adapterReads{}, Subscriptions: &adapterReads{}, Assignments: &adapterReads{}})
	require.NoError(t, err)
	queue, err := NewQueue(1, 1)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	for name, opts := range map[string]AdmissionOptions{
		"no ledger": {Queue: queue, Admitter: admitter},
		"no queue":  {Ledger: newTestLedger(t), Admitter: admitter},
	} {
		t.Run(name, func(t *testing.T) {
			err := RunAdmission(ctx, opts)
			require.Error(t, err)
			assert.NotErrorIs(t, err, context.DeadlineExceeded, "refused at once, not run until the deadline")
		})
	}
}

// adapterReads answers admission's reads for the handoff test.
type adapterReads struct {
	summaries map[int64]*basecamp.RecordingSummary
}

func (r *adapterReads) Summarize(_ context.Context, ref basecamp.RecordingRef) (*basecamp.RecordingSummary, error) {
	s, ok := r.summaries[ref.RecordingID]
	if !ok {
		return nil, &basecamp.Error{Code: basecamp.CodeNotFound, Message: "not found"}
	}
	copied := *s
	return &copied, nil
}

func (r *adapterReads) Subscribed(context.Context, int64) (bool, error) { return false, nil }

func (r *adapterReads) AddedPersonIDs(context.Context, int64, int64) ([]int64, bool, error) {
	return nil, false, nil
}

// mentionMarkup is a mention attachment naming id only inside its sgid, the
// way Basecamp renders one.
func mentionMarkup(id int64) string {
	payload := `{"_rails":{"data":"gid://bc3/Person/` + strconv.FormatInt(id, 10) + `","pur":"attachable"}}`
	sgid := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return `<bc-attachment sgid="` + sgid + `" content-type="application/vnd.basecamp.mention"></bc-attachment>`
}
