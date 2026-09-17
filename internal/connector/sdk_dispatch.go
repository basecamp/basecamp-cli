package connector

import (
	"context"
	"fmt"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

// AdoptionScanLimit bounds a reply listing: the adopted-reply rule needs the
// replies after an acknowledgement, not a conversation's whole history, and a
// settlement must not page a busy Campfire from its beginning.
const AdoptionScanLimit = 500

// AdoptionScanTimeout bounds the listing in time as well, since settlement
// runs on a context a shutdown does not cancel.
const AdoptionScanTimeout = 30 * time.Second

// SDKReplies lists the agent's replies at a destination through the SDK, for
// the adopted-reply rule.
type SDKReplies struct {
	Client  *basecamp.AccountClient
	AgentID int64
}

var _ ReplyLister = SDKReplies{}

// AgentReplies implements ReplyLister. The listing is exhaustive: the rule
// adopts only when exactly one reply matches, and a page left unread could
// hold the second.
func (r SDKReplies) AgentReplies(ctx context.Context, _ int64, kind string, recordingID int64, since time.Time) ([]AgentReply, error) {
	ctx, cancel := context.WithTimeout(ctx, AdoptionScanTimeout)
	defer cancel()
	var out []AgentReply
	keep := func(id int64, creator *basecamp.Person, created time.Time) {
		if creator != nil && creator.ID == r.AgentID && created.After(since) {
			out = append(out, AgentReply{ID: id, CreatedAt: created})
		}
	}
	switch admission.ReplyKind(kind) {
	case admission.ReplyComment:
		result, err := r.Client.Comments().List(ctx, recordingID, &basecamp.CommentListOptions{Limit: AdoptionScanLimit})
		if err != nil {
			return nil, err
		}
		for _, c := range result.Comments {
			keep(c.ID, c.Creator, c.CreatedAt)
		}
	case admission.ReplyChatLine:
		// Newest first: the replies the rule cares about are the ones after
		// the acknowledgement, not the beginning of the room.
		result, err := r.Client.Campfires().ListLines(ctx, recordingID, &basecamp.CampfireLineListOptions{
			Limit: AdoptionScanLimit, Sort: "created_at", Direction: "desc",
		})
		if err != nil {
			return nil, err
		}
		for _, l := range result.Lines {
			keep(l.ID, l.Creator, l.CreatedAt)
		}
	default:
		return nil, fmt.Errorf("connector: no reply listing for %q", kind)
	}
	return out, nil
}

// SDKMembership lists the buckets the agent can see, for intake's reconnect.
type SDKMembership struct {
	Client *basecamp.AccountClient
}

var _ MembershipSource = SDKMembership{}

// Buckets implements MembershipSource.
func (m SDKMembership) Buckets(ctx context.Context) ([]int64, error) {
	result, err := m.Client.Projects().List(ctx, nil)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(result.Projects))
	for _, p := range result.Projects {
		ids = append(ids, p.ID)
	}
	return ids, nil
}
