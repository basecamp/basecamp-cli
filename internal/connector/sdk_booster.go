package connector

import (
	"context"
	"errors"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// SDKBooster boosts a recording as the agent the client authenticates as.
// The SDK sends a boost once and never retries it, since a retried POST can
// post twice.
type SDKBooster struct {
	Client *basecamp.AccountClient
}

var _ Booster = SDKBooster{}

// Boost implements Booster.
func (b SDKBooster) Boost(ctx context.Context, recordingID int64, content string) (int64, error) {
	boost, err := b.Client.Boosts().CreateRecording(ctx, recordingID, content)
	if err != nil {
		return 0, err
	}
	if boost.ID <= 0 {
		return 0, errors.New("basecamp answered the boost without its id")
	}
	return boost.ID, nil
}
