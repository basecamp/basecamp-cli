package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type staticToken struct{}

func (staticToken) AccessToken(context.Context) (string, error) { return "test-token", nil }

func testClient(t *testing.T, handler http.Handler) *basecamp.AccountClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return basecamp.NewClient(&basecamp.Config{BaseURL: srv.URL}, staticToken{}, basecamp.WithMaxRetries(1)).ForAccount("999")
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestSubscriptionReadIsCachedTenMinutes(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/999/recordings/9000/subscription.json", r.URL.Path)
		calls.Add(1)
		_, _ = w.Write([]byte(`{"subscribed":true,"count":1,"subscribers":[]}`))
	}))
	c := &clock{now: testNow}
	subs := NewSubscriptions(client, c.Now)

	for range 3 {
		got, err := subs.Subscribed(context.Background(), 9000)
		require.NoError(t, err)
		assert.True(t, got)
	}
	assert.EqualValues(t, 1, calls.Load())

	c.advance(SubscriptionTTL - time.Second)
	_, err := subs.Subscribed(context.Background(), 9000)
	require.NoError(t, err)
	assert.EqualValues(t, 1, calls.Load())

	c.advance(time.Second)
	_, err = subs.Subscribed(context.Background(), 9000)
	require.NoError(t, err)
	assert.EqualValues(t, 2, calls.Load(), "an answer older than the TTL is read again")
}

func TestSubscriptionFailureIsNotCached(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"subscribed":true}`))
	}))
	subs := NewSubscriptions(client, time.Now)

	_, err := subs.Subscribed(context.Background(), 9000)
	require.Error(t, err)
	got, err := subs.Subscribed(context.Background(), 9000)
	require.NoError(t, err)
	assert.True(t, got)
}

func eventsPage(ids ...int64) []byte {
	type details struct {
		AddedPersonIDs []int64 `json:"added_person_ids,omitempty"`
	}
	type event struct {
		ID      int64    `json:"id"`
		Action  string   `json:"action"`
		Details *details `json:"details,omitempty"`
	}
	page := make([]event, 0, len(ids))
	for _, id := range ids {
		page = append(page, event{ID: id, Action: "assignment_changed", Details: &details{AddedPersonIDs: []int64{id * 10}}})
	}
	b, _ := json.Marshal(page)
	return b
}

func TestAssignmentReadWalksAtMostFivePages(t *testing.T) {
	var pages []string
	var mu sync.Mutex
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/999/recordings/9001/events.json", r.URL.Path)
		mu.Lock()
		pages = append(pages, r.URL.Query().Get("page"))
		mu.Unlock()
		// Every page is full of other events: the walk must stop at its bound.
		_, _ = w.Write(eventsPage(1, 2, 3))
	}))

	added, found, err := (&Assignments{client: client}).AddedPersonIDs(context.Background(), 9001, eventID)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, added)
	assert.Equal(t, []string{"1", "2", "3", "4", "5"}, pages)
}

func TestAssignmentReadFindsTheEvent(t *testing.T) {
	var requests atomic.Int32
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write(eventsPage(7, eventID))
			return
		}
		_, _ = w.Write(eventsPage(1, 2))
	}))

	added, found, err := (&Assignments{client: client}).AddedPersonIDs(context.Background(), 9001, eventID)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, []int64{eventID * 10}, added)
	assert.EqualValues(t, 2, requests.Load())
}

func TestAssignmentReadStopsAtTheEndOfHistory(t *testing.T) {
	var requests atomic.Int32
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Query().Get("page") == "1" {
			_, _ = w.Write(eventsPage(1))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))

	_, found, err := (&Assignments{client: client}).AddedPersonIDs(context.Background(), 9001, eventID)
	require.NoError(t, err)
	assert.False(t, found)
	assert.EqualValues(t, 2, requests.Load())
}

func TestAssignmentEventWithoutDetailsIsUnverified(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `[{"id":%d,"action":"assignment_changed"}]`, eventID)
	}))
	added, found, err := (&Assignments{client: client}).AddedPersonIDs(context.Background(), 9001, eventID)
	require.NoError(t, err)
	assert.False(t, found, "found without details says nothing about who was added")
	assert.Nil(t, added)
}

func TestNotSubscribedIsAlwaysReadFresh(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"subscribed":false}`))
			return
		}
		_, _ = w.Write([]byte(`{"subscribed":true}`))
	}))
	subs := NewSubscriptions(client, time.Now)

	got, err := subs.Subscribed(context.Background(), 9000)
	require.NoError(t, err)
	assert.False(t, got)
	got, err = subs.Subscribed(context.Background(), 9000)
	require.NoError(t, err)
	assert.True(t, got, "a subscription made after a negative answer is seen")
}

func TestAssignmentEventWithDetailsButNoAddedIDsIsUnverified(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `[{"id":%d,"action":"assignment_changed","details":{}}]`, eventID)
	}))
	_, found, err := (&Assignments{client: client}).AddedPersonIDs(context.Background(), 9001, eventID)
	require.NoError(t, err)
	assert.False(t, found)
}

func TestAssignmentEventThatAddedNobodyIsEvidence(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `[{"id":%d,"action":"assignment_changed","details":{"added_person_ids":[],"removed_person_ids":[5]}}]`, eventID)
	}))
	added, found, err := (&Assignments{client: client}).AddedPersonIDs(context.Background(), 9001, eventID)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Empty(t, added)
}

func TestSDKReadsOwnOneRetryBudget(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	// An ordinary client would retry three times underneath the admitter's
	// five attempts.
	reads := NewSDKReads(&basecamp.Config{BaseURL: srv.URL}, staticToken{}, "999")

	_, err := reads.Subscriptions.Subscribed(context.Background(), 1)
	require.Error(t, err)
	assert.EqualValues(t, 1, calls.Load(), "one HTTP request per admission attempt")
}

func TestMembershipSeesSomeoneAddedAfterTheListingWasCached(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = fmt.Fprintf(w, `[{"id":%d,"client":false}]`, operatorID)
			return
		}
		_, _ = fmt.Fprintf(w, `[{"id":%d,"client":false},{"id":%d,"client":false}]`, operatorID, memberID)
	}))
	c := &clock{now: testNow}
	members := NewMembers(client, c.Now)

	got, err := members.NonClientMember(context.Background(), routedProj, operatorID)
	require.NoError(t, err)
	assert.True(t, got)
	c.advance(MembershipRefreshFloor)
	got, err = members.NonClientMember(context.Background(), routedProj, memberID)
	require.NoError(t, err)
	assert.True(t, got)
	assert.EqualValues(t, 2, calls.Load())
}

func TestMembershipFailureIsNotCached(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprintf(w, `[{"id":%d,"client":false}]`, memberID)
	}))
	members := NewMembers(client, time.Now)

	_, err := members.NonClientMember(context.Background(), routedProj, memberID)
	require.Error(t, err)
	got, err := members.NonClientMember(context.Background(), routedProj, memberID)
	require.NoError(t, err)
	assert.True(t, got)
}

func TestMembershipListingExpires(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprintf(w, `[{"id":%d,"client":false}]`, memberID)
	}))
	c := &clock{now: testNow}
	members := NewMembers(client, c.Now)

	for range 2 {
		_, err := members.NonClientMember(context.Background(), routedProj, memberID)
		require.NoError(t, err)
	}
	assert.EqualValues(t, 1, calls.Load())
	c.advance(MembershipTTL)
	_, err := members.NonClientMember(context.Background(), routedProj, memberID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, calls.Load(), "someone removed from the project stops being trusted within the TTL")
}

func TestMembershipExcludesClientsAndIsCached(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, fmt.Sprintf("/999/projects/%d/people.json", routedProj), r.URL.Path)
		calls.Add(1)
		_, _ = fmt.Fprintf(w, `[{"id":%d,"name":"Member","personable_type":"User","client":false},{"id":%d,"name":"Client","personable_type":"Client","client":true},{"id":%d,"name":"Other agent","personable_type":"Agent","client":false}]`, memberID, clientID, otherAgent)
	}))
	members := NewMembers(client, time.Now)

	for _, tc := range []struct {
		id   int64
		want bool
	}{{memberID, true}, {clientID, false}, {otherAgent, false}, {strangerID, false}} {
		got, err := members.NonClientMember(context.Background(), routedProj, tc.id)
		require.NoError(t, err)
		assert.Equal(t, tc.want, got, "person %d", tc.id)
	}
	assert.EqualValues(t, 1, calls.Load(), "a client or an agent the listing names is a cached refusal; someone it does not name, within the refresh floor, too")
}

func TestARefusalTheListingNamesNeedsNoFreshListing(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprintf(w, `[{"id":%d,"client":true},{"id":%d,"personable_type":"Agent","client":false}]`, clientID, otherAgent)
	}))
	c := &clock{now: testNow}
	members := NewMembers(client, c.Now)

	_, err := members.NonClientMember(context.Background(), routedProj, clientID)
	require.NoError(t, err)
	c.advance(MembershipRefreshFloor)
	for range 20 {
		for _, id := range []int64{clientID, otherAgent} {
			got, err := members.NonClientMember(context.Background(), routedProj, id)
			require.NoError(t, err)
			assert.False(t, got)
		}
	}
	assert.EqualValues(t, 1, calls.Load(), "a client or agent the listing names is refused from the cache, past the floor")
}

func TestUnnamedPeopleCostOneListingPerFloor(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprintf(w, `[{"id":%d,"client":false}]`, memberID)
	}))
	c := &clock{now: testNow}
	members := NewMembers(client, c.Now)

	_, err := members.NonClientMember(context.Background(), routedProj, memberID)
	require.NoError(t, err)
	c.advance(MembershipRefreshFloor)
	for i := range int64(50) {
		got, err := members.NonClientMember(context.Background(), routedProj, 5000+i)
		require.NoError(t, err)
		assert.False(t, got)
	}
	assert.EqualValues(t, 2, calls.Load(), "fifty strangers after the floor: one fresh listing")
}

func TestCacheIsBounded(t *testing.T) {
	c := newTTLCache[int64, bool](time.Now, time.Hour)
	for i := range int64(maxCacheEntries + 10) {
		c.put(i, true)
	}
	assert.LessOrEqual(t, len(c.entries), maxCacheEntries)
}
