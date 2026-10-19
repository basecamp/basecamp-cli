//go:build unix

package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
)

func loadAckBoost(t *testing.T) setup.AckBoost {
	t.Helper()
	f, err := setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	return f.Ack()
}

// --ack-boost sets the boost the connector acknowledges with, --no-ack-boost
// turns it off, and a run that passes neither keeps it.
func TestConnectSetupSetsTheAckBoost(t *testing.T) {
	s := startConnectSetupServer(t)
	firstSetup(t, s)
	assert.Equal(t, setup.AckBoost{Content: "👀"}, loadAckBoost(t), "on by default")

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--ack-boost", "On it")
	require.NoError(t, err, out)
	assert.Equal(t, setup.AckBoost{Content: "On it"}, loadAckBoost(t))

	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), serveArg())
	require.NoError(t, err, out)
	assert.Equal(t, setup.AckBoost{Content: "On it"}, loadAckBoost(t), "kept by a run that does not pass it")

	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--no-ack-boost")
	require.NoError(t, err, out)
	assert.Equal(t, setup.AckBoost{Off: true}, loadAckBoost(t))
}

// A boost Basecamp would refuse, and both flags at once, are refused before
// anything is written.
func TestConnectSetupRefusesABadAckBoost(t *testing.T) {
	for name, args := range map[string][]string{
		"empty":           {"--ack-boost", ""},
		"seventeen runes": {"--ack-boost", strings.Repeat("👀", 17)},
		"both flags":      {"--ack-boost", "👍", "--no-ack-boost"},
	} {
		t.Run(name, func(t *testing.T) {
			s := startConnectSetupServer(t)
			args = append([]string{"--operator", fmt.Sprint(setupOperatorPerson), serveArg()}, args...)
			out, err := runConnectSetupCmd(t, connectSetupApp(t, s, "agent"), args...)
			require.Error(t, err, out)
			assertNotWritten(t, "agent")
		})
	}
}

// Show says how requests are acknowledged, the default included, which the
// file itself leaves out.
func TestConnectShowSaysHowRequestsAreAcknowledged(t *testing.T) {
	s := startConnectSetupServer(t)
	firstSetup(t, s)

	show := func(t *testing.T, format output.Format) string {
		t.Helper()
		app := newConnectSetupApp(t, s, "agent")
		var buf bytes.Buffer
		app.Output = output.New(output.Options{Format: format, Writer: &buf})
		out, err := runConnectShowCmd(t, app)
		require.NoError(t, err, out)
		return buf.String()
	}
	ackOf := func(t *testing.T) setup.AckBoost {
		t.Helper()
		var envelope struct {
			Data struct {
				AckBoost setup.AckBoost `json:"ack_boost"`
			} `json:"data"`
		}
		raw := show(t, output.FormatJSON)
		require.NoError(t, json.Unmarshal([]byte(raw), &envelope), raw)
		return envelope.Data.AckBoost
	}

	data, err := os.ReadFile(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	require.NotContains(t, string(data), "ack_boost", "the default is not in the file")
	assert.Equal(t, setup.AckBoost{Content: "👀"}, ackOf(t))
	assert.Contains(t, show(t, output.FormatStyled), `boosts "👀"`)

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--no-ack-boost")
	require.NoError(t, err, out)
	assert.Equal(t, setup.AckBoost{Off: true}, ackOf(t))
	assert.Contains(t, show(t, output.FormatStyled), "off: the session boosts each request")
}
