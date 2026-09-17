package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnectProjectFlagRepeatsAndRefusesNonIDs(t *testing.T) {
	cmd := NewConnectCmd()
	require.NoError(t, cmd.Flags().Parse([]string{"--project", "12", "--project", "34,12"}))
	flag := cmd.Flags().Lookup("project")
	assert.Equal(t, "string", flag.Value.Type(), "the global flag's type is kept")
	ids, err := parseProjectIDs(*flag.Value.(*repeatedString))
	require.NoError(t, err)
	assert.Equal(t, []int64{12, 34}, ids)

	_, err = parseProjectIDs([]string{"abc"})
	assert.Error(t, err)
	_, err = parseProjectIDs([]string{""})
	assert.Error(t, err)
}

func TestConnectStateLivesUnderXDGStateHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	home, err := connectStateHome()
	require.NoError(t, err)
	assert.Equal(t, dir, home)
	got, err := ensurePrivateChain(home, "basecamp", "connect", "2914079-1")
	require.NoError(t, err)
	assert.DirExists(t, got)
}
