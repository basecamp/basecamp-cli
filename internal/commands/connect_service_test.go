//go:build linux

package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
		// A working manager reads the file that was just written, which is
		// what the fragment check asks it.
		if len(args) > 0 && args[0] == "show" {
			path, err := connectServiceUnitPath("agent")
			if err != nil {
				return nil, err
			}
			return []byte(path + "\n"), nil
		}
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
	unit := connectServiceUnit("/usr/bin/basecamp", "agent", nil, false, false, nil)

	assert.Contains(t, unit, "\nRestart=always\n", "a unit that does not restart supervises nothing")
	assert.Contains(t, unit, "\nRestartSec=5\n")
	assert.Contains(t, unit, "\nWantedBy=default.target\n", "without an [Install] section the unit cannot be enabled")
}

// SIGTERM is how the connector is asked to stop, and it spends its last
// seconds canceling live workers and posting their completions. A unit
// that killed it outright would leave those records needing redispatch by
// hand, and would count its own 143 as a crash.
func TestConnectServiceUnitLetsTheConnectorSettleItsWorkers(t *testing.T) {
	unit := connectServiceUnit("/usr/bin/basecamp", "agent", nil, false, false, nil)

	assert.Contains(t, unit, "\nKillSignal=SIGTERM\n")
	assert.Contains(t, unit, "\nTimeoutStopSec=90\n")
	assert.Contains(t, unit, "\nSuccessExitStatus=143\n", "143 is the connector's clean exit on SIGTERM, not a failure")
}

func TestConnectServiceUnitRecordsTheRun(t *testing.T) {
	unit := connectServiceUnit("/usr/bin/basecamp", "agent", []int64{12345, 67890}, true, true, nil)

	exec := unitDirective(t, unit, "ExecStart")
	assert.Equal(t,
		`"/usr/bin/basecamp" "connect" "--profile" "agent" "--project" "12345" "--project" "67890" "--shadow" "--hold"`,
		exec)
}

// Every word in the command line is quoted, so a path with a space in it
// stays one argument rather than becoming two.
func TestConnectServiceUnitQuotesTheExecutablePath(t *testing.T) {
	unit := connectServiceUnit(`/home/a b/go bin/basecamp`, "agent", nil, false, false, nil)

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
		{"show", "-p", "FragmentPath", "--value", "basecamp-connect-agent.service"},
		{"enable", "basecamp-connect-agent.service"},
		{"reset-failed", "basecamp-connect-agent.service"},
		{"restart", "basecamp-connect-agent.service"},
	}, *calls, "reloaded, checked to be the file systemd reads, enabled, cleared and restarted, or the new command line never runs")
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

// A connector that can never start — a credential the service cannot
// reach, a worker that is not installed — must become visible rather than
// be retried forever. Restart=always on its own reports `activating` for as
// long as the machine is up, which is a service claiming health it has not
// got.
func TestConnectServiceUnitStopsRetryingAConnectorThatCannotStart(t *testing.T) {
	unit := connectServiceUnit("/usr/bin/basecamp", "agent", nil, false, false, nil)

	assert.Contains(t, unit, "\nStartLimitIntervalSec=300\n")
	assert.Contains(t, unit, "\nStartLimitBurst=5\n")
}

// The user manager does not have the shell's PATH and computes the XDG
// defaults itself, so a unit that pins nothing runs a connector that reads
// another connect.json, keeps its ledger elsewhere, and looks for its
// worker on a PATH that has not got it — active, and failing every
// dispatch.
func TestConnectServiceUnitPinsTheEnvironmentInstallVerified(t *testing.T) {
	unit := connectServiceUnit("/usr/bin/basecamp", "agent", nil, false, false,
		[]string{"PATH=/home/a/bin:/usr/bin", "XDG_CONFIG_HOME=/home/a/.config"})

	assert.Contains(t, unit, `Environment="PATH=/home/a/bin:/usr/bin"`)
	assert.Contains(t, unit, `Environment="XDG_CONFIG_HOME=/home/a/.config"`)
}

func TestConnectServiceEnvRefusesAValueThatWouldSplitTheUnitFile(t *testing.T) {
	t.Setenv("PATH", "/usr/bin\nExecStart=/bin/sh")

	_, err := connectServiceEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PATH")
}

// enable --now starts it, and a restart straight after would stop that
// process and start another — with intake, or a dispatch, possibly already
// begun in between.
func TestConnectServiceInstallStartsTheConnectorOnce(t *testing.T) {
	calls := connectServiceHome(t)
	writeConnectSetup(t, "agent")
	app, _ := connectServiceApp(t, "agent")

	require.NoError(t, runConnectServiceInstallForTest(t, app))

	for _, c := range *calls {
		assert.NotContains(t, c, "--now", "enable --now starts a process that the restart then kills: %v", c)
	}
}

// systemd has to be reading the file that was just written. A custom
// XDG_CONFIG_HOME set in this shell alone puts the unit where the user
// manager will never look, and enable would go on to succeed against some
// older unit.
func TestConnectServiceInstallRefusesWhenSystemdReadsAnotherFile(t *testing.T) {
	connectServiceHome(t)
	writeConnectSetup(t, "agent")
	prev := runSystemctl
	runSystemctl = func(args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "show" {
			return []byte("/etc/systemd/user/basecamp-connect-agent.service\n"), nil
		}
		return nil, nil
	}
	t.Cleanup(func() { runSystemctl = prev })
	app, _ := connectServiceApp(t, "agent")

	_, err := runConnectServiceCmd(t, app, "install")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "/etc/systemd/user/basecamp-connect-agent.service")
}

// The platform gate is the run command's, so a Mac is refused before a unit
// is written for a connector that would not start on it.
func TestConnectServiceRefusesAPlatformTheConnectorDoesNotRunOn(t *testing.T) {
	connectServiceHome(t)
	writeConnectSetup(t, "agent")
	prev := connectServiceGOOS
	connectServiceGOOS = "darwin"
	t.Cleanup(func() { connectServiceGOOS = prev })
	app, _ := connectServiceApp(t, "agent")

	_, err := runConnectServiceCmd(t, app, "install")
	require.Error(t, err)
	assert.Contains(t, err.Error(), connectLinuxOnlyReason)

	path, err := connectServiceUnitPath("agent")
	require.NoError(t, err)
	assert.NoFileExists(t, path)
}

// When systemctl is missing, or fails in the running rather than in what it
// was asked, its combined output is empty and the error is the only account
// there is.
func TestConnectServiceInstallKeepsTheReasonWhenSystemctlSaysNothing(t *testing.T) {
	connectServiceHome(t)
	writeConnectSetup(t, "agent")
	prev := runSystemctl
	runSystemctl = func(_ ...string) ([]byte, error) {
		return nil, errors.New("exec: \"systemctl\": executable file not found in $PATH")
	}
	t.Cleanup(func() { runSystemctl = prev })
	app, _ := connectServiceApp(t, "agent")

	_, err := runConnectServiceCmd(t, app, "install")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "executable file not found")
}

// runConnectServiceInstallForTest runs install and returns its error.
func runConnectServiceInstallForTest(t *testing.T, app *appctx.App) error {
	t.Helper()
	_, err := runConnectServiceCmd(t, app, "install")
	return err
}

// systemd expands its own %% specifiers everywhere and $VAR in a command
// line even inside double quotes, so a home directory with a percent in it,
// or a path holding $HOME verbatim, would be rewritten into something else
// before the connector ever ran.
func TestConnectServiceUnitSurvivesAPathSystemdWouldRewrite(t *testing.T) {
	unit := connectServiceUnit(`/opt/%u/$agent/base"camp`, "agent", nil, false, false,
		[]string{`PATH=/opt/%u/bin:/x$y`})

	assert.Contains(t, unitDirective(t, unit, "ExecStart"), `"/opt/%%u/$$agent/base\"camp"`,
		"a command line expands both %% and $")
	assert.Contains(t, unit, `Environment="PATH=/opt/%%u/bin:/x$y"`,
		"Environment expands %% but takes $ literally, so doubling it there would corrupt the value")
}

// A control character in a path must not end the directive and begin
// another.
func TestConnectServiceUnitEscapesControlCharacters(t *testing.T) {
	unit := connectServiceUnit("/opt/a\tb/basecamp", "agent", nil, false, false, nil)

	exec := unitDirective(t, unit, "ExecStart")
	assert.Contains(t, exec, `\t`)
	assert.NotContains(t, exec, "\t", "a real tab is written as an escape, not passed through")
}

// The unit gives up after five failed starts, and systemd then refuses to
// restart it until the window passes — "start of the service was attempted
// too often". So the install that follows a fix has to clear the counter,
// or installing again would do nothing for five minutes, which is what the
// command tells people to do.
func TestConnectServiceInstallClearsAFailedUnitBeforeStartingIt(t *testing.T) {
	calls := connectServiceHome(t)
	writeConnectSetup(t, "agent")
	app, _ := connectServiceApp(t, "agent")

	_, err := runConnectServiceCmd(t, app, "install")
	require.NoError(t, err)

	order := make([]string, 0, len(*calls))
	for _, c := range *calls {
		order = append(order, c[0])
	}
	require.Contains(t, order, "reset-failed")
	assert.Less(t, indexOfCall(order, "reset-failed"), indexOfCall(order, "restart"),
		"clearing the counter after the restart would be too late")
}

// A stop that failed may have left the connector running, and a summary
// that said "Stopped" would be telling someone the opposite of what
// happened.
func TestConnectServiceUninstallSaysWhenItCouldNotStopTheConnector(t *testing.T) {
	connectServiceHome(t)
	writeConnectSetup(t, "agent")
	app, said := connectServiceApp(t, "agent")
	_, err := runConnectServiceCmd(t, app, "install", "--no-enable")
	require.NoError(t, err)

	prev := runSystemctl
	runSystemctl = func(args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "disable" {
			return []byte("Failed to disable unit: Connection reset by peer"), errors.New("exit status 1")
		}
		return nil, nil
	}
	t.Cleanup(func() { runSystemctl = prev })
	said.Reset()

	_, err = runConnectServiceCmd(t, app, "uninstall")
	require.NoError(t, err, "the unit file still has to go")
	assert.Contains(t, said.String(), "may still be running")
	assert.NotContains(t, said.String(), "Stopped basecamp-connect-agent.service")

	path, err := connectServiceUnitPath("agent")
	require.NoError(t, err)
	assert.NoFileExists(t, path)
}

func indexOfCall(order []string, want string) int {
	for i, c := range order {
		if c == want {
			return i
		}
	}
	return -1
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
