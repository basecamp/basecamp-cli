package admission

import (
	"context"
	"encoding/json"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// eventBoostCreated is the feed's one type for every boost, whatever it
// landed on.
const eventBoostCreated = "boost.created"

// boostDetails is the part of boost.created's details admission reads. The
// feed publishes boosted_event_id as null for a boost on the recording itself
// and as the event's id for a boost on one of the recording's events.
type boostDetails struct {
	BoostID        int64  `json:"boost_id"`
	BoostedEventID *int64 `json:"boosted_event_id"`
}

// opens reports whether the gate left trigger open.
func opens(gate GateResult, trigger Trigger) bool {
	for _, rule := range gate.Rules {
		if rule.Trigger == trigger {
			return true
		}
	}
	return false
}

// boostOnRecording reads the boost's id from the pointer's details, and
// settles from them alone what they can.
//
// A boost on an event is discarded as not_addressed. Basecamp addresses it to
// whoever performed the event (a completion, a move), not to the recording's
// author, and the boosted rule admits a boost on something the agent wrote:
// the agent's own comment, line or post. Whether the agent performed a given
// event is not something this rule reads.
//
// Details without a boost id are not a boost.created Basecamp writes, and are
// discarded as an invalid pointer: there is no boost to read.
func boostOnRecording(ev Event) (int64, Reason) {
	var d boostDetails
	if len(ev.Details) == 0 || json.Unmarshal(ev.Details, &d) != nil || d.BoostID <= 0 {
		return 0, ReasonInvalidPointer
	}
	if d.BoostedEventID != nil {
		return 0, ReasonNotAddressed
	}
	return d.BoostID, ""
}

// readBoost reads the boost the event names. It is the content an admitted
// boost hands off, and it names the boosted recording's type, which the
// summary read routes by.
//
// The boost must be on the pointer's recording, and its booster must be the
// performer the gate trusted: the boost is the words handed off, so whoever
// wrote them is held to the trust the performer was. A read that answered
// without either is a read that failed.
func (a *decision) readBoost(ctx context.Context, ev Event, boostID int64) (*basecamp.Boost, State, Reason, error) {
	var boost *basecamp.Boost
	err := a.retry(ctx, func() error {
		var err error
		boost, err = a.reads.Boosts.Get(ctx, boostID)
		return err
	})
	if err != nil {
		reason, ctxErr := failure(ctx, err)
		if ctxErr != nil {
			return nil, "", "", ctxErr
		}
		return nil, StateBlocked, reason, nil
	}
	switch {
	case boost == nil, boost.Booster == nil, boost.Booster.ID <= 0,
		boost.Recording == nil, boost.Recording.ID != ev.RecordingID, boost.Recording.Type == "":
		return nil, StateBlocked, ReasonReadFailed, nil
	case boost.Booster.ID != ev.Performer():
		return nil, StateDiscarded, ReasonUntrustedAuthor, nil
	}
	return boost, "", "", nil
}
