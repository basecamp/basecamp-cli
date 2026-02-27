package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/version"
)

// versionChecker and homebrewChecker abstract external checks for testability.
var (
	versionChecker  = fetchLatestVersion
	homebrewChecker = isHomebrew
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

	if latest == current {
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
		upgrade := exec.CommandContext(ctx, "brew", "upgrade", "basecamp")
		upgrade.Stdout = w
		upgrade.Stderr = cmd.ErrOrStderr()
		if err := upgrade.Run(); err != nil {
			return fmt.Errorf("brew upgrade failed: %w", err)
		}
		return app.OK(
			map[string]string{"status": "upgraded", "from": current, "to": latest},
			output.WithSummary(fmt.Sprintf("Upgraded %s → %s", current, latest)),
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

// isHomebrew returns true if the binary appears to be installed via Homebrew.
func isHomebrew(ctx context.Context) bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}

	// Check common Homebrew prefix paths
	if strings.Contains(exe, "/Cellar/") || strings.Contains(exe, "/homebrew/") {
		return true
	}

	// Check if brew knows about us
	out, err := exec.CommandContext(ctx, "brew", "list", "basecamp").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}
