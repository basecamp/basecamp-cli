package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/harness"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

const (
	codexMarketplaceTimeout = 20 * time.Second
	codexInstallTimeout     = 20 * time.Second
	codexVerifyTimeout      = 5 * time.Second
)

// runCodexSetupCommand runs a codex subcommand, capturing stdout for --json
// parsing while streaming stderr (progress, prompts, errors) to the given
// writer. Captured stderr is returned separately so structured stdout parsing
// is never defeated by stderr noise, while failure sniffing still sees
// everything the command said.
var runCodexSetupCommand = func(ctx context.Context, stderr io.Writer, path string, args ...string) (stdoutOutput, stderrOutput []byte, err error) {
	command := exec.CommandContext(ctx, path, args...) //nolint:gosec // path comes from FindCodexBinary
	var stdout, captured bytes.Buffer
	command.Stdout = &stdout
	if stderr != nil {
		command.Stderr = io.MultiWriter(stderr, &captured)
	} else {
		command.Stderr = &captured
	}
	err = command.Run()
	return stdout.Bytes(), captured.Bytes(), err
}

// runCodexSetup performs the Codex setup steps for the interactive wizard.
// Failures warn and continue — Codex setup never aborts `basecamp setup`.
func runCodexSetup(cmd *cobra.Command, styles *tui.Styles) error {
	w := cmd.OutOrStdout()
	progress := func(message string) {
		fmt.Fprintln(w, styles.Muted.Render("  "+message))
	}

	if err := installCodexPlugin(cmd.Context(), cmd.ErrOrStderr(), progress); err != nil {
		fmt.Fprintln(w, styles.Warning.Render("  Codex plugin setup failed: "+err.Error()))
		var setupErr *agentSetupError
		if errors.As(err, &setupErr) && len(setupErr.Manual) > 0 {
			fmt.Fprintln(w, styles.Muted.Render("  Try manually:"))
			for _, manual := range setupErr.Manual {
				fmt.Fprintln(w, styles.Bold.Render("    "+manual))
			}
		}
		fmt.Fprintln(w, styles.Muted.Render("  Then verify with: basecamp doctor"))
		return nil
	}

	fmt.Fprintln(w, styles.RenderStatus(true, "37signals marketplace ready"))
	fmt.Fprintln(w, styles.RenderStatus(true, "Codex plugin installed and enabled"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, styles.Muted.Render("  Start a new Codex thread to load the Basecamp skills."))
	return nil
}

func runCodexSetupNonInteractive(cmd *cobra.Command) error {
	return installCodexPlugin(cmd.Context(), cmd.ErrOrStderr(), func(string) {})
}

func installCodexPlugin(parent context.Context, stderr io.Writer, progress func(string)) error {
	codexPath := harness.FindCodexBinary()
	if codexPath == "" {
		// No Codex subcommands here — they would be impossible to run.
		return &agentSetupError{
			Summary: "Codex executable not found — install Codex, then re-run setup",
			Manual:  []string{"basecamp setup codex"},
		}
	}

	progress("Registering 37signals marketplace…")
	stdout, stderrOutput, err := runCodexStep(parent, stderr, codexMarketplaceTimeout, codexPath,
		"plugin", "marketplace", "add", harness.CodexMarketplaceSource, "--json")
	alreadyAdded := codexMarketplaceAlreadyAdded(stdout, stderrOutput)
	if err != nil && !alreadyAdded {
		return codexSetupError("marketplace add failed: " + codexCommandFailure(stdout, stderrOutput, err))
	}
	if alreadyAdded {
		progress("Refreshing 37signals marketplace…")
		upgradeStdout, upgradeStderr, upgradeErr := runCodexStep(parent, stderr, codexMarketplaceTimeout, codexPath,
			"plugin", "marketplace", "upgrade", harness.CodexMarketplaceName, "--json")
		if upgradeErr != nil {
			return codexSetupError("marketplace upgrade failed: " + codexCommandFailure(upgradeStdout, upgradeStderr, upgradeErr))
		}
	}

	progress("Installing basecamp plugin…")
	stdout, stderrOutput, err = runCodexStep(parent, stderr, codexInstallTimeout, codexPath,
		"plugin", "add", harness.CodexExpectedPluginKey, "--json")
	if err != nil && !codexPluginAlreadyInstalled(stdout, stderrOutput) {
		return codexSetupError("plugin add failed: " + codexCommandFailure(stdout, stderrOutput, err))
	}

	progress("Verifying installation…")
	verifyCtx, cancel := context.WithTimeout(parent, codexVerifyTimeout)
	defer cancel()
	check := harness.CheckCodexPluginContext(verifyCtx)
	if check.Status != "pass" {
		detail := check.Message
		if check.Hint != "" {
			detail += "; " + check.Hint
		}
		return codexSetupError("plugin verification failed: " + detail)
	}
	return nil
}

func runCodexStep(parent context.Context, stderr io.Writer, timeout time.Duration, path string, args ...string) (stdoutOutput, stderrOutput []byte, err error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	return runCodexSetupCommand(ctx, stderr, path, args...)
}

func codexMarketplaceAlreadyAdded(stdout, stderr []byte) bool {
	var result struct {
		AlreadyAdded bool `json:"alreadyAdded"`
	}
	if json.Unmarshal(stdout, &result) == nil && result.AlreadyAdded {
		return true
	}
	message := strings.ToLower(string(stdout) + " " + string(stderr))
	return strings.Contains(message, "already") &&
		(strings.Contains(message, "registered") || strings.Contains(message, "configured") || strings.Contains(message, "exists"))
}

func codexPluginAlreadyInstalled(stdout, stderr []byte) bool {
	message := strings.ToLower(string(stdout) + " " + string(stderr))
	return strings.Contains(message, "already") && strings.Contains(message, "installed")
}

func codexCommandFailure(stdout, stderr []byte, err error) string {
	message := strings.TrimSpace(strings.TrimSpace(string(stdout)) + "\n" + strings.TrimSpace(string(stderr)))
	if len(message) > 500 {
		message = message[:500]
	}
	if message == "" {
		return err.Error()
	}
	return message
}

func codexSetupError(summary string) *agentSetupError {
	return &agentSetupError{
		Summary: summary,
		Manual: []string{
			fmt.Sprintf("codex plugin marketplace add %s", harness.CodexMarketplaceSource),
			fmt.Sprintf("codex plugin marketplace upgrade %s", harness.CodexMarketplaceName),
			fmt.Sprintf("codex plugin add %s", harness.CodexExpectedPluginKey),
		},
	}
}
