//go:build unix

package setup

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

func TestApplyKeepsWhatIsNotPassed(t *testing.T) {
	f := validFile(t)
	f.Trust = admission.Trust{Mode: admission.TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{7}}

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

func TestApplyServedProjects(t *testing.T) {
	base := validFile(t)

	t.Run("serving a project adds it with no settings", func(t *testing.T) {
		out, err := Apply(base, Changes{Serve: []int64{777}})
		require.NoError(t, err)
		require.Contains(t, out.Projects, int64(777))
		assert.Equal(t, admission.Project{}, out.Projects[777])
	})
	t.Run("serving a project again keeps class and watch_completions", func(t *testing.T) {
		out, err := Apply(base, Changes{Serve: []int64{projectID}})
		require.NoError(t, err)
		assert.Equal(t, "internal", out.Projects[projectID].Class)
		assert.True(t, out.Projects[projectID].WatchCompletions)
	})
	t.Run("a project id that is not one is refused", func(t *testing.T) {
		_, err := Apply(base, Changes{Serve: []int64{0}})
		assert.Error(t, err)
		_, err = Apply(base, Changes{Serve: []int64{-1}})
		assert.Error(t, err)
	})
	t.Run("class and watch_completions need the project served", func(t *testing.T) {
		_, err := Apply(base, Changes{Classes: map[int64]string{777: "internal"}})
		assert.Error(t, err)
		_, err = Apply(base, Changes{WatchCompletions: map[int64]bool{777: true}})
		assert.Error(t, err)
	})
	t.Run("class and watch_completions apply to a project served in the same run", func(t *testing.T) {
		out, err := Apply(base, Changes{
			Serve:            []int64{777},
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

		again, err := Apply(out, Changes{Remove: []int64{projectID}})
		require.NoError(t, err, "unserving a project that is already gone is idempotent")
		assert.Equal(t, out.Projects, again.Projects)
		_, err = Apply(base, Changes{Remove: []int64{projectID}, Serve: []int64{projectID}})
		assert.Error(t, err, "serving and removing one project in one run")
	})
}

func TestApplyDispatchSettings(t *testing.T) {
	out, err := Apply(validFile(t), Changes{Driver: DriverACP, Concurrency: 4, Deadline: 90 * time.Minute})
	require.NoError(t, err)
	assert.Equal(t, DriverACP, out.Driver)
	assert.Equal(t, 4, out.Concurrency)
	assert.Equal(t, Duration(90*time.Minute), out.Deadline)
}
