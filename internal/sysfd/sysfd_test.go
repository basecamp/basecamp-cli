package sysfd_test

import (
	"math"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/sysfd"
)

// What a process can be told on a command line, and what it cannot.
func TestParse(t *testing.T) {
	for value, want := range map[string]int{
		"0":                         0,
		"1":                         1,
		"3":                         3,
		" 3 ":                       3,
		"+3":                        3,
		strconv.Itoa(math.MaxInt32): math.MaxInt32,
	} {
		t.Run("takes "+value, func(t *testing.T) {
			fd, err := sysfd.Parse(value)
			require.NoError(t, err)
			assert.Equal(t, want, fd.Int(), "and reads the number it was given")
		})
	}
	for name, value := range map[string]string{
		"nothing":           "",
		"whitespace":        "   ",
		"a word":            "three",
		"a number and more": "3x",
		"a negative":        "-1",
		"hexadecimal":       "0x3",
		"a float":           "3.0",
		"past int32":        strconv.FormatInt(math.MaxInt32+1, 10),
		"far past int64":    "99999999999999999999",
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			_, err := sysfd.Parse(value)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "descriptor", "and says what it refused")
		})
	}
}

// The value is in the refusal: a caller passing on the error names the
// descriptor that was wrong, not just that one was.
func TestParseNamesTheValue(t *testing.T) {
	_, err := sysfd.Parse("three")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"three"`)

	_, err = sysfd.Parse("-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-1")
}

// A descriptor Go hands back as a uintptr comes through unchanged.
func TestOf(t *testing.T) {
	file, err := os.Open(os.DevNull)
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })

	fd, err := sysfd.Of(file.Fd())
	require.NoError(t, err)
	assert.Equal(t, int(file.Fd()), fd.Int())
	assert.Equal(t, file.Fd(), fd.Uintptr())
}

func TestOfRefusesWhatIntCannotHold(t *testing.T) {
	_, err := sysfd.Of(uintptr(math.MaxInt32) + 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "out of range")
}

// The three shapes a caller asks for are the same number.
func TestADescriptorIsOneNumber(t *testing.T) {
	fd, err := sysfd.Parse("7")
	require.NoError(t, err)

	assert.Equal(t, 7, fd.Int())
	assert.Equal(t, uintptr(7), fd.Uintptr())
	assert.Equal(t, "7", fd.String())
}
