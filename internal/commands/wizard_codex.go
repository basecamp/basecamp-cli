package commands

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/harness"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

var runCodexSetupCommand = func(ctx context.Context, path string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, path, args...).CombinedOutput() //nolint:gosec // path comes from FindCodexBinary
}

func init() {
	agentSetupHandlers["codex"] = agentSetupHandler{
		Labels: []string{
			"Add the 37signals marketplace to Codex",
			"Install the basecamp plugin for Codex",
		},
		Confirm:           "Set up Basecamp for your coding agents?",
		Run:               runCodexSetup,
		RunNonInteractive: runCodexSetupNonInteractive,
	}
}

func runCodexSetup(cmd *cobra.Command, styles *tui.Styles) error {
	if err := installCodexPlugin(cmd.Context()); err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	fmt.Fprintln(w, styles.RenderStatus(true, "37signals marketplace ready"))
	fmt.Fprintln(w, styles.RenderStatus(true, "Codex plugin installed and enabled"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, styles.Muted.Render("  Start a new Codex thread to load the Basecamp skills."))
	fmt.Fprintln(w, styles.Muted.Render("  Review and trust the plugin hooks with /hooks."))
	return nil
}

func runCodexSetupNonInteractive(cmd *cobra.Command) error {
	return installCodexPlugin(cmd.Context())
}

func installCodexPlugin(parent context.Context) error {
	codexPath := harness.FindCodexBinary()
	if codexPath == "" {
		return codexSetupError("Codex executable not found")
	}

	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	output, err := runCodexSetupCommand(ctx, codexPath, "plugin", "marketplace", "add", harness.CodexMarketplaceSource, "--json")
	if err != nil {
		if codexMarketplaceAlreadyAdded(output) {
			upgradeOutput, upgradeErr := runCodexSetupCommand(ctx, codexPath, "plugin", "marketplace", "upgrade", harness.CodexMarketplaceName, "--json")
			if upgradeErr != nil {
				return codexSetupError("marketplace upgrade failed: " + codexCommandFailure(upgradeOutput, upgradeErr))
			}
		} else {
			return codexSetupError("marketplace add failed: " + codexCommandFailure(output, err))
		}
	}

	output, err = runCodexSetupCommand(ctx, codexPath, "plugin", "add", harness.CodexExpectedPluginKey, "--json")
	if err != nil && !codexPluginAlreadyInstalled(output) {
		return codexSetupError("plugin add failed: " + codexCommandFailure(output, err))
	}

	check := harness.CheckCodexPlugin()
	if check.Status != "pass" {
		detail := check.Message
		if check.Hint != "" {
			detail += "; " + check.Hint
		}
		return codexSetupError("plugin verification failed: " + detail)
	}
	return nil
}

func codexMarketplaceAlreadyAdded(output []byte) bool {
	message := strings.ToLower(string(output))
	return strings.Contains(message, "already") &&
		(strings.Contains(message, "registered") || strings.Contains(message, "configured") || strings.Contains(message, "exists"))
}

func codexPluginAlreadyInstalled(output []byte) bool {
	message := strings.ToLower(string(output))
	return strings.Contains(message, "already") && strings.Contains(message, "installed")
}

func codexCommandFailure(output []byte, err error) string {
	message := strings.TrimSpace(string(output))
	if len(message) > 500 {
		message = message[:500]
	}
	if message == "" {
		return err.Error()
	}
	return message
}

func codexSetupError(message string) error {
	return fmt.Errorf("%s\nmanual Codex commands:\n  codex plugin marketplace add %s --json\n  codex plugin marketplace upgrade %s --json\n  codex plugin add %s --json",
		message,
		harness.CodexMarketplaceSource,
		harness.CodexMarketplaceName,
		harness.CodexExpectedPluginKey,
	)
}
