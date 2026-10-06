//go:build unix

package setup

import (
	"testing"

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
		_, err := Apply(base, Changes{Trust: admission.TrustOperator, Allow: []int64{9}})
		assert.Error(t, err)
	})
	// Named operators beside the project's members, who participate: one
	// run can say who operates the agent and open it to the project.
	t.Run("allow names operators alongside project trust", func(t *testing.T) {
		out, err := Apply(base, Changes{Trust: admission.TrustProject, Allow: []int64{9, 3}})
		require.NoError(t, err)
		assert.Equal(t, admission.TrustProject, out.Trust.Mode)
		assert.Equal(t, []int64{3, 9}, out.Trust.AllowlistIDs)
		_, err = out.Policy(agentID)
		assert.NoError(t, err, "admission accepts the result")

		// A later run's --allow replaces the operators it kept, never adds.
		again, err := Apply(out, Changes{Trust: admission.TrustProject, Allow: []int64{5}})
		require.NoError(t, err)
		assert.Equal(t, []int64{5}, again.Trust.AllowlistIDs)
	})
	t.Run("allowlist with nobody on it is refused", func(t *testing.T) {
		_, err := Apply(base, Changes{Trust: admission.TrustAllowlist})
		assert.Error(t, err)
	})
	// What a run does not pass is kept: the operators named before stay
	// operators under project trust, and only --trust operator, which has
	// no one to name, clears them.
	t.Run("moving to project trust keeps the named operators", func(t *testing.T) {
		f := base
		f.Trust = admission.Trust{Mode: admission.TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{7}}
		out, err := Apply(f, Changes{Trust: admission.TrustProject})
		require.NoError(t, err)
		assert.Equal(t, admission.TrustProject, out.Trust.Mode)
		assert.Equal(t, []int64{7}, out.Trust.AllowlistIDs)
		_, err = out.Policy(agentID)
		assert.NoError(t, err, "admission accepts the result")

		again, err := Apply(out, Changes{Trust: admission.TrustProject, Serve: []int64{777}})
		require.NoError(t, err)
		assert.Equal(t, []int64{7}, again.Trust.AllowlistIDs, "a re-run to serve a project keeps them")
	})
	t.Run("operator trust clears the named operators", func(t *testing.T) {
		f := base
		f.Trust = admission.Trust{Mode: admission.TrustProject, OperatorID: operatorID, AllowlistIDs: []int64{7}}
		out, err := Apply(f, Changes{Trust: admission.TrustOperator})
		require.NoError(t, err)
		assert.Empty(t, out.Trust.AllowlistIDs)
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

// The assignment opt-in rides with the allowlist it opts in: setting it needs
// someone named, and a run that empties the list takes it away too.
func TestApplyAssignmentOptIn(t *testing.T) {
	on, off := true, false
	base := validFile(t)

	t.Run("set with the people it opts in", func(t *testing.T) {
		out, err := Apply(base, Changes{Allow: []int64{9}, AllowAssignments: &on})
		require.NoError(t, err)
		assert.True(t, out.Trust.AllowAssignments)
		_, err = out.Policy(agentID)
		assert.NoError(t, err, "admission accepts the result")
	})
	t.Run("refused with nobody named", func(t *testing.T) {
		_, err := Apply(base, Changes{AllowAssignments: &on})
		assert.Error(t, err)
	})
	optedIn := base
	optedIn.Trust = admission.Trust{Mode: admission.TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{7}, AllowAssignments: true}
	t.Run("kept when neither it nor the list is passed", func(t *testing.T) {
		out, err := Apply(optedIn, Changes{Serve: []int64{777}})
		require.NoError(t, err)
		assert.True(t, out.Trust.AllowAssignments)
	})
	// The opt-in was given for the people named then. A run that names
	// others does not hand them assignments unless it says so again.
	t.Run("reset when the list is replaced without it", func(t *testing.T) {
		out, err := Apply(optedIn, Changes{Allow: []int64{8}})
		require.NoError(t, err)
		assert.False(t, out.Trust.AllowAssignments)

		out, err = Apply(optedIn, Changes{Allow: []int64{8}, AllowAssignments: &on})
		require.NoError(t, err)
		assert.True(t, out.Trust.AllowAssignments, "restated with the new list")
	})
	t.Run("set over the list connect.json keeps", func(t *testing.T) {
		f := base
		f.Trust = admission.Trust{Mode: admission.TrustAllowlist, OperatorID: operatorID, AllowlistIDs: []int64{7}}
		out, err := Apply(f, Changes{AllowAssignments: &on})
		require.NoError(t, err)
		assert.True(t, out.Trust.AllowAssignments)
		assert.Equal(t, []int64{7}, out.Trust.AllowlistIDs)
	})
	t.Run("refused under project trust with nobody named", func(t *testing.T) {
		_, err := Apply(base, Changes{Trust: admission.TrustProject, AllowAssignments: &on})
		assert.Error(t, err, "the members are participants, and the opt-in never covers them")
	})
	t.Run("turned off", func(t *testing.T) {
		out, err := Apply(optedIn, Changes{AllowAssignments: &off})
		require.NoError(t, err)
		assert.False(t, out.Trust.AllowAssignments)
	})
	t.Run("dropped with the list", func(t *testing.T) {
		out, err := Apply(optedIn, Changes{Trust: admission.TrustOperator})
		require.NoError(t, err)
		assert.False(t, out.Trust.AllowAssignments)
		_, err = out.Policy(agentID)
		assert.NoError(t, err)
	})
}
