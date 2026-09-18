package setup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

func TestNoRepositoryAtProvesOnlyWhatItCanRead(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	repo := filepath.Join(dir, "repo", "app")
	require.NoError(t, os.MkdirAll(plain, 0o700))
	require.NoError(t, os.MkdirAll(repo, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "repo", ".git"), 0o700))

	assert.True(t, NoRepositoryAt(plain))
	assert.False(t, NoRepositoryAt(repo), "a .git above the directory is a repository")
	assert.False(t, NoRepositoryAt(filepath.Join(dir, "not-there")), "a directory that is not there proves nothing")

	if os.Geteuid() != 0 {
		locked := filepath.Join(dir, "locked")
		require.NoError(t, os.MkdirAll(filepath.Join(locked, "route"), 0o700))
		require.NoError(t, os.Chmod(locked, 0o000))
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
		assert.False(t, NoRepositoryAt(filepath.Join(locked, "route")), "a directory that cannot be read proves nothing")
	}
}

// With worktrees on, a route that is not in a repository is a route no task
// ever runs in, and setup says so before it writes connect.json.
func TestRouteChecksRefuseARouteWithNoRepositoryWhenWorktreesAreOn(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	repo := filepath.Join(dir, "repo")
	require.NoError(t, os.MkdirAll(plain, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o700))

	for _, c := range []struct {
		name      string
		path      string
		worktrees bool
		status    string
	}{
		{"worktrees on, no repository", plain, true, StatusFail},
		{"worktrees on, a repository", repo, true, StatusPass},
		{"worktrees off, no repository", plain, false, StatusPass},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := File{Profile: "agent", Worktrees: c.worktrees, Projects: map[int64]admission.Route{1: {Path: c.path}}}
			checks := RouteChecks(context.Background(), &fakeReader{}, f)
			require.Len(t, checks, 1)
			assert.Equal(t, c.status, checks[0].Status, checks[0].Message)
			if c.status == StatusFail {
				assert.Contains(t, checks[0].Message, "not in a git repository")
				assert.Contains(t, checks[0].Hint, "--worktrees=false")
			}
		})
	}
}
