package admission

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// Read bounds.
const (
	// SubscriptionTTL is how long a subscription answer is reused.
	SubscriptionTTL = 10 * time.Minute
	// MembershipTTL is how long a project's member list is reused.
	MembershipTTL = 10 * time.Minute
	// MaxAssignmentPages bounds the events read for an assignment delta.
	MaxAssignmentPages = 5
	// maxCacheEntries bounds each cache, so a process alive for weeks never
	// grows past it whatever it has seen.
	maxCacheEntries = 4096
)

// NewSDKReads builds the reads over a client acting as the agent, in the
// account. The client is built here, with the SDK's own retries off: the
// admitter owns the retry budget (DefaultReadAttempts per read), and a second
// layer underneath would multiply it.
func NewSDKReads(cfg *basecamp.Config, tokens basecamp.TokenProvider, accountID string, opts ...basecamp.ClientOption) Reads {
	opts = append(slices.Clone(opts), basecamp.WithMaxRetries(1))
	client := basecamp.NewClient(cfg, tokens, opts...).ForAccount(accountID)
	return Reads{
		Summaries:     client.Recordings(),
		Subscriptions: NewSubscriptions(client, time.Now),
		Assignments:   &Assignments{client: client},
		Members:       NewMembers(client, time.Now),
	}
}

// Subscriptions reads the agent's own subscription to a recording, cached for
// SubscriptionTTL. Only answers are cached; a failed read is retried fresh.
type Subscriptions struct {
	client *basecamp.AccountClient
	cache  *ttlCache[int64, bool]
}

// NewSubscriptions builds the cached subscription read.
func NewSubscriptions(client *basecamp.AccountClient, now func() time.Time) *Subscriptions {
	return &Subscriptions{client: client, cache: newTTLCache[int64, bool](now, SubscriptionTTL)}
}

// Subscribed implements SubscriptionReader. Only "subscribed" is served from
// the cache. "Not subscribed" is always read fresh, because it discards the
// event: a subscription made a minute ago must not be answered by a snapshot
// from before it.
func (s *Subscriptions) Subscribed(ctx context.Context, recordingID int64) (bool, error) {
	if v, ok := s.cache.get(recordingID); ok && v {
		return true, nil
	}
	sub, err := s.client.Subscriptions().Get(ctx, recordingID)
	if err != nil {
		return false, err
	}
	// Only "subscribed" is ever served, so only it takes a slot: a stream of
	// recordings the agent is not on must not evict the ones it is.
	if sub.Subscribed {
		s.cache.put(recordingID, true)
	}
	return sub.Subscribed, nil
}

// Assignments walks a recording's events, newest first as Basecamp serves
// them, at most MaxAssignmentPages pages, for one assignment event.
type Assignments struct {
	client *basecamp.AccountClient
}

// AddedPersonIDs implements AssignmentReader.
func (a *Assignments) AddedPersonIDs(ctx context.Context, recordingID, eventID int64) ([]int64, bool, error) {
	for page := 1; page <= MaxAssignmentPages; page++ {
		res, err := a.client.Events().List(ctx, recordingID, &basecamp.EventListOptions{Page: page})
		if err != nil {
			return nil, false, err
		}
		for _, ev := range res.Events {
			if ev.ID != eventID {
				continue
			}
			if ev.Details == nil || ev.Details.AddedPersonIDs == nil {
				// Found, but without the delta it exists to report — no
				// details, or details without added_person_ids: not evidence
				// of who was added, any more than a miss is. An explicit empty
				// list is evidence, and is returned as such.
				return nil, false, nil
			}
			return ev.Details.AddedPersonIDs, true, nil
		}
		if len(res.Events) == 0 {
			break
		}
	}
	return nil, false, nil
}

// Members reads a project's people and answers membership from that listing.
type Members struct {
	client     *basecamp.AccountClient
	now        func() time.Time
	cache      *ttlCache[int64, projectPeople]
	refreshing keyedMutex
}

// projectPeople is one listing: who is a member for trust.
type projectPeople struct {
	members map[int64]bool
}

// MembershipRefreshFloor bounds how often a project's listing is read.
const MembershipRefreshFloor = 30 * time.Second

// NewMembers builds the cached membership read.
func NewMembers(client *basecamp.AccountClient, now func() time.Time) *Members {
	return &Members{client: client, now: now, cache: newTTLCache[int64, projectPeople](now, MembershipTTL)}
}

// NonClientMember implements MemberReader. A member for trust is a person the
// project's listing names who is neither a client nor an Agent principal (the
// listing serves agents, with client false).
//
// "Yes" is served from a cached listing for its TTL. "No" discards the event,
// so it is served only from a listing read at or after asOf; everyone the
// listing did not yet know about, or knew as a client since promoted, was
// added after the event and cannot have been trusted at it. With only an
// older listing, the listing is read again — at most once per
// MembershipRefreshFloor per project, so a burst of events costs one listing,
// which then answers every event seen before it. Inside the floor the answer
// is ErrMembershipUnverified: held, never refused.
func (m *Members) NonClientMember(ctx context.Context, bucketID, personID int64, asOf time.Time) (bool, error) {
	if member, answered, err := m.fromCache(bucketID, personID, asOf); answered {
		return member, err
	}
	// One refresh per project at a time. Whoever waited here looks at the
	// cache again first: the refresh it waited on may already answer it.
	unlock, err := m.refreshing.lock(ctx, strconv.FormatInt(bucketID, 10))
	if err != nil {
		return false, err
	}
	defer unlock()
	if member, answered, err := m.fromCache(bucketID, personID, asOf); answered {
		return member, err
	}

	// Stamped when the request started, not when it landed: a listing
	// describes the project as of some moment after it was asked for, so an
	// event seen while it was in flight is not taken as covered by it.
	started := m.now()
	res, err := m.client.People().ListProjectPeople(ctx, bucketID, nil)
	if err != nil {
		return false, err
	}
	people := projectPeople{members: make(map[int64]bool, len(res.People))}
	for _, p := range res.People {
		if p.ID > 0 && !p.Client && p.PersonableType != personableAgent {
			people.members[p.ID] = true
		}
	}
	m.cache.putIfNewer(bucketID, people, started)
	return people.members[personID], nil
}

// fromCache answers from the cached listing when it can: a member for the
// TTL, a refusal only from a listing asked for at or after asOf. Inside the
// refresh floor it answers ErrMembershipUnverified. answered is false when
// the listing must be read.
func (m *Members) fromCache(bucketID, personID int64, asOf time.Time) (member, answered bool, err error) {
	people, fetched, ok := m.cache.getWithAge(bucketID)
	switch {
	case !ok:
		return false, false, nil
	case people.members[personID] || !fetched.Before(asOf):
		return people.members[personID], true, nil
	case m.now().Sub(fetched) < MembershipRefreshFloor:
		return false, true, ErrMembershipUnverified
	}
	return false, false, nil
}

type ttlCache[K comparable, V any] struct {
	mu      sync.Mutex
	now     func() time.Time
	ttl     time.Duration
	entries map[K]ttlEntry[V]
}

type ttlEntry[V any] struct {
	value   V
	fetched time.Time
}

func newTTLCache[K comparable, V any](now func() time.Time, ttl time.Duration) *ttlCache[K, V] {
	return &ttlCache[K, V]{now: now, ttl: ttl, entries: map[K]ttlEntry[V]{}}
}

func (c *ttlCache[K, V]) get(key K) (V, bool) {
	v, _, ok := c.getWithAge(key)
	return v, ok
}

func (c *ttlCache[K, V]) getWithAge(key K) (V, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || c.now().Sub(e.fetched) >= c.ttl {
		var zero V
		return zero, time.Time{}, false
	}
	return e.value, e.fetched, true
}

func (c *ttlCache[K, V]) put(key K, value V) {
	c.putIfNewer(key, value, c.now())
}

// putIfNewer stores value as fetched at the given time, unless the cache
// already holds a value fetched later: an answer that was slower to arrive
// never replaces a newer one.
func (c *ttlCache[K, V]) putIfNewer(key K, value V, fetched time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok && e.fetched.After(fetched) {
		return
	}
	now := c.now()
	if len(c.entries) >= maxCacheEntries {
		for k, e := range c.entries {
			if now.Sub(e.fetched) >= c.ttl {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= maxCacheEntries {
			clear(c.entries)
		}
	}
	c.entries[key] = ttlEntry[V]{value: value, fetched: fetched}
}
