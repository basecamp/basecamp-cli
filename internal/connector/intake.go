package connector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"slices"
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
	// Lines, when set, is the writer the pointer lines go through, and
	// Pointers is ignored. One process stdout has one writer and one lock, so
	// a caller wiring intake and admission onto the same stdout passes the
	// same writer to both rather than relying on them to find each other.
	Lines *ndjson.Writer
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
// downstream of the queue, so a slow admission or a busy dispatcher is
// absorbed by the backlog rather than felt by the socket — until the backlog
// reaches its pause threshold, where intake stops reading the feed on purpose
// rather than let the queue grow without bound.
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
	served *servedPolls
	// listed is what the project lister last said, and it is authoritative: a
	// bucket it stops naming is revoked.
	listed map[int64]bool
	// membershipRetry is the first backoff after a failed membership read.
	membershipRetry time.Duration
	// learned holds buckets an arriving event proved visible that the lister
	// has not named. It is provisional — the next listing that omits one takes
	// it back — so a bucket learned at runtime never becomes permanent truth.
	learned map[int64]bool
	// relearned is when each bucket was last learned. A revoked bucket is
	// learned again at most once per membership interval, so revocation
	// cannot become a reconnect per event.
	relearned map[int64]time.Time
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

	// stranded holds ids this process committed to the ledger and then failed
	// to hand over. They are the one thing the ledger's own dedupe would hide:
	// the row is there, so every retry of the event is suppressed as a
	// duplicate, and without this the id would wait for a restart.
	stranded map[int64]struct{}

	repairs     sync.WaitGroup
	repairQueue chan Loss
	// repairQueueSize and repairSweep override the pool's defaults in tests.
	repairQueueSize int
	repairSweep     time.Duration
	// inFlight is the losses a worker is walking or the queue is holding, so
	// the sweeper does not offer one twice.
	inFlight map[int64]bool
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
	if len(opts.Filters.Reasons) > 0 {
		// Reasons is the inbox lane's dimension — why an event reached YOU —
		// and the account feed does not carry it. The SDK refuses it when the
		// feed is built, but that is too late: Run starts the repair workers
		// before the feed exists, so an open loss could walk under a filter
		// set the account lane cannot honor, and a dropped dimension widens a
		// read rather than narrowing it. Configuration that cannot mean what
		// it says fails here, before any wire work.
		return nil, errors.New("connector: intake filters: reasons filter the inbox, which the account feed does not carry")
	}
	// The filter set is the checkpoint's identity, and it is also what every
	// subscription, recorded loss and repair walk runs under. A caller that
	// kept its slices could change all of those while the key stays frozen on
	// what it was built from, so they are copied here as the SDK's WithFilters
	// copies its own.
	opts.Filters = eventfeed.Filters{
		Types:             slices.Clone(opts.Filters.Types),
		Buckets:           slices.Clone(opts.Filters.Buckets),
		Creators:          slices.Clone(opts.Filters.Creators),
		Performers:        slices.Clone(opts.Filters.Performers),
		ExcludePerformers: slices.Clone(opts.Filters.ExcludePerformers),
		ActorTypes:        slices.Clone(opts.Filters.ActorTypes),
		Reasons:           slices.Clone(opts.Filters.Reasons),
	}

	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	// The queue reports a callback that panicked, and a queue built by a
	// caller who did not think about that would report it nowhere. It is
	// adopted under the queue's own lock, because the queue may already be
	// in use — admission takes from it — and a logger the caller did set is
	// kept.
	opts.Queue.adoptLogger(opts.Logger)
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
		pointer:   newPointerWriter(opts.Pointers, opts.Lines),
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
	in.resetRunState()
	// Repairs get a child lifetime that ends when Run does, whatever the
	// reason. A repair is off the delivery path: a terminal feed must not wait
	// out its sixty-second cadence, and an unfinished repair resumes on the
	// next start from the loss record.
	repairCtx, stopRepairs := context.WithCancel(ctx)
	// Deferred first, so it runs last: the pool is forgotten only once its
	// workers have stopped, and the next Run starts its own.
	defer in.releaseRepairWorkers()
	defer in.repairs.Wait()
	defer stopRepairs()
	in.lifetime = repairCtx

	if err := in.resumeReconciliation(repairCtx); err != nil {
		return err
	}
	if err := in.requeueSeen(ctx); err != nil {
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

// resetRunState clears everything scoped to one Run.
//
// Run is reusable — each one builds its own repair pool — so nothing a
// previous one decided may leak into the next: a checkpoint it saved would
// clear this run's --since before this run has saved anything, and an abort it
// suffered would end this one before it began. What survives is knowledge
// about the account rather than the run: the listed and learned projects.
func (in *Intake) resetRunState() {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.checkpointed = false
	in.abortErr = nil
	in.hasReentry = false
	in.reentry = eventfeed.Start{}
	in.reentryLog = ""
	in.reentryReplays = false
	in.replaying = false
	in.enteredByReentry = false
	in.promotedThisRun = false
	select {
	case <-in.reconnect:
	default:
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

	// Past this line the row is committed, so the event is invisible to every
	// later delivery of itself: it is this process's to finish handing over,
	// or to remember that it did not. Both failures below are that.
	if err := in.pointer.write(event, lane); err != nil {
		in.strand(event.ID)
		return err
	}
	if err := in.queue.Offer(ctx, event.ID); err != nil {
		in.strand(event.ID)
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
			// The reason is the peer's own text, and a log is read in a
			// terminal like everything else the connector writes.
			in.log.Warn("feed socket disconnected", "reason", richtext.SanitizeSingleLine(reason), "error", err)
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
			// A saved position is progress, whether or not its page carried
			// an event: this connection is no longer a re-entry waiting to
			// prove itself, and a later refusal is an ordinary one.
			in.enteredByReentry = false
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
		in.startRepair(loss)
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

// strandedIDs is the ids a handover failed on, oldest first, as the feed
// served them.
func (in *Intake) strandedIDs() []int64 {
	in.mu.Lock()
	defer in.mu.Unlock()
	ids := make([]int64, 0, len(in.stranded))
	for id := range in.stranded {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// strand remembers an id the ledger has but the queue does not.
//
// Every commit goes through ingest, and every handover failure after a commit
// goes through here, so there is one place an event can be stranded and one
// place that answers for it. A crash loses the list, and nothing is lost with
// it: the next start offers every record still in seen.
func (in *Intake) strand(id int64) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.stranded == nil {
		in.stranded = map[int64]struct{}{}
	}
	in.stranded[id] = struct{}{}
}

// sweepStranded offers the ids a failed handover left behind.
//
// A restart is not an answer for a connector that runs for weeks, and a
// repair walk is the case that makes it urgent: its failure closes nothing
// downstream, the id's loss row is already resolved by the commit, and the
// reconciliation that follows sees nothing missing. The event would be
// recorded, never judged, and never mentioned again.
//
// It offers and only then forgets: an offer refused by a canceled context
// leaves the id stranded for the next sweep, or for the next start.
func (in *Intake) sweepStranded(ctx context.Context) {
	ids := in.strandedIDs()
	if len(ids) == 0 {
		return
	}
	for _, id := range ids {
		if err := in.queue.Offer(ctx, id); err != nil {
			in.log.Warn("an event the ledger holds could not be handed over; it stays for the next sweep", "event_id", id, "error", err)
			return
		}
		in.mu.Lock()
		delete(in.stranded, id)
		in.mu.Unlock()
	}
}

// requeueSeen hands every record still in seen to the queue.
//
// The ledger row is written before the pointer line and before the hand-off,
// so that a crash can never lose the pointer — but that ordering means a
// failure after the commit leaves an event the ledger's own dedupe will
// suppress on every retry. Nothing may be left in that state, so a start
// offers every record nothing has judged yet. Offering one twice costs at
// most a second decision, never a second verdict: the queue carries ids, and
// admission's commit applies only at the revision its decision loaded.
//
// The pointer line is NOT written again here. A record that got no line when
// it was recorded — the write that failed — never gets one, and the work still
// happens through the queue. Re-emitting would put duplicate lines in front of
// every watcher for the ordinary case, to close a gap in the stream rather
// than in the work.
func (in *Intake) requeueSeen(ctx context.Context) error {
	// What is cleared at the end is what was stranded when this began. A
	// repair walk is already running by now, and it serves OLD ids, which the
	// paging below may already have passed: one stranded mid-pass would be
	// forgotten by a clear that assumed it had offered everything.
	carried := in.strandedIDs()
	var after int64
	for {
		records, err := in.ledger.RecordsInStateAfter(ctx, StateSeen, after, requeueBatch)
		if err != nil {
			return err
		}
		for _, record := range records {
			if err := in.queue.Offer(ctx, record.ID); err != nil {
				// A shutdown mid-requeue leaves the rest recorded and still
				// seen, which the next start offers again.
				return err
			}
			after = record.ID
		}
		if len(records) < requeueBatch {
			// Everything in seen has just been offered, the ids a previous
			// run stranded included.
			in.mu.Lock()
			for _, id := range carried {
				delete(in.stranded, id)
			}
			in.mu.Unlock()
			return nil
		}
	}
}

// requeueBatch is how many seen records a start reads at a time.
const requeueBatch = 500

// resumeReconciliation restarts every open loss's repair walk on start. A
// crash between the overflow and its repair is a delay, not a loss of the
// record.
func (in *Intake) resumeReconciliation(ctx context.Context) error {
	in.startRepairWorkers(ctx)
	losses, err := in.ledger.OpenLosses(ctx)
	if err != nil {
		return err
	}
	for _, loss := range losses {
		in.log.Info("resuming reconciliation of an open loss", "loss_id", loss.ID, "repair_since", loss.RepairSince)
		in.startRepair(loss)
	}
	return nil
}

// startRepair hands a loss to the repair workers.
//
// Repairs are bounded: an overloaded feed can raise an overflow per dropped
// event, and a goroutine and a poll source per loss would answer an API that
// is already struggling with a storm of walks. A loss that finds the queue
// full is left open on disk, which the next start resumes.
func (in *Intake) startRepair(loss Loss) {
	if loss.ResolvedAt != nil {
		return
	}
	in.mu.Lock()
	queue := in.repairQueue
	in.mu.Unlock()
	if queue == nil {
		// No pool: nobody would walk this loss. It stays open on disk, and
		// the next start resumes it.
		in.log.Warn("no repair workers are running; this loss stays open for the next start", "loss_id", loss.ID)
		return
	}
	in.mu.Lock()
	if in.inFlight == nil {
		in.inFlight = make(map[int64]bool)
	}
	if in.inFlight[loss.ID] {
		in.mu.Unlock()
		return
	}
	in.inFlight[loss.ID] = true
	in.mu.Unlock()

	select {
	case queue <- loss:
	default:
		// No room now. The sweeper offers it again, so a loss is never left
		// with nothing that will attempt it.
		in.mu.Lock()
		delete(in.inFlight, loss.ID)
		in.mu.Unlock()
		in.log.Warn("the repair queue is full; this loss is left for the sweep", "loss_id", loss.ID)
	}
}

// startRepairWorkers starts the fixed repair pool, once.
//
// Every Add happens here, on the goroutine that later waits, before anything
// can wait: a worker that added itself as it picked work up would be adding to
// a group a shutdown may already be waiting on, which Go refuses outright.
func (in *Intake) startRepairWorkers(ctx context.Context) {
	in.mu.Lock()
	if in.repairQueue != nil {
		in.mu.Unlock()
		return
	}
	size := in.repairQueueSize
	if size <= 0 {
		size = repairQueueDepth
	}
	queue := make(chan Loss, size)
	in.repairQueue = queue
	in.mu.Unlock()
	for range maxConcurrentRepairs {
		in.repairs.Add(1)
		go in.repairWorker(ctx, queue)
	}
	in.repairs.Add(1)
	go in.sweepLosses(ctx)
}

// sweepLosses is the periodic repair of both things that can be left behind:
// an event committed but never handed over, and an open loss nothing is
// walking.
//
// One goroutine does both, so a handover that waits at the pause threshold
// also holds up the re-offering of open losses. That is the right way round:
// the backlog is full, the pipeline is stopped, and starting more repair
// walks would only make the backlog worse.
//
// The queue is bounded, so an overloaded connector can turn one away; and a
// walk can end early, leaving its loss open. Neither may leave a loss with
// nothing that will attempt it again, and waiting for a restart is not an
// answer for a connector that runs for weeks. The sweep is what closes that:
// every open loss is either being walked, or is offered again here.
func (in *Intake) sweepLosses(ctx context.Context) {
	defer in.repairs.Done()
	every := in.repairSweep
	if every <= 0 {
		every = defaultRepairSweep
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			in.sweepStranded(ctx)
			losses, err := in.ledger.OpenLosses(ctx)
			if err != nil {
				in.log.Warn("could not read the open losses", "error", err)
				continue
			}
			for _, loss := range losses {
				in.startRepair(loss)
			}
		}
	}
}

// defaultRepairSweep is how often the open losses are re-offered.
const defaultRepairSweep = time.Minute

// releaseRepairWorkers forgets a pool whose workers have stopped, so the next
// Run starts its own. A queue left in place would take losses nobody walks,
// and say nothing.
func (in *Intake) releaseRepairWorkers() {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.repairQueue = nil
	// The marks say "a worker has this", and the workers are gone. A loss
	// still sitting in the dropped queue would otherwise be skipped by every
	// later run and every sweep.
	in.inFlight = nil
}

// repairWorker walks one loss at a time until ctx ends.
//
// A walk holds its worker for as long as it takes, the waits between its
// passes included, so a loss can sit in the queue past its own window and get
// the single catch-up pass a closed window allows. Recovery still happens; it
// happens later.
func (in *Intake) repairWorker(ctx context.Context, queue chan Loss) {
	defer in.repairs.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case loss := <-queue:
			in.runRepair(ctx, loss)
		}
	}
}

func (in *Intake) runRepair(ctx context.Context, loss Loss) {
	defer func() {
		in.mu.Lock()
		delete(in.inFlight, loss.ID)
		in.mu.Unlock()
	}()
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
}

// takeSnapshot records the buckets the lister names at the moment this
// connection subscribes — the same set the cable snapshots.
func (in *Intake) takeSnapshot(ctx context.Context) {
	if in.opts.Membership == nil {
		return
	}
	buckets, err := in.opts.Membership.Buckets(ctx)
	if err != nil {
		in.log.Warn("could not read the agent's projects; keeping the previous listing", "error", err)
		return
	}
	in.adoptListing(buckets)
}

// adoptListing replaces the authoritative set and revokes every learned bucket
// the listing does not name. It reports whether the listing changed; the first
// listing is a baseline, not a change.
func (in *Intake) adoptListing(buckets []int64) bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	listed := make(map[int64]bool, len(buckets))
	for _, id := range buckets {
		listed[id] = true
	}
	// A learned bucket is provisional, and the lister is the trust boundary:
	// one it no longer names is revoked, however recently it was learned, and
	// the next event from it is unknown again.
	for id := range in.learned {
		if !listed[id] {
			delete(in.learned, id)
			in.log.Info("a project the lister no longer names is no longer held", "bucket_id", id)
		}
	}
	changed := in.listed != nil && !sameBuckets(in.listed, listed)
	in.listed = listed
	return changed
}

func sameBuckets(a, b map[int64]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if !b[id] {
			return false
		}
	}
	return true
}

// noteBucket asks for a reconnect when an event arrives from a bucket the live
// subscription may not hold. The poll lane authorizes at read time, so it
// covers the project immediately; the live lane cannot until it re-subscribes.
func (in *Intake) noteBucket(bucketID int64) {
	in.mu.Lock()
	known := in.opts.Membership == nil || in.listed[bucketID] || in.learned[bucketID]
	if !known {
		// A bucket revoked by a listing is unknown again, but relearning it
		// is rate-limited: without that, a project the lister never names —
		// archived, or past its page — would cost a reconnect per event.
		if since, ok := in.relearned[bucketID]; ok && in.now().Sub(since) < in.opts.MembershipInterval {
			known = true
		} else {
			if in.learned == nil {
				in.learned = make(map[int64]bool)
				in.relearned = make(map[int64]time.Time)
			}
			in.learned[bucketID] = true
			in.relearned[bucketID] = in.now()
		}
	}
	in.mu.Unlock()
	if known {
		return
	}
	in.log.Info("an event arrived from a project the live subscription does not hold", "bucket_id", bucketID)
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

// maxConcurrentRepairs bounds repair walks in flight, and repairQueueDepth
// the losses waiting for one.
const (
	maxConcurrentRepairs = 2
	repairQueueDepth     = 1024
)

// defaultMembershipRetry is the first wait before re-reading a membership
// listing that failed; it doubles up to the membership interval.
const defaultMembershipRetry = 5 * time.Second

func (in *Intake) hasSnapshot() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.listed != nil
}

// membershipChanged adopts a fresh listing and reports whether the
// authoritative set changed.
func (in *Intake) membershipChanged(buckets []int64) bool {
	return in.adoptListing(buckets)
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

func newPointerWriter(w io.Writer, shared *ndjson.Writer) *pointerWriter {
	if shared != nil {
		return &pointerWriter{out: shared}
	}
	return &pointerWriter{out: ndjson.NewWriter(w)}
}

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
