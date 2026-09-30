//go:build unix

package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/admission"
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
	// As the real one: the acp driver has no spawn preflight.
	connectWorkerPreflight = func(_ context.Context, f setup.File) (driver.Preflight, bool) {
		return p, f.Driver != setup.DriverACP
	}
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
	p := &scriptedPrompter{confirms: []bool{true}}
	guided(t, p)

	out, err := runConnectSetupCmd(t, bareSetupApp(t, s, "agent"))
	require.NoError(t, err, out)

	assert.Contains(t, out, "First, connect this computer to your agent")
	assert.Contains(t, out, "✓ Connected this computer as Marie Chef\n")
	assert.Contains(t, out, "✓ Operator (you) is the only person who can give it work.")
	assert.Equal(t, []string{
		"Work in all 2 of Marie Chef's projects? Connector, Launch",
	}, p.asked)
	assert.Contains(t, out, "✓ Claude Code 2.1.283 is ready")
	assert.Contains(t, out, "✓ Everything checks out")
	assert.Contains(t, out, "Marie Chef is set up on this computer")
	assert.Contains(t, out, "Works in:  Connector, Launch")
	assert.Contains(t, out, "Running:   no")
	assert.Contains(t, out, "To start it, run this in the folder it should work in, and leave it running.")
	assert.Contains(t, out, "  basecamp connect -P agent\n")
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
	guided(t, &scriptedPrompter{confirms: []bool{false}, inputs: []string{"2"}})

	out, err := runConnectSetupCmd(t, connectSetupApp(t, s, "agent"))
	require.NoError(t, err, out)

	assert.Contains(t, out, "1. Connector")
	assert.Contains(t, out, "2. Launch")
	f, err := setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Len(t, f.Projects, 1)
	assert.Contains(t, f.Projects, setupProject2)
}

// Switching to the agent's profile keeps what this invocation named: an
// --account the profile isn't bound to is refused, as it is with -P, not
// quietly replaced by the profile's own (Codex on #794).
func TestGuidedConnectSetupKeepsAnExplicitAccount(t *testing.T) {
	s := startConnectSetupServer(t)
	ownedByTheOperator(s)
	s.agentProjects = projectsNamed(setupProject)
	connectSetupApp(t, s, "agent")
	guided(t, &scriptedPrompter{confirms: []bool{true}})

	app := newConnectSetupApp(t, s, "")
	app.Flags.Account = "777"
	app.Config.AccountID = "777"
	app.Config.Sources["account_id"] = string(config.SourceFlag)

	out, err := runConnectSetupCmd(t, app)
	require.Error(t, err, out)
	assert.Contains(t, err.Error(), "this command named account 777")
	assertNotWritten(t, "agent")
}

// Resuming a finished setup still checks the credential it will run on: a
// read-only one can't reply or acknowledge, so the resumed setup refuses it
// as a first setup does, rather than saying all is well (Codex on #794).
func TestGuidedConnectSetupResumedRefusesAReadOnlyCredential(t *testing.T) {
	s := startConnectSetupServer(t)
	firstSetup(t, s)
	ownedByTheOperator(s)
	s.agentProjects = projectsNamed(setupProject)
	app := newConnectSetupApp(t, s, "agent")
	creds, err := app.Auth.GetStore().Load(app.Auth.CredentialKey())
	require.NoError(t, err)
	creds.Scope = "read"
	require.NoError(t, app.Auth.GetStore().Save(app.Auth.CredentialKey(), creds))
	guided(t, &scriptedPrompter{})

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"))
	require.Error(t, err, out)
	assert.Contains(t, err.Error(), "not full access")
	assert.NotContains(t, out, "is set up on this computer")
}

// A worker with no preflight of its own (the acp driver) is still checked,
// as doctor checks it: its pinned adapter must be installed, or setup says so
// instead of calling the agent set up (Codex on #794).
func TestGuidedConnectSetupChecksAnACPWorker(t *testing.T) {
	s := startConnectSetupServer(t)
	firstSetup(t, s)
	ownedByTheOperator(s)
	s.agentProjects = projectsNamed(setupProject)
	path := connectSetupPath(t, "agent")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	doc["driver"] = setup.DriverACP
	raw, err = json.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	t.Setenv("XDG_DATA_HOME", t.TempDir()) // no adapters installed
	guided(t, &scriptedPrompter{})

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"))
	require.Error(t, err, out)
	assert.Contains(t, err.Error(), "adapter")
	assert.NotContains(t, out, "is set up on this computer")
}

// On a platform the connector doesn't run on, guided setup stops before it
// connects anything: connecting would replace the secret a Linux computer may
// be running this agent on, for a connector that can't run here (Codex on
// #794).
func TestGuidedConnectSetupRefusesAnUnsupportedPlatformBeforeConnecting(t *testing.T) {
	s := startConnectSetupServer(t)
	ownedByTheOperator(s)
	s.agentProjects = projectsNamed(setupProject)
	guided(t, &scriptedPrompter{})
	connectServiceGOOS = "darwin"

	out, err := runConnectSetupCmd(t, bareSetupApp(t, s, "agent"))
	require.Error(t, err, out)
	assert.Contains(t, err.Error(), "Linux only, not darwin")
	assert.NotContains(t, out, "connect this computer")
	assertNotWritten(t, "agent")
}

// A file another setup repaired while this one was asking is resumed, not
// moved aside (Codex on #794).
func TestGuidedConnectSetupResumesAFileRepairedWhileItAsked(t *testing.T) {
	s := startConnectSetupServer(t)
	firstSetup(t, s)
	ownedByTheOperator(s)
	s.agentProjects = projectsNamed(setupProject)
	path := connectSetupPath(t, "agent")
	good, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("{"), 0o600))
	guided(t, &scriptedPrompter{confirms: []bool{true}, onConfirm: func(string) {
		require.NoError(t, os.WriteFile(path, good, 0o600)) // the other setup finishes first
	}})

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"))
	require.NoError(t, err, out)
	assert.Contains(t, out, "is set up on this computer")
	aside, err := filepath.Glob(path + ".broken-*")
	require.NoError(t, err)
	assert.Empty(t, aside, "the repaired file was not moved aside")
}

// With every project taken out of connect.json there is nowhere to try the
// agent: the summary says so and how to add one (Codex on #794).
func TestGuidedConnectSetupWithNoProjectsServedSaysHowToAddOne(t *testing.T) {
	s := startConnectSetupServer(t)
	firstSetup(t, s)
	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--unserve", strconv.FormatInt(setupProject, 10))
	require.NoError(t, err, out)
	ownedByTheOperator(s)
	s.agentProjects = projectsNamed(setupProject)
	guided(t, &scriptedPrompter{})

	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"))
	require.NoError(t, err, out)
	assert.Contains(t, out, "Works in:  no projects")
	assert.Contains(t, out, "basecamp connect setup -P agent --serve <project-id>")
	assert.NotContains(t, out, "in one of those projects")
}

// Switching to the agent profile checks its base URL as root checks the one
// it started with: an insecure one is a setup error, not a panic in the SDK
// (Codex on #794).
func TestGuidedConnectSetupRefusesAnInsecureAgentProfileURL(t *testing.T) {
	s := startConnectSetupServer(t)
	connectSetupApp(t, s, "agent")
	guided(t, &scriptedPrompter{})
	_, err := registerProfile("agent", &config.ProfileConfig{BaseURL: "http://example.com", AccountID: "999", Scope: "full"})
	require.NoError(t, err)

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, ""))
	require.Error(t, err, out)
	assert.Contains(t, err.Error(), "base_url")
}

// Setup never offers the background service, which would run the agent in
// the home directory, and never touches systemd: it says how to start the
// agent in the folder it should work in.
func TestGuidedConnectSetupNeverOffersTheBackgroundService(t *testing.T) {
	s := startConnectSetupServer(t)
	ownedByTheOperator(s)
	s.agentProjects = projectsNamed(setupProject)
	p := &scriptedPrompter{confirms: []bool{true}}
	guided(t, p)
	prev := runSystemctl
	runSystemctl = func(args ...string) ([]byte, error) {
		t.Errorf("setup ran systemctl %v", args)
		return nil, nil
	}
	t.Cleanup(func() { runSystemctl = prev })

	out, err := runConnectSetupCmd(t, connectSetupApp(t, s, "agent"))
	require.NoError(t, err, out)

	assert.Equal(t, []string{"Work in Marie Chef's project, Connector?"}, p.asked)
	assert.Contains(t, out, "  basecamp connect -P agent\n")
	for _, service := range []string{"whenever you log in", "in the background", "service install", "systemctl"} {
		assert.NotContains(t, out, service)
	}
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
	assert.Contains(t, out, "Next: add it to the projects it should work in (in Basecamp, Adminland → Manage agents → Marie Chef → Edit), then run `basecamp connect setup -P agent` again.")
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

	app := bareSetupApp(t, s, "work")
	app.Flags.Profile = "work"
	out, err := runConnectSetupCmd(t, app)
	require.Error(t, err, out)
	var apiErr *output.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, brokenLauncher.Checks[0].Message, apiErr.Message)
	// The profile it was working on, which a bare `connect setup` may not
	// choose again (Codex on #794).
	assert.Equal(t, "Reinstall Claude Code, or fix the launcher at /home/me/.local/bin/claude. Then run this again: basecamp connect setup -P work", apiErr.Hint)
	assert.Empty(t, p.asked, "nothing is asked of a person whose AI cannot start")
	assert.NotContains(t, out, "is set up on this computer")
	assertNotWritten(t, "work")
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
	assert.Equal(t, "Then run this again: basecamp connect setup -P agent", apiErr.Hint)
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
	p := &scriptedPrompter{}
	guided(t, p)

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"))
	require.NoError(t, err, out)

	assert.Empty(t, p.asked)
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

// Guided setup asks about the agent it read. If the profile is connected to a
// different agent while a question is open, setup saves nothing for the new
// one: the person answered for the first (Codex on #794).
func TestGuidedConnectSetupRefusesAnAgentSwappedMidQuestion(t *testing.T) {
	s := startConnectSetupServer(t)
	ownedByTheOperator(s)
	s.agentProjects = projectsNamed(setupProject)
	p := &scriptedPrompter{confirms: []bool{true}, onConfirm: func(string) {
		s.mu.Lock()
		s.agentID++ // another agent's credential now answers for the profile
		s.mu.Unlock()
	}}
	guided(t, p)

	out, err := runConnectSetupCmd(t, connectSetupApp(t, s, "agent"))
	require.Error(t, err, out)
	var apiErr *output.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Contains(t, apiErr.Message, "connected to a different agent while setup was asking")
	assertNotWritten(t, "agent")
}

// A setup made for the same agent while guided setup was asking isn't merged
// into: guided setup told the person only its owner could give it work, and
// the other setup's trust may say otherwise (Codex on #794).
func TestGuidedConnectSetupRefusesASetupWrittenMidQuestion(t *testing.T) {
	s := startConnectSetupServer(t)
	ownedByTheOperator(s)
	s.agentProjects = projectsNamed(setupProject)
	app := connectSetupApp(t, s, "agent")
	p := &scriptedPrompter{confirms: []bool{true}, onConfirm: func(string) {
		out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--trust", "project", serveArg())
		require.NoError(t, err, out)
	}}
	guided(t, p)

	out, err := runConnectSetupCmd(t, app)
	require.Error(t, err, out)
	var apiErr *output.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Contains(t, apiErr.Message, "set up by another command while setup was asking")
	loaded, err := setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Equal(t, admission.TrustProject, loaded.Trust.Mode, "the other setup's file is left as it wrote it")
}

// A lock's metadata names a pid; the connector holding it started before it
// took the lock. A process the kernel gave the pid to after a crash started
// later, and isn't the connector (Codex on #794).
func TestHolderStillRunsOnlyForTheProcessThatTookTheLock(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	assert.True(t, holderStillRuns(connector.InstanceHolderInfo{PID: os.Getpid(), StartedAt: now}),
		"this process started before now")
	longAgo := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	assert.False(t, holderStillRuns(connector.InstanceHolderInfo{PID: os.Getpid(), StartedAt: longAgo}),
		"a lock taken before this process existed was taken by another")
	assert.False(t, holderStillRuns(connector.InstanceHolderInfo{PID: os.Getpid(), StartedAt: "not a time"}))
}
