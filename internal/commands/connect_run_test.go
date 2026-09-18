package commands

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/driver/acp"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
)

func TestConnectProjectFlagRepeatsAndRefusesNonIDs(t *testing.T) {
	cmd := NewConnectCmd()
	require.NoError(t, cmd.Flags().Parse([]string{"--project", "12", "--project", "34,12"}))
	flag := cmd.Flags().Lookup("project")
	assert.Equal(t, "string", flag.Value.Type(), "the global flag's type is kept")
	ids, err := parseProjectIDs(*flag.Value.(*repeatedString))
	require.NoError(t, err)
	assert.Equal(t, []int64{12, 34}, ids)

	_, err = parseProjectIDs([]string{"abc"})
	assert.Error(t, err)
	_, err = parseProjectIDs([]string{""})
	assert.Error(t, err)
}

func TestConnectStateLivesUnderXDGStateHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	home, err := connectStateHome()
	require.NoError(t, err)
	assert.Equal(t, dir, home)
	got, err := ensurePrivateChain(home, "basecamp", "connect", "2914079-1")
	require.NoError(t, err)
	assert.DirExists(t, got)
}

// Copilot on #738: macOS passed this check and then failed every non-shadow
// dispatch at the worker's MCP handshake, because the token hand-over onto
// an inherited descriptor is accepted only where those descriptors are
// sealed — Linux (#736).
func TestConnectRunsOnLinuxOnly(t *testing.T) {
	assert.True(t, connectSupportedOS("linux"))
	for _, goos := range []string{"darwin", "freebsd", "openbsd", "windows"} {
		assert.False(t, connectSupportedOS(goos), goos)
	}
}

// Copilot: dispatch authorization follows connect.json as it is now.
func TestServedProjectsFollowConnectJSON(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "connect")
	require.NoError(t, os.Mkdir(dir, 0o700))
	path := filepath.Join(dir, "connect.json")
	file := setup.New("agent")
	file.AccountID = "2914079"
	file.Agent = setup.Agent{PersonID: 52007412, Kind: setup.KindAgent}
	file.Trust.OperatorID = 26909558
	file.Projects = map[int64]admission.Project{48929974: {Class: "internal"}}
	write := func(f setup.File) {
		data, err := json.Marshal(f)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, data, 0o600))
	}
	write(file)

	clock := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	served := newConnectServed(path, file, slog.New(slog.DiscardHandler))
	served.now = func() time.Time { return clock }
	current, err := served.Current()
	require.NoError(t, err)
	require.Contains(t, current, int64(48929974))
	assert.Equal(t, "internal", current[48929974].Class)

	unserved := file
	unserved.Projects = map[int64]admission.Project{}
	write(unserved)
	clock = clock.Add(connectServedTTL)
	current, err = served.Current()
	require.NoError(t, err, "serving nothing is an answer, not a failure")
	assert.Empty(t, current, "a project no longer served stops authorizing dispatch without a restart")

	// Each failure is checked from a *non-empty* last-good state, and on the
	// map that failing call returned — not on one a previous call left in
	// the variable. Asserting the stale one passes however much
	// authorization a broken read hands back, which is the one place in this
	// change where that would cost the most (Copilot on #765).
	for _, tc := range []struct {
		name   string
		break_ func()
		why    string
	}{
		{
			name: "a file naming another agent",
			break_: func() {
				other := file
				other.Agent.PersonID = 1
				write(other)
			},
			why: "a file naming another agent is a failure to read the answer, not the answer",
		},
		{
			name:   "a file that no longer parses",
			break_: func() { require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600)) },
			why:    "a file that no longer loads is reported as unreadable, never as an empty served set",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Back to serving something, so a leak of stale authorization
			// has something to leak.
			write(file)
			clock = clock.Add(connectServedTTL)
			good, err := served.Current()
			require.NoError(t, err)
			require.NotEmpty(t, good, "the last good read served a project")

			tc.break_()
			clock = clock.Add(connectServedTTL)
			broken, err := served.Current()
			assert.Error(t, err, tc.why)
			assert.Empty(t, broken, "and it hands back no authorization at all, stale or otherwise")
		})
	}
}

// Copilot and review r2: the run's --project scope reaches the dispatcher.
func TestConnectDispatcherGetsTheRunsScopeAndSettings(t *testing.T) {
	file := setup.New("agent")
	file.Concurrency = 3
	file.Deadline = setup.Duration(90 * time.Minute)
	opts := connectDispatcherOptions(connectDispatch{
		File: file, Buckets: []int64{48929974}, Profile: "agent",
		Executable: "/usr/local/bin/basecamp", StateDir: "/state/2914079-1", SessionsDir: "/state/2914079-1/sessions",
	})
	assert.Equal(t, []int64{48929974}, opts.Buckets, "the projects this run hears are the projects it dispatches")
	assert.Equal(t, 3, opts.Concurrency)
	assert.Equal(t, 90*time.Minute, opts.Deadline)
	assert.Equal(t, "agent", opts.MCP.Profile)
	assert.Equal(t, "/state/2914079-1", opts.MCP.StateDir)
	assert.Equal(t, "/state/2914079-1/sessions", opts.PrivateDir)
}

// The credential rule: a file that carries a task token lives outside the
// state directory and every working directory.
func TestConnectSessionFilesLiveOutsideTheStateDirectory(t *testing.T) {
	runtime := t.TempDir()
	state := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("XDG_STATE_HOME", state)
	file := setup.New("agent")
	file.AccountID = "2914079"
	file.Agent = setup.Agent{PersonID: 52007412, Kind: setup.KindAgent}

	dir, err := connectSessionsDir(file)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(dir, runtime+string(filepath.Separator)))
	stateDir, err := connectStateDir(file, false)
	require.NoError(t, err)
	assert.False(t, strings.HasPrefix(dir, stateDir), "not under the state directory")
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

// Card 22's review: a unix socket path is 103 bytes at most, and doctor says
// so before a dispatch discovers it.
func TestDoctorWarnsWhenSessionPathsCannotTakeASocket(t *testing.T) {
	file := setup.New("agent")
	file.AccountID = "2914079"
	file.Agent = setup.Agent{PersonID: 52007412, Kind: setup.KindAgent}

	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	sessions := connectSessionsPath(file)
	assert.True(t, connector.TokenSocketFits(filepath.Join(sessions, strings.Repeat("a", connector.AttemptIDLength))),
		"a per-user runtime directory takes one")

	deep, err := os.MkdirTemp("/tmp", "bcc-doctor-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(deep) })
	deep = filepath.Join(deep, strings.Repeat("d", 40), strings.Repeat("e", 40))
	require.NoError(t, os.MkdirAll(deep, 0o700))
	t.Setenv("XDG_RUNTIME_DIR", deep)
	sessions = connectSessionsPath(file)
	assert.False(t, connector.TokenSocketFits(filepath.Join(sessions, strings.Repeat("a", connector.AttemptIDLength))),
		"and a deep one does not, which is what doctor warns about")
}

// The check doctor actually runs, not only the paths behind it.
func TestTheDoctorCheckReadsTheProfilesConnectorLayout(t *testing.T) {
	app := &appctx.App{Config: &config.Config{}}
	assert.Nil(t, checkConnectorSessionPaths(app), "no profile, nothing to say")

	// A config home of this test's own: the check must never read the
	// person's real one.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	app.Config.ActiveProfile = "agent"
	assert.Nil(t, checkConnectorSessionPaths(app), "a profile with no connect.json is not a connector")

	file := setup.New("agent")
	file.AccountID = "2914079"
	file.Agent = setup.Agent{PersonID: 52007412, Kind: setup.KindAgent}
	file.Trust.OperatorID = 26909558
	file.Projects = map[int64]admission.Project{48929974: {}}
	path, err := setup.Path(config.GlobalConfigDir(), "agent")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	data, err := json.Marshal(file)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))

	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	check := checkConnectorSessionPaths(app)
	require.NotNil(t, check)
	assert.Equal(t, "pass", check.Status, check.Message)

	deep, err := os.MkdirTemp("/tmp", "bcc-doctor-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(deep) })
	deep = filepath.Join(deep, strings.Repeat("d", 40), strings.Repeat("e", 40))
	require.NoError(t, os.MkdirAll(deep, 0o700))
	t.Setenv("XDG_RUNTIME_DIR", deep)
	check = checkConnectorSessionPaths(app)
	require.NotNil(t, check)
	assert.Equal(t, "warn", check.Status)
	assert.Contains(t, check.Hint, "XDG_RUNTIME_DIR", "and says what to do about it")
}

func TestConnectDriverRunsTheWorkersPinnedACPAdapterFromWhereItWasInstalled(t *testing.T) {
	d, err := connectDriver(setup.DriverSpawn, setup.WorkerClaude, "")
	require.NoError(t, err)
	assert.Equal(t, setup.WorkerClaude, d.Name())

	dir := t.TempDir()
	_, err = connectDriver(setup.DriverACP, setup.WorkerClaude, dir)
	require.ErrorIs(t, err, acp.ErrAdapterMissing, "an adapter that is not installed is never fetched")

	pkg := filepath.Join(dir, "node_modules", filepath.FromSlash(acp.ClaudeAgentACP.Package))
	require.NoError(t, os.MkdirAll(pkg, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pkg, "package.json"),
		[]byte(`{"name":"`+acp.ClaudeAgentACP.Package+`","version":"`+acp.ClaudeAgentACP.Version+`"}`), 0o600))
	bin := filepath.Join(dir, "node_modules", ".bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, acp.ClaudeAgentACP.Name), []byte("#!/bin/sh\n"), 0o700))
	d, err = connectDriver(setup.DriverACP, setup.WorkerClaude, dir)
	require.NoError(t, err)
	assert.Equal(t, acp.Name, d.Name())

	// A relative directory is the operator's, from where they run the command.
	t.Chdir(filepath.Dir(dir))
	d, err = connectDriver(setup.DriverACP, setup.WorkerClaude, filepath.Base(dir))
	require.NoError(t, err)
	assert.Equal(t, acp.Name, d.Name())

	_, err = connectDriver(setup.DriverACP, "nobody", dir)
	assert.Error(t, err)
}

// Copilot on #738: intake takes only a positive --since as an override, so a
// negative one was accepted here and then quietly ignored there — the run
// resumed from the ledger while the person who typed it believed otherwise.
func TestANegativeSinceIsRefusedRatherThanIgnored(t *testing.T) {
	zero, err := connectSinceOverride(0)
	require.NoError(t, err)
	assert.Zero(t, zero, "the default still means: resume from the ledger")

	at, err := connectSinceOverride(1234)
	require.NoError(t, err)
	assert.Equal(t, int64(1234), at)

	_, err = connectSinceOverride(-1)
	require.Error(t, err)
	var usage *output.Error
	require.ErrorAs(t, err, &usage)
	assert.Equal(t, output.CodeUsage, usage.Code)
}

// Copilot on #738: a shadow keeps its own ledger, lock and checkpoint, and
// intake's contract is that two connectors in one account never share a
// checkpoint lineage. A shadow beside the connector it watches is two.
func TestAShadowRunHasACheckpointLineageOfItsOwn(t *testing.T) {
	assert.Equal(t, "basecamp-connect-52007412", connectConsumerNamespace(52007412, false),
		"and the connector's own lineage does not move")
	assert.NotEqual(t, connectConsumerNamespace(52007412, false), connectConsumerNamespace(52007412, true))
}
