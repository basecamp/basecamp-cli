package commands

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// A connector whose agent was disconnected in Basecamp, or connected on
// another computer, says so in plain words and names the command that
// reconnects it, not the feed's internals.
func TestTheConnectorSaysPlainlyWhenItsAgentWasDisconnected(t *testing.T) {
	refused := fmt.Errorf("intake: %w", &eventfeed.TerminalError{
		Reason: eventfeed.ReasonAuthorizationFailed,
		Msg:    "3 consecutive connection-level authorization failures",
		Err:    errors.New("event feed mint failed (unauthorized)"),
	})

	state, detail, err := connectorStoppedBy(refused, "Ryan Singer (agent)", "agent", confirmed)
	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, output.CodeAuth, e.Code)
	assert.Equal(t, "Ryan Singer (agent) was disconnected in Basecamp, or connected on another computer", e.Message)
	assert.Equal(t, "Reconnect it: basecamp connect setup -P agent", e.Hint)
	assert.NotContains(t, e.Message+e.Hint, "authorization_failed")
	assert.Equal(t, connector.ConnectionDisconnected, state)
	assert.Equal(t, e.Message, detail)

	_, _, err = connectorStoppedBy(refused, "Ryan\nSinger (agent)", "my agent", confirmed)
	require.ErrorAs(t, err, &e)
	assert.NotContains(t, e.Message, "\n")
	assert.Equal(t, "Reconnect it: basecamp connect setup -P 'my agent'", e.Hint)
}

// A token renewal Basecamp refuses is the same disconnect, whether the feed
// classed it as a failed mint or a failed poll (Codex on #806).
func TestARefusedTokenRenewalIsTheSameDisconnect(t *testing.T) {
	refused := output.ErrAuth("Minting an agent token was refused (invalid_client)")
	refused.Cause = auth.ErrAgentCredentialRefused
	for _, reason := range []eventfeed.TerminalReason{eventfeed.ReasonMintFailed, eventfeed.ReasonPollFailed} {
		err := fmt.Errorf("intake: %w", &eventfeed.TerminalError{Reason: reason, Err: fmt.Errorf("renew the token: %w", refused)})
		state, _, got := connectorStoppedBy(err, "Ryan Singer (agent)", "agent", mustNotAsk(t))
		var e *output.Error
		require.ErrorAs(t, got, &e, reason)
		assert.Equal(t, "Ryan Singer (agent) was disconnected in Basecamp, or connected on another computer", e.Message, reason)
		assert.Equal(t, connector.ConnectionDisconnected, state, reason)
	}
}

// The feed's authorization_failed also counts forbidden answers, so it is
// called a disconnect only when Basecamp confirms the credential is refused
// (Copilot on #806).
func TestAnAuthorizationFailureTheCredentialSurvivesIsNotCalledADisconnect(t *testing.T) {
	err := fmt.Errorf("intake: %w", &eventfeed.TerminalError{Reason: eventfeed.ReasonAuthorizationFailed, Msg: "3 consecutive connection-level authorization failures"})
	state, detail, got := connectorStoppedBy(err, "Ryan Singer (agent)", "agent", func() bool { return false })
	assert.Same(t, err, got)
	assert.Equal(t, connector.ConnectionStopped, state)
	assert.Empty(t, detail)
}

// With no name to give, the message still reads as a sentence.
func TestADisconnectWithoutTheAgentsNameSaysYourAgent(t *testing.T) {
	e := errAgentDisconnected("", "agent")
	assert.Equal(t, "Your agent was disconnected in Basecamp, or connected on another computer", e.Message)
	assert.Equal(t, "Reconnect it: basecamp connect setup -P agent", e.Hint)
}

// Every other way a part can stop the connector keeps its own words.
func TestTheConnectorKeepsOtherStopsAsTheyAre(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("intake: %w", &eventfeed.TerminalError{Reason: eventfeed.ReasonProtocolFatal, Msg: "bad frame"}),
		fmt.Errorf("dispatch: %w", errors.New("stopped on its own")),
	} {
		state, detail, got := connectorStoppedBy(err, "Ryan Singer (agent)", "agent", confirmed)
		assert.Same(t, err, got)
		assert.Equal(t, connector.ConnectionStopped, state)
		assert.Empty(t, detail)
	}
}

// Status says the agent was disconnected first, with the command that
// reconnects it.
func TestConnectStatusLeadsWithDisconnected(t *testing.T) {
	detail := "Ryan Singer (agent) was disconnected in Basecamp, or connected on another computer"
	report := connectStatusReport{Profile: "agent", Status: connector.Status{Connection: &connector.ConnectionStatus{
		State: connector.ConnectionDisconnected, PID: 42, ChangedAt: time.Now(), Detail: detail,
	}}}
	var out bytes.Buffer
	renderConnectStatus(&out, report)
	lines := strings.Split(out.String(), "\n")
	require.Greater(t, len(lines), 2)
	assert.Equal(t, "  Disconnected: "+detail+". Reconnect it: basecamp connect setup -P agent", lines[2])
	assert.True(t, strings.HasPrefix(connectStatusSummary(report), "disconnected"))

	report.Status.Connection.State, report.Status.Connection.Detail = connector.ConnectionStopped, ""
	out.Reset()
	renderConnectStatus(&out, report)
	assert.NotContains(t, out.String(), "Disconnected")
	assert.NotContains(t, connectStatusSummary(report), "disconnected")
}

func confirmed() bool { return true }

// mustNotAsk is a check that fails the test if it is made: a refused renewal
// is Basecamp's answer already.
func mustNotAsk(t *testing.T) func() bool {
	return func() bool { t.Error("asked Basecamp again about a refusal it already gave"); return true }
}
