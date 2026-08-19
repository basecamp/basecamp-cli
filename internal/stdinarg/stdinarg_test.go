package stdinarg

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAllowExactArg(t *testing.T) {
	allow := ParseAllow("arg:0")
	assert.True(t, allow.Arg(0))
	assert.False(t, allow.Arg(1))
	assert.False(t, allow.Flag("data"))
	assert.False(t, allow.Empty())
}

func TestParseAllowOpenEndedArg(t *testing.T) {
	allow := ParseAllow("arg:1+")
	assert.False(t, allow.Arg(0))
	assert.True(t, allow.Arg(1))
	assert.True(t, allow.Arg(5))
}

func TestParseAllowFlags(t *testing.T) {
	allow := ParseAllow("flag:data flag:out")
	assert.True(t, allow.Flag("data"))
	assert.True(t, allow.Flag("out"))
	assert.False(t, allow.Flag("body"))
	assert.False(t, allow.Arg(0))
}

func TestParseAllowMixed(t *testing.T) {
	allow := ParseAllow("arg:0 arg:2+ flag:description")
	assert.True(t, allow.Arg(0))
	assert.False(t, allow.Arg(1))
	assert.True(t, allow.Arg(2))
	assert.True(t, allow.Arg(3))
	assert.True(t, allow.Flag("description"))
}

func TestParseAllowEmptyAndGarbage(t *testing.T) {
	assert.True(t, ParseAllow("").Empty())
	assert.True(t, ParseAllow("arg:x bogus flag").Empty())
}

func TestIsPipedNonFileReader(t *testing.T) {
	assert.True(t, IsPiped(strings.NewReader("piped")))
}

func TestIsPipedCharDevice(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	require.NoError(t, err)
	defer devNull.Close()

	assert.False(t, IsPiped(devNull))
}

func TestIsPipedRegularFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	require.NoError(t, err)
	defer f.Close()

	assert.True(t, IsPiped(f))
}
