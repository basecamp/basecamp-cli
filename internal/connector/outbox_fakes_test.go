package connector

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

// Test fixtures for the outbox. Names carry an "ob" prefix so they never
// collide with the dispatcher's own test helpers.

const (
	obRoute           = "/work/connector"
	obEventRecording  = int64(10304028972) // testEvent's recording
	obReplyRecording  = int64(10304028989) // admittedVerdict's reply destination
	obCampfire        = int64(10304030000)
	obOtherPersonID   = int64(1001)
	obUnreachableNote = "listing refused"
)

// obClock is a settable clock shared by a ledger.
type obClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *obClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *obClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// obLedger is a ledger with the lifecycle hooks installed and a settable
// clock.
func obLedger(t *testing.T) (*Ledger, *obClock) {
	t.Helper()
	ledger := newTestLedger(t)
	clock := &obClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	ledger.now = clock.Now
	ledger.SetHooks(LifecycleHooks(ledger, LifecycleOptions{}))
	return ledger, clock
}

func obAdmit(t *testing.T, ledger *Ledger, id int64, key string) {
	t.Helper()
	seenRecord(t, ledger, id)
	_, err := ledger.Admission().Commit(context.Background(), admittedVerdict(id, 0, key))
	require.NoError(t, err)
}

// obNoRouteVerdict is a mention in a project with no route.
func obNoRouteVerdict(id, revision int64, reply admission.ReplyDestination) admission.Verdict {
	v := admittedVerdict(id, revision, "recording:10304028989")
	v.State, v.Reason = admission.StateBlocked, admission.ReasonNoRoute
	v.Routed, v.Route, v.Class, v.Snapshot = false, "", "", nil
	v.Reply = &reply
	return v
}

func obLaunch(t *testing.T, ledger *Ledger, id int64) Launch {
	t.Helper()
	l, err := ledger.LaunchTask(context.Background(), LaunchSpec{EventID: id, Route: obRoute, Driver: "fake", Deadline: time.Hour})
	require.NoError(t, err)
	return l
}

func obIntent(t *testing.T, ledger *Ledger, key string) Intent {
	t.Helper()
	intents, err := ledger.Intents(context.Background(), IntentFilter{})
	require.NoError(t, err)
	for _, in := range intents {
		if in.Key == key {
			return in
		}
	}
	t.Fatalf("no intent %s", key)
	return Intent{}
}

func obIntents(t *testing.T, ledger *Ledger) []Intent {
	t.Helper()
	intents, err := ledger.Intents(context.Background(), IntentFilter{})
	require.NoError(t, err)
	return intents
}

// fakeBasecamp is Basecamp as the outbox sees it: messages at destinations,
// each with its creator.
type fakeBasecamp struct {
	mu       sync.Mutex
	nextID   int64
	messages map[Destination][]fakeMessage
	posts    int
	lists    int

	// beforePost runs before a message is created; an error fails the post
	// with nothing created.
	beforePost func(dest Destination, body string) error
	// afterPost runs after a message is created; an error fails the post
	// with the message already created.
	afterPost func(dest Destination, id int64) error
	listErr   error
	clock     func() time.Time
}

type fakeMessage struct {
	PostedMessage
	creator int64
}

func newFakeBasecamp(clock func() time.Time) *fakeBasecamp {
	return &fakeBasecamp{nextID: 90000, messages: map[Destination][]fakeMessage{}, clock: clock}
}

func obKey(d Destination) Destination { return Destination{Kind: d.Kind, RecordingID: d.RecordingID} }

// add puts a message at a destination as if someone had posted it.
func (f *fakeBasecamp) add(dest Destination, creator int64, content string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.messages[obKey(dest)] = append(f.messages[obKey(dest)], fakeMessage{
		PostedMessage: PostedMessage{ID: f.nextID, CreatedAt: f.clock(), Content: content}, creator: creator,
	})
	return f.nextID
}

func (f *fakeBasecamp) Post(_ context.Context, dest Destination, body string) (int64, error) {
	f.mu.Lock()
	f.posts++
	before, after := f.beforePost, f.afterPost
	f.mu.Unlock()
	if before != nil {
		if err := before(dest, body); err != nil {
			return 0, err
		}
	}
	id := f.add(dest, adapterAgentID, body)
	if after != nil {
		if err := after(dest, id); err != nil {
			return 0, err
		}
	}
	return id, nil
}

func (f *fakeBasecamp) List(_ context.Context, dest Destination, since time.Time) ([]PostedMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []PostedMessage
	for _, m := range f.messages[obKey(dest)] {
		if m.creator == adapterAgentID && !m.CreatedAt.Before(since) {
			out = append(out, m.PostedMessage)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeBasecamp) at(dest Destination) []fakeMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeMessage(nil), f.messages[obKey(dest)]...)
}

func (f *fakeBasecamp) postCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.posts
}

var errWire = errors.New("connection reset by peer")

func obOutbox(t *testing.T, ledger *Ledger, poster Poster) *Outbox {
	t.Helper()
	ob, err := NewOutbox(OutboxOptions{Ledger: ledger, Poster: poster})
	require.NoError(t, err)
	return ob
}
