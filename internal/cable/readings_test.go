package cable

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Basecamp beats no ping of its own, so the client's own ping is the only frame
// that arrives on an idle connection. actioncable-go calls a connection stale
// after six seconds without one and redials a live socket, which is what a
// watch looks like when it reconnects every six seconds forever.
func TestThePingOutpacesTheStaleWindow(t *testing.T) {
	const staleAfter = 6 * time.Second // actioncable-go's default

	assert.LessOrEqual(t, pingInterval, staleAfter/2)
}

// An appearance lapses after 30 seconds and takes the broadcasts with it, since
// Basecamp only broadcasts to a user it considers online.
func TestTheAppearanceOutpacesItsTTL(t *testing.T) {
	const connectionTTL = 30 * time.Second // User::Appearant::Appearances::CONNECTION_TTL

	assert.LessOrEqual(t, appearInterval, connectionTTL/2)
}
