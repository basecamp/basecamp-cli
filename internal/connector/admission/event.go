package admission

import (
	"encoding/json"
	"time"
)

// ActorTypeAgent is the feed's actor type for an Agent person. Only the push
// lane carries actor_type; a poll row leaves it empty.
const ActorTypeAgent = "agent"

// Event is the pointer admission decides on: the fields intake stores for a
// seen record (internal/connector Record in basecamp-cli PR 729), and nothing
// else. It carries no title, no content, no URL and no names; whatever
// admission needs beyond ids it reads.
type Event struct {
	// ID is the feed-global event id.
	ID int64
	// EventType is the cataloged type, "comment.created" and so on.
	EventType string
	// BucketID is the project the recording lives in.
	BucketID int64
	// RecordingID is the recording the event references.
	RecordingID int64
	// CreatorID is the person the event is attributed to.
	CreatorID int64
	// PerformedByID is the agent that carried out a delegated action on the
	// creator's behalf; nil for a direct action.
	PerformedByID *int64
	// ActorType is "person" or "agent" on the push lane, empty on the poll lane.
	ActorType string
	// Details is the type-specific detail object, verbatim; nil for the v1
	// trigger types, which publish none.
	Details json.RawMessage

	// Revision is the ledger record's revision when it was loaded. It is not
	// part of the feed's pointer: the ledger's Commit compares it, so a
	// decision made on an older load never overwrites a newer one.
	Revision int64
	// SeenAt is when intake wrote the pointer, on this machine's clock — the
	// clock membership listings are stamped with, which is why intake and
	// admission must run on one machine. A backward step of that clock by Δ
	// lets a listing up to Δ older than the event pass as newer. A membership refusal counts as
	// verified only from a project listing asked for after it. The Records
	// adapter must set it; zero falls back to the time admission started
	// deciding, which a busy project can keep from ever verifying a refusal.
	SeenAt time.Time
}

// Performer is the effective performer: the delegating agent when the action
// was delegated, else the creator. It is what the feed's performer filters
// match and what the trust set is checked against.
func (e Event) Performer() int64 {
	if e.PerformedByID != nil {
		return *e.PerformedByID
	}
	return e.CreatorID
}
