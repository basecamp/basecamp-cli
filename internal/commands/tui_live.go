package commands

import (
	"context"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/cable"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
)

// liveReadings is the workspace's stream of "your notifications changed",
// opened the same way `basecamp watch` opens it.
//
// The workspace calls this off the drawing loop and retries it on a backoff of
// its own, so both the endpoint lookup and the dial can take their time here,
// and a failure costs a stale sidebar rather than a broken screen. An account
// nobody has settled on yet has nothing to watch — the picker is still up — and
// answering with no stream is what the workspace expects for that.
func liveReadings(app *appctx.App) workspace.ReadingsWatcher {
	return func(ctx context.Context) (<-chan struct{}, error) {
		if err := app.RequireAccount(); err != nil {
			return nil, err
		}

		cableURL, options, err := cableEndpoint(ctx, app, "")
		if err != nil {
			return nil, err
		}

		return cable.WatchReadings(ctx, cableURL, app.Auth, options...)
	}
}
