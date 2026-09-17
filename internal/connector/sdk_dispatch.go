package connector

import (
	"context"
	"fmt"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

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
	var out []AgentReply
	keep := func(id int64, creator *basecamp.Person, created time.Time) {
		if creator != nil && creator.ID == r.AgentID && created.After(since) {
			out = append(out, AgentReply{ID: id, CreatedAt: created})
		}
	}
	switch admission.ReplyKind(kind) {
	case admission.ReplyComment:
		result, err := r.Client.Comments().List(ctx, recordingID, &basecamp.CommentListOptions{Limit: -1})
		if err != nil {
			return nil, err
		}
		for _, c := range result.Comments {
			keep(c.ID, c.Creator, c.CreatedAt)
		}
	case admission.ReplyChatLine:
		result, err := r.Client.Campfires().ListLines(ctx, recordingID, &basecamp.CampfireLineListOptions{Limit: -1})
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
