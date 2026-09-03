package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	actioncable "github.com/basecamp/actioncable-go"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/cable"
	"github.com/basecamp/basecamp-cli/internal/hostutil"
	"github.com/basecamp/basecamp-cli/internal/observability"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/urlarg"
)

// unreadsChannel is the channel the web sidebar subscribes to. Basecamp writes
// a reading per recording, so one thing happening rings it several times.
const unreadsChannel = "UnreadsChannel"

// Offline users don't get broadcasts, and appearing is the only thing that makes
// a connection online — for 30 seconds at a time, so the watch says it again on
// appearInterval, the same heartbeat the web keeps.
const (
	appearanceChannel = "AppearanceChannel"
	appearInterval    = 15 * time.Second
)

// Basecamp beats no ping of its own — MonitoringChannel answers ours instead —
// so a client that never pings sees no frame for six seconds and redials on a
// live connection. pingInterval is half that window, which is what the web uses.
const (
	monitoringChannel = "MonitoringChannel"
	pingInterval      = 3 * time.Second
)

// A failed read of the notifications is retried on its own, doubling the wait
// each time so a server that stays down isn't hammered. asyncScriptLimit caps
// how many --run-async commands run at once, so a busy morning can't fork a
// process per notification.
const (
	firstWatchRetry   = 2 * time.Second
	longestWatchRetry = 2 * time.Minute
	asyncScriptLimit  = 16

	// One change lands as several broadcasts, so the doorbell is answered once,
	// after they have all rung.
	watchSettle = 500 * time.Millisecond
)

// The kinds of notification a watch reports, named for whoever reads them
// rather than after Basecamp's own type and section fields. Two need both to
// tell apart: a ping is a chat in a circle, which is a direct or group message,
// and a bubble is anything Basecamp decided to show again.
var watchTypes = map[string]string{
	"Mention":     "mention",
	"Chat":        "chat",
	"Comment":     "comment",
	"Message":     "message",
	"Assignment":  "assignment",
	"Completion":  "completion",
	"Question":    "question",
	"BoostReport": "boost",
	"Event":       "event",
}

const (
	pingType   = "ping"
	bubbleType = "bubble"

	// The watch's own news, written to stdout only: a script runs per
	// notification, and neither of these is one. Neither can be filtered out
	// with --types.
	watchReady        = "ready"
	watchDisconnected = "disconnected"
)

// Basecamp's own section names, which decide the two types its type field
// cannot.
const (
	pingsSection   = "pings"
	bubblesSection = "bubbles"
)

// watchTypeOf is the kind a notification is reported as.
//
// A type Basecamp added since this list was written passes through under its
// own name, lowercased, rather than being dropped: an agent told about
// something it does not recognize can still act on it, and one told nothing
// cannot.
func watchTypeOf(notification basecamp.Notification) string {
	switch {
	case notification.Section == bubblesSection:
		return bubbleType
	case notification.Type == "Chat" && notification.Section == pingsSection:
		return pingType
	}

	if named, ok := watchTypes[notification.Type]; ok {
		return named
	}

	return strings.ToLower(notification.Type)
}

// watchableTypes is every kind --types accepts, in the order the help lists
// them: the ones aimed at a person first.
func watchableTypes() []string {
	named := make([]string, 0, 2+len(watchTypes))
	named = append(named, pingType, bubbleType)
	for _, name := range watchTypes {
		named = append(named, name)
	}
	slices.Sort(named)

	return named
}

type watchCommand struct {
	cmd         *cobra.Command
	types       []string
	asyncScript string
	syncScript  string
	exitOnFirst bool
	timeout     time.Duration
}

// NewWatchCmd creates the watch command.
func NewWatchCmd() *cobra.Command {
	watchCommand := &watchCommand{}
	watchCommand.cmd = &cobra.Command{
		Use:   "watch",
		Short: "Follow your notifications as they arrive",
		Long: `Print notifications as they arrive, one JSON object per line. Runs until interrupted.

These are the same notifications the web sidebar shows — the Hey! menu — and they
arrive over the same Action Cable stream the sidebar listens to, so nothing polls
and nothing waits. That is what this is for: being told you were mentioned, or that
something you follow changed, without a loop that wakes up every few minutes to ask.

Every line names what arrived and what to do about it: bucket_id and recording_id
address the thing itself, sgid marks it read, and url is what 'basecamp show' takes.
Title, excerpt, project and creator are there to decide whether to act at all.

--types narrows what is reported. Every kind is reported by default, and a kind this
version has no name for is passed through under Basecamp's own, so nothing is
silently dropped.

Notifications can drive a command instead of being printed, and that is a choice
between two behaviors: --run-async spawns the command per notification and moves on,
so a slow one never holds up the watch and two can overlap; --run-sync waits for
each and runs them in order. Pass one or the other.

Two lines describe the watch itself rather than a notification: "ready" once the
first read is in and the subscription is live (again after every reconnect), and
"disconnected" when the connection drops. Both go to stdout only, --types never
filters them, and neither runs a script or counts towards --exit-on-first.

Only arrivals are reported. Marking something read is something you did, not
something that happened to you, so it is not an event. And the first read is a
baseline rather than a backlog: what was already waiting when the watch started is
not reported — read that with 'basecamp notifications list'.`,
		Annotations: map[string]string{
			"agent_notes": "Long-running. Writes one JSON object per notification to stdout (NDJSON), not the usual envelope. Use --exit-on-first with --timeout to block until something arrives and then exit — this is the alternative to polling in a sleep loop.\n" +
				"Account-wide — no --in <project> needed. Each line carries bucket_id, recording_id, sgid and url to act on. The first read is a baseline; use 'notifications list' for what is already waiting.",
		},
		Example: `  basecamp watch
  basecamp watch --types mention,ping
  basecamp watch --types mention --exit-on-first --timeout 8h
  basecamp watch --types mention --run-async 'notify-send -a Basecamp "$BASECAMP_TITLE"'
  basecamp watch --run-sync ./triage.sh`,
		Args: cobra.NoArgs,
		RunE: watchCommand.run,
	}

	flags := watchCommand.cmd.Flags()
	flags.StringSliceVar(&watchCommand.types, "types", nil, "Kinds of notification to report (default all): "+strings.Join(watchableTypes(), ", "))
	flags.StringVar(&watchCommand.asyncScript, "run-async", "", "Shell command to spawn per change, without waiting for it")
	flags.StringVar(&watchCommand.syncScript, "run-sync", "", "Shell command to run per change, one at a time, waiting for each")
	flags.BoolVar(&watchCommand.exitOnFirst, "exit-on-first", false, "Exit after the first change")
	flags.DurationVar(&watchCommand.timeout, "timeout", 0, "Give up waiting after this long (for example 30m)")

	return watchCommand.cmd
}

func (c *watchCommand) run(cmd *cobra.Command, args []string) error {
	app := appctx.FromContext(cmd.Context())
	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	types, err := c.watchedTypes()
	if err != nil {
		return err
	}
	if c.asyncScript != "" && c.syncScript != "" {
		return output.ErrUsage("pass either --run-async or --run-sync, not both")
	}

	ctx, stopListeningForSignals := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stopListeningForSignals()

	if c.timeout > 0 {
		timed, giveUp := context.WithTimeout(ctx, c.timeout)
		defer giveUp()
		ctx = timed
	}

	watch := &readingsWatch{
		app:         app,
		types:       types,
		asyncScript: c.asyncScript,
		syncScript:  c.syncScript,
		exitOnFirst: c.exitOnFirst,
		out:         cmd.OutOrStdout(),
		errOut:      cmd.ErrOrStderr(),
		styled:      !app.IsMachineOutput(),
		connection:  make(chan struct{}, 1),
		running:     make(chan struct{}, asyncScriptLimit),
		seen:        map[int64]seenReading{},
		latest:      map[int64]basecamp.Notification{},
	}

	// The baseline is read before the dial so a notification that lands during
	// the handshake is reported by the first ring rather than swallowed as part
	// of the backlog.
	if err := watch.readBaseline(ctx); err != nil {
		return err
	}

	client, err := c.dial(ctx, app, watch)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	subscription, err := client.Subscribe(ctx, actioncable.Identifier{Channel: unreadsChannel},
		actioncable.OnConnected(func(reconnected bool) {
			if reconnected {
				watch.noteConnection(true)
			}
		}),
		actioncable.OnDisconnected(func(willReconnect bool) { watch.noteConnection(false) }),
		actioncable.OnRejected(func() { watch.rejected.Store(true) }))
	if err != nil {
		if errors.Is(err, actioncable.ErrRejected) {
			return output.ErrForbidden("Basecamp turned down a subscription to your notifications — this token may not carry the scope for it")
		}
		return output.ErrAPI(0, fmt.Sprintf("could not subscribe to your notifications: %v", err))
	}

	appearance, err := client.Subscribe(ctx, actioncable.Identifier{Channel: appearanceChannel})
	if err != nil {
		return appearanceError(err)
	}
	watch.appearance = appearance

	monitoring, err := client.Subscribe(ctx, actioncable.Identifier{Channel: monitoringChannel})
	if err != nil {
		return output.ErrAPI(0, fmt.Sprintf("could not join the channel that keeps the connection alive: %v", err))
	}
	watch.monitoring = monitoring

	if err := watch.listen(ctx, subscription); err != nil {
		return err
	}

	// A synchronous script's verdict is the command's verdict when we only
	// waited for the one change, and there's no other way to answer with its
	// exit code.
	if c.exitOnFirst && watch.lastScriptExit != 0 {
		os.Exit(watch.lastScriptExit)
	}

	return nil
}

// watchedTypes is the kinds --types asked for, or nothing at all, which reports
// every kind.
func (c *watchCommand) watchedTypes() (map[string]bool, error) {
	if len(c.types) == 0 {
		return nil, nil
	}

	wanted := map[string]bool{}
	for _, name := range c.types {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if !slices.Contains(watchableTypes(), name) {
			return nil, output.ErrUsage(fmt.Sprintf("unknown type %q — pass any of %s", name, strings.Join(watchableTypes(), ", ")))
		}
		wanted[name] = true
	}

	if len(wanted) == 0 {
		return nil, output.ErrUsage("--types needs at least one of " + strings.Join(watchableTypes(), ", "))
	}

	return wanted, nil
}

func (c *watchCommand) dial(ctx context.Context, app *appctx.App, watch *readingsWatch) (*actioncable.Client, error) {
	appURL := c.appURL(ctx, app, watch)
	if appURL == "" {
		return nil, output.ErrUsageHint(
			"Could not work out which Action Cable server to connect to",
			"Set "+cable.CableURLEnv+" to your cable endpoint, for example wss://chat.app.basecamp.com/"+app.Config.AccountID)
	}

	cableURL, err := cable.URL(appURL, app.Config.AccountID)
	if err != nil {
		return nil, err
	}

	// The cable server's own name is not on config.action_cable's origin
	// allowlist — the web app's is — so say which Basecamp this connection
	// belongs to rather than letting the client assume the endpoint's host.
	options := append(tracedCable(app), actioncable.WithOrigin(cable.AppURLHost(appURL)))

	client, err := cable.Dial(ctx, cableURL, app.Auth, options...)
	if err != nil {
		return nil, watchDialError(err)
	}

	return client, nil
}

// tracedCable sends the cable client's own account of itself — why a connection
// ended, what it dropped — to the trace log, which is the only place a dropped
// frame or a redial is visible from outside.
func tracedCable(app *appctx.App) []actioncable.Option {
	if !app.Tracer.Enabled(observability.TraceHTTP) {
		return nil
	}

	return []actioncable.Option{actioncable.WithLogger(actioncable.LoggerFunc(func(format string, args ...any) {
		app.Tracer.Log(observability.TraceHTTP, format, args...)
	}))}
}

// appURL is where this account's web app lives. Basecamp serves the cable from a
// host of its own, which nothing in the API reports, so the web host is taken
// from an app_url the notifications already carry and turned into the cable host
// the same way the web client does it.
//
// A localhost base URL is a development server, which serves its own cable, and
// BASECAMP_CABLE_URL settles it for anything else — a staging deployment whose
// cable host is not chat.<host>, say.
func (c *watchCommand) appURL(ctx context.Context, app *appctx.App, watch *readingsWatch) string {
	if os.Getenv(cable.CableURLEnv) != "" {
		// URL hands the override straight back, so nothing has to be resolved
		// to reach it — and a development server is reachable this way too.
		return app.Config.BaseURL
	}
	if host := cable.AppURLHost(app.Config.BaseURL); host != "" && isLocalBaseURL(app.Config.BaseURL) {
		return host
	}
	if host := cable.AppURLHost(watch.anyAppURL); host != "" {
		return host
	}

	return cable.AppURLHost(accountAppHref(ctx, app))
}

// accountAppHref asks the authorization document where this account's web app
// lives. It is the answer when the notifications carried none — an account with
// nothing in its sidebar at all — and it is empty against a BC5 issuer, whose
// document drops app_href.
func accountAppHref(ctx context.Context, app *appctx.App) string {
	endpoint, err := app.Auth.AuthorizationEndpoint(ctx)
	if err != nil {
		return ""
	}

	info, err := app.SDK.Authorization().GetInfo(ctx, &basecamp.GetInfoOptions{Endpoint: endpoint, FilterProduct: "bc3"})
	if err != nil {
		return ""
	}

	for _, account := range info.Accounts {
		if strconv.FormatInt(account.ID, 10) == app.Config.AccountID {
			return account.AppHREF
		}
	}

	return ""
}

// isLocalBaseURL says whether the configured base URL is a development server,
// which serves its own cable rather than having a host for it.
func isLocalBaseURL(baseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Host == "" {
		return false
	}

	return hostutil.IsLocalhost(parsed.Host)
}

// appearanceError says what a refused appearance costs, which is everything:
// Basecamp broadcasts to a user it considers online, and nothing else makes one
// online. A read-only token is refused until AppearanceChannel declares appear
// and away read-only.
func appearanceError(err error) error {
	if errors.Is(err, actioncable.ErrRejected) {
		return output.ErrForbidden("Basecamp turned down this token's appearance, so it would never be sent any notifications")
	}

	return output.ErrAPI(0, fmt.Sprintf("could not tell Basecamp the watch is here: %v", err))
}

// watchDialError tells the two ways a dial fails apart: the server turned the
// credentials down, or it couldn't be reached at all.
func watchDialError(err error) error {
	var disconnect *actioncable.DisconnectError
	if errors.As(err, &disconnect) && disconnect.Reason == actioncable.ReasonUnauthorized {
		return output.ErrAuth("Basecamp's cable server turned these credentials down — run `basecamp auth login` again")
	}

	return output.ErrNetwork(fmt.Errorf("could not connect to Basecamp's cable server: %w", err))
}

// seenReading is what the last read found: whether the notification was unread,
// how much activity it carried, and when it became unread. A notification
// already unread whose count grew is news again, and the time is what tells an
// unread that fell outside the response's window from one that was read.
type seenReading struct {
	unread   bool
	count    int32
	unreadAt time.Time
}

// watchEvent is one notification, as a line of NDJSON or as a script's stdin —
// or a word about the watch itself, which carries only a type and a time.
//
// The identifiers come first because they are what an agent acts on: bucket and
// recording address the thing itself, the sgid marks it read, and the url is
// what `basecamp show` takes. The rest is context for deciding whether to act.
type watchEvent struct {
	Type           string `json:"type"`
	At             string `json:"at"`
	NotificationID int64  `json:"notification_id,omitempty"`
	BucketID       int64  `json:"bucket_id,omitempty"`
	RecordingID    int64  `json:"recording_id,omitempty"`
	SGID           string `json:"sgid,omitempty"`
	URL            string `json:"url,omitempty"`
	BasecampType   string `json:"basecamp_type,omitempty"`
	Section        string `json:"section,omitempty"`
	Title          string `json:"title,omitempty"`
	Excerpt        string `json:"excerpt,omitempty"`
	Project        string `json:"project,omitempty"`
	Creator        string `json:"creator,omitempty"`
	UnreadCount    int32  `json:"unread_count,omitempty"`
}

// readingsWatch holds what a run of the command follows: what the last read
// found, and what to do with a change once it arrives.
type readingsWatch struct {
	app            *appctx.App
	appearance     *actioncable.Subscription
	monitoring     *actioncable.Subscription
	types          map[string]bool
	asyncScript    string
	syncScript     string
	exitOnFirst    bool
	out            io.Writer
	errOut         io.Writer
	styled         bool
	connection     chan struct{}
	transitionsMu  sync.Mutex
	transitions    []bool
	rejected       atomic.Bool
	seen           map[int64]seenReading
	latest         map[int64]basecamp.Notification
	anyAppURL      string
	settle         <-chan time.Time
	backoff        time.Duration
	retry          <-chan time.Time
	running        chan struct{}
	reported       int
	lastScriptExit int
}

// readBaseline records what is already waiting, without reporting any of it.
// Basecamp serves no changes feed for notifications, so there is no cursor to
// start behind: the watch reports what happens from here on, and a failed first
// read is fatal rather than retried — a baseline that never landed would report
// the whole sidebar as news.
func (w *readingsWatch) readBaseline(ctx context.Context) error {
	found, err := w.readNotifications(ctx)
	if err != nil {
		return err
	}
	w.seen = found

	return nil
}

func (w *readingsWatch) listen(ctx context.Context, subscription *actioncable.Subscription) error {
	appearing := time.NewTicker(appearInterval)
	defer appearing.Stop()

	pinging := time.NewTicker(pingInterval)
	defer pinging.Stop()

	w.appear(ctx)
	w.ping(ctx)
	w.announce(watchReady)

	for !w.finished() {
		select {
		case <-ctx.Done():
			// An interrupt or a --timeout is how a watch is meant to end.
			return nil
		case <-appearing.C:
			w.appear(ctx)
		case <-pinging.C:
			w.ping(ctx)
		case <-w.connection:
			if err := w.followConnection(ctx); err != nil {
				return err
			}
		case <-w.settle:
			w.settle = nil
			if err := w.reread(ctx); err != nil {
				return err
			}
		case <-w.retry:
			w.retry = nil
			if err := w.reread(ctx); err != nil {
				return err
			}
		case message, open := <-subscription.Messages():
			if !open {
				return w.closedError(ctx)
			}
			w.ring(message)
		}
	}

	return nil
}

// closedError tells the two ways the subscription's messages dry up apart: the
// watch was interrupted or timed out, which is how it's meant to end, or the
// connection went away for good and nothing is listening any more — which a
// watch left running unattended has to hear about rather than exiting quietly.
func (w *readingsWatch) closedError(ctx context.Context) error {
	switch {
	case ctx.Err() != nil:
		return nil //nolint:nilerr // an interrupt or a --timeout is how a watch is meant to end
	case w.rejected.Load():
		return output.ErrAuth("Basecamp's cable server turned this subscription down — run `basecamp auth login` again")
	default:
		return output.ErrNetwork(errors.New("nothing is watching for changes any more — Basecamp's cable server hung up for good"))
	}
}

// appear says the watch is here, which is what makes Basecamp broadcast to it
// at all. It appears on nothing: appearing on a recording tells the server the
// reader is looking at it, which excludes them from its unread updates — the
// opposite of what a watch wants.
func (w *readingsWatch) appear(ctx context.Context) {
	if w.appearance == nil {
		return
	}

	if err := w.appearance.Perform(ctx, "appear", map[string]any{"appearing_on": []string{}}); err != nil {
		fmt.Fprintf(w.errOut, "warning: could not tell Basecamp the watch is here: %v\n", err)
	}
}

// ping asks for the pong that keeps the connection from looking dead. The pong
// is delivered to a subscription nobody reads, which is fine — arriving at all
// is the whole point of it.
func (w *readingsWatch) ping(ctx context.Context) {
	if w.monitoring == nil {
		return
	}

	if err := w.monitoring.Perform(ctx, "ping", nil); err != nil {
		fmt.Fprintf(w.errOut, "warning: could not ping Basecamp: %v\n", err)
	}
}

// ring is the doorbell. Basecamp writes a reading per recording, so one thing
// happening rings it several times, and the payload is a set of signed ids
// rather than anything a client can read. So the ring only says "something
// changed": the re-read that follows says what, and one is ever armed.
func (w *readingsWatch) ring(actioncable.Message) {
	if w.settle == nil {
		w.settle = time.After(watchSettle)
	}
}

// noteConnection is called from the cable client's own goroutine with every
// drop and reconnect. The transitions queue up in the order they happened, and
// the signal never blocks the connection: one wake-up drains them all.
func (w *readingsWatch) noteConnection(connected bool) {
	w.transitionsMu.Lock()
	w.transitions = append(w.transitions, connected)
	w.transitionsMu.Unlock()

	select {
	case w.connection <- struct{}{}:
	default:
	}
}

// followConnection acts on the queued transitions in order: a drop is
// announced, a reconnect re-reads and then announces ready. Order is what keeps
// a reader's picture right — a reconnect that completed while a slow re-read
// held the loop must not have its earlier drop announced after its ready.
func (w *readingsWatch) followConnection(ctx context.Context) error {
	for {
		connected, queued := w.nextTransition()
		if !queued {
			return nil
		}
		if connected {
			// A reconnect is a new connection token on the server's side, so the
			// last appearance died with the old one.
			w.appear(ctx)
			if err := w.reread(ctx); err != nil {
				return err
			}
			w.announce(watchReady)
		} else {
			w.announce(watchDisconnected)
		}
	}
}

// nextTransition takes the oldest queued transition, if there is one.
func (w *readingsWatch) nextTransition() (connected, queued bool) {
	w.transitionsMu.Lock()
	defer w.transitionsMu.Unlock()

	if len(w.transitions) == 0 {
		return false, false
	}
	connected = w.transitions[0]
	w.transitions = w.transitions[1:]

	return connected, true
}

// reread reads the notifications again and reports what changed since the last
// look. A read that failed is retried on the backoff with the record where it
// was, so the change that prompted it is still ahead of the watch.
func (w *readingsWatch) reread(ctx context.Context) error {
	found, err := w.readNotifications(ctx)
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return nil //nolint:nilerr // an interrupt or a --timeout is how a watch is meant to end
		case permanentReadError(err):
			return err
		default:
			fmt.Fprintf(w.errOut, "warning: could not read your notifications: %v\n", err)
			w.readAgainLater()
			return nil
		}
	}
	// A read that worked lets the retry delay start over.
	w.backoff = 0

	arrivals := w.arrivalsBetween(w.seen, found)
	w.seen = found
	w.forgetWhatFellOff(found)

	for _, arrival := range arrivals {
		w.report(ctx, arrival)
	}

	return nil
}

// forgetWhatFellOff drops the notifications the last read no longer carried, so
// a watch left running for a week does not keep every one it ever saw.
func (w *readingsWatch) forgetWhatFellOff(found map[int64]seenReading) {
	for id := range w.latest {
		if _, still := found[id]; !still {
			delete(w.latest, id)
		}
	}
}

// readNotifications reads the sidebar's notifications and records what it
// found. Bubble ups are left out: a resurfacing is Basecamp deciding to show
// something again, not something happening.
func (w *readingsWatch) readNotifications(ctx context.Context) (map[int64]seenReading, error) {
	result, err := w.app.Account().MyNotifications().GetWithOptions(ctx, 0, basecamp.WithLimitBubbleUps())
	if err != nil {
		return nil, convertSDKError(err)
	}

	found := make(map[int64]seenReading, len(result.Unreads)+len(result.Reads))
	for _, notification := range result.Unreads {
		found[notification.ID] = seenReading{unread: true, count: notification.UnreadCount, unreadAt: notificationTime(notification)}
		w.remember(notification)
	}
	for _, notification := range result.Reads {
		found[notification.ID] = seenReading{count: notification.UnreadCount}
		w.remember(notification)
	}
	w.keepWhatFellOutsideTheWindow(found, len(result.Unreads))

	return found, nil
}

// maxUnreadWindow is how many unreads the endpoint answers with, from
// My::ReadingsController::MAX_UNREADS. Pruning does not start until 200, so
// between the two a reader's oldest unreads are missing from the response
// rather than read.
const maxUnreadWindow = 100

// keepWhatFellOutsideTheWindow carries forward the unreads this read could not
// have carried. Without it an unread pushed out of the window by newer ones is
// forgotten, and reported all over again as an arrival the next time it comes
// back into view.
func (w *readingsWatch) keepWhatFellOutsideTheWindow(found map[int64]seenReading, unreads int) {
	if unreads < maxUnreadWindow {
		return
	}

	oldest := time.Time{}
	for _, now := range found {
		if now.unread && (oldest.IsZero() || now.unreadAt.Before(oldest)) {
			oldest = now.unreadAt
		}
	}

	for id, then := range w.seen {
		if _, carried := found[id]; !carried && then.unread && then.unreadAt.Before(oldest) {
			found[id] = then
		}
	}
}

// remember keeps the notification itself, for the line a change is reported as,
// and the first app_url it sees — which is how the watch learns the web host
// the cable server's own name is built from.
func (w *readingsWatch) remember(notification basecamp.Notification) {
	w.latest[notification.ID] = notification
	if w.anyAppURL == "" {
		w.anyAppURL = notification.AppURL
	}
}

// arrivalsBetween is what arrived between two reads.
//
// Only unreads are reported: a notification going read is something the reader
// did, not something that happened to them. A notification that was already
// read when the watch first saw it is a backlog rather than news. The unread
// count is what makes a second reply on a thread news again, since the
// notification itself stays the same row.
func (w *readingsWatch) arrivalsBetween(before, after map[int64]seenReading) []watchEvent {
	var events []watchEvent

	for id, now := range after {
		then, known := before[id]
		if now.unread && (!known || !then.unread || now.count > then.count) {
			events = append(events, w.eventFor(id, now))
		}
	}

	slices.SortFunc(events, func(a, b watchEvent) int { return int(a.NotificationID - b.NotificationID) })

	return events
}

func (w *readingsWatch) eventFor(id int64, now seenReading) watchEvent {
	notification := w.latest[id]
	creator := ""
	if notification.Creator != nil {
		creator = notification.Creator.Name
	}

	event := watchEvent{
		Type:           watchTypeOf(notification),
		At:             watchTime(notificationTime(notification)),
		NotificationID: id,
		SGID:           notification.ReadableSGID,
		URL:            notification.AppURL,
		BasecampType:   notification.Type,
		Section:        notification.Section,
		Title:          richtext.SanitizeSingleLine(notification.Title),
		Excerpt:        richtext.SanitizeSingleLine(notification.ContentExcerpt),
		Project:        richtext.SanitizeSingleLine(notification.BucketName),
		Creator:        richtext.SanitizeSingleLine(creator),
		UnreadCount:    now.count,
	}
	event.BucketID, event.RecordingID = addressedBy(notification.AppURL)

	return event
}

// addressedBy is the project and recording a notification's app_url names, so
// nothing downstream has to take a URL apart to act on it.
func addressedBy(appURL string) (bucketID, recordingID int64) {
	parsed := urlarg.Parse(appURL)
	if parsed == nil {
		return 0, 0
	}

	bucketID, _ = strconv.ParseInt(parsed.ProjectID, 10, 64)
	recordingID, _ = strconv.ParseInt(parsed.RecordingID, 10, 64)

	return bucketID, recordingID
}

// notificationTime is when the row happened, by whichever of the transition
// times the notification carries — the same field each list is ordered by.
func notificationTime(notification basecamp.Notification) time.Time {
	switch {
	case notification.UnreadAt != nil:
		return *notification.UnreadAt
	case notification.ReadAt != nil:
		return *notification.ReadAt
	default:
		return notification.CreatedAt
	}
}

// permanentReadError tells a read that will never work from one that might.
// Credentials the server won't take don't get better by waiting two minutes,
// and a watch that retried them silently would sit there for hours and still
// exit 0.
func permanentReadError(err error) bool {
	switch output.AsError(err).Code {
	case output.CodeUsage, output.CodeAuth, output.CodeForbidden:
		return true
	default:
		return false
	}
}

// readAgainLater arms the retry that comes back to a read that failed. Without
// it a failed read consumes the ring that prompted it, and the change stays
// invisible until the next notification happens along.
func (w *readingsWatch) readAgainLater() {
	if w.retry == nil {
		w.backoff = min(max(2*w.backoff, firstWatchRetry), longestWatchRetry)
		w.retry = time.After(w.backoff)
	}
}

// report hands one change on — printed, or run through the script.
func (w *readingsWatch) report(ctx context.Context, event watchEvent) {
	if w.finished() || !w.reporting(event) {
		return
	}
	w.reported++

	switch {
	case w.asyncScript != "":
		w.spawnScript(ctx, event)
	case w.syncScript != "":
		w.lastScriptExit = w.runScript(ctx, event)
	case w.styled:
		fmt.Fprintln(w.out, watchLine(event))
	default:
		w.writeJSON(event)
	}
}

// reporting says whether a notification is handed on. No --types means every
// kind, including one this version has no name for.
func (w *readingsWatch) reporting(event watchEvent) bool {
	return w.types == nil || w.types[event.Type]
}

func (w *readingsWatch) finished() bool {
	return w.exitOnFirst && w.reported > 0
}

// announce writes a word about the watch itself to whoever reads the stream. A
// script runs per change and this is not one, so a --run-* watch isn't told,
// and it never counts towards --exit-on-first.
func (w *readingsWatch) announce(news string) {
	if w.asyncScript != "" || w.syncScript != "" {
		return
	}

	event := watchEvent{Type: news, At: watchTime(time.Now())}
	if w.styled {
		fmt.Fprintln(w.out, watchLine(event))
	} else {
		w.writeJSON(event)
	}
}

// spawnScript starts the script and leaves it to get on with it. Whether it
// worked, and whether it overlaps with the next one, is the script's business —
// and it outlives the watch, so interrupting `basecamp` doesn't cut a script off
// halfway.
//
// Only asyncScriptLimit of them run at once: a busy morning carries a lot of
// changes, and a slow script would have a process per notification all fighting
// for the machine. Once they're all busy the watch waits for one to finish — or
// for an interrupt, which drops the change rather than hanging on a script that
// never ends.
func (w *readingsWatch) spawnScript(ctx context.Context, event watchEvent) {
	command, err := w.scriptCommand(context.WithoutCancel(ctx), w.asyncScript, event)
	if err != nil {
		fmt.Fprintf(w.errOut, "warning: could not run %q: %v\n", w.asyncScript, err)
		return
	}

	select {
	case w.running <- struct{}{}:
	case <-ctx.Done():
		return
	}

	if err := command.Start(); err != nil {
		<-w.running
		fmt.Fprintf(w.errOut, "warning: could not run %q: %v\n", w.asyncScript, err)
		return
	}

	go func() {
		_ = command.Wait()
		<-w.running
	}()
}

func (w *readingsWatch) runScript(ctx context.Context, event watchEvent) int {
	command, err := w.scriptCommand(ctx, w.syncScript, event)
	if err != nil {
		fmt.Fprintf(w.errOut, "warning: could not run %q: %v\n", w.syncScript, err)
		return 1
	}

	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			fmt.Fprintf(w.errOut, "warning: %q exited %d\n", w.syncScript, exit.ExitCode())
			return exit.ExitCode()
		}

		fmt.Fprintf(w.errOut, "warning: could not run %q: %v\n", w.syncScript, err)
		return 1
	}

	return 0
}

// scriptCommand hands the event over twice: as JSON on the script's stdin, for
// jq, and as environment variables, for a one-liner that only wants to know
// what happened.
func (w *readingsWatch) scriptCommand(ctx context.Context, script string, event watchEvent) (*exec.Cmd, error) {
	payload, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}

	command := shellCommand(ctx, script)
	command.Stdin = bytes.NewReader(append(payload, '\n'))
	command.Stdout = w.out
	command.Stderr = w.errOut
	command.Env = append(withoutWatchVariables(os.Environ()), event.environment()...)

	return command, nil
}

// The variables a script is handed per event. Any of them already in the
// environment — a watch started by another watch's script, say — would reach
// the script for an event that does not set it: a project name on a change from
// another project, a creator on one that has none. They are the event's to set
// or leave unset.
var watchVariables = []string{
	"BASECAMP_TYPE", "BASECAMP_AT", "BASECAMP_NOTIFICATION_ID", "BASECAMP_BUCKET_ID",
	"BASECAMP_RECORDING_ID", "BASECAMP_SGID", "BASECAMP_URL", "BASECAMP_BASECAMP_TYPE",
	"BASECAMP_SECTION", "BASECAMP_TITLE", "BASECAMP_EXCERPT", "BASECAMP_PROJECT",
	"BASECAMP_CREATOR", "BASECAMP_UNREAD_COUNT",
}

func withoutWatchVariables(environment []string) []string {
	return slices.DeleteFunc(slices.Clone(environment), func(variable string) bool {
		name, _, _ := strings.Cut(variable, "=")
		return slices.Contains(watchVariables, name)
	})
}

func (e watchEvent) environment() []string {
	environment := []string{
		"BASECAMP_TYPE=" + e.Type,
		"BASECAMP_AT=" + e.At,
	}

	for name, value := range map[string]string{
		"BASECAMP_SGID":          e.SGID,
		"BASECAMP_URL":           e.URL,
		"BASECAMP_BASECAMP_TYPE": e.BasecampType,
		"BASECAMP_SECTION":       e.Section,
		"BASECAMP_TITLE":         e.Title,
		"BASECAMP_EXCERPT":       e.Excerpt,
		"BASECAMP_PROJECT":       e.Project,
		"BASECAMP_CREATOR":       e.Creator,
	} {
		if value != "" {
			environment = append(environment, name+"="+value)
		}
	}

	for name, value := range map[string]int64{
		"BASECAMP_NOTIFICATION_ID": e.NotificationID,
		"BASECAMP_BUCKET_ID":       e.BucketID,
		"BASECAMP_RECORDING_ID":    e.RecordingID,
		"BASECAMP_UNREAD_COUNT":    int64(e.UnreadCount),
	} {
		if value != 0 {
			environment = append(environment, name+"="+strconv.FormatInt(value, 10))
		}
	}
	slices.Sort(environment)

	return environment
}
func (w *readingsWatch) writeJSON(event watchEvent) {
	payload, err := json.Marshal(event)
	if err != nil {
		fmt.Fprintf(w.errOut, "warning: could not write a change: %v\n", err)
		return
	}

	fmt.Fprintln(w.out, string(payload))
}

func shellCommand(ctx context.Context, script string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/c", script) // #nosec G204 -- running the command the caller asked for is the point
	}

	return exec.CommandContext(ctx, "sh", "-c", script) // #nosec G204 -- running the command the caller asked for is the point
}

func watchLine(event watchEvent) string {
	var description string
	switch {
	case event.Type == watchReady:
		description = "watching for notifications"
	case event.Type == watchDisconnected:
		description = "connection lost — reconnecting"
	case event.Creator != "":
		description = fmt.Sprintf("%s — %s", event.Creator, truncateLine(event.Title, 60))
	case event.Title != "":
		description = truncateLine(event.Title, 60)
	default:
		description = fmt.Sprintf("notification %d", event.NotificationID)
	}

	return fmt.Sprintf("%s  %-12s %-24s %s", event.At, event.Type, truncateLine(event.Project, 24), description)
}

func truncateLine(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}

	return string(runes[:max(limit-1, 0)]) + "…"
}

func watchTime(at time.Time) string {
	if at.IsZero() {
		return ""
	}

	return at.UTC().Format("2006-01-02T15:04:05.000Z")
}
