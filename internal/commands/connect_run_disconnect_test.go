package commands

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
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

	state, detail, err := connectorStoppedBy(refused, anAgent("Ryan Singer (agent)", "agent"), confirmed)
	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, output.CodeAuth, e.Code)
	assert.Equal(t, "Ryan Singer (agent) was disconnected in Basecamp, or connected on another computer", e.Message)
	assert.Equal(t, "Reconnect it: basecamp connect setup -P agent", e.Hint)
	assert.NotContains(t, e.Message+e.Hint, "authorization_failed")
	assert.Equal(t, connector.ConnectionDisconnected, state)
	assert.Equal(t, e.Message, detail)

	_, _, err = connectorStoppedBy(refused, anAgent("Ryan\nSinger (agent)", "my agent"), confirmed)
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
		state, _, got := connectorStoppedBy(err, anAgent("Ryan Singer (agent)", "agent"), mustNotAsk(t))
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
	state, detail, got := connectorStoppedBy(err, anAgent("Ryan Singer (agent)", "agent"), func() bool { return false })
	assert.Same(t, err, got)
	assert.Equal(t, connector.ConnectionStopped, state)
	assert.Empty(t, detail)
}

// A connector started after a disconnect meets it reading who it is: as a
// refused token renewal, or as a 401 for a token minted before the
// disconnect, which is still cached (Copilot on #806). A 401 is not a
// disconnect for a bot user, which has none.
func TestADisconnectMetAtStartIsRecognisedForAnAgentOnly(t *testing.T) {
	refused := output.ErrAuth("Minting an agent token was refused (invalid_client)")
	refused.Cause = auth.ErrAgentCredentialRefused
	unauthorized := &basecamp.Error{Code: basecamp.CodeAuth, Message: "unauthorized", HTTPStatus: 401}
	serverError := &basecamp.Error{Code: basecamp.CodeAPI, Message: "oops", HTTPStatus: 500}

	assert.True(t, agentDisconnectedAtStart(setup.KindAgent, refused))
	assert.True(t, agentDisconnectedAtStart(setup.KindAgent, fmt.Errorf("read me: %w", unauthorized)))
	assert.False(t, agentDisconnectedAtStart(setup.KindAgent, serverError))
	assert.False(t, agentDisconnectedAtStart(setup.KindAgent, nil))
	assert.False(t, agentDisconnectedAtStart(setup.KindBotUser, unauthorized), "a bot user is never disconnected")
}

// A bot user started after its login was revoked meets it as a refused
// refresh, and is told the bot is signed out, in the words a running
// connector uses, with the login that signs it in again. A 401 alone, or a
// refresh that failed some other way, keeps its own error.
func TestARefusedBotLoginMetAtStartSaysTheBotIsSignedOut(t *testing.T) {
	bot := aBot("", "bot", 4242)
	got := connectStartFailure(t.Context(), bot, fmt.Errorf("renew the token: %w", refusedLogin()))
	assert.Equal(t, errBotSignedOut("", "bot", 4242), got)

	for _, err := range []error{
		&basecamp.Error{Code: basecamp.CodeAuth, Message: "unauthorized", HTTPStatus: 401},
		output.ErrRateLimit(30),
		output.ErrAPI(503, "token refresh failed: unavailable"),
	} {
		var e *output.Error
		require.ErrorAs(t, connectStartFailure(t.Context(), bot, err), &e)
		assert.True(t, strings.HasPrefix(e.Message, `Could not read who profile "bot" is: `), e.Message)
	}
	assert.False(t, botSignedOutAtStart(setup.KindAgent, refusedLogin()), "an Agent's credential has no login to refuse")
}

// A bot user whose login Basecamp refused is not told it was disconnected,
// a thing that happens only to an Agent: it is told it is signed out, with
// the login that signs it in again under its pinned identity, and status
// records it as signed out. Every way of meeting the refusal says the same.
func TestABotUserWhoseLoginWasRefusedIsSignedOut(t *testing.T) {
	feedRefused := fmt.Errorf("intake: %w", &eventfeed.TerminalError{Reason: eventfeed.ReasonAuthorizationFailed, Msg: "3 consecutive connection-level authorization failures"})
	renewalRefused := fmt.Errorf("intake: %w", &eventfeed.TerminalError{Reason: eventfeed.ReasonPollFailed, Err: fmt.Errorf("renew the token: %w", refusedLogin())})
	for name, stop := range map[string]struct {
		err     error
		refused func() bool
	}{
		"a refresh the feed met":         {renewalRefused, mustNotAsk(t)},
		"a refresh the watch met":        {fmt.Errorf("credential: %w", refusedLogin()), mustNotAsk(t)},
		"a 401 the watch confirmed":      {fmt.Errorf("credential: %w", errCredentialNotTaken), mustNotAsk(t)},
		"the feed's authorization fails": {feedRefused, confirmed},
	} {
		state, detail, got := connectorStoppedBy(stop.err, aBot("Triage\nBot", "my bot", 4242), stop.refused)
		var e *output.Error
		require.ErrorAs(t, got, &e, name)
		assert.Equal(t, output.CodeAuth, e.Code, name)
		assert.Equal(t, "Triage Bot is no longer signed in: Basecamp refused its login", e.Message, name)
		assert.Equal(t, "Sign it in again: basecamp auth login -P 'my bot' --expect-identity 4242", e.Hint, name)
		assert.NotContains(t, e.Message, "disconnected", name)
		assert.Equal(t, connector.ConnectionSignedOut, state, name)
		assert.Equal(t, e.Message, detail, name)
	}

	state, _, got := connectorStoppedBy(feedRefused, aBot("Triage Bot", "bot", 4242), func() bool { return false })
	assert.Same(t, feedRefused, got, "an authorization failure Basecamp does not confirm keeps its own words")
	assert.Equal(t, connector.ConnectionStopped, state)

	assert.Equal(t, "Your bot user is no longer signed in: Basecamp refused its login", errBotSignedOut("", "bot", 4242).Message)
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
		state, detail, got := connectorStoppedBy(err, anAgent("Ryan Singer (agent)", "agent"), confirmed)
		assert.Same(t, err, got)
		assert.Equal(t, connector.ConnectionStopped, state)
		assert.Empty(t, detail)
	}
}

// Status says the bot user is signed out first, with the login that signs
// it in again.
func TestConnectStatusLeadsWithSignedOut(t *testing.T) {
	detail := "Triage Bot is no longer signed in: Basecamp refused its login"
	report := connectStatusReport{Profile: "bot", identity: 4242, Status: connector.Status{Connection: &connector.ConnectionStatus{
		State: connector.ConnectionSignedOut, PID: 42, ChangedAt: time.Now(), Detail: detail,
	}}}
	var out bytes.Buffer
	renderConnectStatus(&out, report)
	lines := strings.Split(out.String(), "\n")
	require.Greater(t, len(lines), 2)
	assert.Equal(t, "  Signed out: "+detail+". Sign it in again: basecamp auth login -P bot --expect-identity 4242", lines[2])
	assert.NotContains(t, out.String(), "Disconnected")
	assert.True(t, strings.HasPrefix(connectStatusSummary(report), "signed out"))
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

// anAgent is a connector running as an Agent.
func anAgent(name, profile string) connectRunAs {
	return connectRunAs{Kind: setup.KindAgent, Name: name, Profile: profile}
}

// aBot is a connector running as a bot user pinned to identity.
func aBot(name, profile string, identity int64) connectRunAs {
	return connectRunAs{Kind: setup.KindBotUser, Name: name, Profile: profile, IdentityID: identity}
}

// refusedLogin is a bot user's refresh the token endpoint refused, as the
// auth package returns it.
func refusedLogin() error {
	e := output.ErrAuth("Your session has expired or was revoked")
	e.Cause = auth.ErrLoginRefused
	return e
}

// mustNotAsk is a check that fails the test if it is made: a refused renewal
// is Basecamp's answer already.
func mustNotAsk(t *testing.T) func() bool {
	return func() bool { t.Error("asked Basecamp again about a refusal it already gave"); return true }
}
