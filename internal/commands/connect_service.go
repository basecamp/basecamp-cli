package commands

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// The connector has no daemon of its own and is not going to grow one: it
// runs in the foreground, exits 130 and 143 on SIGINT and SIGTERM after
// canceling and settling its live tasks, and leaves being restarted to the
// OS. These commands write the unit that does the restarting, and take it
// away again.
//
// systemd only. The connector runs on Linux alone, for the reason the run
// command gives, so there is no second supervisor to write for: a launchd
// agent would supervise a process that refuses to start.

// connectServiceUnitPrefix begins every unit this writes. The profile name
// completes it, so one machine can supervise several agents.
const connectServiceUnitPrefix = "basecamp-connect-"

// connectServiceRestartSec is how long systemd waits before starting the
// connector again. Long enough that a profile which cannot start — a
// credential the service cannot reach, a policy file someone else can
// write — burns through systemd's default start-limit burst and lands in
// `failed` where a person can see it, rather than spinning.
const connectServiceRestartSec = 5

// connectServiceStopSec is how long systemd waits after SIGTERM before it
// resorts to SIGKILL. The connector spends that time canceling live
// workers and posting their `ended at shutdown` completions; killed early,
// those completions are never written and the records need `redispatch` by
// hand. Ninety seconds is systemd's own default, stated here rather than
// inherited so that changing it is a decision someone made.
const connectServiceStopSec = 90

// runSystemctl runs systemctl for the calling user. A variable so tests
// drive install and uninstall without a session bus.
var runSystemctl = func(args ...string) ([]byte, error) {
	path, err := exec.LookPath("systemctl")
	if err != nil {
		return nil, err
	}
	return exec.Command(path, append([]string{"--user"}, args...)...).CombinedOutput() //nolint:gosec // path from LookPath, args are literals and validated ids
}

// connectServiceFlags are the parts of a run this unit records.
//
// Only the flags that describe a standing service are here. `--since`
// enters the feed at one id and is a one-shot: written into a unit it would
// re-enter there on every restart, which is the opposite of resuming, so it
// is not accepted. `--driver` and `--acp-adapters` are overrides of what
// connect.json already holds, and connect.json is where a service's
// settings belong — that also keeps every value in the unit either a
// validated profile name or a run of digits, with no free text to quote.
type connectServiceFlags struct {
	projects []string
	shadow   bool
	hold     bool
	noEnable bool
}

// newConnectServiceCmd is the service group.
func newConnectServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Install or remove the OS service that keeps the connector running",
		Long: `Install or remove a systemd user unit that runs the connector and starts
it again when it stops.

The connector itself stays in the foreground: the unit is the supervisor,
as it is for any other long-running command. One unit per profile, so a
machine can serve several agents.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newConnectServiceInstallCmd(), newConnectServiceUninstallCmd())
	return cmd
}

func newConnectServiceInstallCmd() *cobra.Command {
	var f connectServiceFlags
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Write and start the systemd user unit for a profile's connector",
		Long: `Write a systemd user unit that runs this profile's connector, then enable
and start it. The unit restarts the connector whenever it stops, so a
crash or a kill brings it back.

The unit records the run you asked for: the profile, and any --project,
--shadow or --hold. It does not take --since, which enters the feed at one
id and would re-enter there on every restart instead of resuming; nor
--driver or --acp-adapters, which override what connect.json holds, and a
standing service's settings belong in connect.json.

Installing again over an existing unit rewrites it and restarts the
service with the new arguments.

Examples:
  basecamp connect service install -P agent
  basecamp connect service install -P agent --project 12345 --shadow
  basecamp connect service install -P agent --no-enable`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runConnectServiceInstall(cmd, &f)
		},
	}
	fl := cmd.Flags()
	fl.Var((*repeatedString)(&f.projects), "project", "Only hear events in this project id (repeatable; default every project the agent can see)")
	fl.BoolVar(&f.shadow, "shadow", false, "Run in shadow: admit and log in an isolated state directory, dispatch and post nothing")
	fl.BoolVar(&f.hold, "hold", false, "Run with the durable hold set: intake and admission run, nothing is dispatched or posted")
	fl.BoolVar(&f.noEnable, "no-enable", false, "Write the unit but do not enable or start it")
	return cmd
}

func newConnectServiceUninstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop the profile's connector service and remove its unit",
		Long: `Stop and disable the profile's connector service, then remove its unit
file.

The connector is stopped with SIGTERM, so it cancels its live workers,
posts their completions and exits, the same as an interrupt at a terminal.
Nothing else is removed: the ledger, the checkpoint and connect.json stay
where they are, and installing again resumes from them.

Removing a unit that is not there succeeds and says so.

Examples:
  basecamp connect service uninstall -P agent`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runConnectServiceUninstall(cmd)
		},
	}
	return cmd
}

// connectServiceProfile is the profile these commands act on, refused here
// rather than by the unit failing later.
func connectServiceProfile(app *appctx.App) (string, error) {
	if app == nil {
		return "", fmt.Errorf("app not initialized")
	}
	if !connectSupportedOS(runtime.GOOS) {
		return "", connectUnsupportedOSError(runtime.GOOS)
	}
	name := app.Config.ActiveProfile
	if name == "" {
		return "", output.ErrUsageHint("The connector's service needs the agent's profile", "Pass -P/--profile <name>, a profile set up with `basecamp connect setup`.")
	}
	if !isValidProfileName(name) {
		return "", output.ErrUsage(fmt.Sprintf("Invalid profile name %q: use only letters, numbers, hyphens, and underscores", name))
	}
	return name, nil
}

// connectServiceUnitName is the unit file's name for a profile.
func connectServiceUnitName(profile string) string {
	return connectServiceUnitPrefix + profile + ".service"
}

// connectServiceUnitPath is where the unit goes: the user unit directory
// under the XDG config home, which is where `systemctl --user` looks.
func connectServiceUnitPath(profile string) (string, error) {
	if !isValidProfileName(profile) {
		return "", fmt.Errorf("invalid profile name %q", profile)
	}
	home := os.Getenv("XDG_CONFIG_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = filepath.Join(h, ".config")
	}
	return filepath.Join(home, "systemd", "user", connectServiceUnitName(profile)), nil
}

// connectServiceUnit renders the unit.
//
// Every value in it is either the profile name, which is validated to
// letters, numbers, hyphens and underscores, or a project id, which is
// parsed as a number before it gets here. Nothing a person typed reaches
// the file as text, so no directive can be smuggled in on a second line.
func connectServiceUnit(exe, profile string, projects []int64, shadow, hold bool) string {
	args := []string{"connect", "--profile", profile}
	for _, id := range projects {
		args = append(args, "--project", strconv.FormatInt(id, 10))
	}
	if shadow {
		args = append(args, "--shadow")
	}
	if hold {
		args = append(args, "--hold")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Written by `basecamp connect service install`. Edits are lost the next\n")
	fmt.Fprintf(&b, "# time it runs; change the run instead and install again.\n")
	fmt.Fprintf(&b, "[Unit]\n")
	fmt.Fprintf(&b, "Description=Basecamp agent connector for profile %s\n", profile)
	fmt.Fprintf(&b, "Documentation=https://github.com/basecamp/basecamp-cli\n")
	fmt.Fprintf(&b, "After=network-online.target\n")
	fmt.Fprintf(&b, "Wants=network-online.target\n\n")
	fmt.Fprintf(&b, "[Service]\n")
	fmt.Fprintf(&b, "Type=simple\n")
	fmt.Fprintf(&b, "ExecStart=%s\n", systemdExecLine(exe, args))
	fmt.Fprintf(&b, "Restart=always\n")
	fmt.Fprintf(&b, "RestartSec=%d\n", connectServiceRestartSec)
	// The connector's own contract with a supervisor: SIGTERM cancels live
	// workers, settles them and exits 143, and 143 is therefore a clean
	// stop rather than a failure.
	fmt.Fprintf(&b, "KillSignal=SIGTERM\n")
	fmt.Fprintf(&b, "TimeoutStopSec=%d\n", connectServiceStopSec)
	fmt.Fprintf(&b, "SuccessExitStatus=143\n\n")
	fmt.Fprintf(&b, "[Install]\n")
	fmt.Fprintf(&b, "WantedBy=default.target\n")
	return b.String()
}

// systemdExecLine renders an ExecStart command line. systemd reads
// double-quoted arguments with C-style escapes, which is what the
// executable's own path may need; the arguments after it are literals and
// validated ids.
func systemdExecLine(exe string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, systemdQuote(exe))
	for _, a := range args {
		parts = append(parts, systemdQuote(a))
	}
	return strings.Join(parts, " ")
}

// systemdQuote quotes one word for a unit file's command line.
func systemdQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

func runConnectServiceInstall(cmd *cobra.Command, f *connectServiceFlags) error {
	app := appctx.FromContext(cmd.Context())
	profile, err := connectServiceProfile(app)
	if err != nil {
		return err
	}
	projects, err := parseProjectIDs(f.projects)
	if err != nil {
		return err
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i] < projects[j] })

	// A profile with no setup would give a unit that starts, fails, and is
	// restarted until systemd gives up. Refuse it here, where the reason
	// can be read, rather than in a journal five restarts later.
	if err := connectServiceRequireSetup(profile); err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find this program's own path, which the unit has to name: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	path, err := connectServiceUnitPath(profile)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("cannot create %s: %w", richtext.SanitizeSingleLine(filepath.Dir(path)), err)
	}
	unit := connectServiceUnit(exe, profile, projects, f.shadow, f.hold)
	if err := os.WriteFile(path, []byte(unit), 0o600); err != nil {
		return fmt.Errorf("cannot write %s: %w", richtext.SanitizeSingleLine(path), err)
	}

	name := connectServiceUnitName(profile)
	summary := fmt.Sprintf("Wrote %s", path)
	enabled := false
	if !f.noEnable {
		if out, err := connectServiceEnable(name); err != nil {
			return output.ErrUsageHint(
				fmt.Sprintf("Wrote %s, but could not start it: %s", richtext.SanitizeSingleLine(path), richtext.SanitizeSingleLine(strings.TrimSpace(string(out)))),
				"Start it yourself: systemctl --user daemon-reload && systemctl --user enable --now "+name)
		}
		enabled = true
		summary = fmt.Sprintf("Wrote %s and started %s", path, name)
	}
	return app.OK(map[string]any{"unit": name, "path": path, "enabled": enabled},
		output.WithSummary(summary))
}

// connectServiceRequireSetup refuses a profile the connector could not run.
func connectServiceRequireSetup(profile string) error {
	path, err := setup.Path(config.GlobalConfigDir(), profile)
	if err != nil {
		return output.ErrUsage(err.Error())
	}
	f, err := setup.Load(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return output.ErrNotFoundHint("connect.json for profile", profile,
			"The profile has not been set up, so the service would restart a connector that cannot start. Set it up: basecamp connect setup -P "+richtext.ShellQuote(profile)+" --operator-profile '<your profile>' --serve <project-id>")
	case err != nil:
		return output.ErrUsageHint("connect.json cannot be used: "+setup.ErrorText(err),
			"No unit was written. Fix or remove "+richtext.SanitizeSingleLine(path)+", then run setup again.")
	}
	if f.Profile != profile {
		return output.ErrUsageHint(fmt.Sprintf("%s names profile %q, not %q", richtext.SanitizeSingleLine(path), f.Profile, profile),
			"No unit was written. Remove "+richtext.SanitizeSingleLine(path)+" and run setup again for this profile.")
	}
	return nil
}

func connectServiceEnable(unit string) ([]byte, error) {
	if out, err := runSystemctl("daemon-reload"); err != nil {
		return out, err
	}
	// enable --now on an already-running unit leaves it running with the
	// old command line, so the restart is asked for explicitly.
	if out, err := runSystemctl("enable", "--now", unit); err != nil {
		return out, err
	}
	return runSystemctl("restart", unit)
}

func runConnectServiceUninstall(cmd *cobra.Command) error {
	app := appctx.FromContext(cmd.Context())
	profile, err := connectServiceProfile(app)
	if err != nil {
		return err
	}
	path, err := connectServiceUnitPath(profile)
	if err != nil {
		return err
	}
	name := connectServiceUnitName(profile)

	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return app.OK(map[string]any{"unit": name, "path": path, "removed": false},
			output.WithSummary(fmt.Sprintf("No unit at %s; nothing to remove", path)))
	}

	// Stopping is best effort: the unit file must go even when there is no
	// session bus to talk to, or an uninstall on a machine without a user
	// session would leave the unit behind for the next login to start.
	_, _ = runSystemctl("disable", "--now", name)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot remove %s: %w", richtext.SanitizeSingleLine(path), err)
	}
	_, _ = runSystemctl("daemon-reload")
	return app.OK(map[string]any{"unit": name, "path": path, "removed": true},
		output.WithSummary(fmt.Sprintf("Stopped %s and removed %s", name, path)))
}
