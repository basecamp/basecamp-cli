//go:build unix

package commands

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/setup"
)

func TestGuidedProjectSelectionWarnsBeforeTheAllProjectsQuestion(t *testing.T) {
	var out bytes.Buffer
	asked := false
	p := &scriptedPrompter{confirms: []bool{true}, onConfirm: func(question string) {
		asked = true
		assert.Contains(t, question, "Work in all 2")
		// This fails if the warning is only in --help or after confirmation.
		assert.Contains(t, out.String(), "a request both admit can be answered twice")
	}}
	guided(t, p)
	chosen, err := chooseGuidedProjects(&out, guidedAgent{Profile: "agent-b", Me: setup.Person{Name: "Synthetic Agent"}, Listed: true,
		Projects: []guidedProject{{ID: setupProject, Name: "A"}, {ID: setupProject2, Name: "B"}}})
	require.NoError(t, err)
	assert.True(t, asked)
	assert.Len(t, chosen, 2)
}
