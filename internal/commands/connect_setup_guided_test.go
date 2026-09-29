//go:build unix

package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// scriptedPrompter answers the guided setup's questions in order, and fails
// the question it has no answer for.
type scriptedPrompter struct {
	confirms  []bool
	inputs    []string
	asked     []string
	onConfirm func(question string)
}

func (p *scriptedPrompter) Confirm(question string, _ bool) (bool, error) {
	p.asked = append(p.asked, question)
	if p.onConfirm != nil {
		p.onConfirm(question)
	}
	if len(p.confirms) == 0 {
		return false, errors.New("unexpected question: " + question)
	}
	answer := p.confirms[0]
	p.confirms = p.confirms[1:]
	return answer, nil
}

func (p *scriptedPrompter) Input(question string) (string, error) {
	p.asked = append(p.asked, question)
	if len(p.inputs) == 0 {
		return "", errors.New("unexpected question: " + question)
	}
	answer := p.inputs[0]
	p.inputs = p.inputs[1:]
	return answer, nil
}

// guided runs setup as if a person were at the terminal, answering with p,
// on Linux, with the browser kept shut and the state home a temp dir.
func guided(t *testing.T, p *scriptedPrompter) {
	t.Helper()
	prevInteractive, prevAsk, prevConnection, prevGOOS := connectSetupInteractive, connectSetupAsk, connectSetupConnection, connectServiceGOOS
	connectSetupInteractive = func(*appctx.App) bool { return true }
	connectSetupAsk = p
	connectSetupConnection = agentConnectFlags{softwareName: "basecamp connect", noBrowser: true}
	connectServiceGOOS = "linux"
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Cleanup(func() {
		connectSetupInteractive, connectSetupAsk, connectSetupConnection, connectServiceGOOS = prevInteractive, prevAsk, prevConnection, prevGOOS
	})
	workerAnswers(t, readyWorker)
}

// readyWorker is a worker whose preflight passes.
var readyWorker = driver.Preflight{Product: "Claude Code", Version: "2.1.283", Checks: []driver.PreflightCheck{
	{Name: driver.PreflightStarts, Status: driver.PreflightPass, Message: "Claude Code 2.1.283 starts (/usr/local/bin/claude)"},
	{Name: driver.PreflightFlags, Status: driver.PreflightPass, Message: "knows all 13 options the connector passes"},
	{Name: driver.PreflightLogin, Status: driver.PreflightPass, Message: "logged in"},
}}

// brokenLauncher is the worker a manual run met: a claude on PATH that hands
// over to a Claude Code that is not there.
var brokenLauncher = driver.Preflight{Product: "Claude Code", Checks: []driver.PreflightCheck{
	{Name: driver.PreflightStarts, Status: driver.PreflightFail,
		Message: "The claude on your PATH is a launcher that couldn't find Claude Code (claude: line 2: /home/me/.local/share/mise/installs/claude/latest/claude: No such file or directory)",
		Hint:    "Reinstall Claude Code, or fix the launcher at /home/me/.local/bin/claude."},
}}

// workerAnswers answers the worker preflight with p, for the test.
func workerAnswers(t *testing.T, p driver.Preflight) {
	t.Helper()
	prev := connectWorkerPreflight
	connectWorkerPreflight = func(context.Context, setup.File) (driver.Preflight, bool) { return p, true }
	t.Cleanup(func() { connectWorkerPreflight = prev })
}

func ownedByTheOperator(s *connectSetupServer) {
	s.boss = map[string]any{"id": setupOperatorPerson, "name": "Operator"}
}

func projectsNamed(ids ...int64) []map[string]any {
	names := map[int64]string{setupProject: "Connector", setupProject2: "Launch"}
	projects := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		projects = append(projects, map[string]any{"id": id, "name": names[id]})
	}
	return projects
}

// A personal agent's operator is its owner, as Basecamp names them in the
// agent's own profile: no flag, and no second login.
func TestConnectSetupTakesAPersonalAgentsOwnerAsTheOperator(t *testing.T) {
	s := startConnectSetupServer(t)
	ownedByTheOperator(s)

	out, err := runConnectSetupCmd(t, connectSetupApp(t, s, "agent"), serveArg())
	require.NoError(t, err, out)

	assert.Contains(t, out, "the agent's owner")
	f, err := setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Equal(t, setupOperatorPerson, f.Trust.OperatorID)
}

// An agent Basecamp gives no owner still needs its operator named.
func TestConnectSetupWithoutAnOwnerStillNeedsAnOperator(t *testing.T) {
	s := startConnectSetupServer(t)

	out, err := runConnectSetupCmd(t, connectSetupApp(t, s, "agent"), serveArg())
	require.Error(t, err, out)
	var apiErr *output.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "Setup needs to know who the operator is", apiErr.Message)
	assertNotWritten(t, "agent")
}

// From nothing: the guided setup connects this computer, takes the owner as
// the operator, serves every one of the agent's projects, and says so in a
// few plain lines: the ids, the profile and the file path stay with `auth
// agent connect`, `connect setup` with flags, and `connect doctor`.
func TestGuidedConnectSetupFromNothing(t *testing.T) {
	s := startConnectSetupServer(t)
	ownedByTheOperator(s)
	s.agentProjects = projectsNamed(setupProject, setupProject2)
	p := &scriptedPrompter{confirms: []bool{true, false}}
	guided(t, p)

	out, err := runConnectSetupCmd(t, bareSetupApp(t, s, "agent"))
	require.NoError(t, err, out)

	assert.Contains(t, out, "First, connect this computer to your agent")
	assert.Contains(t, out, "✓ Connected this computer as Marie Chef\n")
	assert.Contains(t, out, "✓ Operator (you) is the only person who can give it work.")
	assert.Equal(t, []string{
		"Work in all 2 of Marie Chef's projects? Connector, Launch",
		"Start your agent now, and whenever you log in?",
	}, p.asked)
	assert.Contains(t, out, "✓ Claude Code 2.1.283 is ready")
	assert.Contains(t, out, "✓ Everything checks out")
	assert.Contains(t, out, "Marie Chef is set up on this computer")
	assert.Contains(t, out, "Works in:  Connector, Launch")
	assert.Contains(t, out, "Running:   no")
	assert.Contains(t, out, "Once it's running, mention Marie Chef in one of those projects to try it.")
	for _, internal := range []string{"Connected profile", "Account: 999", "auth status", "connect.json", "Identity", "Person " + strconv.FormatInt(setupOperatorPerson, 10)} {
		assert.NotContains(t, out, internal)
	}

	f, err := setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Equal(t, setupOperatorPerson, f.Trust.OperatorID)
	assert.Len(t, f.Projects, 2)
}

func TestGuidedConnectSetupPicksProjectsByNumber(t *testing.T) {
	s := startConnectSetupServer(t)
	ownedByTheOperator(s)
	s.agentProjects = projectsNamed(setupProject, setupProject2)
	guided(t, &scriptedPrompter{confirms: []bool{false, false}, inputs: []string{"2"}})

	out, err := runConnectSetupCmd(t, connectSetupApp(t, s, "agent"))
	require.NoError(t, err, out)

	assert.Contains(t, out, "1. Connector")
	assert.Contains(t, out, "2. Launch")
	f, err := setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Len(t, f.Projects, 1)
	assert.Contains(t, f.Projects, setupProject2)
}

// Started in the background, the agent is running, and setup says so in the
// summary rather than in the service's own report of the unit it wrote.
func TestGuidedConnectSetupStartsTheAgentInTheBackground(t *testing.T) {
	s := startConnectSetupServer(t)
	ownedByTheOperator(s)
	s.agentProjects = projectsNamed(setupProject)
	guided(t, &scriptedPrompter{confirms: []bool{true, true}})
	systemctlAnswers(t, nil)

	out, err := runConnectSetupCmd(t, connectSetupApp(t, s, "agent"))
	require.NoError(t, err, out)

	assert.Contains(t, out, "Running:   yes")
	assert.Contains(t, out, "\nMention Marie Chef in one of those projects to try it.")
	assert.NotContains(t, out, "Wrote ")
	assert.NotContains(t, out, "loginctl")
}

// A service that cannot start is said plainly, with the command that runs
// the agent now; the systemd detail is `connect service install`'s to give.
func TestGuidedConnectSetupSaysPlainlyWhenTheServiceCannotStart(t *testing.T) {
	s := startConnectSetupServer(t)
	ownedByTheOperator(s)
	s.agentProjects = projectsNamed(setupProject)
	guided(t, &scriptedPrompter{confirms: []bool{true, true}})
	systemctlAnswers(t, errors.New("the user manager does not see a unit by that name"))

	out, err := runConnectSetupCmd(t, connectSetupApp(t, s, "agent"))
	require.NoError(t, err, out)

	assert.Contains(t, out, "I couldn't start it in the background on this computer.")
	assert.Contains(t, out, "Run this now and leave it running: basecamp connect -P agent")
	assert.Contains(t, out, "To see why, run: basecamp connect service install -P agent")
	assert.NotContains(t, out, "user manager")
	assert.Contains(t, out, "Running:   no")
}

// systemctlAnswers makes every systemctl call succeed, or fail with err,
// without running it.
func systemctlAnswers(t *testing.T, err error) {
	t.Helper()
	prev := runSystemctl
	runSystemctl = func(args ...string) ([]byte, error) {
		if err != nil {
			return nil, err
		}
		if len(args) > 0 && args[0] == "show" {
			path, err := connectServiceUnitPath("agent")
			return []byte(path + "\n"), err
		}
		return nil, nil
	}
	t.Cleanup(func() { runSystemctl = prev })
}

// An agent in no projects yet is the next step of a first setup, in
// Basecamp, and not a failure: setup says what to do there and ends cleanly.
func TestGuidedConnectSetupStopsWhenTheAgentIsInNoProjects(t *testing.T) {
	s := startConnectSetupServer(t)
	ownedByTheOperator(s)
	guided(t, &scriptedPrompter{})

	out, err := runConnectSetupCmd(t, connectSetupApp(t, s, "agent"))
	require.NoError(t, err, out)
	assert.Contains(t, out, "Marie Chef isn't in any projects yet")
	assert.Contains(t, out, "Next: add it to the projects it should work in (in Basecamp, Adminland → Manage agents → Marie Chef → Edit), then run `basecamp connect setup` again.")
	assert.NotContains(t, out, "is set up on this computer")
	assertNotWritten(t, "agent")
}

// An AI that cannot start is caught here, before connect.json is written and
// before anyone mentions the agent: setup stops, says why in plain words, and
// says how to fix it.
func TestGuidedConnectSetupStopsWhenTheWorkerCannotStart(t *testing.T) {
	s := startConnectSetupServer(t)
	ownedByTheOperator(s)
	s.agentProjects = projectsNamed(setupProject)
	p := &scriptedPrompter{}
	guided(t, p)
	workerAnswers(t, brokenLauncher)

	out, err := runConnectSetupCmd(t, bareSetupApp(t, s, "agent"))
	require.Error(t, err, out)
	var apiErr *output.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, brokenLauncher.Checks[0].Message, apiErr.Message)
	assert.Equal(t, "Reinstall Claude Code, or fix the launcher at /home/me/.local/bin/claude. Then run this again: basecamp connect setup", apiErr.Hint)
	assert.Empty(t, p.asked, "nothing is asked of a person whose AI cannot start")
	assert.NotContains(t, out, "is set up on this computer")
	assertNotWritten(t, "agent")
}

// A finished setup whose AI has since stopped starting is not called set up.
func TestGuidedConnectSetupOnAFinishedSetupChecksTheWorker(t *testing.T) {
	s := startConnectSetupServer(t)
	firstSetup(t, s)
	ownedByTheOperator(s)
	s.agentProjects = projectsNamed(setupProject)
	guided(t, &scriptedPrompter{})
	loggedOut := driver.Preflight{Product: "Claude Code", Version: "2.1.283", Checks: []driver.PreflightCheck{
		readyWorker.Checks[0], readyWorker.Checks[1],
		{Name: driver.PreflightLogin, Status: driver.PreflightFail, Message: "Claude Code is logged out on this computer — run `claude` and log in"},
	}}
	workerAnswers(t, loggedOut)

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"))
	require.Error(t, err, out)
	var apiErr *output.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "Claude Code is logged out on this computer — run `claude` and log in", apiErr.Message)
	assert.Equal(t, "Then run this again: basecamp connect setup", apiErr.Hint)
	assert.NotContains(t, out, "is set up on this computer")
}

// A computer whose connection Basecamp no longer takes is offered a fresh
// one, and carries on from there.
func TestGuidedConnectSetupOffersToReconnectARefusedCredential(t *testing.T) {
	s := startConnectSetupServer(t)
	ownedByTheOperator(s)
	s.agentProjects = projectsNamed(setupProject)
	app := connectSetupApp(t, s, "agent")
	expireAgentToken(t, app)
	s.refuseSecret = true
	p := &scriptedPrompter{confirms: []bool{true, true, false}, onConfirm: func(q string) {
		if q == "Connect this computer to it again?" {
			s.refuseSecret = false // the person approves a new connection
		}
	}}
	guided(t, p)

	out, err := runConnectSetupCmd(t, app)
	require.NoError(t, err, out)

	assert.Contains(t, out, "was disconnected in Basecamp, or it was connected on another computer")
	assert.Equal(t, "Connect this computer to it again?", p.asked[0])
	assert.Equal(t, 2, s.intakeCount(), "a second connection ran")
	_, err = setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
}

func TestGuidedConnectSetupLeavesARefusedCredentialThePersonKeeps(t *testing.T) {
	s := startConnectSetupServer(t)
	app := connectSetupApp(t, s, "agent")
	expireAgentToken(t, app)
	s.refuseSecret = true
	guided(t, &scriptedPrompter{confirms: []bool{false}})

	out, err := runConnectSetupCmd(t, app)
	require.Error(t, err, out)
	assert.Equal(t, 1, s.intakeCount())
}

// A connect.json that cannot be read is set aside and set up again, when
// the person says so.
func TestGuidedConnectSetupRebuildsABrokenConnectJSON(t *testing.T) {
	s := startConnectSetupServer(t)
	firstSetup(t, s)
	ownedByTheOperator(s)
	s.agentProjects = projectsNamed(setupProject)
	path := connectSetupPath(t, "agent")
	require.NoError(t, os.WriteFile(path, []byte(`{"version": 1, "watch_completion": true`), 0o600))
	p := &scriptedPrompter{confirms: []bool{true, true, false}}
	guided(t, p)

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"))
	require.NoError(t, err, out)

	assert.Contains(t, out, "can't be used")
	assert.Equal(t, "Work in Marie Chef's project, Connector?", p.asked[1])
	aside, err := filepath.Glob(path + ".broken-*")
	require.NoError(t, err)
	assert.Len(t, aside, 1, "the broken file is kept beside the new one")
	f, err := setup.Load(path)
	require.NoError(t, err)
	assert.Contains(t, f.Projects, setupProject)
}

// On a finished setup the guided setup changes nothing and says where
// things are.
func TestGuidedConnectSetupOnAFinishedSetupSaysSo(t *testing.T) {
	s := startConnectSetupServer(t)
	firstSetup(t, s)
	ownedByTheOperator(s)
	s.agentProjects = projectsNamed(setupProject)
	path := connectSetupPath(t, "agent")
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	p := &scriptedPrompter{confirms: []bool{false}}
	guided(t, p)

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"))
	require.NoError(t, err, out)

	assert.Equal(t, []string{"Start your agent now, and whenever you log in?"}, p.asked)
	assert.Contains(t, out, "Marie Chef is set up on this computer")
	assert.Contains(t, out, "Works in:  Connector")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))
}

// A policy flag makes setup the scriptable command, even at a terminal.
func TestConnectSetupWithAPolicyFlagIsNeverGuided(t *testing.T) {
	s := startConnectSetupServer(t)
	p := &scriptedPrompter{}
	guided(t, p)

	out, err := runConnectSetupCmd(t, connectSetupApp(t, s, "agent"), "--operator", fmt.Sprint(setupOperatorPerson), serveArg())
	require.NoError(t, err, out)
	assert.Empty(t, p.asked)
	assert.NotContains(t, out, "is set up on this computer")
}

// expireAgentToken leaves the profile's agent credential holding an expired
// token, so the next command has to mint a new one from the secret.
func expireAgentToken(t *testing.T, app *appctx.App) {
	t.Helper()
	store := app.Auth.GetStore()
	creds, err := store.Load(app.Auth.CredentialKey())
	require.NoError(t, err)
	creds.ExpiresAt = 1
	require.NoError(t, store.Save(app.Auth.CredentialKey(), creds))
}
