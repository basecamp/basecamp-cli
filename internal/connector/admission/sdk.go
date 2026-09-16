package admission

import (
	"context"
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

// NewSDKReads builds the reads over an SDK client acting as the agent.
func NewSDKReads(client *basecamp.AccountClient) Reads {
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
	s.cache.put(recordingID, sub.Subscribed)
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
			if ev.Details == nil {
				// Found, but without the delta it exists to report: that is
				// not evidence of who was added, any more than a miss is.
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

// Members reads a project's people and answers membership from that list,
// cached per project for MembershipTTL.
type Members struct {
	client *basecamp.AccountClient
	cache  *ttlCache[int64, map[int64]bool]
}

// NewMembers builds the cached membership read.
func NewMembers(client *basecamp.AccountClient, now func() time.Time) *Members {
	return &Members{client: client, cache: newTTLCache[int64, map[int64]bool](now, MembershipTTL)}
}

// NonClientMember implements MemberReader. A member for trust is a person
// the project's listing names who is neither a client nor an Agent principal
// (the listing serves agents, with client false). Only "member" is served
// from the cache; "not a member" discards the event, so it is always answered
// from a fresh listing, and someone added a minute ago is seen.
func (m *Members) NonClientMember(ctx context.Context, bucketID, personID int64) (bool, error) {
	members, ok := m.cache.get(bucketID)
	if !ok || !members[personID] {
		res, err := m.client.People().ListProjectPeople(ctx, bucketID, nil)
		if err != nil {
			return false, err
		}
		members = make(map[int64]bool, len(res.People))
		for _, p := range res.People {
			if p.ID > 0 && !p.Client && p.PersonableType != personableAgent {
				members[p.ID] = true
			}
		}
		m.cache.put(bucketID, members)
	}
	return members[personID], nil
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
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || c.now().Sub(e.fetched) >= c.ttl {
		var zero V
		return zero, false
	}
	return e.value, true
}

func (c *ttlCache[K, V]) put(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
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
	c.entries[key] = ttlEntry[V]{value: value, fetched: now}
}
