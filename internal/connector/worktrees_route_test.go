package connector

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Telling a route that is misconfigured from one that could not be read this
// once. The first is refused and said out loud; the second is waited on, as
// every Prepare failure used to be.

func TestPrepareRefusesARouteThatIsNotARepository(t *testing.T) {
	h := newWorktreeHarness(t)
	outside := filepath.Join(filepath.Dir(h.repo), "no-repository-here")
	require.NoError(t, os.MkdirAll(outside, 0o700))

	_, err := h.wt.Prepare(context.Background(), outside, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRouteUnusable)
	assert.Empty(t, h.wt.RoutesWaiting(), "a directory that will not become a repository on its own is not waited on")
}

func TestPrepareRefusesARepositoryWithNoCommit(t *testing.T) {
	h := newWorktreeHarness(t)
	empty := filepath.Join(filepath.Dir(h.repo), "empty-repository")
	require.NoError(t, os.MkdirAll(empty, 0o700))
	h.git(empty, "init", "-q", "-b", "main")

	_, err := h.wt.Prepare(context.Background(), empty, 2)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRouteUnusable)
	assert.Empty(t, h.wt.RoutesWaiting())
}

// git that will not run is not a route that is not a repository. The proof is
// the disk, not git's exit status, which is 128 either way.
func TestPrepareWaitsWhenGitCannotRun(t *testing.T) {
	h := newWorktreeHarness(t)
	route := filepath.Join(h.repo, "app")
	wt := h.worktrees(filepath.Join(h.repo, "no-such-git"))

	_, err := wt.Prepare(context.Background(), route, 3)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrRouteUnusable)
	assert.Equal(t, []string{route}, wt.RoutesWaiting(), "a route whose git failed is tried again")
}

// A directory that is not there proves nothing about repositories: it may be
// a volume that is not mounted yet.
func TestPrepareWaitsOnARouteThatIsNotThere(t *testing.T) {
	h := newWorktreeHarness(t)
	gone := filepath.Join(filepath.Dir(h.repo), "not-mounted")

	_, err := h.wt.Prepare(context.Background(), gone, 4)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrRouteUnusable)
	assert.Equal(t, []string{gone}, h.wt.RoutesWaiting())
}

// Neither does a directory that could not be read. git says the same thing
// it says outside a repository, and this is the case the proof exists to
// keep out of the refusal.
func TestPrepareWaitsOnARouteThatCannotBeRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
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
