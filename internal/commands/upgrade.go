package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/version"
)

const (
	homebrewCask               = "basecamp/tap/basecamp-cli"
	legacyHomebrewCask         = "basecamp/tap/basecamp"
	homebrewCaskroomPath       = "/caskroom/basecamp-cli/"
	legacyHomebrewCaskroomPath = "/caskroom/basecamp/"
	scoopApp                   = "basecamp-cli"
	legacyScoopApp             = "basecamp"
	scoopAppPath               = "/scoop/apps/basecamp-cli/"
	legacyScoopAppPath         = "/scoop/apps/basecamp/"
	scoopShimPath              = "/scoop/shims/"
	globalScoopRootPath        = "/programdata/scoop/"
	scoopCommandBaseName       = "basecamp"
)

// versionChecker and package manager helpers abstract external checks for testability.
var (
	versionChecker          = fetchLatestVersion
	releaseFetcher          = fetchLatestRelease
	executablePathResolver  = resolvedExecutablePath
	brewPrefixResolver      = resolveBrewPrefix
	scoopPrefixResolver     = resolveScoopPrefix
	homebrewChecker         = isHomebrew
	legacyHomebrewCasker    = hasLegacyHomebrewCask
	homebrewUpgrader        = upgradeHomebrew
	scoopChecker            = isScoop
	legacyScoopChecker      = hasLegacyScoop
	scoopGlobalScopeChecker = isGlobalScoopInstall
	scoopUpgrader           = upgradeScoop
)

// NewUpgradeCmd creates the upgrade command.
func NewUpgradeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade to the latest version",
		Long:  "Check for updates and upgrade the Basecamp CLI to the latest version.",
		RunE:  runUpgrade,
	}
}

func runUpgrade(cmd *cobra.Command, args []string) error {
	app := appctx.FromContext(cmd.Context())

	w := cmd.OutOrStdout()
	if app.IsMachineOutput() {
		w = cmd.ErrOrStderr()
	}

	current := version.Version
	if current == "dev" {
		return app.OK(
			map[string]string{"status": "dev", "version": current},
			output.WithSummary("Development build — upgrade not applicable (build from source)"),
		)
	}

	fmt.Fprintf(w, "Current version: %s\n", current)
	fmt.Fprint(w, "Checking for updates… ")

	release, err := releaseFetcher()
	if err != nil {
		fmt.Fprintln(w, "failed")
		return &output.Error{
			Code:    "upgrade_failed",
			Message: fmt.Sprintf("could not check for updates: %v", err),
			Hint:    "Check network access to api.github.com and retry. In CI, set GITHUB_TOKEN to avoid anonymous rate limits.",
		}
	}
	latest := release.Version

	if !isUpdateAvailable(current, latest) {
		fmt.Fprintln(w, "already up to date")
		return app.OK(
			map[string]string{"status": "up_to_date", "version": current},
			output.WithSummary(fmt.Sprintf("Already up to date (%s)", current)),
		)
	}

	fmt.Fprintf(w, "update available: %s\n", latest)

	ctx := cmd.Context()
	if homebrewChecker(ctx) {
		fmt.Fprintln(w, "Upgrading via Homebrew…")
		if err := homebrewUpgrader(ctx, w, cmd.ErrOrStderr()); err != nil {
			return &output.Error{
				Code:    "upgrade_failed",
				Message: fmt.Sprintf("brew upgrade failed for cask %s: %v", homebrewCask, err),
				Hint:    fmt.Sprintf("Run manually for detail: brew upgrade --cask %s", homebrewCask),
			}
		}
		return confirmManagedUpgrade(ctx, app, "homebrew", homebrewBinaryPath(ctx), current, latest,
			fmt.Sprintf("brew reinstall --cask %s", homebrewCask))
	}

	if scoopChecker(ctx) {
		global := scoopGlobalScopeChecker(ctx)
		fmt.Fprintln(w, "Upgrading via Scoop…")
		if err := scoopUpgrader(ctx, global, w, cmd.ErrOrStderr()); err != nil {
			return &output.Error{
				Code:    "upgrade_failed",
				Message: fmt.Sprintf("scoop update failed for app %s: %v", scoopApp, err),
				Hint:    fmt.Sprintf("Run manually for detail: scoop update%s %s", scoopGlobalFlag(global), scoopApp),
			}
		}
		return confirmManagedUpgrade(ctx, app, "scoop", scoopBinaryPath(ctx), current, latest,
			fmt.Sprintf("scoop uninstall%s %s && scoop install%s %s", scoopGlobalFlag(global), scoopApp, scoopGlobalFlag(global), scoopApp))
	}

	if legacyHomebrewCasker(ctx) {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "The CLI cask has been renamed. To upgrade, run:")
		fmt.Fprintf(w, "  brew uninstall --cask %s\n", legacyHomebrewCask)
		fmt.Fprintf(w, "  brew install --cask %s\n", homebrewCask)
		return &output.Error{
			Code:    "upgrade_required",
			Message: fmt.Sprintf("update available (%s → %s) but the Homebrew cask was renamed — migration required", current, latest),
			Hint:    fmt.Sprintf("Run: brew uninstall --cask %s && brew install --cask %s", legacyHomebrewCask, homebrewCask),
		}
	}

	if legacyScoopChecker(ctx) {
		global := scoopGlobalScopeChecker(ctx)
		fmt.Fprintln(w)
		fmt.Fprintln(w, "The CLI Scoop manifest has been renamed. To upgrade, run:")
		fmt.Fprintf(w, "  scoop uninstall%s %s\n", scoopGlobalFlag(global), legacyScoopApp)
		fmt.Fprintf(w, "  scoop install%s %s\n", scoopGlobalFlag(global), scoopApp)
		return &output.Error{
			Code:    "upgrade_required",
			Message: fmt.Sprintf("update available (%s → %s) but the Scoop manifest was renamed — migration required", current, latest),
			Hint: fmt.Sprintf("Run: scoop uninstall%s %s && scoop install%s %s",
				scoopGlobalFlag(global), legacyScoopApp, scoopGlobalFlag(global), scoopApp),
		}
	}

	// A `go install` build (stable or pseudo version alike) has no release
	// asset lineage to swap in — the module toolchain owns it.
	if goInstallChecker() {
		return &output.Error{
			Code:    "upgrade_required",
			Message: fmt.Sprintf("update available (%s → %s) but this binary was built with go install — upgrade it the same way", current, latest),
			Hint:    "Run: go install github.com/basecamp/basecamp-cli/cmd/basecamp@latest",
		}
	}

	target, err := selfUpdateTargetResolver()
	if err != nil {
		downloadURL := releaseTagURL(latest)
		fmt.Fprintln(w)
		fmt.Fprintf(w, "Download the latest release from:\n  %s\n", downloadURL)
		return &output.Error{
			Code:    "upgrade_required",
			Message: fmt.Sprintf("update available (%s → %s) but the running executable could not be resolved: %v", current, latest, err),
			Hint:    "Download manually: " + downloadURL,
		}
	}

	if reason, hint := selfUpdateIneligibility(target); reason != "" {
		return &output.Error{
			Code:    "upgrade_required",
			Message: fmt.Sprintf("update available (%s → %s) but this install can't be self-updated (%s)", current, latest, reason),
			Hint:    hint,
		}
	}

	// Serialize the mutating phase. The release-metadata check above is
	// read-only and runs unlocked; the lock is taken before any asset
	// download or filesystem mutation so a concurrent upgrade cannot touch
	// the same executable and a concurrent invocation's sidecar cleanup
	// cannot reap this upgrade's live files.
	lock, err := upgradeLocker(target)
	if err != nil {
		return errUpgradeFailedHint(
			fmt.Sprintf("could not begin the upgrade: %v", err),
			"If another basecamp upgrade is running, wait for it to finish and retry.",
		)
	}
	defer func() { _ = lock.Unlock() }()

	return runNativeSelfUpdate(ctx, app.OK, w, target, current, release)
}

// confirmManagedUpgrade verifies a package-manager upgrade actually landed by
// probing the manager-derived entrypoint. No success is reported without a
// confirmed version: a probe that can't run is upgrade_unverified, a probe
// that reports anything but the latest version is upgrade_incomplete.
func confirmManagedUpgrade(ctx context.Context, app *appctx.App, method, probePath, current, latest, reinstallCmd string) error {
	if probePath == "" {
		return &output.Error{
			Code:    "upgrade_unverified",
			Message: fmt.Sprintf("%s reported success but the installed binary could not be located to confirm the version", method),
			Hint:    fmt.Sprintf("Run `basecamp version` to confirm; if it still reports %s, run: %s", current, reinstallCmd),
		}
	}

	reported, err := binaryVersionProber(ctx, probePath)
	if err != nil {
		return &output.Error{
			Code:    "upgrade_unverified",
			Message: fmt.Sprintf("%s reported success but probing %s failed: %v", method, probePath, err),
			Hint:    fmt.Sprintf("Run `basecamp version` to confirm; if it still reports %s, run: %s", current, reinstallCmd),
		}
	}

	// Semantic comparison, accepting reported >= latest: a release published
	// while the manager ran can legitimately install something newer than the
	// snapshot fetched at the start. An unparseable probe result fails safely
	// as unconfirmed rather than pretending to know either way.
	reportedSemver, latestSemver := normalizeSemver(reported), normalizeSemver(latest)
	if !semver.IsValid(reportedSemver) || !semver.IsValid(latestSemver) {
		return &output.Error{
			Code:    "upgrade_unverified",
			Message: fmt.Sprintf("%s reported success but the installed version %q could not be interpreted (expected %s)", method, reported, latest),
			Hint:    fmt.Sprintf("Run `basecamp version` to confirm; if it still reports %s, run: %s", current, reinstallCmd),
		}
	}
	if semver.Compare(reportedSemver, latestSemver) < 0 {
		return &output.Error{
			Code:    "upgrade_incomplete",
			Message: fmt.Sprintf("%s exited successfully but basecamp still reports %s (expected %s, upgrading from %s)", method, reported, latest, current),
			Hint:    "Try: " + reinstallCmd,
		}
	}

	return app.OK(
		map[string]string{"status": "upgraded", "from": current, "to": reported, "method": method},
		output.WithSummary(fmt.Sprintf("Upgraded %s → %s", current, reported)),
	)
}

// homebrewBinaryPath derives the brew-managed entrypoint from `brew --prefix`.
// os.Executable is deliberately not used here: Go documents it may return the
// symlink or the resolved target — possibly stale after the cask swap.
func homebrewBinaryPath(ctx context.Context) string {
	prefix, err := brewPrefixResolver(ctx)
	if err != nil || prefix == "" {
		return ""
	}
	return filepath.Join(prefix, "bin", "basecamp")
}

// scoopBinaryPath derives the scoop-managed entrypoint from `scoop prefix`
// (the loaded module on Windows is the exe, not the shim, and may be stale).
func scoopBinaryPath(ctx context.Context) string {
	prefix, ok := scoopPrefixResolver(ctx, scoopApp)
	if !ok || prefix == "" {
		return ""
	}
	return filepath.Join(filepath.FromSlash(prefix), "basecamp.exe")
}

func resolveBrewPrefix(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "brew", "--prefix").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func upgradeHomebrew(ctx context.Context, stdout io.Writer, stderr io.Writer) error {
	upgrade := exec.CommandContext(ctx, "brew", "upgrade", "--cask", homebrewCask)
	upgrade.Stdout = stdout
	upgrade.Stderr = stderr
	return upgrade.Run()
}

func upgradeScoop(ctx context.Context, global bool, stdout io.Writer, stderr io.Writer) error {
	args := []string{"update"}
	if global {
		args = append(args, "-g")
	}
	args = append(args, scoopApp)

	upgrade := exec.CommandContext(ctx, "scoop", args...)
	upgrade.Stdout = stdout
	upgrade.Stderr = stderr
	return upgrade.Run()
}

// isHomebrew returns true if the running CLI binary appears to come from the renamed Homebrew cask.
func isHomebrew(_ context.Context) bool {
	exe, ok := executablePathResolver()
	if !ok {
		return false
	}

	return strings.Contains(exe, homebrewCaskroomPath)
}

func hasLegacyHomebrewCask(_ context.Context) bool {
	exe, ok := executablePathResolver()
	if !ok {
		return false
	}

	return strings.Contains(exe, legacyHomebrewCaskroomPath)
}

// isScoop returns true if the running CLI binary appears to come from the renamed Scoop app.
func isScoop(ctx context.Context) bool {
	return detectScoopInstallSource(ctx) == scoopInstallSourceRenamed
}

func hasLegacyScoop(ctx context.Context) bool {
	return detectScoopInstallSource(ctx) == scoopInstallSourceLegacy
}

type scoopInstallSource int

const (
	scoopInstallSourceUnknown scoopInstallSource = iota
	scoopInstallSourceRenamed
	scoopInstallSourceLegacy
)

func detectScoopInstallSource(ctx context.Context) scoopInstallSource {
	exe, ok := executablePathResolver()
	if !ok {
		return scoopInstallSourceUnknown
	}

	switch {
	case strings.Contains(exe, scoopAppPath):
		return scoopInstallSourceRenamed
	case strings.Contains(exe, legacyScoopAppPath):
		return scoopInstallSourceLegacy
	case isScoopShimExecutable(exe):
		global := hasGlobalScoopPathPrefix(exe)
		if prefix, ok := scoopPrefixResolver(ctx, scoopApp); ok && scoopPrefixMatchesShimScope(prefix, global) {
			return scoopInstallSourceRenamed
		}
		if prefix, ok := scoopPrefixResolver(ctx, legacyScoopApp); ok && scoopPrefixMatchesShimScope(prefix, global) {
			return scoopInstallSourceLegacy
		}
	}

	return scoopInstallSourceUnknown
}

func isScoopShimExecutable(exe string) bool {
	if !strings.Contains(exe, scoopShimPath) {
		return false
	}

	name := strings.TrimSuffix(filepath.Base(exe), filepath.Ext(exe))
	return name == scoopCommandBaseName
}

// resolveScoopPrefix returns the installed app root reported by `scoop prefix`.
// Scoop already checks local installs first, then global installs, so there is
// no separate scope flag to thread through here.
func resolveScoopPrefix(ctx context.Context, app string) (string, bool) {
	switch app {
	case scoopApp, legacyScoopApp:
		// allowed
	default:
		return "", false
	}

	out, err := exec.CommandContext(ctx, "scoop", "prefix", app).Output() //nolint:gosec // G204: app is validated against known constants above
	if err != nil {
		return "", false
	}

	prefix := strings.ToLower(filepath.ToSlash(strings.TrimSpace(string(out))))
	if prefix == "" {
		return "", false
	}

	return prefix, true
}

func scoopPrefixMatchesShimScope(prefix string, global bool) bool {
	if global {
		return hasGlobalScoopPathPrefix(prefix)
	}

	return !hasGlobalScoopPathPrefix(prefix)
}

func isGlobalScoopInstall(_ context.Context) bool {
	exe, ok := executablePathResolver()
	if !ok {
		return false
	}

	return hasGlobalScoopPathPrefix(exe)
}

func hasGlobalScoopPathPrefix(path string) bool {
	prefix := strings.TrimSuffix(globalScoopRootPath, "/")
	path = stripWindowsVolume(path)
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func stripWindowsVolume(path string) string {
	if len(path) >= 2 && path[1] == ':' {
		return path[2:]
	}

	return path
}

func scoopGlobalFlag(global bool) string {
	if global {
		return " -g"
	}

	return ""
}

func resolvedExecutablePath() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}

	if resolved, resolveErr := filepath.EvalSymlinks(exe); resolveErr == nil {
		exe = resolved
	}

	return strings.ToLower(filepath.ToSlash(exe)), true
}
