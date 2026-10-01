package connector

import (
	"context"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

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
