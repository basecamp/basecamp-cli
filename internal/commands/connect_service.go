package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/driver"
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

// connectServiceStartLimit bounds the restarting. Restart=always on its own
// will retry a connector that can never start — a credential the service
// cannot reach, a worker that is not installed — for as long as the machine
// is up, and `is-active` says `activating` the whole time. That is a service
// reporting health it does not have. Five starts inside five minutes, which
// at RestartSec=5 takes about twenty-five seconds, puts the unit in `failed`
// where a person and `systemctl --user is-active` both see it.
const (
	connectServiceStartLimitSec = 300
	connectServiceStartLimitN   = 5
)

// connectServiceStopSec is how long systemd waits after SIGTERM before it
// resorts to SIGKILL. The connector spends that time canceling live
// workers and posting their `ended at shutdown` completions; killed early,
// those completions are never written and the records need `redispatch` by
// hand. Ninety seconds is systemd's own default, stated here rather than
// inherited so that changing it is a decision someone made.
const connectServiceStopSec = 90

// connectServiceGOOS is the platform these commands answer for. A variable
// so the refusal on a platform the connector does not run on is testable
// from the one platform it does.
var connectServiceGOOS = runtime.GOOS

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
	if !connectSupportedOS(connectServiceGOOS) {
		return "", connectUnsupportedOSError(connectServiceGOOS)
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
// The command line holds only the profile name, validated to letters,
// numbers, hyphens and underscores, and project ids parsed as numbers
// before they get here, so nothing a person typed reaches it as text.
//
// The Environment lines are the exception and are free text: they carry
// PATH and the XDG variables as install found them. They are quoted, and
// connectServiceEnv refuses any that holds a newline, which is the only
// character that could end the directive and begin another.
func connectServiceUnit(exe, profile string, projects []int64, shadow, hold bool, env []string, envFile string) string {
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
	fmt.Fprintf(&b, "Wants=network-online.target\n")
	// [Unit], not [Service]: systemd moved the start limit here in v229.
	fmt.Fprintf(&b, "StartLimitIntervalSec=%d\n", connectServiceStartLimitSec)
	fmt.Fprintf(&b, "StartLimitBurst=%d\n\n", connectServiceStartLimitN)
	fmt.Fprintf(&b, "[Service]\n")
	fmt.Fprintf(&b, "Type=simple\n")
	// A systemd user manager starts from its own environment, not from the
	// shell that installed this. Without these the connector would look for
	// another connect.json, keep its ledger somewhere else, and run a worker
	// it could not find — active, and failing every dispatch.
	for _, e := range env {
		fmt.Fprintf(&b, "Environment=%s\n", systemdQuote(e, false))
	}
	// Where a credential goes, if the driver needs one. The leading dash
	// makes it optional, so the ordinary case has no file at all; install
	// never writes it, because install would be writing a secret.
	if envFile != "" {
		fmt.Fprintf(&b, "EnvironmentFile=-%s\n", systemdPath(envFile))
	}
	fmt.Fprintf(&b, "ExecStart=%s\n", systemdExecLine(exe, args))
	fmt.Fprintf(&b, "Restart=always\n")
	fmt.Fprintf(&b, "RestartSec=%d\n", connectServiceRestartSec)
	// The connector's own contract with a supervisor: SIGTERM cancels live
	// workers, settles them and exits 143, and 143 is therefore a clean
	// stop rather than a failure.
	fmt.Fprintf(&b, "KillSignal=SIGTERM\n")
	// mixed, not the default control-group: that signals the workers at the
	// same instant as the connector, and a worker's session ending before
	// the connector's context is canceled is read as StopLost rather than
	// StopShutdown — a stranded attempt needing redispatch by hand, which is
	// the opposite of what TimeoutStopSec above is for. Only the connector
	// gets the SIGTERM; systemd still SIGKILLs whatever is left in the
	// cgroup once the timeout passes.
	fmt.Fprintf(&b, "KillMode=mixed\n")
	fmt.Fprintf(&b, "TimeoutStopSec=%d\n", connectServiceStopSec)
	fmt.Fprintf(&b, "SuccessExitStatus=143\n\n")
	fmt.Fprintf(&b, "[Install]\n")
	fmt.Fprintf(&b, "WantedBy=default.target\n")
	return b.String()
}

// systemdExecLine renders an ExecStart command line.
func systemdExecLine(exe string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, systemdQuote(exe, true))
	for _, a := range args {
		parts = append(parts, systemdQuote(a, true))
	}
	return strings.Join(parts, " ")
}

// systemdQuote quotes one value for a unit file.
//
// Quoting is not enough on its own. systemd expands its own % specifiers
// everywhere — %u is the user name, %% is a literal percent — and expands
// $VAR in a command line even inside double quotes. A home directory with a
// percent in it, or a path holding $HOME verbatim, would otherwise be
// rewritten into something else or refused. So % is always doubled, and $
// is doubled only where it means anything: expandDollar is true for a
// command line and false for Environment=, which takes $ literally.
//
// Control characters are written as C escapes, which systemd reads inside
// double quotes, rather than being allowed to end the directive.
func systemdQuote(s string, expandDollar bool) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '\\' || r == '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '%':
			b.WriteString("%%")
		case r == '$' && expandDollar:
			b.WriteString("$$")
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// systemdPath writes a path for a directive that takes the rest of the
// line as one path, such as EnvironmentFile=.
//
// Unlike ExecStart= and Environment=, these are not split into words, so
// systemd neither removes quotes nor decodes C escapes: a quoted path is a
// relative path beginning with a quote, which systemd ignores, and \x20 is
// four literal characters. Spaces need nothing. Specifiers are still
// expanded, so % is doubled. A line break cannot be written at all, and
// connectServiceEnvFile refuses a path that holds one.
func systemdPath(s string) string {
	return strings.ReplaceAll(s, "%", "%%")
}

// connectServiceEnvKeys are the variables the unit pins. A systemd user
// manager is started at login with an environment of its own: it does not
// have the shell's PATH, and it computes XDG defaults itself. Left to it,
// the connector reads a different connect.json from the one install just
// checked, keeps its ledger somewhere else, and runs the worker by bare
// name — `claude`, `codex` — off a PATH that does not have it. The unit
// would be active and every dispatch would fail.
//
// The list is the connector's own, not a new one: driver.BaseEnv is what a
// worker may inherit, and connector.MCPServerEnv what the worker's MCP
// server needs on top. A fourth list here would drift from those two, and
// an allowlist that drifts is an allowlist that reads as configuration and
// does nothing.
func connectServiceEnvKeys() []string {
	keys := append([]string{}, driver.BaseEnv...)
	keys = append(keys, connector.MCPServerEnv...)
	for name, secret := range connectServiceDriverEnv {
		if !secret {
			keys = append(keys, name)
		}
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

// connectServiceDriverEnv is what the drivers read beyond driver.BaseEnv,
// and whether each one authenticates somebody. Half of them do, and a unit
// file is not where a credential goes: `systemctl cat` prints it, a backup
// copies it, and driver.BaseEnv already says the worker environment carries
// "nothing that authenticates anyone".
//
// So the settings are pinned and the credentials are not. A credential the
// service needs goes in the environment file the unit reads, which the
// person owns and install never writes.
//
// TestConnectServiceClassifiesEveryDriverVariable keeps this map level with
// the drivers' own lists, so a new one is a failing test rather than a
// variable that quietly stops reaching a worker.
var connectServiceDriverEnv = map[string]bool{
	"CLAUDE_CONFIG_DIR":  false,
	"ANTHROPIC_BASE_URL": false,
	"ANTHROPIC_API_KEY":  true,
	"CODEX_HOME":         false,
	"CODEX_API_KEY":      true,
	"OPENAI_BASE_URL":    false,
	"OPENAI_API_KEY":     true,
}

// connectServiceMissingCredentials names the driver credentials this shell
// has and the service will not, so a person hears it at install rather than
// discovering it in a dispatch that failed.
func connectServiceMissingCredentials() []string {
	var missing []string
	for name, secret := range connectServiceDriverEnv {
		if !secret {
			continue
		}
		if v, ok := os.LookupEnv(name); ok && v != "" {
			missing = append(missing, name)
		}
	}
	slices.Sort(missing)
	return missing
}

// connectServiceEnvFile is where the unit reads the credentials install
// will not copy: beside the profile's own connector state, owner-only.
func connectServiceEnvFile(profile string) (string, error) {
	path, err := setup.Path(config.GlobalConfigDir(), profile)
	if err != nil {
		return "", err
	}
	envFile := filepath.Join(filepath.Dir(path), "service.env")
	// systemd ignores a relative EnvironmentFile=, and GlobalConfigDir keeps
	// a relative XDG_CONFIG_HOME as it is.
	if !filepath.IsAbs(envFile) {
		return "", output.ErrUsageHint(
			fmt.Sprintf("%s is not an absolute path, and systemd ignores a relative environment file", richtext.SanitizeSingleLine(envFile)),
			"Set XDG_CONFIG_HOME to an absolute path in this shell, or unset it, then install again. No unit was written.")
	}
	if strings.ContainsAny(envFile, "\n\r\x00") {
		return "", output.ErrUsageHint(
			fmt.Sprintf("%s holds a newline or a null byte, which cannot go into a unit file", richtext.SanitizeSingleLine(envFile)),
			"Fix XDG_CONFIG_HOME in this shell, then install again. No unit was written.")
	}
	return envFile, nil
}

// connectServiceEnv is the environment to pin, in unit form.
func connectServiceEnv() ([]string, error) {
	var env []string
	for _, k := range connectServiceEnvKeys() {
		v, ok := os.LookupEnv(k)
		if !ok || v == "" {
			continue
		}
		if strings.ContainsAny(v, "\n\r\x00") {
			return nil, output.ErrUsageHint(
				fmt.Sprintf("%s holds a newline or a null byte, which cannot go into a unit file", k),
				"Fix "+k+" in this shell, then install again. No unit was written.")
		}
		env = append(env, k+"="+v)
	}
	return env, nil
}

// connectServiceExecutable is the path the unit should run, which is the
// stable one rather than the resolved one.
//
// os.Executable reads /proc/self/exe, which the kernel has already followed
// to the real file. On a mise or Nix installation that is a versioned store
// path behind a shim: baking it into a unit means the service keeps running
// the version installed today after an upgrade, and fails to start at all
// once that version is collected. So the name the caller was invoked by is
// resolved through PATH and made absolute, without following the last
// symlink — the shim is the point.
//
// The resolved path is the fallback, for a binary invoked by a name that no
// longer finds it.
func connectServiceExecutable() (string, error) {
	if len(os.Args) > 0 && os.Args[0] != "" {
		if found, err := exec.LookPath(os.Args[0]); err == nil {
			if abs, err := filepath.Abs(found); err == nil {
				return abs, nil
			}
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot find this program's own path, which the unit has to name: %w", err)
	}
	return exe, nil
}

// connectServiceVersionedDirs are path fragments that mark a directory a
// version manager removes when that version is uninstalled or upgraded
// away. A shim is stable and PATH usually finds one, but a shell that has
// the versioned bin directory on PATH itself — mise activate, a Go binary
// installed with mise's Go — makes that the path the unit runs.
var connectServiceVersionedDirs = []string{"/mise/installs/", "/.asdf/installs/", "/nix/store/"}

// connectServiceExecutableWarning says when the unit runs a binary that an
// upgrade will delete: the service keeps running until its next restart,
// then fails to start at all, which is a failure a long way from its cause.
func connectServiceExecutableWarning(exe string) string {
	for _, dir := range connectServiceVersionedDirs {
		if strings.Contains(exe, dir) {
			return "The unit runs " + richtext.SanitizeSingleLine(exe) + ", which is inside a version manager's install directory and goes away when that version does. " +
				"Install the service again after upgrading, or run install from a basecamp on a path that does not change"
		}
	}
	return ""
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

	exe, err := connectServiceExecutable()
	if err != nil {
		return err
	}

	path, err := connectServiceUnitPath(profile)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("cannot create %s: %w", richtext.SanitizeSingleLine(filepath.Dir(path)), err)
	}
	env, err := connectServiceEnv()
	if err != nil {
		return err
	}
	envFile, err := connectServiceEnvFile(profile)
	if err != nil {
		return err
	}
	unit := connectServiceUnit(exe, profile, projects, f.shadow, f.hold, env, envFile)
	// Written whole or not at all. A direct write truncates the unit that is
	// enabled and running now, so an install that ran out of disk would
	// leave a half a unit: systemd keeps running from what it already loaded
	// and then refuses to load it at the next boot, which is a failure a
	// reboot away from whoever caused it.
	if err := writeFileAtomic(path, []byte(unit), 0o600); err != nil {
		return fmt.Errorf("cannot write %s: %w", richtext.SanitizeSingleLine(path), err)
	}

	name := connectServiceUnitName(profile)
	summary := fmt.Sprintf("Wrote %s", path)
	enabled := false
	if !f.noEnable {
		if out, err := connectServiceEnable(name, path); err != nil {
			// systemctl says nothing at all when it is missing, or when the
			// failure is in running it rather than in what it was asked; the
			// error is the only account of that, so it is not thrown away.
			why := strings.TrimSpace(string(out))
			if why == "" {
				why = err.Error()
			}
			return output.ErrUsageHint(
				fmt.Sprintf("Wrote %s, but could not start it: %s", richtext.SanitizeSingleLine(path), richtext.SanitizeSingleLine(why)),
				"Start it yourself: systemctl --user daemon-reload && systemctl --user enable "+name+" && systemctl --user restart "+name)
		}
		enabled = true
		summary = fmt.Sprintf("Wrote %s and started %s", path, name)
		if warning := connectServiceLingerWarning(cmd.Context()); warning != "" {
			summary += ". " + warning
		}
	}
	if warning := connectServiceExecutableWarning(exe); warning != "" {
		summary += ". " + warning
	}
	if missing := connectServiceMissingCredentials(); len(missing) > 0 {
		summary += fmt.Sprintf(". %s %s set here and will not be in the service, which never carries a credential: put %s in %s (owner-only) and the unit will read it",
			strings.Join(missing, ", "),
			map[bool]string{true: "is", false: "are"}[len(missing) == 1],
			map[bool]string{true: "it", false: "them"}[len(missing) == 1],
			envFile)
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

// connectServiceEnable reloads, enables and starts the unit.
//
// enable without --now, then restart. `enable --now` starts it, and a
// restart straight after would stop that process and start another —
// and the first one can have begun intake, or a dispatch, in between.
// restart starts an inactive unit and refreshes a running one, so it is
// both the first start and the way a reinstall picks up a new command
// line.
func connectServiceEnable(unit, wantPath string) ([]byte, error) {
	if out, err := runSystemctl("daemon-reload"); err != nil {
		return out, err
	}
	// Before anything is started: the unit the manager found has to be the
	// unit that was just written. A user manager computes its own search
	// path from its own environment, so a custom XDG_CONFIG_HOME set in
	// this shell alone puts the file somewhere it will never look — and
	// `enable` would go on to succeed against some older unit, or fail
	// with a message about a unit that does exist.
	if out, err := connectServiceCheckFragment(unit, wantPath); err != nil {
		return out, err
	}
	if out, err := runSystemctl("enable", unit); err != nil {
		return out, err
	}
	// A unit sitting in start-limit-hit refuses to restart until its window
	// expires — "start of the service was attempted too often" — so a
	// reinstall made right after fixing whatever stopped it would do
	// nothing for five minutes. Clearing the counter is what makes
	// installing again the way to recover, which is what install says it
	// is. Best effort: a unit that was never failed has nothing to clear
	// and systemctl says so.
	_, _ = runSystemctl("reset-failed", unit)
	return runSystemctl("restart", unit)
}

// connectServiceCheckFragment asks systemd which file it reads for this
// unit, and refuses when that is not the file just written.
func connectServiceCheckFragment(unit, wantPath string) ([]byte, error) {
	out, err := runSystemctl("show", "-p", "FragmentPath", "--value", unit)
	if err != nil {
		return out, err
	}
	got := strings.TrimSpace(string(out))
	if got == wantPath {
		return nil, nil
	}
	where := "nowhere: the user manager does not see a unit by that name"
	if got != "" {
		where = got
	}
	return nil, fmt.Errorf("systemd reads %s for %s, not the file just written; "+
		"the user manager's unit search path is computed from its own environment, not from this shell's XDG_CONFIG_HOME", where, unit)
}

// connectServiceLingerWarning reports when the user manager will not be
// running after a reboot, so the unit's own "Restart" promise stops at the
// next power cut.
func connectServiceLingerWarning(ctx context.Context) string {
	// The process's own user, not $USER: exec runs no shell, so an unset
	// USER would be passed to loginctl as the four characters "$USER" and
	// the lookup would fail silently — no warning, on exactly the machines
	// most likely to need one. USER can also name a different account than
	// the one whose manager this unit is being installed into.
	me, err := user.Current()
	if err != nil {
		return ""
	}
	name := me.Username
	// Bounded: loginctl talks to logind, and an install must not hang on a
	// warning it can do without.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "loginctl", "show-user", name, "-p", "Linger", "--value").Output() //nolint:gosec // fixed argv, the name is this process's own user
	if err != nil {
		return ""
	}
	if strings.TrimSpace(string(out)) == "yes" {
		return ""
	}
	return "This user's systemd manager starts at login, so the service will not come back after a reboot until someone logs in. " +
		"To have it start at boot: sudo loginctl enable-linger " + richtext.ShellQuote(name)
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

	// A missing file does not mean a stopped connector: systemd goes on
	// running a unit it has already loaded after its file is removed by
	// hand. Returning "nothing to remove" here would report the opposite of
	// what uninstall promises, so the stop is attempted either way and only
	// the wording changes.
	_, statErr := os.Stat(path)
	hadFile := statErr == nil

	// Stopping is best effort: the unit file must go even when there is no
	// session bus to talk to, or an uninstall on a machine without a user
	// session would leave the unit behind for the next login to start.
	// Best effort is not the same as unreported, though — a stop that
	// failed may have left the connector running, and saying "Stopped" then
	// would be the summary telling someone the opposite of what happened.
	stopOut, stopErr := runSystemctl("disable", "--now", name)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot remove %s: %w", richtext.SanitizeSingleLine(path), err)
	}
	_, _ = runSystemctl("daemon-reload")

	summary := fmt.Sprintf("Stopped %s and removed %s", name, path)
	if !hadFile {
		summary = fmt.Sprintf("No unit file at %s; stopped %s in case it was still loaded", path, name)
	}
	stopped := true
	// A unit systemd has never heard of is the idempotent case, not a
	// failure: uninstalling twice must succeed.
	if stopErr != nil && !connectServiceUnknownUnit(stopOut) {
		stopped = false
		why := strings.TrimSpace(string(stopOut))
		if why == "" {
			why = stopErr.Error()
		}
		summary = fmt.Sprintf("Removed %s, but could not stop %s, which may still be running: %s",
			path, name, richtext.SanitizeSingleLine(why))
	}
	return app.OK(map[string]any{"unit": name, "path": path, "removed": hadFile, "stopped": stopped},
		output.WithSummary(summary))
}

// connectServiceUnknownUnit reports whether systemctl refused because it has
// never heard of the unit, which is what uninstalling an uninstalled service
// looks like and is the one refusal that counts as success.
//
// "no such file or directory" is deliberately not among these. systemctl
// says it for a missing unit and also for "Failed to connect to bus: No
// such file or directory", which is a different thing entirely: the bus is
// gone, nothing was stopped, and the connector may still be running.
// Treating that as an uninstalled service would report the connector
// stopped when it is not.
func connectServiceUnknownUnit(out []byte) bool {
	text := strings.ToLower(string(out))
	if strings.Contains(text, "failed to connect to bus") || strings.Contains(text, "connection refused") {
		return false
	}
	return strings.Contains(text, "not loaded") ||
		strings.Contains(text, "does not exist")
}

// writeFileAtomic writes data to path through a temporary file in the same
// directory, so a reader never sees a partial file and a failed write leaves
// whatever was there before.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
