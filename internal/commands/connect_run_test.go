package commands

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
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
