//go:build unix

package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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
		State: connector.ConnectionRunning, PID: 42, ChangedAt: time.Now(), Detail: dangerousOnThisRun,
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

// A launch runs in dangerous mode only while this run may use it at all and
// connect.json says so now: turning it off takes effect at the next launch.
func TestDangerousLaunchesFollowTheFileWithinWhatTheRunAllows(t *testing.T) {
	fileOn := true
	file := func() bool { return fileOn }

	assert.True(t, dangerousLaunches(true, file)())
	assert.False(t, dangerousLaunches(false, file)(), "a run that may not use it never does")

	launches := dangerousLaunches(true, file)
	fileOn = false
	assert.False(t, launches(), "turning it off takes effect at the next launch")
	fileOn = true
	assert.False(t, launches(), "turning it back on takes a restart")
}

// A run may use dangerous mode only when connect.json has it on, trusts the
// operator alone, and nobody else's request is still waiting from an earlier
// run.
func TestDangerousModeIsAllowedOnlyForARunWithNothingOfOthersWaiting(t *testing.T) {
	ledger, err := connector.OpenLedger(t.TempDir() + "/state/connector.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ledger.Close() })
	ctx := t.Context()

	file := setup.File{Dangerous: true, Trust: admission.Trust{Mode: admission.TrustOperator, OperatorID: 111}}
	allowed, err := dangerousAllowedThisRun(ctx, ledger, file)
	require.NoError(t, err)
	assert.True(t, allowed)

	off := file
	off.Dangerous = false
	allowed, err = dangerousAllowedThisRun(ctx, ledger, off)
	require.NoError(t, err)
	assert.False(t, allowed)

	shared := file
	shared.Trust.Mode = admission.TrustProject
	allowed, err = dangerousAllowedThisRun(ctx, ledger, shared)
	require.NoError(t, err)
	assert.False(t, allowed)
}

// With dangerous mode on, the operator can't change without turning it off:
// the person whose requests run with a shell would otherwise change without
// anyone having turned it on for them.
func TestTheOperatorCantChangeWhileDangerousModeIsOn(t *testing.T) {
	before := setup.File{Dangerous: true, Trust: admission.Trust{Mode: admission.TrustOperator, OperatorID: 111}}
	after := before
	after.Trust.OperatorID = 222
	require.ErrorIs(t, setup.CheckDangerousOperator(before, after), setup.ErrDangerousOperatorChange)

	var out bytes.Buffer
	err := refusedChange(&out, "agent", setup.ErrDangerousOperatorChange)
	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Contains(t, e.Message, "the person who can give your agent work can't change")
	assert.Contains(t, e.Hint, "--dangerous=false")

	after.Dangerous = false
	require.NoError(t, setup.CheckDangerousOperator(before, after), "turned off in the same run")
	first := setup.File{}
	require.NoError(t, setup.CheckDangerousOperator(first, before), "a first setup has no operator to change")

	// Turned on for a new operator in one run: the person typed yes to it.
	off := before
	off.Dangerous = false
	on := off
	on.Dangerous, on.Trust.OperatorID = true, 222
	require.NoError(t, setup.CheckDangerousOperator(off, on), "turned on, not swapped while on")
}

// The off switch works with Basecamp out of reach: turning dangerous mode off
// on its own is saved with nothing asked of the server (Codex on #810).
func TestTurningDangerousModeOffNeedsNoBasecamp(t *testing.T) {
	s := startConnectSetupServer(t)
	firstSetup(t, s)
	guided(t, &scriptedPrompter{inputs: []string{"yes"}})
	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--dangerous")
	require.NoError(t, err, out)
	require.True(t, loadAgentFile(t).Dangerous)

	app := newConnectSetupApp(t, s, "agent")
	s.srv.Close() // Basecamp is down, or the credential refused
	out, err = runConnectSetupCmd(t, app, "--dangerous=false")
	require.NoError(t, err, out)
	assert.Contains(t, out, "Dangerous mode is off")
	assert.False(t, loadAgentFile(t).Dangerous)

	out, err = runConnectSetupCmd(t, app, "--dangerous=false")
	require.NoError(t, err, out)
	assert.Contains(t, out, "already off")
}

// The off switch works with BASECAMP_TOKEN set, which stops every other
// setup (cubic on #810).
func TestTurningDangerousModeOffWorksWithAnEnvironmentToken(t *testing.T) {
	s := startConnectSetupServer(t)
	firstSetup(t, s)
	guided(t, &scriptedPrompter{inputs: []string{"yes"}})
	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--dangerous")
	require.NoError(t, err, out)

	t.Setenv("BASECAMP_TOKEN", "a-token")
	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--dangerous=false")
	require.NoError(t, err, out)
	assert.False(t, loadAgentFile(t).Dangerous)
	_, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--dangerous")
	require.Error(t, err, "anything else still stops at the token")
}

// Dangerous mode is read fresh for every decision, and only for the operator
// the connector started trusting (Codex on #810): turning it off reaches the
// next decision at once, and a file that now names someone else is off.
func TestDangerousModeIsReadFreshForTheStartingOperator(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "connect")
	require.NoError(t, os.Mkdir(dir, 0o700))
	path := filepath.Join(dir, "connect.json")
	file := setup.New("agent")
	file.AccountID = "2914079"
	file.Agent = setup.Agent{PersonID: 52007412, Kind: setup.KindAgent}
	file.Trust.OperatorID = 111
	file.Projects = map[int64]admission.Project{48929974: {}}
	file.Dangerous = true
	write := func(f setup.File) {
		data, err := json.Marshal(f)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, data, 0o600))
	}
	write(file)
	clock := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	served := newConnectServed(path, file, slog.New(slog.DiscardHandler))
	served.now = func() time.Time { return clock } // the cache never expires: a fresh read must not need it to

	assert.True(t, served.DangerousFor(111))
	assert.False(t, served.DangerousFor(222), "not for an operator the connector did not start with")

	off := file
	off.Dangerous = false
	write(off)
	assert.False(t, served.DangerousFor(111), "turned off, at the very next decision")

	other := file
	other.Trust.OperatorID = 222
	write(other)
	assert.False(t, served.DangerousFor(111), "the file names another operator now")
}

// Status says when the running connector isn't using dangerous mode, rather
// than claiming it is.
func TestConnectStatusSaysWhenThisRunIsNotUsingDangerousMode(t *testing.T) {
	report := connectStatusReport{Profile: "agent", Dangerous: true, Status: connector.Status{Connection: &connector.ConnectionStatus{
		State: connector.ConnectionRunning, PID: 42, ChangedAt: time.Now(), Detail: dangerousOffThisRun,
	}}}
	var out bytes.Buffer
	renderConnectStatus(&out, report)
	assert.Contains(t, out.String(), "the running connector isn't using it: requests from other people")
	assert.NotContains(t, out.String(), "Dangerous mode is on: the agent can run any command")
	assert.Equal(t, "dangerous mode on but not in use until a restart after other people's waiting requests finish", strings.SplitN(connectStatusSummary(report), ",", 2)[0])

	// Turned on while it was already running, without dangerous mode.
	report.Status.Connection.Detail = ""
	out.Reset()
	renderConnectStatus(&out, report)
	assert.Contains(t, out.String(), "the running connector isn't using it: it was started before dangerous mode was turned on. Restart it")
	assert.NotContains(t, out.String(), "Dangerous mode is on: the agent can run any command")
	assert.Equal(t, "dangerous mode on but not in use until a restart", strings.SplitN(connectStatusSummary(report), ",", 2)[0])
}

// The fixes refusals print are commands that run as printed.
func TestDangerousRefusalsPrintRunnableCommands(t *testing.T) {
	for _, refused := range []error{setup.ErrDangerousWhileShared, setup.ErrDangerousOperatorChange} {
		var e *output.Error
		require.ErrorAs(t, refusedChange(io.Discard, "agent", refused), &e)
		assert.Contains(t, e.Hint, "basecamp connect setup -P agent --dangerous=false.")
		assert.NotContains(t, e.Hint, "you want")
	}
}
