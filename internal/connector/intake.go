package connector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"

	"github.com/basecamp/basecamp-cli/internal/connector/ndjson"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// Defaults for the recovery timers.
const (
	// DefaultRepairInterval is how often a repair walk re-runs while a loss is
	// open. It matches the feed's own repair poll, and it is deliberately
	// longer than the poll lane's ~30s safety delay: a walk that outran the
	// delay would call a still-committing event missing.
	DefaultRepairInterval = 60 * time.Second
	// DefaultRepairWindow is how long a loss stays open before the ids still
	// missing are called unrecovered. At the sixty-second cadence that is
	// about ten repair polls, each well past the safety delay.
	DefaultRepairWindow = 10 * time.Minute
	// DefaultMembershipInterval is how often the agent's project list is
	// re-read. The cable snapshots the agent's buckets when it subscribes, so
	// a project granted afterwards is invisible on the live lane until the
	// connection is remade.
	DefaultMembershipInterval = 10 * time.Minute
)

// MembershipSource lists the buckets the agent can currently see.
type MembershipSource interface {
	Buckets(ctx context.Context) ([]int64, error)
}

// Options configures intake.
type Options struct {
	// Origin is the API base URL. It is part of the checkpoint identity and
	// the origin every continuation URL is validated against.
	Origin string
	// AccountID is the Basecamp account.
	AccountID string
	// ConsumerNamespace names this connector's checkpoint lineage. Two
	// connectors in one account must not share one.
	ConsumerNamespace string
	// Filters is the feed's filter set. The checkpoint is keyed by its digest,
	// so changing it re-enters under a new lineage.
	Filters eventfeed.Filters
	// SinceEventID, when positive, enters just after that event id whatever
	// the ledger holds — the `--since` override.
	SinceEventID int64

	Ledger *Ledger
	Queue  *Queue
	Minter eventfeed.TicketMinter
	// PollsFor returns a fresh poll source per walk. The SDK's live source
	// holds walk state (the order a continuation must follow), so the feed's
	// connection and each repair walk get their own. Polls, when set instead,
	// is shared by all of them — for test fakes that hold no walk state.
	PollsFor func() eventfeed.PollSource
	Polls    eventfeed.PollSource

	// Pointers receives one NDJSON line per newly seen event. Writes are
	// serialized: an interleaved write tears a line and breaks the watcher
	// reading it.
	Pointers io.Writer
	// Logger receives everything else. Pointer lines are the protocol;
	// logging is not.
	Logger *slog.Logger

	// Membership, when set, drives the reconnect that makes a newly granted
	// project visible on the live lane.
	Membership         MembershipSource
	MembershipInterval time.Duration

	RepairInterval time.Duration
	RepairWindow   time.Duration

	Clock     func() time.Time
	Transport eventfeed.CableTransport
}

// LiveOptions binds intake to the live feed: the SDK's own seams over the
// generated operations, with their redirect guard, their two-410 mapping and
// their continuation checks. The caller fills in the rest (account, namespace,
// ledger, queue, filters).
//
// Build the Live without a debug logger or request hooks in production: the
// SDK client logs request URLs through them, and a poll URL carries the feed
// position.
func LiveOptions(live *eventfeed.Live) Options {
	return Options{
		Origin:   live.Origin(),
		Minter:   live.Minter(),
		PollsFor: live.Polls,
	}
}

// Intake is the feed's delivery path: write the pointer, hand over the id.
//
// Everything else — reading the recording, judging it, dispatching it — is
// downstream of the queue, so a slow admission or a busy dispatcher can never
// stall the socket.
type Intake struct {
	opts    Options
	ledger  *Ledger
	queue   *Queue
	log     *slog.Logger
	now     func() time.Time
	pointer *pointerWriter

	key eventfeed.CheckpointKey
	// positions reads the safe re-entry ids; the ledger, except in tests that
	// need the read to fail.
	positions pollServedReader

	mu sync.Mutex
	// served is the current connection's wrapped poll source.
	served   *servedPolls
	snapshot map[int64]bool
	// membershipRetry is the first backoff after a failed membership read.
	membershipRetry time.Duration
	// learned holds buckets events proved visible that the lister did not
	// name.
	learned   map[int64]bool
	reconnect chan struct{}
	// cancelRun ends the current connection; nil between connections.
	cancelRun context.CancelFunc
	// promotedThisRun is whether this connection has recorded a poll-served
	// id. Until it has, the package's own reset cursor is zero, and a refused
	// position would send it to the present.
	promotedThisRun bool
	// reentry is the safe entry the next connection takes after a refused
	// stored position; hasReentry says whether one is pending.
	reentry    eventfeed.Start
	hasReentry bool
	// replaying is whether this connection entered at the beginning of served
	// history on purpose, to recover.
	replaying bool
	// reentryReplays is whether the pending re-entry is a replay.
	reentryReplays bool
	// checkpointed is whether any connection in this run has saved a
	// position.
	checkpointed bool
	// enteredByReentry is whether this connection is itself the safe
	// re-entry after a refused position.
	enteredByReentry bool
	reentryLog       string
	// abortErr ends the run: set when continuing could only mean entering
	// the feed somewhere unsafe.
	abortErr error

	repairs sync.WaitGroup
	// lifetime is Run's context. Repair walks are bound to it rather than to
	// a connection, so a reconnect does not abandon a walk and a shutdown does
	// not strand Run waiting on one — an unfinished walk simply resumes on the
	// next start, which is what OpenLosses is for.
	lifetime context.Context
	// repairSleep replaces the wait between repair polls. Tests set it so ten
	// minutes of repair cadence does not take ten minutes.
	repairSleep func(ctx context.Context, d time.Duration) error
}

// New builds intake.
func New(opts Options) (*Intake, error) {
	switch {
	case opts.Ledger == nil:
		return nil, errors.New("connector: intake needs a ledger")
	case opts.Queue == nil:
		return nil, errors.New("connector: intake needs a queue")
	case opts.Minter == nil || (opts.Polls == nil && opts.PollsFor == nil):
		return nil, errors.New("connector: intake needs the feed's two seams")
	case opts.AccountID == "":
		return nil, errors.New("connector: intake needs an account id")
	case opts.ConsumerNamespace == "":
		return nil, errors.New("connector: intake needs a consumer namespace")
	}

	if opts.PollsFor == nil {
		shared := opts.Polls
		opts.PollsFor = func() eventfeed.PollSource { return shared }
	}

	origin, err := eventfeed.CanonicalOrigin(opts.Origin)
	if err != nil {
		return nil, fmt.Errorf("connector: intake origin: %w", err)
	}
	if err := opts.Filters.Validate(); err != nil {
		return nil, fmt.Errorf("connector: intake filters: %w", err)
	}

	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.RepairInterval <= 0 {
		opts.RepairInterval = DefaultRepairInterval
	}
	if opts.RepairWindow <= 0 {
		opts.RepairWindow = DefaultRepairWindow
	}
	if opts.MembershipInterval <= 0 {
		opts.MembershipInterval = DefaultMembershipInterval
	}

	in := &Intake{
		opts:      opts,
		ledger:    opts.Ledger,
		queue:     opts.Queue,
		log:       opts.Logger,
		now:       opts.Clock,
		pointer:   newPointerWriter(opts.Pointers),
		reconnect: make(chan struct{}, 1),
		key: eventfeed.CheckpointKey{
			Origin:            origin,
			AccountID:         opts.AccountID,
			ConsumerNamespace: opts.ConsumerNamespace,
			FilterKey:         opts.Filters.FilterKey(),
		},
	}
	in.ledger.now = opts.Clock
	in.positions = opts.Ledger
	return in, nil
}

// pollServedReader is the slice of the ledger re-entry reads.
type pollServedReader interface {
	LastPollServedID(ctx context.Context, key eventfeed.CheckpointKey) (int64, error)
	LineagePollServedID(ctx context.Context, key eventfeed.CheckpointKey) (int64, error)
}

// CheckpointKey is the identity this intake's position is stored under.
func (in *Intake) CheckpointKey() eventfeed.CheckpointKey { return in.key }

// Run consumes the feed until ctx is canceled or the feed terminates.
//
// It reconnects on its own for two reasons only — membership, and a stored
// position refused before this run had a safe re-entry of its own. Everything
// else the feed can recover from, it recovers from inside the package.
func (in *Intake) Run(ctx context.Context) error {
	// Repairs get a child lifetime that ends when Run does, whatever the
	// reason. A repair is off the delivery path: a terminal feed must not wait
	// out its sixty-second cadence, and an unfinished repair resumes on the
	// next start from the loss record.
	repairCtx, stopRepairs := context.WithCancel(ctx)
	defer in.repairs.Wait()
	defer stopRepairs()
	in.lifetime = repairCtx

	if err := in.resumeReconciliation(repairCtx); err != nil {
		return err
	}

	since := in.opts.SinceEventID
	for {
		err := in.runOnce(ctx, since)
		// --since is an entry, not a standing instruction: once a checkpoint
		// has been saved, a reconnect resumes from it. Until then the explicit
		// entry is the only position this run has, and a connection that ends
		// before its first save must not leave the next one entering at the
		// present.
		if in.checkpointSaved() {
			since = 0
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch {
		case errors.Is(err, errReconnect):
			in.log.Info("reconnecting the feed")
		default:
			return err
		}
	}
}

// errReconnect asks the supervisor for a fresh connection. It is not a
// failure.
var errReconnect = errors.New("connector: reconnect the feed")

func (in *Intake) runOnce(ctx context.Context, since int64) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// This connection IS the reconnect any request made between connections
	// asked for; a latch left standing would turn its first terminal error
	// into a silent reconnect.
	in.reconnectRequested()

	in.mu.Lock()
	in.cancelRun = cancel
	in.promotedThisRun = false
	reentry, hasReentry, reentryLog, reentryReplays := in.reentry, in.hasReentry, in.reentryLog, in.reentryReplays
	in.hasReentry = false
	in.replaying = false
	in.enteredByReentry = hasReentry
	in.mu.Unlock()
	defer func() {
		in.mu.Lock()
		in.cancelRun = nil
		in.mu.Unlock()
	}()

	in.takeSnapshot(runCtx)

	_, hadPosition, err := in.ledger.Load(runCtx, in.key)
	if err != nil {
		return err
	}
	ownServed, err := in.ledger.LastPollServedID(runCtx, in.key)
	if err != nil {
		return err
	}
	lineageServed, err := in.ledger.LineagePollServedID(runCtx, in.key)
	if err != nil {
		return err
	}
	lineageHasPosition, err := in.ledger.LineageHasPosition(runCtx, in.key)
	if err != nil {
		return err
	}

	start := eventfeed.StartResume()
	switch {
	case since > 0:
		start = eventfeed.StartAfter(since)
		in.log.Info("entering the feed after an explicit event id", "since", since)
	case hasReentry:
		start = reentry
		in.setReplaying(reentryReplays)
		in.log.Warn("the stored position was refused; " + reentryLog)
	case hadPosition:
		in.log.Info("resuming the feed from the stored position", "filter_key", in.key.FilterKey)
	case ownServed > 0:
		// This filter set has progress but no position: the package confirms
		// a page before it saves the page's position, so a failed save leaves
		// exactly this. Its own id, never another filter set's.
		start = eventfeed.StartAfter(ownServed)
		in.log.Warn("no stored position for this filter set; re-entering after its own last poll-served id",
			"since", ownServed, "filter_key", in.key.FilterKey)
	case lineageServed > 0:
		// A filter change: this digest has no position, but the consumer's
		// poll lane had reached this id under another. Entering at the
		// present would skip everything since.
		start = eventfeed.StartAfter(lineageServed)
		in.log.Warn("no position for this filter set; re-entering after the last poll-served id under the previous one",
			"since", lineageServed, "filter_key", in.key.FilterKey)
	case lineageHasPosition:
		// A filter change from a set that had read up to a position but
		// recorded no served id. Entering at the present would skip what
		// followed that position; replaying from the beginning costs reads
		// the ledger absorbs.
		start = eventfeed.StartBeginning()
		in.setReplaying(true)
		in.log.Warn("no position or poll-served id for this filter set, but the previous one had read up to a position; replaying from the beginning of served history",
			"filter_key", in.key.FilterKey)
	default:
		// Said out loud because it is a real loss of history, not a neutral
		// default: everything committed before this moment is never served.
		in.log.Warn("no stored position: entering the feed at the present, so nothing committed before now will be served",
			"filter_key", in.key.FilterKey)
	}

	options := []eventfeed.Option{
		eventfeed.WithFilters(in.opts.Filters),
		eventfeed.WithStart(start),
		eventfeed.WithCheckpointStore(in.ledger),
		eventfeed.WithConsumerNamespace(in.opts.ConsumerNamespace),
		eventfeed.WithSignalHandler(in.handleSignal),
		eventfeed.WithObserver(in.observer(runCtx)),
		eventfeed.WithRepairInterval(in.opts.RepairInterval),
	}
	if in.opts.Transport != nil {
		options = append(options, eventfeed.WithTransport(in.opts.Transport))
	}

	served := &servedPolls{inner: in.opts.PollsFor()}
	in.mu.Lock()
	in.served = served
	in.mu.Unlock()

	feed, err := eventfeed.New(in.key.Origin, in.opts.AccountID, in.opts.Minter, served, options...)
	if err != nil {
		return fmt.Errorf("connector: build feed: %w", err)
	}
	// Deferred in this order so they run in the reverse: the connection is
	// canceled first, which is what lets the membership watcher and the feed
	// return, and only then are they waited on.
	defer func() {
		_ = feed.Close()
		feed.Wait()
	}()
	// The watcher's context is created and canceled here, in its owner, so
	// the cancel is provably reached on every path out of this connection.
	watchCtx, stopWatch := context.WithCancel(runCtx)
	defer stopWatch()
	awaitMembership := in.watchMembership(watchCtx)
	defer func() {
		stopWatch()
		awaitMembership()
	}()
	defer cancel()

	var feedErr error
	for event, err := range feed.Events(runCtx) {
		if err != nil {
			feedErr = err
			break
		}
		// Run's context, not the connection's: a reconnect canceling the
		// connection while this hand-off waits for queue room would leave the
		// event seen in the ledger and never queued, since the re-served page
		// no longer reports it new. The reconnect waits for the hand-off; the
		// feed stays paused either way.
		if err := in.ingest(ctx, event, LaneOf(event)); err != nil {
			if in.reconnectRequested() {
				return errReconnect
			}
			return err
		}
	}

	in.mu.Lock()
	aborted := in.abortErr
	in.mu.Unlock()
	if aborted != nil {
		return aborted
	}
	if in.reconnectRequested() {
		return errReconnect
	}
	// The two 410s are told apart below this package: the SDK's live seam
	// maps the feed's FeedPositionGoneError (epoch required) to the gap
	// signal, and refuses the inbox's InboxPositionGoneError shape on the
	// account lane as unrecoverable, so it ends the feed here as a terminal
	// rather than resuming as if it were the epoch's.
	return feedErr
}

// ingest is the whole of intake: one pointer written, one id handed over.
func (in *Intake) ingest(ctx context.Context, event eventfeed.Event, lane Lane) error {
	fresh, err := in.ledger.RecordSeen(ctx, event, lane)
	if err != nil {
		// A pointer that could not be written is a pointer the restart will
		// not know about. Nothing downstream is allowed to proceed as if it
		// had been.
		return err
	}
	if !fresh {
		// The ordinary case: the poll lane serving what the live lane already
		// delivered, or a restart re-walking a page. Dedupe is the point.
		return nil
	}

	if err := in.pointer.write(event, lane); err != nil {
		return err
	}
	if err := in.queue.Offer(ctx, event.ID); err != nil {
		return err
	}
	// Only once the id is handed over: the reconnect cancels the connection
	// this event arrived on, and the event must not be stranded between the
	// ledger and the queue by it.
	in.noteBucket(event.BucketID)
	return nil
}

// LaneOf says which lane served an event.
//
// The rows are not labeled, but the two shapes differ: actor_type and
// visible_to_clients are push-lane transport fields that poll rows omit, and
// the SDK keeps both presence-bearing — a string whose empty value is outside
// the vocabulary, and a *bool — rather than defaulting them, precisely so this
// is answerable.
//
// It labels records and pointer lines. It decides nothing about re-entry: the
// last poll-served id is taken from what the poll source served, not from
// guessing which lane a delivery came from.
func LaneOf(event eventfeed.Event) Lane {
	if event.ActorType == "" && event.VisibleToClients == nil {
		return LanePoll
	}
	return LaneLive
}

// servedPolls wraps the feed's poll source to record what each page SERVED.
//
// The last poll-served id has to count served events, not delivered ones. The
// package suppresses the poll copy of any event the live lane already
// delivered, and the poll lane runs about thirty seconds behind the live one
// by design, so in steady state nearly every poll page delivers nothing.
// Counting deliveries would leave the id at zero, and every re-entry that
// needs it would fall back to a full replay. This is what the package counts
// for its own reset cursor too.
//
// The repair walk does not go through here: its pages are not the feed's
// position and must never advance the feed's re-entry.
type servedPolls struct {
	inner eventfeed.PollSource

	mu sync.Mutex
	// byPosition holds the highest id a page served, keyed by the position
	// that page issued, until the package confirms the page was delivered.
	byPosition map[string]int64
}

func (s *servedPolls) Poll(ctx context.Context, cursor eventfeed.Cursor, filters eventfeed.Filters) (eventfeed.PollPage, error) {
	page, err := s.inner.Poll(ctx, cursor, filters)
	if err != nil || page.Position == "" {
		return page, err
	}
	var highest int64
	for _, event := range page.Events {
		highest = max(highest, event.ID)
	}
	if highest > 0 {
		s.mu.Lock()
		if s.byPosition == nil {
			s.byPosition = make(map[string]int64)
		}
		s.byPosition[page.Position] = max(s.byPosition[page.Position], highest)
		s.mu.Unlock()
	}
	return page, err
}

// confirmed returns the highest id served by the page that issued position,
// and forgets every page served before it: pages are polled and delivered one
// at a time, so nothing earlier can still be waiting.
func (s *servedPolls) confirmed(position string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	highest := s.byPosition[position]
	clear(s.byPosition)
	return highest
}

// confirmPollServed records the served id of a page the package has just
// finished delivering. Only then: an ingest that failed partway through a page
// leaves the page undelivered and its ids unrecorded.
func (in *Intake) confirmPollServed(ctx context.Context, position string) {
	in.mu.Lock()
	served := in.served
	in.mu.Unlock()
	if served == nil {
		return
	}
	highest := served.confirmed(position)
	if highest == 0 {
		// An empty page. Ordinary — the walk crossed rows the filters exclude
		// — and it serves no id, so it advances nothing here.
		return
	}
	if err := in.ledger.NotePollServed(ctx, in.key, highest); err != nil {
		in.log.Error("could not record the last poll-served id", "error", err)
		return
	}
	in.mu.Lock()
	in.promotedThisRun = true
	in.mu.Unlock()
}

func (in *Intake) observer(ctx context.Context) eventfeed.Observer {
	return eventfeed.Observer{
		Connected: func() { in.log.Info("feed socket connected") },
		Confirmed: func() { in.log.Info("feed subscription confirmed") },
		Disconnected: func(reason string, err error) {
			in.log.Warn("feed socket disconnected", "reason", reason, "error", err)
		},
		CatchUpStarted: func(eventfeed.Cursor) { in.log.Info("feed catch-up walk started") },
		PageDelivered: func(_ int, position string) {
			// Detached deliberately: the position the page just moved must be
			// recorded even if the run's context is on its way down, or a
			// shutdown mid-page loses the id the next re-entry needs.
			in.confirmPollServed(context.WithoutCancel(ctx), position) //nolint:contextcheck // detached on purpose, see above
		},
		CaughtUp: func() {
			// A replay that reached the head without meeting the epoch is
			// over; nothing after this is part of it.
			in.setReplaying(false)
			// "Caught up with the walk", not "caught up with the account":
			// delivery has write-time brakes that write no addressing and say
			// nothing, so a quiet feed is never proof of a quiet project.
			in.log.Info("feed walk reached its head and the buffer drained")
		},
		Checkpoint: func(string) {
			in.mu.Lock()
			in.checkpointed = true
			in.mu.Unlock()
		},
		CheckpointSaveFailed: func(err error) {
			in.log.Error("could not save the feed position", "error", err)
		},
		Gap: func(epochAfterID int64, resumeOrigin string) {
			in.log.Warn("feed served a 410", "epoch_after_id", epochAfterID, "resume_origin", resumeOrigin)
		},
		PositionRejected: func(kind eventfeed.PollErrorKind) {
			in.log.Warn("feed rejected the held position", "kind", kind.String())
			in.onPositionRejected(ctx)
		},
		FilterConflict: func(positionDigest, filtersDigest string) {
			in.log.Warn("feed position was minted for a different filter set",
				"position_digest", positionDigest, "filters_digest", filtersDigest)
		},
		StaleConnection: func(d time.Duration) {
			in.log.Warn("feed socket went stale", "since_last_frame", d)
		},
		BufferOverflow: func(dropped int) {
			in.log.Warn("live buffer overflowed", "dropped", dropped)
		},
	}
}

// handleSignal decides what a semantic signal means for this connector. It
// runs synchronously on the delivery path, so it does only what must happen
// before the disposition takes effect and starts the rest elsewhere.
func (in *Intake) handleSignal(signal eventfeed.Signal) eventfeed.Disposition {
	ctx := context.WithoutCancel(context.Background())

	switch s := signal.(type) {
	case eventfeed.FeedGap:
		// The ACCOUNT lane's 410, and the only one that reaches here: the
		// adapter never maps a retention 410 onto this signal. The resume URL
		// is followed exactly as served — the entry class is the server's
		// decision, read out of its cursor, never substituted.
		epoch := s.EpochAfterID
		note := "the feed's served history before the epoch is gone"
		in.mu.Lock()
		if in.replaying {
			// Expected, not a loss: this connection chose to replay from the
			// beginning to recover, and the beginning is below the epoch. The
			// label covers that one entry; any later 410 on this connection is
			// a real gap and is recorded as one.
			note = "a recovery replay from the beginning of served history met the epoch, as expected; not a loss"
			in.replaying = false
		}
		in.mu.Unlock()
		if _, err := in.ledger.RecordGap(ctx, Gap{
			DetectedAt:   in.now(),
			Class:        GapEpoch,
			EpochAfterID: &epoch,
			EntryClass:   entryClassOf(s.ResumeURL),
			Note:         note,
		}); err != nil {
			// A gap we cannot write down is a gap nothing will ever report.
			in.log.Error("could not record the feed gap; refusing to continue past it", "error", err)
			return eventfeed.Terminate
		}
		return eventfeed.Accept

	case eventfeed.BufferOverflow:
		loss, err := in.ledger.RecordLoss(ctx, s.DroppedIDs, in.now(), in.opts.RepairWindow, in.opts.Filters)
		if err != nil {
			// Accept means owning the incompleteness. Owning it begins with
			// it being on disk: accepting after a failed write would leave a
			// loss that no restart could ever find, which is the one outcome
			// worse than terminating.
			in.log.Error("could not record the buffer overflow; refusing to accept it", "error", err)
			return eventfeed.Terminate
		}
		in.log.Warn("live buffer overflowed; reconciling",
			"dropped", s.DroppedCount, "loss_id", loss.ID, "repair_since", loss.RepairSince)
		in.startRepair(in.repairContext(), loss)
		return eventfeed.Accept
	}
	return eventfeed.Terminate
}

// entryClassOf reads the entry class out of the cursor the server served.
func entryClassOf(resumeURL string) EntryClass {
	parsed, err := url.Parse(resumeURL)
	if err != nil {
		return EntryUnknown
	}
	switch since := parsed.Query().Get("since"); since {
	case "now":
		return EntryPresent
	case "":
		return EntryUnknown
	default:
		if _, err := strconv.ParseInt(since, 10, 64); err != nil {
			return EntryUnknown
		}
		return EntryReplay
	}
}

// resumeReconciliation restarts every open loss's repair walk on start. A
// crash between the overflow and its repair is a delay, not a loss of the
// record.
func (in *Intake) resumeReconciliation(ctx context.Context) error {
	losses, err := in.ledger.OpenLosses(ctx)
	if err != nil {
		return err
	}
	for _, loss := range losses {
		in.log.Info("resuming reconciliation of an open loss", "loss_id", loss.ID, "repair_since", loss.RepairSince)
		in.startRepair(ctx, loss)
	}
	return nil
}

// repairContext is the lifetime a repair walk runs under. handleSignal is
// invoked by the feed with no context of its own, so the walk takes Run's.
func (in *Intake) repairContext() context.Context {
	if in.lifetime != nil {
		return in.lifetime
	}
	return context.Background()
}

func (in *Intake) startRepair(ctx context.Context, loss Loss) {
	if loss.ResolvedAt != nil {
		return
	}
	in.repairs.Add(1)
	go func() {
		defer in.repairs.Done()
		// Off the delivery path: nothing about live intake waits for this.
		walker := &repairWalker{
			ledger:   in.ledger,
			polls:    in.opts.PollsFor(),
			origin:   in.key.Origin,
			filters:  in.opts.Filters,
			ingest:   in.ingest,
			now:      in.now,
			interval: in.opts.RepairInterval,
			log:      in.log,
			sleep:    in.repairSleep,
		}
		switch err := walker.reconcileLoss(ctx, loss); {
		case err == nil:
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			// A shutdown mid-walk is a delay: the loss is still open on disk
			// and the next start picks it up where this one left off.
			in.log.Info("reconciliation paused by shutdown; it resumes on the next start", "loss_id", loss.ID)
		default:
			in.log.Error("reconciliation of a loss ended early", "loss_id", loss.ID, "error", err)
		}
	}()
}

// takeSnapshot records the buckets the agent can see at the moment this
// connection subscribes — the same set the cable snapshots.
func (in *Intake) takeSnapshot(ctx context.Context) {
	if in.opts.Membership == nil {
		return
	}
	buckets, err := in.opts.Membership.Buckets(ctx)
	if err != nil {
		in.log.Warn("could not read the agent's projects; keeping the previous snapshot", "error", err)
		return
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	in.snapshot = make(map[int64]bool, len(buckets)+len(in.learned))
	for _, id := range buckets {
		in.snapshot[id] = true
	}
	for id := range in.learned {
		in.snapshot[id] = true
	}
}

// noteBucket asks for a reconnect when an event arrives from a bucket the live
// snapshot did not hold. The poll lane authorizes at read time, so it covers
// the new project immediately; the live lane cannot until it re-subscribes.
func (in *Intake) noteBucket(bucketID int64) {
	in.mu.Lock()
	// With no snapshot — the read at subscribe failed — the live
	// subscription's buckets are unknown, not "everything". An event from a
	// bucket not yet learned asks for one reconnect.
	known := (in.snapshot == nil && in.opts.Membership == nil) || in.snapshot[bucketID] || in.learned[bucketID]
	in.mu.Unlock()
	if known {
		return
	}
	in.log.Info("an event arrived from a project the live subscription does not hold", "bucket_id", bucketID)
	// Learned for the life of the process, and merged into every later
	// snapshot. A project the membership list never names — archived, or past
	// the lister's page — would otherwise cost a reconnect per event, forever.
	in.mu.Lock()
	if in.learned == nil {
		in.learned = make(map[int64]bool)
	}
	in.learned[bucketID] = true
	if in.snapshot != nil {
		in.snapshot[bucketID] = true
	}
	in.mu.Unlock()
	in.requestReconnect()
}

// onPositionRejected handles a refused position on a connection that has not
// yet recorded a poll-served id of its own.
//
// The package re-enters from an in-memory id that starts at zero each run, so
// right after a restart it would take the present and skip everything since
// the refused position. The connection is ended before that re-entry polls,
// and remade at a safe entry:
//
//   - this filter set's own last poll-served id, when it has one;
//   - otherwise the beginning of served history. A position can be saved
//     from empty pages alone, so "no poll-served id" does not mean "nothing
//     to skip". Replaying costs reads the ledger's dedupe absorbs; entering at
//     the present, or at another filter set's id, costs events.
func (in *Intake) onPositionRejected(ctx context.Context) {
	in.mu.Lock()
	promoted := in.promotedThisRun
	reentered := in.enteredByReentry
	in.mu.Unlock()
	if !promoted && reentered {
		// The safe re-entry was itself refused before it served a page.
		// Whatever the server objects to, another re-entry will not cure it,
		// and reconnecting again would mint, dial and poll in a tight loop.
		in.abort(errors.New("connector: the feed refused the safe re-entry after a refused position; not retrying"))
		return
	}
	if promoted {
		// The package's own reset cursor is this run's poll-served id, which
		// is at least what the ledger holds.
		return
	}
	served, err := in.positions.LastPollServedID(ctx, in.key)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// The connection is already ending — a reconnect or a shutdown —
			// so the package's re-entry will not poll. Nothing to decide.
			return
		}
		// Not knowing where it is safe to re-enter is a reason to stop, not
		// to guess.
		in.abort(fmt.Errorf("connector: a position was refused and the safe re-entry could not be read: %w", err))
		return
	}

	in.mu.Lock()
	if served > 0 {
		in.reentry = eventfeed.StartAfter(served)
		in.reentryLog = "re-entering after this filter set's last poll-served id " + strconv.FormatInt(served, 10)
	} else {
		in.reentry = eventfeed.StartBeginning()
		in.reentryLog = "no poll-served id for this filter set; re-entering at the beginning of served history"
	}
	in.reentryReplays = served == 0
	in.hasReentry = true
	in.mu.Unlock()
	in.requestReconnect()
}

func (in *Intake) setReplaying(replaying bool) {
	in.mu.Lock()
	in.replaying = replaying
	in.mu.Unlock()
}

func (in *Intake) checkpointSaved() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.checkpointed
}

// abort ends the current connection and the run with err.
func (in *Intake) abort(err error) {
	in.mu.Lock()
	if in.abortErr == nil {
		in.abortErr = err
	}
	cancel := in.cancelRun
	in.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// watchMembership re-reads the agent's projects on a timer and asks for a
// reconnect when the set changes. It runs until ctx ends; the returned
// function waits for it to have stopped.
func (in *Intake) watchMembership(ctx context.Context) func() {
	if in.opts.Membership == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		retry := in.membershipRetry
		if retry <= 0 {
			retry = defaultMembershipRetry
		}
		for {
			// A snapshot the subscribe-time read never produced is retried on
			// a backoff rather than left for a full interval: until one exists
			// there is no baseline to notice a change against.
			wait := in.opts.MembershipInterval
			if !in.hasSnapshot() {
				wait = min(retry, in.opts.MembershipInterval)
				retry = min(retry*2, in.opts.MembershipInterval)
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			buckets, err := in.opts.Membership.Buckets(ctx)
			if err != nil {
				// A failed read leaves the snapshot alone. Treating it as a
				// change would reconnect the feed every time the API
				// hiccupped.
				in.log.Warn("could not refresh the agent's projects", "error", err)
				continue
			}
			if in.membershipChanged(buckets) {
				in.requestReconnect()
				return
			}
		}
	}()
	return func() { <-done }
}

// defaultMembershipRetry is the first wait before re-reading a membership
// listing that failed; it doubles up to the membership interval.
const defaultMembershipRetry = 5 * time.Second

func (in *Intake) hasSnapshot() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.snapshot != nil
}

// membershipChanged compares a fresh read with the snapshot. With no snapshot
// — the read at subscribe failed — the fresh read becomes the baseline;
// otherwise a failed first read would disable change detection for good.
//
// Learned buckets (proved visible by an event, never named by the list) are
// ignored on the way out, so the list omitting them is not a change. Once the
// list names one, it is listed like any other, so its later revocation is.
func (in *Intake) membershipChanged(buckets []int64) bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.snapshot == nil {
		in.snapshot = make(map[int64]bool, len(buckets))
		for _, id := range buckets {
			in.snapshot[id] = true
		}
		return false
	}
	for _, id := range buckets {
		if !in.snapshot[id] {
			return true
		}
	}
	for _, id := range buckets {
		delete(in.learned, id)
	}
	listed := 0
	for id := range in.snapshot {
		if !in.learned[id] {
			listed++
		}
	}
	return listed != len(buckets)
}

// requestReconnect marks a reconnect due and ends the current connection now,
// rather than whenever the feed next yields.
func (in *Intake) requestReconnect() {
	select {
	case in.reconnect <- struct{}{}:
	default:
	}
	in.mu.Lock()
	cancel := in.cancelRun
	in.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (in *Intake) reconnectRequested() bool {
	select {
	case <-in.reconnect:
		return true
	default:
		return false
	}
}

// pointerWriter writes the NDJSON pointer lines through the connector's shared
// line writer: whole lines, never interleaved.
type pointerWriter struct {
	out *ndjson.Writer
}

func newPointerWriter(w io.Writer) *pointerWriter { return &pointerWriter{out: ndjson.NewWriter(w)} }

// Pointer is the line intake writes to stdout for each newly seen event. It
// carries what the feed carried and nothing more: no title, no body, no URL,
// no names. Whoever wants those pays for a read.
type Pointer struct {
	EventID       int64  `json:"event_id"`
	EventType     string `json:"event_type"`
	Kind          string `json:"kind"`
	Action        string `json:"action"`
	BucketID      int64  `json:"bucket_id"`
	CreatorID     int64  `json:"creator_id"`
	PerformedByID *int64 `json:"performed_by_id"`
	RecordingID   int64  `json:"recording_id"`
	CreatedAt     string `json:"created_at"`
	Lane          Lane   `json:"lane"`
	State         string `json:"state"`
}

func (p *pointerWriter) write(event eventfeed.Event, lane Lane) error {
	// Every string from Basecamp is stripped of terminal controls. The line is
	// a wire, but it is also what a person watching the connector sees: JSON
	// escapes C0 controls but passes C1 controls such as U+009B (CSI) through
	// as raw UTF-8, which a terminal executes. Admission's lines apply the
	// same rule.
	clean := richtext.SanitizeTerminal
	err := p.out.WriteLine(Pointer{
		EventID:       event.ID,
		EventType:     clean(event.EventType),
		Kind:          clean(event.Kind),
		Action:        clean(event.Action),
		BucketID:      event.BucketID,
		CreatorID:     event.CreatorID,
		PerformedByID: event.PerformedByID,
		RecordingID:   event.RecordingID,
		CreatedAt:     event.CreatedAt.UTC().Format(time.RFC3339Nano),
		Lane:          lane,
		State:         string(StateSeen),
	})
	if err != nil {
		return fmt.Errorf("connector: pointer line: %w", err)
	}
	return nil
}
