//go:build !dev

package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStubTUICmd_ReturnsError(t *testing.T) {
	cmd := NewTUICmd()
	require.NotNil(t, cmd)
	assert.Equal(t, "tui [url]", cmd.Use)

	// --trace must be accepted (matches dev surface)
	cmd.SetArgs([]string{"--trace"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only available in development builds")
}

func TestStubTUICmd_AcceptsURLArg(t *testing.T) {
	cmd := NewTUICmd()
	cmd.SetArgs([]string{"https://3.basecamp.com/99/buckets/42"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only available in development builds")
}
