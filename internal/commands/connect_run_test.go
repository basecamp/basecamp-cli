package commands

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/admission"
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
// sealed — Linux (#736), and macOS, which seals them through /dev/fd.
func TestConnectRunsOnLinuxAndMacOS(t *testing.T) {
	assert.True(t, connectSupportedOS("linux"))
	assert.True(t, connectSupportedOS("darwin"))
	for _, goos := range []string{"freebsd", "openbsd", "windows"} {
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

// A second connector for an agent is refused with the holder named, and with
// why a second one is never needed: one connector serves every repo, because
// the session reading its requests picks the repo. A shadow run keeps its own
// lock, so a shadow refused met another shadow and is told only that.
func TestASecondConnectorIsToldOneServesEveryRepo(t *testing.T) {
	for _, tc := range []struct {
		name    string
		shadow  bool
		message string
		hint    string
		notHint string
	}{
		{name: "connector", message: "Another connector for this agent is already running",
			hint: "One connector serves every repo", notHint: "shadow"},
		{name: "shadow", shadow: true, message: "Another shadow run for this agent is already running",
			hint: "only another shadow run holds this one", notHint: "every repo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			holder, err := connector.AcquireInstanceLock(dir, "2914079", 52007412, time.Now())
			require.NoError(t, err)
			t.Cleanup(func() { _ = holder.Release() })

			_, err = acquireConnectLock(dir, "2914079", 52007412, tc.shadow)
			var refusal *output.Error
			require.ErrorAs(t, err, &refusal)
			assert.Equal(t, output.CodeLockUnavailable, refusal.Code)
			assert.Equal(t, output.ExitRateLimit, output.ExitCodeFor(refusal.Code), "exit 5, as every lock refusal")
			assert.Contains(t, refusal.Message, tc.message)
			assert.Contains(t, refusal.Message, fmt.Sprintf("held by pid %d since ", os.Getpid()), "the refusal names the holder")
			assert.Contains(t, refusal.Hint, tc.hint)
			assert.NotContains(t, refusal.Hint, tc.notHint)
		})
	}
}
