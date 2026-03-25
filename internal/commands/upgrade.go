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
	executablePathResolver  = resolvedExecutablePath
	scoopPrefixChecker      = hasScoopPrefix
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

	latest, err := versionChecker()
	if err != nil {
		fmt.Fprintln(w, "failed")
		return fmt.Errorf("could not check for updates: %w", err)
	}

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
			return fmt.Errorf("brew upgrade failed for cask %s: %w", homebrewCask, err)
		}
		return app.OK(
			map[string]string{"status": "upgraded", "from": current, "to": latest},
			output.WithSummary(fmt.Sprintf("Upgraded %s → %s", current, latest)),
		)
	}

	if scoopChecker(ctx) {
		global := scoopGlobalScopeChecker(ctx)
		fmt.Fprintln(w, "Upgrading via Scoop…")
		if err := scoopUpgrader(ctx, global, w, cmd.ErrOrStderr()); err != nil {
			return fmt.Errorf("scoop update failed for app %s: %w", scoopApp, err)
		}
		return app.OK(
			map[string]string{"status": "upgraded", "from": current, "to": latest},
			output.WithSummary(fmt.Sprintf("Upgraded %s → %s", current, latest)),
		)
	}

	if legacyHomebrewCasker(ctx) {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "The CLI cask has been renamed. To upgrade, run:")
		fmt.Fprintf(w, "  brew uninstall --cask %s\n", legacyHomebrewCask)
		fmt.Fprintf(w, "  brew install --cask %s\n", homebrewCask)
		return app.OK(
			map[string]string{
				"status":      "migration_required",
				"from":        current,
				"to":          latest,
				"legacy_cask": legacyHomebrewCask,
				"replacement": homebrewCask,
			},
			output.WithSummary("Homebrew cask rename detected — manual migration required"),
		)
	}

	if legacyScoopChecker(ctx) {
		global := scoopGlobalScopeChecker(ctx)
		fmt.Fprintln(w)
		fmt.Fprintln(w, "The CLI Scoop manifest has been renamed. To upgrade, run:")
		fmt.Fprintf(w, "  scoop uninstall%s %s\n", scoopGlobalFlag(global), legacyScoopApp)
		fmt.Fprintf(w, "  scoop install%s %s\n", scoopGlobalFlag(global), scoopApp)
		return app.OK(
			map[string]string{
				"status":          "migration_required",
				"from":            current,
				"to":              latest,
				"legacy_manifest": legacyScoopApp,
				"replacement":     scoopApp,
			},
			output.WithSummary("Scoop manifest rename detected — manual migration required"),
		)
	}

	downloadURL := fmt.Sprintf("https://github.com/basecamp/basecamp-cli/releases/tag/v%s", latest)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Download the latest release from:\n")
	fmt.Fprintf(w, "  %s\n", downloadURL)
	return app.OK(
		map[string]string{"status": "update_available", "from": current, "to": latest, "download_url": downloadURL},
		output.WithSummary(fmt.Sprintf("Update available: %s → %s", current, latest)),
	)
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
	case isScoopShimExecutable(exe) && scoopPrefixChecker(ctx, scoopApp):
		return scoopInstallSourceRenamed
	case isScoopShimExecutable(exe) && scoopPrefixChecker(ctx, legacyScoopApp):
		return scoopInstallSourceLegacy
	default:
		return scoopInstallSourceUnknown
	}
}

func isScoopShimExecutable(exe string) bool {
	if !strings.Contains(exe, scoopShimPath) {
		return false
	}

	name := strings.TrimSuffix(filepath.Base(exe), filepath.Ext(exe))
	return name == scoopCommandBaseName
}

func hasScoopPrefix(ctx context.Context, app string) bool {
	switch app {
	case scoopApp, legacyScoopApp:
		// allowed
	default:
		return false
	}

	return exec.CommandContext(ctx, "scoop", "prefix", app).Run() == nil //nolint:gosec // G204: app is validated against known constants above
}

func isGlobalScoopInstall(_ context.Context) bool {
	exe, ok := executablePathResolver()
	if !ok {
		return false
	}

	return strings.Contains(exe, globalScoopRootPath)
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
