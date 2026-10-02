//go:build dev

package commands

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnectHandoffGraceParsesAGoDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"":      0,
		"2s":    2 * time.Second,
		"750ms": 750 * time.Millisecond,
		"1m30s": 90 * time.Second,
	} {
		got, err := parseConnectHandoffGrace(in)
		require.NoError(t, err, "%q", in)
		assert.Equal(t, want, got, "%q", in)
	}
}

func TestConnectHandoffGraceRefusesWhatIsNotAPositiveDuration(t *testing.T) {
	for _, in := range []string{"2", "soon", "0s", "-1s"} {
		_, err := parseConnectHandoffGrace(in)
		require.Error(t, err, "%q", in)
		assert.Contains(t, err.Error(), connectHandoffGraceEnv, "%q", in)
	}
}

func TestConnectHandoffGraceReadsTheEnvironment(t *testing.T) {
	t.Setenv(connectHandoffGraceEnv, "3s")
	got, err := connectHandoffGrace()
	require.NoError(t, err)
	assert.Equal(t, 3*time.Second, got)
}
