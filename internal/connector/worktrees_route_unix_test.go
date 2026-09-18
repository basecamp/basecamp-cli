//go:build unix

package connector

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A directory that could not be read proves nothing either. git says the
// same thing it says outside a repository — exit 128 — and this is the case
// the proof exists to keep out of the refusal.
//
// Unix only, and by a build tag rather than a check inside the test: a mode
// of 0o000 is what makes a directory unsearchable, and it is a Unix mode.
// Windows does not enforce it, so the fixture would not build the state the
// test is about. NoRepositoryAt itself runs everywhere and needs no tag: an
// error that is not "does not exist" is no proof whatever produced it.
func TestPrepareWaitsOnARouteThatCannotBeRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory, whatever its mode")
	}
	h := newWorktreeHarness(t)
	locked := filepath.Join(filepath.Dir(h.repo), "locked")
	route := filepath.Join(locked, "route")
	require.NoError(t, os.MkdirAll(route, 0o700))
	require.NoError(t, os.Chmod(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	_, err := h.wt.Prepare(context.Background(), route, 5)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrRouteUnusable)
	assert.Equal(t, []string{route}, h.wt.RoutesWaiting())
}
