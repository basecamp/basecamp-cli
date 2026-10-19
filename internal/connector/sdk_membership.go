package connector

import (
	"context"
	"errors"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// SDKMembership lists the buckets the agent can see, for intake's reconnect:
// its projects and its Pings. A Ping is a bucket of its own, and the live
// subscription holds it like a project, so leaving Pings out read every line
// in one as from a bucket the subscription did not hold, and reconnected the
// feed for it again each membership interval.
type SDKMembership struct {
	Client *basecamp.AccountClient
}

var _ MembershipSource = SDKMembership{}

// Buckets implements MembershipSource. Basecamp answers 403 for the Pings of
// a caller whose company has turned Pings off, which is a listing of none.
func (m SDKMembership) Buckets(ctx context.Context) ([]int64, error) {
	result, err := m.Client.Projects().List(ctx, nil)
	if err != nil {
		return nil, err
	}
	pings, err := m.Client.Pings().List(ctx)
	if apiErr, ok := errors.AsType[*basecamp.Error](err); ok && apiErr.Code == basecamp.CodeForbidden {
		pings, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(result.Projects)+len(pings))
	for _, p := range result.Projects {
		ids = append(ids, p.ID)
	}
	for _, p := range pings {
		ids = append(ids, p.ID)
	}
	return ids, nil
}
