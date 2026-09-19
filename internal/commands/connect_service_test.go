//go:build unix

package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// connectServiceHome points the config and unit directories at a temp home
// and records the systemctl calls instead of making them.
func connectServiceHome(t *testing.T) *[][]string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))

	var calls [][]string
	prev := runSystemctl
	runSystemctl = func(args ...string) ([]byte, error) {
		calls = append(calls, args)
		return nil, nil
	}
	t.Cleanup(func() { runSystemctl = prev })
	return &calls
}

// writeConnectSetup puts a connect.json where the service commands look for
// one, so the profile reads as set up.
func writeConnectSetup(t *testing.T, profile string) {
	t.Helper()
	path, err := setup.Path(config.GlobalConfigDir(), profile)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	f := setup.File{
		Version:     1,
		Profile:     profile,
		AccountID:   "2914079",
		Agent:       setup.Agent{PersonID: 52007412, Kind: "bot_user", IdentityID: 4242},
		Trust:       admission.Trust{Mode: admission.TrustOperator, OperatorID: 26909558},
		Projects:    map[int64]admission.Project{48699913: {}},
		Driver:      "spawn",
		Concurrency: 1,
		Deadline:    setup.Duration(30 * time.Minute),
	}
	b, err := json.Marshal(f)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, b, 0o600))
}

func connectServiceApp(t *testing.T, profile string) (*appctx.App, *bytes.Buffer) {
	t.Helper()
	cfg, err := config.Load(config.FlagOverrides{})
	require.NoError(t, err)
	cfg.ActiveProfile = profile
	said := &bytes.Buffer{}
	return &appctx.App{
		Config: cfg,
		Output: output.New(output.Options{Format: output.FormatStyled, Writer: said}),
	}, said
}

//nolint:contextcheck // the context is handed to the command, not to a call
func runConnectServiceCmd(t *testing.T, app *appctx.App, args ...string) (string, error) {
	t.Helper()
	cmd := NewConnectCmd()
	cmd.SetArgs(append([]string{"service"}, args...))
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.Execute()
	return out.String(), err
}

// The unit's whole point: systemd starts the connector again when it stops.
// Without Restart the unit is a launcher, not a supervisor, and a killed
// connector stays dead.
func TestConnectServiceUnitRestartsTheConnector(t *testing.T) {
	unit := connectServiceUnit("/usr/bin/basecamp", "agent", nil, false, false)

	assert.Contains(t, unit, "\nRestart=always\n", "a unit that does not restart supervises nothing")
	assert.Contains(t, unit, "\nRestartSec=5\n")
	assert.Contains(t, unit, "\nWantedBy=default.target\n", "without an [Install] section the unit cannot be enabled")
}

// SIGTERM is how the connector is asked to stop, and it spends its last
// seconds canceling live workers and posting their completions. A unit
// that killed it outright would leave those records needing redispatch by
// hand, and would count its own 143 as a crash.
func TestConnectServiceUnitLetsTheConnectorSettleItsWorkers(t *testing.T) {
	unit := connectServiceUnit("/usr/bin/basecamp", "agent", nil, false, false)

	assert.Contains(t, unit, "\nKillSignal=SIGTERM\n")
	assert.Contains(t, unit, "\nTimeoutStopSec=90\n")
	assert.Contains(t, unit, "\nSuccessExitStatus=143\n", "143 is the connector's clean exit on SIGTERM, not a failure")
}

func TestConnectServiceUnitRecordsTheRun(t *testing.T) {
	unit := connectServiceUnit("/usr/bin/basecamp", "agent", []int64{12345, 67890}, true, true)

	exec := unitDirective(t, unit, "ExecStart")
	assert.Equal(t,
		`"/usr/bin/basecamp" "connect" "--profile" "agent" "--project" "12345" "--project" "67890" "--shadow" "--hold"`,
		exec)
}

// Every word in the command line is quoted, so a path with a space in it
// stays one argument rather than becoming two.
func TestConnectServiceUnitQuotesTheExecutablePath(t *testing.T) {
	unit := connectServiceUnit(`/home/a b/go bin/basecamp`, "agent", nil, false, false)

	assert.Contains(t, unitDirective(t, unit, "ExecStart"), `"/home/a b/go bin/basecamp" "connect"`)
}

// --since enters the feed at one id. In a unit it would re-enter there on
// every restart instead of resuming from the ledger, so the flag is not
// offered at all; likewise the two overrides that belong in connect.json.
func TestConnectServiceInstallRefusesTheFlagsAUnitMustNotCarry(t *testing.T) {
	connectServiceHome(t)
	writeConnectSetup(t, "agent")

	for _, flag := range []string{"--since=5", "--driver=acp", "--acp-adapters=/tmp/a"} {
		app, _ := connectServiceApp(t, "agent")
		out, err := runConnectServiceCmd(t, app, "install", flag)
		require.Error(t, err, flag)
		assert.Contains(t, out+err.Error(), "unknown flag", flag)
	}
}

func TestConnectServiceInstallWritesAndStartsTheUnit(t *testing.T) {
	calls := connectServiceHome(t)
	writeConnectSetup(t, "agent")
	app, _ := connectServiceApp(t, "agent")

	_, err := runConnectServiceCmd(t, app, "install", "--project", "48699913")
	require.NoError(t, err)

	path, err := connectServiceUnitPath("agent")
	require.NoError(t, err)
	b, err := os.ReadFile(path) //nolint:gosec // path built from a temp home
	require.NoError(t, err)
	assert.Contains(t, string(b), `"--profile" "agent" "--project" "48699913"`)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	assert.Equal(t, [][]string{
		{"daemon-reload"},
		{"enable", "--now", "basecamp-connect-agent.service"},
		{"restart", "basecamp-connect-agent.service"},
	}, *calls, "the unit has to be reloaded, enabled and restarted or the new command line never runs")
}

// A unit for a profile that was never set up would start a connector that
// refuses to run, and systemd would restart it until it gave up. The
// refusal belongs here, where its reason can be read.
func TestConnectServiceInstallRefusesAProfileWithNoSetup(t *testing.T) {
	calls := connectServiceHome(t)
	app, _ := connectServiceApp(t, "agent")

	_, err := runConnectServiceCmd(t, app, "install")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connect.json")

	path, err := connectServiceUnitPath("agent")
	require.NoError(t, err)
	assert.NoFileExists(t, path, "no unit is written for a profile that cannot run")
	assert.Empty(t, *calls, "and systemd is not asked to start one")
}

func TestConnectServiceInstallNoEnableOnlyWritesTheUnit(t *testing.T) {
	calls := connectServiceHome(t)
	writeConnectSetup(t, "agent")
	app, _ := connectServiceApp(t, "agent")

	_, err := runConnectServiceCmd(t, app, "install", "--no-enable")
	require.NoError(t, err)

	path, err := connectServiceUnitPath("agent")
	require.NoError(t, err)
	assert.FileExists(t, path)
	assert.Empty(t, *calls)
}

func TestConnectServiceUninstallStopsAndRemoves(t *testing.T) {
	calls := connectServiceHome(t)
	writeConnectSetup(t, "agent")
	app, _ := connectServiceApp(t, "agent")
	_, err := runConnectServiceCmd(t, app, "install", "--no-enable")
	require.NoError(t, err)
	*calls = nil

	_, err = runConnectServiceCmd(t, app, "uninstall")
	require.NoError(t, err)

	path, err := connectServiceUnitPath("agent")
	require.NoError(t, err)
	assert.NoFileExists(t, path)
	assert.Equal(t, [][]string{
		{"disable", "--now", "basecamp-connect-agent.service"},
		{"daemon-reload"},
	}, *calls)
}

// Removing what is not there is not an error: an uninstall that failed
// because it had already run would be repeated by hand until someone
// checked why.
func TestConnectServiceUninstallOfNothingSucceeds(t *testing.T) {
	calls := connectServiceHome(t)
	app, said := connectServiceApp(t, "agent")

	_, err := runConnectServiceCmd(t, app, "uninstall")
	require.NoError(t, err)
	assert.Contains(t, said.String(), "nothing to remove")
	assert.Empty(t, *calls)
}

// One unit per profile, so a machine can supervise several agents without
// one install standing on another's.
func TestConnectServiceUnitIsNamedPerProfile(t *testing.T) {
	assert.Equal(t, "basecamp-connect-agent.service", connectServiceUnitName("agent"))
	assert.Equal(t, "basecamp-connect-other_bot.service", connectServiceUnitName("other_bot"))

	a, err := connectServiceUnitPath("agent")
	require.NoError(t, err)
	b, err := connectServiceUnitPath("other")
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
	assert.Equal(t, "user", filepath.Base(filepath.Dir(a)), "systemctl --user reads systemd/user")
}

// A profile name is the only part of the unit's name a person chooses, and
// a name with a slash or a newline in it would name another file or add a
// directive. It never gets that far: the same validation the run command
// applies refuses it first.
func TestConnectServiceRefusesAProfileNameThatIsNotOne(t *testing.T) {
	for _, name := range []string{"../evil", "a b", "a\nExecStart=/bin/sh", "a/b"} {
		_, err := connectServiceUnitPath(name)
		assert.Error(t, err, name)
	}
}

// unitDirective returns the value of a unit file's directive.
func unitDirective(t *testing.T, unit, key string) string {
	t.Helper()
	for _, line := range strings.Split(unit, "\n") {
		if after, ok := strings.CutPrefix(line, key+"="); ok {
			return after
		}
	}
	t.Fatalf("no %s in unit:\n%s", key, unit)
	return ""
}
