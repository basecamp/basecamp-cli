//go:build unix

package commands

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
)

func loadAgentFile(t *testing.T) setup.File {
	t.Helper()
	f, err := setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	return f
}

func notAtATerminal(t *testing.T) {
	t.Helper()
	prev := connectSetupInteractive
	connectSetupInteractive = func(*appctx.App) bool { return false }
	t.Cleanup(func() { connectSetupInteractive = prev })
}

// Nobody at a terminal — a script, or an AI running the command — can't turn
// dangerous mode on. Nothing is asked and nothing changes.
func TestDangerousModeNeedsAPersonAtATerminal(t *testing.T) {
	s := startConnectSetupServer(t)
	firstSetup(t, s)
	notAtATerminal(t)

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--dangerous")
	require.Error(t, err, out)
	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Contains(t, e.Message, "needs you at a terminal")
	assert.Contains(t, e.Hint, "A script or an AI can't turn it on")
	assert.False(t, loadAgentFile(t).Dangerous)
}

// A person at the terminal is told what it means, and only a typed yes turns
// it on. Setup then shows it among the checks.
func TestDangerousModeTurnsOnOnlyForATypedYes(t *testing.T) {
	s := startConnectSetupServer(t)
	firstSetup(t, s)

	p := &scriptedPrompter{inputs: []string{"no"}}
	guided(t, p)
	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--dangerous")
	require.Error(t, err, out)
	assert.Contains(t, err.Error(), "Dangerous mode was left off")
	assert.Contains(t, out, "Dangerous mode lets your agent run any command on this computer")
	assert.Equal(t, []string{"Type yes to turn on dangerous mode"}, p.asked)
	assert.False(t, loadAgentFile(t).Dangerous)

	guided(t, &scriptedPrompter{inputs: []string{" YES "}})
	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--dangerous")
	require.NoError(t, err, out)
	assert.True(t, loadAgentFile(t).Dangerous)
	assert.Contains(t, out, "Dangerous mode")
	assert.Contains(t, out, "can run any command on this computer")

	// Already on: nothing to confirm again.
	guided(t, &scriptedPrompter{})
	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--dangerous")
	require.NoError(t, err, out)

	// Turning it off asks nothing.
	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--dangerous=false")
	require.NoError(t, err, out)
	assert.False(t, loadAgentFile(t).Dangerous)
}

// Asking for dangerous mode while other people can give the agent work is
// refused with the reasons, and the one command that makes it possible.
func TestDangerousModeWhileSharedIsRefusedWithTheReasons(t *testing.T) {
	s := startConnectSetupServer(t)
	firstSetup(t, s)
	guided(t, &scriptedPrompter{})
	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--trust", "project")
	require.NoError(t, err, out)

	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--dangerous")
	require.Error(t, err, out)
	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Contains(t, e.Message, "only for an agent that you alone can give work to")
	assert.Equal(t, "To use it, make the agent yours alone in the same run: basecamp connect setup -P agent --trust operator --dangerous", e.Hint)
	assert.Contains(t, out, "Why these don't go together yet")
	assert.Contains(t, out, "Anyone who can give your agent work could ask it to run anything")
	f := loadAgentFile(t)
	assert.False(t, f.Dangerous)
	assert.Equal(t, admission.TrustProject, f.Trust.Mode)
}

// Letting other people in while dangerous mode is on is refused the same
// way, with what to turn off first.
func TestSharingWhileDangerousIsRefusedWithTheReasons(t *testing.T) {
	s := startConnectSetupServer(t)
	firstSetup(t, s)
	guided(t, &scriptedPrompter{inputs: []string{"yes"}})
	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--dangerous")
	require.NoError(t, err, out)

	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--trust", "project")
	require.Error(t, err, out)
	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Contains(t, e.Message, "Dangerous mode is on, so other people can't be allowed to give your agent work yet")
	assert.Contains(t, e.Hint, "--dangerous=false")
	assert.Contains(t, out, "Why these don't go together yet")
	f := loadAgentFile(t)
	assert.True(t, f.Dangerous)
	assert.Equal(t, admission.TrustOperator, f.Trust.Mode)
}

// Status leads with dangerous mode while it's on.
func TestConnectStatusSaysWhenDangerousModeIsOn(t *testing.T) {
	report := connectStatusReport{Profile: "agent", Dangerous: true, Status: connector.Status{Connection: &connector.ConnectionStatus{
		State: connector.ConnectionRunning, PID: 42, ChangedAt: time.Now(),
	}}}
	var out bytes.Buffer
	renderConnectStatus(&out, report)
	assert.Contains(t, out.String(), "  Dangerous mode is on: the agent can run any command on this computer, as you. Turn it off: basecamp connect setup -P agent --dangerous=false")
	assert.True(t, strings.HasPrefix(connectStatusSummary(report), "dangerous mode on"))

	report.Dangerous = false
	out.Reset()
	renderConnectStatus(&out, report)
	assert.NotContains(t, out.String(), "Dangerous")
}

// A launch runs in dangerous mode only while connect.json says so and the
// connector was started with its owner alone trusted: trust is read once, at
// start, so a connector admitting others never gets a shell for their work.
func TestDangerousLaunchesNeedAConnectorStartedForItsOwnerAlone(t *testing.T) {
	fileOn := true
	file := func() bool { return fileOn }

	assert.True(t, dangerousLaunches(admission.TrustOperator, file)())
	assert.False(t, dangerousLaunches(admission.TrustProject, file)(), "started trusting the project")
	assert.False(t, dangerousLaunches(admission.TrustAllowlist, file)(), "started trusting others")

	launches := dangerousLaunches(admission.TrustOperator, file)
	fileOn = false
	assert.False(t, launches(), "turning it off takes effect at the next launch")
}
