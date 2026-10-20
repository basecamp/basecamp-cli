//go:build unix

package setup

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A connect.json that never set the acknowledgement boosts the default: an
// existing setup gets it on upgrade.
func TestAFileWithNoAckBoostAcknowledgesWithTheDefault(t *testing.T) {
	assert.Equal(t, AckBoost{Content: "👀"}, validFile(t).Ack())
}

// The setting survives a save and a load, and the default is written as no
// key at all, which a CLI from before the key still opens.
func TestTheAckBoostRoundTripsAndTheDefaultIsNotWritten(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change AckBoost
		key    bool
	}{
		{"custom", AckBoost{Content: "On it"}, true},
		{"off", AckBoost{Off: true}, true},
		{"default", AckBoost{Content: DefaultAckContent}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, err := Path(configDir(t), "agent")
			require.NoError(t, err)
			f, err := Apply(validFile(t), Changes{AckBoost: &tc.change})
			require.NoError(t, err)
			require.NoError(t, save(path, f))

			data, err := os.ReadFile(path)
			require.NoError(t, err)
			var raw map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(data, &raw))
			assert.Equal(t, tc.key, raw["ack_boost"] != nil)

			loaded, err := Load(path)
			require.NoError(t, err)
			assert.Equal(t, tc.change, loaded.Ack())
		})
	}
}

// Setting the default again takes a custom setting's key back out.
func TestSettingTheDefaultAgainRemovesTheKey(t *testing.T) {
	f, err := Apply(validFile(t), Changes{AckBoost: &AckBoost{Off: true}})
	require.NoError(t, err)
	f, err = Apply(f, Changes{AckBoost: &AckBoost{Content: DefaultAckContent}})
	require.NoError(t, err)
	assert.Nil(t, f.AckBoost)
}

// Basecamp takes a boost of at most 16 characters, counted as runes, and an
// empty one is no acknowledgement; neither is set, and neither is read.
func TestAnAckBoostBasecampWouldRefuseIsRefused(t *testing.T) {
	sixteen := strings.Repeat("👀", 16)
	_, err := Apply(validFile(t), Changes{AckBoost: &AckBoost{Content: sixteen}})
	require.NoError(t, err, "16 runes is 64 bytes, and still fits")

	for name, ack := range map[string]AckBoost{
		"seventeen runes":  {Content: sixteen + "!"},
		"empty":            {Content: ""},
		"blank":            {Content: "  "},
		"off with content": {Off: true, Content: "👀"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Apply(validFile(t), Changes{AckBoost: &ack})
			require.Error(t, err)

			f := validFile(t)
			f.AckBoost = &ack
			data, err := json.Marshal(f)
			require.NoError(t, err)
			_, err = Parse(data)
			require.Error(t, err, "a hand-edited file is refused too")
		})
	}
}
