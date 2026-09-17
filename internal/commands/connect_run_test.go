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

func TestConnectRunsOnLinuxAndMacOSOnly(t *testing.T) {
	assert.True(t, connectSupportedOS("linux"))
	assert.True(t, connectSupportedOS("darwin"))
	for _, goos := range []string{"freebsd", "openbsd", "windows"} {
		assert.False(t, connectSupportedOS(goos), goos)
	}
}

// Copilot: dispatch authorization follows connect.json as it is now.
func TestConnectRoutesFollowConnectJSON(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "connect")
	require.NoError(t, os.Mkdir(dir, 0o700))
	path := filepath.Join(dir, "connect.json")
	file := setup.New("agent")
	file.AccountID = "2914079"
	file.Agent = setup.Agent{PersonID: 52007412, Kind: setup.KindAgent}
	file.Trust.OperatorID = 26909558
	file.Projects = map[int64]admission.Route{48929974: {Path: "/work/repo"}}
	write := func(f setup.File) {
		data, err := json.Marshal(f)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, data, 0o600))
	}
	write(file)

	clock := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	routes := newConnectRoutes(path, file, slog.New(slog.DiscardHandler))
	routes.now = func() time.Time { return clock }
	assert.Equal(t, "/work/repo", routes.Current()[48929974].Path)

	unrouted := file
	unrouted.Projects = map[int64]admission.Route{}
	write(unrouted)
	clock = clock.Add(connectRoutesTTL)
	assert.Empty(t, routes.Current(), "an unrouted project stops authorizing dispatch without a restart")

	other := file
	other.Agent.PersonID = 1
	write(other)
	clock = clock.Add(connectRoutesTTL)
	assert.Empty(t, routes.Current(), "a file naming another agent authorizes nothing")

	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))
	clock = clock.Add(connectRoutesTTL)
	assert.Empty(t, routes.Current(), "a file that no longer loads authorizes nothing")
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
	file.Projects = map[int64]admission.Route{48929974: {Path: "/work/repo"}}
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

	_, err = connectDriver(setup.DriverACP, "nobody", dir)
	assert.Error(t, err)
}
