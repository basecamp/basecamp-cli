package setup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

func TestApplyKeepsWhatIsNotPassed(t *testing.T) {
	f := validFile(t)
	f.Trust = admission.Trust{Mode: admission.TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{7}}
	f.Worktrees = true

	out, err := Apply(f, Changes{})
	require.NoError(t, err)
	assert.Equal(t, f, out)
}

func TestApplyDoesNotModifyItsInput(t *testing.T) {
	f := validFile(t)
	before := len(f.Projects)
	_, err := Apply(f, Changes{Remove: []int64{projectID}, Allow: []int64{9}})
	require.NoError(t, err)
	assert.Len(t, f.Projects, before)
	assert.Empty(t, f.Trust.AllowlistIDs)
}

func TestApplyTrust(t *testing.T) {
	base := validFile(t)

	t.Run("allow implies allowlist, sorted and deduplicated", func(t *testing.T) {
		out, err := Apply(base, Changes{Allow: []int64{9, 3, 9}})
		require.NoError(t, err)
		assert.Equal(t, admission.TrustAllowlist, out.Trust.Mode)
		assert.Equal(t, []int64{3, 9}, out.Trust.AllowlistIDs)
	})
	t.Run("allow contradicting the mode is refused", func(t *testing.T) {
		_, err := Apply(base, Changes{Trust: admission.TrustProject, Allow: []int64{9}})
		assert.Error(t, err)
	})
	t.Run("allowlist with nobody on it is refused", func(t *testing.T) {
		_, err := Apply(base, Changes{Trust: admission.TrustAllowlist})
		assert.Error(t, err)
	})
	t.Run("leaving allowlist drops the list", func(t *testing.T) {
		f := base
		f.Trust = admission.Trust{Mode: admission.TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{7}}
		out, err := Apply(f, Changes{Trust: admission.TrustProject})
		require.NoError(t, err)
		assert.Equal(t, admission.TrustProject, out.Trust.Mode)
		assert.Empty(t, out.Trust.AllowlistIDs)
		_, err = out.Policy(agentID)
		assert.NoError(t, err, "admission accepts the result")
	})
	t.Run("the operator is untouched", func(t *testing.T) {
		out, err := Apply(base, Changes{Trust: admission.TrustProject})
		require.NoError(t, err)
		assert.Equal(t, operatorID, out.Trust.OperatorID)
	})
}

func TestApplyRoutes(t *testing.T) {
	base := validFile(t)

	t.Run("a route resolves to the real directory", func(t *testing.T) {
		target := t.TempDir()
		link := filepath.Join(t.TempDir(), "app")
		require.NoError(t, os.Symlink(target, link))

		out, err := Apply(base, Changes{Routes: map[int64]string{777: link}})
		require.NoError(t, err)
		want, err := filepath.EvalSymlinks(target)
		require.NoError(t, err)
		assert.Equal(t, want, out.Projects[777].Path, "the approved entry names where work really runs")
	})
	t.Run("rerouting keeps class and watch_completions", func(t *testing.T) {
		dir := t.TempDir()
		out, err := Apply(base, Changes{Routes: map[int64]string{projectID: dir}})
		require.NoError(t, err)
		assert.Equal(t, "internal", out.Projects[projectID].Class)
		assert.True(t, out.Projects[projectID].WatchCompletions)
	})
	t.Run("a directory that does not exist is refused", func(t *testing.T) {
		_, err := Apply(base, Changes{Routes: map[int64]string{777: filepath.Join(t.TempDir(), "missing")}})
		assert.Error(t, err)
	})
	t.Run("a file is not a directory", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "f")
		require.NoError(t, os.WriteFile(file, nil, 0o600))
		_, err := Apply(base, Changes{Routes: map[int64]string{777: file}})
		assert.Error(t, err)
	})
	t.Run("class and watch_completions need a route", func(t *testing.T) {
		_, err := Apply(base, Changes{Classes: map[int64]string{777: "internal"}})
		assert.Error(t, err)
		_, err = Apply(base, Changes{WatchCompletions: map[int64]bool{777: true}})
		assert.Error(t, err)
	})
	t.Run("class and watch_completions apply with a new route", func(t *testing.T) {
		out, err := Apply(base, Changes{
			Routes:           map[int64]string{777: t.TempDir()},
			Classes:          map[int64]string{777: "client-work"},
			WatchCompletions: map[int64]bool{777: true, projectID: false},
		})
		require.NoError(t, err)
		assert.Equal(t, "client-work", out.Projects[777].Class)
		assert.True(t, out.Projects[777].WatchCompletions)
		assert.False(t, out.Projects[projectID].WatchCompletions)
	})
	t.Run("an invalid class is refused", func(t *testing.T) {
		_, err := Apply(base, Changes{Classes: map[int64]string{projectID: "Has Spaces"}})
		assert.Error(t, err)
	})
	t.Run("remove", func(t *testing.T) {
		out, err := Apply(base, Changes{Remove: []int64{projectID}})
		require.NoError(t, err)
		assert.NotContains(t, out.Projects, projectID)

		_, err = Apply(base, Changes{Remove: []int64{777}})
		assert.Error(t, err, "removing a route that is not there")
		_, err = Apply(base, Changes{Remove: []int64{projectID}, Routes: map[int64]string{projectID: t.TempDir()}})
		assert.Error(t, err, "routing and removing one project in one run")
	})
}

func TestApplyDispatchSettings(t *testing.T) {
	on := true
	out, err := Apply(validFile(t), Changes{Driver: DriverACP, Concurrency: 4, Deadline: 90 * time.Minute, Worktrees: &on})
	require.NoError(t, err)
	assert.Equal(t, DriverACP, out.Driver)
	assert.Equal(t, 4, out.Concurrency)
	assert.Equal(t, Duration(90*time.Minute), out.Deadline)
	assert.True(t, out.Worktrees)
}

func TestResolveDirExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.Mkdir(filepath.Join(home, "work"), 0o700))

	got, err := ResolveDir("~/work")
	require.NoError(t, err)
	want, err := filepath.EvalSymlinks(filepath.Join(home, "work"))
	require.NoError(t, err)
	assert.Equal(t, want, got)
}
