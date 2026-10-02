package fakebasecamp

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The account event feed's poll lane, and the events both lanes carry.

// Lane is where an event is published: the live cable, the poll walk, or
// both.
type Lane int

// The lanes.
const (
	Live Lane = 1 << iota
	Poll
	Both = Live | Poll
)

// Event is one account feed event. Emit fills in what is left zero.
type Event struct {
	// ID is the feed-global event id. Zero takes the next one.
	ID int64
	// EventType is the cataloged type, as "comment.created". Required.
	EventType string
	// Kind and Action default from EventType: comment_created, created.
	Kind   string
	Action string

	BucketID    int64
	RecordingID int64
	CreatorID   int64
	// PerformedByID is the agent that carried out a delegated action; zero
	// for an action performed directly.
	PerformedByID int64
	// ActorType is "person" or "agent"; it defaults to what the effective
	// performer is.
	ActorType        string
	VisibleToClients bool
	// CreatedAt defaults to the moment it is emitted, by the fake's clock
	// (WithClock). It is a real time and not the fake's fixed epoch: the
	// connector hands off only a request created at most
	// connector.HandoffGrace before its run started, judging by this
	// created_at as the feed served it, so an event stamped in the past is
	// discarded as before_this_run.
	CreatedAt time.Time
	// Details is the feed's own detail object, for the types that publish
	// one (boost.created, card.moved).
	Details json.RawMessage
	// AddedPersonIDs is an assignment change's delta. It is not on the
	// feed: it goes into the recording's own history, where admission
	// reads it.
	AddedPersonIDs []int64
}

// performer is who the feed's performer filters match: the delegate when
// there is one, the creator otherwise.
func (ev Event) performer() int64 {
	if ev.PerformedByID != 0 {
		return ev.PerformedByID
	}
	return ev.CreatorID
}

// fedEvent is an emitted event, and the lanes it is on.
type fedEvent struct {
	ev    Event
	lanes Lane
}

// Emit publishes a new event on lanes, and returns it as published. An
// event about a recording the world holds is added to that recording's
// history too, under the same id, as Basecamp records it.
func (s *Server) Emit(ev Event, lanes Lane) Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	last := int64(0)
	if n := len(s.events); n > 0 {
		last = s.events[n-1].ev.ID
	}
	if ev.ID == 0 {
		ev.ID = last + 1
	}
	ev.Details = slices.Clone(ev.Details)
	ev.AddedPersonIDs = slices.Clone(ev.AddedPersonIDs)
	if err := s.completeLocked(&ev, last); err != nil {
		s.r.Errorf("fakebasecamp: emitting %s: %v", ev.EventType, err)
		return ev
	}
	s.events = append(s.events, &fedEvent{ev: ev, lanes: lanes & Both})
	if r, ok := s.world.Recordings[ev.RecordingID]; ok {
		r.Events = append(r.Events, RecordingEvent{
			ID: ev.ID, Action: ev.Action, CreatorID: ev.CreatorID, PerformedByID: ev.PerformedByID,
			CreatedAt: ev.CreatedAt, AddedPersonIDs: ev.AddedPersonIDs,
		})
	}
	if lanes&Live != 0 {
		s.pushLocked(ev)
	}
	s.notifyLocked()
	return ev
}

// Publish puts an emitted event on more lanes. On Live it is pushed again
// even when it was pushed before: a duplicate, as the live lane may send.
// On Poll it is served from then on, to every walk that has not yet passed
// its id; a walk already past it never sees it, as on Basecamp.
func (s *Server) Publish(id int64, lanes Lane) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, found := slices.BinarySearchFunc(s.events, id, func(e *fedEvent, id int64) int { return cmp.Compare(e.ev.ID, id) })
	if !found {
		s.r.Errorf("fakebasecamp: publishing event %d, which was never emitted", id)
		return
	}
	fe := s.events[i]
	fe.lanes |= lanes & Both
	if lanes&Live != 0 {
		s.pushLocked(fe.ev)
	}
	s.notifyLocked()
}

// completeLocked fills an event's defaults and refuses one eventfeed would.
func (s *Server) completeLocked(ev *Event, last int64) error {
	if ev.ID <= last {
		return fmt.Errorf("event %d does not follow event %d: the feed's ids only ascend", ev.ID, last)
	}
	i := strings.LastIndex(ev.EventType, ".")
	if i <= 0 || i == len(ev.EventType)-1 {
		return fmt.Errorf("%q is not a cataloged event type", ev.EventType)
	}
	if ev.BucketID <= 0 || ev.RecordingID <= 0 || ev.CreatorID <= 0 || ev.PerformedByID < 0 {
		return fmt.Errorf("event %d needs a bucket, a recording and a creator", ev.ID)
	}
	if ev.Kind == "" {
		ev.Kind = strings.ReplaceAll(ev.EventType, ".", "_")
	}
	if ev.Action == "" {
		ev.Action = ev.EventType[i+1:]
	}
	if ev.ActorType == "" {
		ev.ActorType = "person"
		if s.world.isAgent(ev.performer()) {
			ev.ActorType = "agent"
		}
	}
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = s.opts.now().UTC()
	}
	return nil
}

// pollRow is an event as the poll lane serves it.
type pollRow struct {
	ID            int64           `json:"id"`
	Kind          string          `json:"kind"`
	Action        string          `json:"action"`
	EventType     string          `json:"event_type"`
	BucketID      int64           `json:"bucket_id"`
	CreatorID     int64           `json:"creator_id"`
	PerformedByID *int64          `json:"performed_by_id"`
	RecordingID   int64           `json:"recording_id"`
	CreatedAt     time.Time       `json:"created_at"`
	Details       json.RawMessage `json:"details,omitempty"`
}

// pushPayload is an event as the cable pushes it: the poll row and the two
// transport members only the push carries.
type pushPayload struct {
	pollRow
	ActorType        string `json:"actor_type"`
	VisibleToClients bool   `json:"visible_to_clients"`
}

func rowOf(ev Event) pollRow {
	row := pollRow{
		ID: ev.ID, Kind: ev.Kind, Action: ev.Action, EventType: ev.EventType,
		BucketID: ev.BucketID, CreatorID: ev.CreatorID, RecordingID: ev.RecordingID,
		CreatedAt: ev.CreatedAt, Details: ev.Details,
	}
	if ev.PerformedByID != 0 {
		performer := ev.PerformedByID
		row.PerformedByID = &performer
	}
	return row
}

// filters are the feed's filter dimensions, on either lane.
type filters struct {
	// present is each dimension the request named, with its value as it
	// was presented: what a page's next URL has to repeat.
	present  url.Values
	types    []string
	buckets  []int64
	creators []int64
	// performers and excluded hold decimal ids or "self".
	performers []string
	excluded   []string
	actorTypes []string
}

// filterKeys are the dimensions, in the order the SDK writes them.
var filterKeys = []string{"types", "buckets", "creators", "performers", "exclude_performers", "actor_types"}

// parseFilters reads the dimensions through get, which answers a key's
// comma-joined value.
func parseFilters(get func(key string) string) (filters, error) {
	f := filters{present: url.Values{}}
	for _, key := range filterKeys {
		raw := get(key)
		if raw == "" {
			continue
		}
		f.present.Set(key, raw)
		values := strings.Split(raw, ",")
		switch key {
		case "types":
			f.types = values
		case "buckets", "creators":
			ids := make([]int64, 0, len(values))
			for _, v := range values {
				id, err := strconv.ParseInt(v, 10, 64)
				if err != nil || id <= 0 {
					return filters{}, fmt.Errorf("%s holds %q, which is not an id", key, v)
				}
				ids = append(ids, id)
			}
			if key == "buckets" {
				f.buckets = ids
			} else {
				f.creators = ids
			}
		case "performers", "exclude_performers":
			for _, v := range values {
				if id, err := strconv.ParseInt(v, 10, 64); v != "self" && (err != nil || id <= 0) {
					return filters{}, fmt.Errorf("%s holds %q, which is neither an id nor self", key, v)
				}
			}
			if key == "performers" {
				f.performers = values
			} else {
				f.excluded = values
			}
		case "actor_types":
			for _, v := range values {
				if v != "person" && v != "agent" {
					return filters{}, fmt.Errorf("actor_types holds %q", v)
				}
			}
			f.actorTypes = values
		}
	}
	return f, nil
}

// visibleLocked reports whether caller sees ev through these filters. An
// event in a project the caller is not on is never seen.
func (s *Server) visibleLocked(ev Event, f filters, caller int64) bool {
	if !s.world.member(ev.BucketID, caller) {
		return false
	}
	names := func(list []string, person int64) bool {
		for _, v := range list {
			if v == "self" && person == caller || v == strconv.FormatInt(person, 10) {
				return true
			}
		}
		return false
	}
	switch {
	case f.types != nil && !slices.Contains(f.types, ev.EventType),
		f.buckets != nil && !slices.Contains(f.buckets, ev.BucketID),
		f.creators != nil && !slices.Contains(f.creators, ev.CreatorID),
		f.performers != nil && !names(f.performers, ev.performer()),
		names(f.excluded, ev.performer()),
		f.actorTypes != nil && !slices.Contains(f.actorTypes, ev.ActorType):
		return false
	}
	return true
}

// position is the opaque token for a walk that has passed id.
func position(id int64) string { return "fake-" + strconv.FormatInt(id, 10) }

// events is the poll lane: a page of the events past the cursor that the
// filters let through. A page walks at most the page size of poll-lane
// rows, so a page can be empty while the walk has more to serve, and it
// says so with next.
func (c *call) events() answer {
	q := c.req.URL.Query()
	f, err := parseFilters(q.Get)
	if err != nil {
		return c.jsonAnswer(http.StatusBadRequest, map[string]string{"error": err.Error(), "reason": "invalid_filter"})
	}
	poll := make([]Event, 0, len(c.s.events))
	for _, fe := range c.s.events {
		if fe.lanes&Poll != 0 {
			poll = append(poll, fe.ev)
		}
	}
	head := int64(0)
	if len(poll) > 0 {
		head = poll[len(poll)-1].ID
	}

	since, pos := q.Get("since"), q.Get("position")
	var cursor int64
	switch {
	case since != "" && pos != "":
		return c.jsonAnswer(http.StatusBadRequest, map[string]string{"error": "Pass a since or a position, not both"})
	case pos != "":
		n, err := strconv.ParseInt(strings.TrimPrefix(pos, "fake-"), 10, 64)
		if !strings.HasPrefix(pos, "fake-") || err != nil || n < 0 {
			return c.jsonAnswer(http.StatusBadRequest, map[string]string{"error": "Unrecognized position", "reason": "invalid_position"})
		}
		cursor = n
	case since == "" || since == "now":
		// Neither is the bare present entry, which Basecamp reads as
		// since=now (the SDK's PollEventsOptions: "leave both empty to
		// enter at the present"). eventfeed never sends one, but `basecamp
		// events poll` does on purpose, so it is on the contract and not
		// reported.
		return c.jsonAnswer(http.StatusOK, map[string]any{"events": []pollRow{}, "position": position(head)})
	default:
		n, err := strconv.ParseInt(since, 10, 64)
		if err != nil || n < 0 {
			return c.jsonAnswer(http.StatusBadRequest, map[string]string{"error": "since is neither now nor an event id"})
		}
		cursor = n
	}

	start, _ := slices.BinarySearchFunc(poll, cursor+1, func(ev Event, id int64) int { return cmp.Compare(ev.ID, id) })
	end := min(start+c.s.opts.pageSize, len(poll))
	rows := []pollRow{}
	at := cursor
	for _, ev := range poll[start:end] {
		at = ev.ID
		if c.s.visibleLocked(ev, f, c.caller) {
			rows = append(rows, rowOf(ev))
		}
	}
	page := map[string]any{"events": rows, "position": position(at)}
	if end < len(poll) {
		next := url.Values{"position": {position(at)}}
		for key, values := range f.present {
			next[key] = values
		}
		page["next"] = c.accountURL("events.json") + "?" + next.Encode()
	}
	return c.jsonAnswer(http.StatusOK, page)
}
