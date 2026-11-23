//go:build unix

package setup

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

func TestCampfireSettings(t *testing.T) {
	f := validFile(t)
	fire := Campfire{Project: projectID, ID: 8000}
	out, err := Apply(f, Changes{Campfires: map[Campfire]bool{fire: true}})
	require.NoError(t, err)
	assert.Empty(t, f.Projects[projectID].ListenCampfires)
	assert.Equal(t, []int64{fire.ID}, out.Projects[projectID].ListenCampfires)
	kept, err := Apply(out, Changes{Serve: []int64{projectID}})
	require.NoError(t, err)
	assert.Equal(t, out, kept)
	data, err := json.Marshal(out)
	require.NoError(t, err)
	loaded, err := Parse(data)
	require.NoError(t, err)
	assert.Equal(t, out, loaded)
	p, err := admission.ParsePolicy(data)
	require.NoError(t, err)
	assert.Equal(t, out.Projects, p.Projects)
	off, err := Apply(out, Changes{Campfires: map[Campfire]bool{fire: false}})
	require.NoError(t, err)
	assert.Empty(t, off.Projects[projectID].ListenCampfires)
	assert.Equal(t, []int64{fire.ID}, out.Projects[projectID].ListenCampfires)
	for _, bad := range []Campfire{{Project: 999, ID: fire.ID}, {Project: projectID, ID: 0}, {Project: projectID, ID: -1}} {
		_, err = Apply(f, Changes{Campfires: map[Campfire]bool{bad: true}})
		require.Error(t, err)
	}
	r := f.Projects[projectID]
	r.ListenCampfires = []int64{-1}
	f.Projects[projectID] = r
	require.Error(t, f.Validate())
}
