package admission

import (
	"context"
	"errors"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// SummaryReader resolves a recording pointer into the SDK's compact
// projection (plan step 10). *basecamp.RecordingsService satisfies it.
type SummaryReader interface {
	Summarize(ctx context.Context, ref basecamp.RecordingRef) (*basecamp.RecordingSummary, error)
}

// SubscriptionReader answers whether the agent — the identity the reads run
// as — is subscribed to a recording.
type SubscriptionReader interface {
	Subscribed(ctx context.Context, recordingID int64) (bool, error)
}

// AssignmentReader finds one assignment event among a recording's events and
// returns the Person ids it added. found is false when the event was not
// within the read's bound.
type AssignmentReader interface {
	AddedPersonIDs(ctx context.Context, recordingID, eventID int64) (added []int64, found bool, err error)
}

// MemberReader answers whether a person is a non-client member of a project.
// Only project trust mode consults it.
//
// A "no" discards the event, so it must come from a listing read at or after
// asOf — the moment the event was seen. When the reader holds only an older
// listing and may not read a fresh one yet, it returns ErrMembershipUnverified
// and the record is held rather than refused.
type MemberReader interface {
	NonClientMember(ctx context.Context, bucketID, personID int64, asOf time.Time) (bool, error)
}

// ErrMembershipUnverified reports a refusal the reader could not verify
// against a listing as recent as the event.
var ErrMembershipUnverified = errors.New("admission: membership not verified against a listing as recent as the event")

// Reads bundles the reads admission may make. Members may be nil unless the
// policy's trust mode is project.
type Reads struct {
	Summaries     SummaryReader
	Subscriptions SubscriptionReader
	Assignments   AssignmentReader
	Members       MemberReader
}
