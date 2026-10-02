package fakebasecamp_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"

	"github.com/basecamp/basecamp-cli/internal/connector/fakebasecamp"
)

// The fake is held to eventfeed itself: the SDK's live connector, over its
// real seams and its real WebSocket transport, run in process against it.

// connectorFilters are the filters `basecamp connect` runs with.
var connectorFilters = eventfeed.Filters{
	Buckets:           []int64{fakebasecamp.ProjectID},
	ExcludePerformers: []int64{fakebasecamp.AgentID},
	ActorTypes:        []string{"person"},
}

// liveFeed is an eventfeed connector reading the fake, and what it delivers.
type liveFeed struct {
	delivered chan eventfeed.Event
	failed    chan error

	mu      sync.Mutex
	dropped int
}

func runLiveFeed(t *testing.T, s *fakebasecamp.Server, bearer string, startAt eventfeed.Start) *liveFeed {
	t.Helper()
	live, err := eventfeed.NewLive(&basecamp.Config{BaseURL: s.URL()}, &basecamp.StaticTokenProvider{Token: bearer},
		"999", eventfeed.AccountLane, basecamp.WithMaxRetries(0))
	require.NoError(t, err)
	f := &liveFeed{delivered: make(chan eventfeed.Event, 100), failed: make(chan error, 1)}
	conn, err := live.Connect(
		eventfeed.WithStart(startAt),
		eventfeed.WithFilters(connectorFilters),
		eventfeed.WithObserver(eventfeed.Observer{Disconnected: func(string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.dropped++
		}}),
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev, err := range conn.Events(ctx) {
			if err != nil {
				f.failed <- err
				return
			}
			f.delivered <- ev
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = conn.Close()
		conn.Wait()
		<-done
	})
	return f
}

// next is the next event the feed delivers.
func (f *liveFeed) next(t *testing.T) eventfeed.Event {
	t.Helper()
	select {
	case ev := <-f.delivered:
		return ev
	case err := <-f.failed:
		require.FailNow(t, "the feed ended", "%v", err)
	case <-bounded(t).Done():
		require.FailNow(t, "no event was delivered")
	}
	return eventfeed.Event{}
}

// expect asserts the feed delivers exactly these events next, in order.
func (f *liveFeed) expect(t *testing.T, events ...fakebasecamp.Event) {
	t.Helper()
	for _, want := range events {
		got := f.next(t)
		assert.Equal(t, want.ID, got.ID)
		assert.Equal(t, want.EventType, got.EventType)
		assert.Equal(t, want.BucketID, got.BucketID)
		assert.Equal(t, want.RecordingID, got.RecordingID)
		assert.Equal(t, want.CreatorID, got.CreatorID)
	}
}

func commentBy(creator, bucket int64) fakebasecamp.Event {
	return fakebasecamp.Event{EventType: "comment.created", BucketID: bucket, RecordingID: 9001, CreatorID: creator}
}

// The catch-up walks the poll lane page by page, through a page the
// filters empty, and an event both lanes carry is delivered once.
func TestLiveFeedWalksThePollLaneAndDedupesAcrossLanes(t *testing.T) {
	s, bearer := start(t, fakebasecamp.WithPageSize(2))
	op, agent := fakebasecamp.OperatorID, fakebasecamp.AgentID
	served, other := fakebasecamp.ProjectID, otherProject

	// History, on the poll lane only. Pages of two: the second page holds
	// only the agent's own event and one in another project, and the
	// filters leave it empty.
	e1 := s.Emit(commentBy(op, served), fakebasecamp.Poll)
	s.Emit(commentBy(op, other), fakebasecamp.Poll)
	s.Emit(commentBy(agent, served), fakebasecamp.Poll)
	s.Emit(commentBy(op, other), fakebasecamp.Poll)
	e5 := s.Emit(commentBy(op, served), fakebasecamp.Poll)

	// Hold the catch-up walk until the subscription is confirmed, so an
	// event emitted now is both buffered from the cable and served by the
	// walk.
	walk := s.Gate(fakebasecamp.RouteEvents)
	feed := runLiveFeed(t, s, bearer, eventfeed.StartBeginning())
	await(t, s, "a confirmed subscription and a held walk", func() bool { return s.Subscribers() == 1 && walk.Waiting() == 1 })
	e6 := s.Emit(commentBy(op, served), fakebasecamp.Both)
	walk.Release()

	feed.expect(t, e1, e5, e6)
	e7 := s.Emit(commentBy(op, served), fakebasecamp.Live)
	feed.expect(t, e7) // not e6 again

	var polls []string
	for _, r := range s.Requests() {
		if r.Route == fakebasecamp.RouteEvents {
			polls = append(polls, r.Query.Encode())
		}
	}
	filters := "actor_types=person&buckets=48699913&exclude_performers=52007412"
	assert.Equal(t, []string{
		filters + "&since=0",
		filters + "&position=fake-2",
		filters + "&position=fake-4",
	}, polls)
}

// A cable dropped while events are published is caught up from the poll
// lane on the way back, before the feed streams again.
func TestLiveFeedCatchesUpAfterADroppedCable(t *testing.T) {
	s, bearer := start(t)
	feed := runLiveFeed(t, s, bearer, eventfeed.StartPresent())
	await(t, s, "a confirmed subscription", func() bool { return s.Subscribers() == 1 })
	feed.expect(t, s.Emit(commentBy(fakebasecamp.OperatorID, fakebasecamp.ProjectID), fakebasecamp.Live))

	// Hold the reconnect at its mint: the gap stays open while the event is
	// published, so only the catch-up can deliver it.
	mint := s.Gate(fakebasecamp.RouteStreamTicket)
	require.Equal(t, 1, s.DropCable())
	await(t, s, "the reconnect", func() bool { return mint.Waiting() == 1 })
	missed := s.Emit(commentBy(fakebasecamp.OperatorID, fakebasecamp.ProjectID), fakebasecamp.Both)
	mint.Release()

	feed.expect(t, missed)
	feed.mu.Lock()
	assert.Equal(t, 1, feed.dropped)
	feed.mu.Unlock()
	feed.expect(t, s.Emit(commentBy(fakebasecamp.OperatorID, fakebasecamp.ProjectID), fakebasecamp.Live))
}
