package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/version"
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
	_ = app // used for output format consistency

	current := version.Version
	if current == "dev" {
		fmt.Println("Development build — upgrade not applicable (build from source)")
		return nil
	}

	fmt.Printf("Current version: %s\n", current)
	fmt.Print("Checking for updates… ")

	latest, err := fetchLatestVersion()
	if err != nil {
		fmt.Println("failed")
		return fmt.Errorf("could not check for updates: %w", err)
	}

	if latest == current {
		fmt.Println("already up to date")
		return nil
	}

	fmt.Printf("update available: %s\n", latest)

	ctx := cmd.Context()
	if isHomebrew(ctx) {
		fmt.Println("Upgrading via Homebrew…")
		upgrade := exec.CommandContext(ctx, "brew", "upgrade", "basecamp")
		upgrade.Stdout = os.Stdout
		upgrade.Stderr = os.Stderr
		if err := upgrade.Run(); err != nil {
			return fmt.Errorf("brew upgrade failed: %w", err)
		}
		fmt.Println("Upgrade complete!")
		return nil
	}

	fmt.Println()
	fmt.Printf("Download the latest release from:\n")
	fmt.Printf("  https://github.com/basecamp/basecamp-cli/releases/tag/v%s\n", latest)
	return nil
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
