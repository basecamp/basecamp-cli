package connector

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

type stubMinter struct{}

func (stubMinter) MintStreamTicket(context.Context) (eventfeed.StreamTicket, error) {
	return eventfeed.StreamTicket{}, nil
}

type scriptedPolls struct {
	pages   []eventfeed.PollPage
	errs    []error
	cursors []eventfeed.Cursor
	filters []eventfeed.Filters
	calls   int
}

func (s *scriptedPolls) Poll(_ context.Context, cursor eventfeed.Cursor, filters eventfeed.Filters) (eventfeed.PollPage, error) {
	s.cursors = append(s.cursors, cursor)
	s.filters = append(s.filters, filters)
	i := s.calls
	s.calls++
	if i < len(s.errs) && s.errs[i] != nil {
		return eventfeed.PollPage{}, s.errs[i]
	}
	if i < len(s.pages) {
		return s.pages[i], nil
	}
	return eventfeed.PollPage{Position: "drained"}, nil
}

type fixedClock struct{ at time.Time }

func (c *fixedClock) now() time.Time { return c.at }

func newTestIntake(t *testing.T, polls eventfeed.PollSource, pointers io.Writer) (*Intake, *Ledger, *Queue) {
	t.Helper()
	ledger := newTestLedger(t)
	queue, err := NewQueue(DefaultBacklogWarn, DefaultBacklogPause)
	require.NoError(t, err)
	if polls == nil {
		polls = &scriptedPolls{}
	}
	intake, err := New(Options{
		Origin:            "https://3.basecampapi.com",
		AccountID:         "2914079",
		ConsumerNamespace: "connector-test",
		Ledger:            ledger,
		Queue:             queue,
		Minter:            stubMinter{},
		Polls:             polls,
		Pointers:          pointers,
		Clock:             (&fixedClock{at: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}).now,
	})
	require.NoError(t, err)
	return intake, ledger, queue
}

func TestLaneOfReadsThePushLaneTransportFields(t *testing.T) {
	poll := testEvent(1)
	assert.Equal(t, LanePoll, LaneOf(poll), "poll rows omit actor_type and visible_to_clients")

	visible := true
	push := testEvent(2)
	push.ActorType = "person"
	push.VisibleToClients = &visible
	assert.Equal(t, LaneLive, LaneOf(push))

	// Presence, not value: visible_to_clients false is still present.
	hidden := false
	pushHidden := testEvent(3)
	pushHidden.ActorType = "agent"
	pushHidden.VisibleToClients = &hidden
	assert.Equal(t, LaneLive, LaneOf(pushHidden))
}

func TestIngestWritesOnePointerAndQueuesOnce(t *testing.T) {
	var pointers bytes.Buffer
	intake, _, queue := newTestIntake(t, nil, &pointers)
	ctx := context.Background()

	require.NoError(t, intake.ingest(ctx, testEvent(17099838500), LaneLive))
	require.NoError(t, intake.ingest(ctx, testEvent(17099838500), LanePoll))

	assert.Equal(t, 1, queue.Depth(), "the poll lane repeating the live lane is not a second unit of work")
	assert.Equal(t, 1, countLines(pointers.String()))

	var pointer Pointer
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(pointers.String())), &pointer))
	assert.Equal(t, int64(17099838500), pointer.EventID)
	assert.Equal(t, "comment.created", pointer.EventType)
	assert.Equal(t, int64(10304028972), pointer.RecordingID)
	assert.Equal(t, string(StateSeen), pointer.State)
}

// The pointer carries what the feed carried and nothing more. Anything with a
// title or a body in it would mean intake had read the recording, which is the
// one thing the delivery path must not do.
func TestPointerLineCarriesNoContent(t *testing.T) {
	var pointers bytes.Buffer
	intake, _, _ := newTestIntake(t, nil, &pointers)
	require.NoError(t, intake.ingest(context.Background(), testEvent(1), LanePoll))

	var raw map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(pointers.String())), &raw))
	for _, forbidden := range []string{"title", "content", "body", "url", "app_url", "creator_name", "excerpt"} {
		assert.NotContains(t, raw, forbidden)
	}
}

func TestPollServedIDAdvancesOnlyOnAPageBoundary(t *testing.T) {
	polls := &scriptedPolls{pages: []eventfeed.PollPage{{
		Events:   []eventfeed.Event{testEvent(17099838500)},
		Position: "p1",
	}}}
	intake, ledger, _ := newTestIntake(t, nil, nil)
	intake.served = &servedPolls{inner: polls}
	ctx := context.Background()

	live := testEvent(17099999999)
	live.ActorType = "person"
	visible := true
	live.VisibleToClients = &visible
	require.NoError(t, intake.ingest(ctx, live, LaneOf(live)))
	intake.confirmPollServed(ctx, "some-other-position")

	served, err := ledger.LastPollServedID(ctx, intake.CheckpointKey())
	require.NoError(t, err)
	assert.Zero(t, served,
		"a live id is far ahead of the poll lane; re-entering at one skips everything inside the safety delay")

	page, err := intake.served.Poll(ctx, eventfeed.Cursor{}, eventfeed.Filters{})
	require.NoError(t, err)
	served, err = ledger.LastPollServedID(ctx, intake.CheckpointKey())
	require.NoError(t, err)
	assert.Zero(t, served, "a page served but not yet delivered records nothing")

	intake.confirmPollServed(ctx, page.Position)
	served, err = ledger.LastPollServedID(ctx, intake.CheckpointKey())
	require.NoError(t, err)
	assert.Equal(t, int64(17099838500), served)
}

func TestOverflowIsOnDiskBeforeItIsAccepted(t *testing.T) {
	intake, ledger, _ := newTestIntake(t, nil, nil)
	ctx := context.Background()

	// One of the dropped ids is already in the ledger: it was dropped from the
	// buffer, not from the connector.
	_, err := ledger.RecordSeen(ctx, testEvent(17099838500), LaneLive)
	require.NoError(t, err)

	disposition := intake.handleSignal(eventfeed.BufferOverflow{
		DroppedIDs:   []int64{17099838500, 17099838501, 17099838502},
		DroppedCount: 3,
	})
	assert.Equal(t, eventfeed.Accept, disposition)

	losses, err := ledger.OpenLosses(ctx)
	require.NoError(t, err)
	require.Len(t, losses, 1, "a loss that only ever lived in memory is one no restart could find")
	assert.Equal(t, 3, losses[0].DroppedCount)
	assert.Equal(t, int64(17099838500), losses[0].RepairSince,
		"the walk enters one below the lowest MISSING id, and the feed's since is exclusive")

	missing, err := ledger.MissingIDs(ctx, losses[0].ID, LossMissing)
	require.NoError(t, err)
	assert.Equal(t, []int64{17099838501, 17099838502}, missing)

	neverLost, err := ledger.MissingIDs(ctx, losses[0].ID, LossNeverLost)
	require.NoError(t, err)
	assert.Equal(t, []int64{17099838500}, neverLost)
}

func TestOverflowIsRefusedWhenItCannotBeWrittenDown(t *testing.T) {
	intake, ledger, _ := newTestIntake(t, nil, nil)
	require.NoError(t, ledger.Close())

	disposition := intake.handleSignal(eventfeed.BufferOverflow{
		DroppedIDs:   []int64{17099838501},
		DroppedCount: 1,
	})
	assert.Equal(t, eventfeed.Terminate, disposition,
		"accepting an incompleteness that was never recorded is worse than ending the feed")
}

func TestOverflowOfIdsAlreadyHeldResolvesImmediately(t *testing.T) {
	intake, ledger, _ := newTestIntake(t, nil, nil)
	ctx := context.Background()
	_, err := ledger.RecordSeen(ctx, testEvent(17099838500), LaneLive)
	require.NoError(t, err)

	assert.Equal(t, eventfeed.Accept, intake.handleSignal(eventfeed.BufferOverflow{
		DroppedIDs:   []int64{17099838500},
		DroppedCount: 1,
	}))

	open, err := ledger.OpenLosses(ctx)
	require.NoError(t, err)
	assert.Empty(t, open, "nothing was lost, so there is nothing to walk for")
}

func TestFeedGapRecordsTheEpochAndTheServedEntryClass(t *testing.T) {
	intake, ledger, _ := newTestIntake(t, nil, nil)
	ctx := context.Background()

	assert.Equal(t, eventfeed.Accept, intake.handleSignal(eventfeed.FeedGap{
		EpochAfterID: 17099838487,
		ResumeURL:    "https://3.basecampapi.com/2914079/events.json?since=17099838487",
	}))

	gaps, err := ledger.Gaps(ctx)
	require.NoError(t, err)
	require.Len(t, gaps, 1)
	assert.Equal(t, GapEpoch, gaps[0].Class)
	require.NotNil(t, gaps[0].EpochAfterID)
	assert.Equal(t, int64(17099838487), *gaps[0].EpochAfterID)
	assert.Equal(t, EntryReplay, gaps[0].EntryClass)
}

func TestFeedGapClassifiesAPresentEntryFromTheServedCursor(t *testing.T) {
	intake, ledger, _ := newTestIntake(t, nil, nil)

	assert.Equal(t, eventfeed.Accept, intake.handleSignal(eventfeed.FeedGap{
		EpochAfterID: 17099838487,
		ResumeURL:    "https://3.basecampapi.com/2914079/events.json?since=now",
	}))

	gaps, err := ledger.Gaps(context.Background())
	require.NoError(t, err)
	require.Len(t, gaps, 1)
	assert.Equal(t, EntryPresent, gaps[0].EntryClass)
}

func TestFeedGapIsRefusedWhenItCannotBeRecorded(t *testing.T) {
	intake, ledger, _ := newTestIntake(t, nil, nil)
	require.NoError(t, ledger.Close())

	assert.Equal(t, eventfeed.Terminate, intake.handleSignal(eventfeed.FeedGap{
		EpochAfterID: 17099838487,
		ResumeURL:    "https://3.basecampapi.com/2914079/events.json?since=17099838487",
	}), "a gap nothing wrote down is a gap nothing will ever report")
}

func TestLedgerRefusesAGapWhoseClassAndEpochDisagree(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()

	_, err := ledger.RecordGap(ctx, Gap{DetectedAt: time.Now(), Class: GapEpoch})
	assert.Error(t, err, "an epoch gap without an epoch is the flattening this schema exists to refuse")

	epoch := int64(1)
	_, err = ledger.RecordGap(ctx, Gap{DetectedAt: time.Now(), Class: GapRetention, EpochAfterID: &epoch})
	assert.Error(t, err, "a retention gap has no epoch to carry")
}

func TestEntryClassOf(t *testing.T) {
	assert.Equal(t, EntryPresent, entryClassOf("https://3.basecampapi.com/1/events.json?since=now"))
	assert.Equal(t, EntryReplay, entryClassOf("https://3.basecampapi.com/1/events.json?since=17099838487"))
	assert.Equal(t, EntryReplay, entryClassOf("https://3.basecampapi.com/1/events.json?since=0"))
	assert.Equal(t, EntryUnknown, entryClassOf("https://3.basecampapi.com/1/events.json"))
	assert.Equal(t, EntryUnknown, entryClassOf("https://3.basecampapi.com/1/events.json?since=soon"))
	assert.Equal(t, EntryUnknown, entryClassOf("://not-a-url"))
}

func TestReconciliationResumesOnStart(t *testing.T) {
	polls := &scriptedPolls{pages: []eventfeed.PollPage{{
		Events:   []eventfeed.Event{testEvent(17099838501)},
		Position: "walk-1",
	}}}
	intake, ledger, _ := newTestIntake(t, polls, nil)
	ctx := context.Background()

	_, err := ledger.RecordLoss(ctx, []int64{17099838501}, time.Now(), time.Minute, eventfeed.Filters{})
	require.NoError(t, err)

	require.NoError(t, intake.resumeReconciliation(ctx))
	intake.repairs.Wait()

	open, err := ledger.OpenLosses(ctx)
	require.NoError(t, err)
	assert.Empty(t, open, "a crash between the overflow and its repair is a delay, not a lost record")

	_, ok, err := ledger.Get(ctx, 17099838501)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestNewRefusesAnIncompleteConfiguration(t *testing.T) {
	ledger := newTestLedger(t)
	queue, err := NewQueue(1, 2)
	require.NoError(t, err)

	base := Options{
		Origin:            "https://3.basecampapi.com",
		AccountID:         "2914079",
		ConsumerNamespace: "connector-test",
		Ledger:            ledger,
		Queue:             queue,
		Minter:            stubMinter{},
		Polls:             &scriptedPolls{},
	}
	_, err = New(base)
	require.NoError(t, err)

	noNamespace := base
	noNamespace.ConsumerNamespace = ""
	_, err = New(noNamespace)
	assert.Error(t, err, "two connectors in one account must not share a checkpoint lineage")

	badFilters := base
	badFilters.Filters = eventfeed.Filters{Types: []string{"comment.created,card.created"}}
	_, err = New(badFilters)
	assert.Error(t, err, "a filter set is validated before any wire attempt")
}

func countLines(s string) int {
	scanner := bufio.NewScanner(strings.NewReader(s))
	n := 0
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			n++
		}
	}
	return n
}

// The card's acceptance number: a burst of a thousand events is seen within a
// minute. The bound here is far tighter than a minute on purpose — the point
// is to catch a regression that makes intake per-event expensive (a fsync per
// row, a read per pointer), not to measure the machine.
func TestABurstOfAThousandEventsIsAbsorbedQuickly(t *testing.T) {
	var pointers bytes.Buffer
	intake, ledger, queue := newTestIntake(t, nil, &pointers)
	ctx := context.Background()

	start := time.Now()
	for id := int64(17099838500); id < 17099838500+1000; id++ {
		require.NoError(t, intake.ingest(ctx, testEvent(id), LanePoll))
	}
	elapsed := time.Since(start)

	assert.Equal(t, 1000, queue.Depth())
	assert.Equal(t, 1000, countLines(pointers.String()))
	seen, err := ledger.CountInState(ctx, StateSeen)
	require.NoError(t, err)
	assert.Equal(t, 1000, seen)
	assert.Less(t, elapsed, 30*time.Second, "intake is the only work on the feed's delivery path")
	t.Logf("1,000 events through intake in %s", elapsed)
}
