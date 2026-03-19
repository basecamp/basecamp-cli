package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/harness"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// agentSetupHandler describes what a single agent's setup step does and how to run it.
type agentSetupHandler struct {
	Labels            []string                                           // what this will do
	Confirm           string                                             // confirmation prompt
	Run               func(cmd *cobra.Command, styles *tui.Styles) error // interactive setup
	RunNonInteractive func(cmd *cobra.Command) error                     // non-interactive setup
}

// agentSetupHandlers maps agent ID → setup handler.
var agentSetupHandlers = map[string]agentSetupHandler{
	"claude": {
		Labels: []string{
			"Add basecamp/claude-plugins marketplace to Claude Code",
			"Install the basecamp plugin for Claude Code",
		},
		Confirm:           "Set up Basecamp for your coding agents?",
		Run:               runClaudeSetup,
		RunNonInteractive: runClaudeSetupNonInteractive,
	},
}

// runClaudeSetup performs the Claude Code-specific setup steps
// (marketplace add + plugin install + skill symlink).
func runClaudeSetup(cmd *cobra.Command, styles *tui.Styles) error {
	w := cmd.OutOrStdout()

	// Clean up stale plugin entries from old marketplaces before checking status.
	var reinstallScopes []string
	if stalePlugins := harness.StalePluginKeys(); len(stalePlugins) > 0 {
		if claudePath := harness.FindClaudeBinary(); claudePath != "" {
			removed, scopes := removeStaleClaudePlugins(cmd.Context(), claudePath, stalePlugins)
			reinstallScopes = scopes
			for _, key := range removed {
				fmt.Fprintln(w, styles.RenderStatus(true, fmt.Sprintf("Removed stale plugin %s", key)))
			}
		}
	}

	// Reinstall at scopes removed from stale entries (preserves project/local installs).
	if len(reinstallScopes) > 0 {
		if claudePath := harness.FindClaudeBinary(); claudePath != "" {
			ctx := cmd.Context()
			mktCmd := exec.CommandContext(ctx, claudePath, "plugin", "marketplace", "add", harness.ClaudeMarketplaceSource) //nolint:gosec // G204: claudePath from FindClaudeBinary
			mktCmd.Stdout = w
			mktCmd.Stderr = cmd.ErrOrStderr()
			_ = mktCmd.Run()

			var scopeErrors []string
			for _, scope := range reinstallScopes {
				args := []string{"plugin", "install", harness.ClaudeExpectedPluginKey, "--scope", scope}
				installCmd := exec.CommandContext(ctx, claudePath, args...) //nolint:gosec // G204: claudePath from FindClaudeBinary
				installCmd.Stdout = w
				installCmd.Stderr = cmd.ErrOrStderr()
				if err := installCmd.Run(); err != nil {
					scopeErrors = append(scopeErrors, scope)
				}
			}
			if len(scopeErrors) > 0 {
				fmt.Fprintln(w, styles.Warning.Render(fmt.Sprintf("  Plugin reinstall failed for scope(s): %s", strings.Join(scopeErrors, ", "))))
			}
		}
	}

	// If the plugin is already installed correctly (or was just reinstalled), skip to skill link repair
	pluginOK := harness.CheckClaudePlugin().Status == "pass"
	if pluginOK {
		fmt.Fprintln(w, styles.RenderStatus(true, "Claude Code plugin installed"))
	} else {
		claudePath := harness.FindClaudeBinary()
		if claudePath == "" {
			fmt.Fprintln(w, styles.Muted.Render("  Claude Code detected but binary not found in PATH."))
			fmt.Fprintln(w, styles.Muted.Render("  Install the plugin manually:"))
			line1, line2 := claudeManualInstallHint(styles)
			fmt.Fprintln(w, line1)
			fmt.Fprintln(w, line2)
		} else {
			ctx := cmd.Context()

			// Register the marketplace (best-effort — may already be registered)
			marketplaceCmd := exec.CommandContext(ctx, claudePath, "plugin", "marketplace", "add", harness.ClaudeMarketplaceSource) //nolint:gosec // G204: claudePath from exec.LookPath
			marketplaceCmd.Stdout = w
			marketplaceCmd.Stderr = cmd.ErrOrStderr()
			if err := marketplaceCmd.Run(); err != nil {
				fmt.Fprintln(w, styles.Warning.Render(fmt.Sprintf("  Marketplace registration failed: %s", err)))
			} else {
				fmt.Fprintln(w, styles.RenderStatus(true, "Marketplace registered"))
			}

			// Install the plugin
			installCmd := exec.CommandContext(ctx, claudePath, "plugin", "install", harness.ClaudeExpectedPluginKey) //nolint:gosec // G204: claudePath from exec.LookPath
			installCmd.Stdout = w
			installCmd.Stderr = cmd.ErrOrStderr()
			if err := installCmd.Run(); err != nil {
				fmt.Fprintln(w, styles.Warning.Render(fmt.Sprintf("  Plugin install failed: %s", err)))
				fmt.Fprintln(w, styles.Muted.Render("  Try manually:"))
				line1, line2 := claudeManualInstallHint(styles)
				fmt.Fprintln(w, line1)
				fmt.Fprintln(w, line2)
			} else {
				verify := harness.CheckClaudePlugin()
				if verify.Status == "pass" {
					fmt.Fprintln(w, styles.RenderStatus(true, "Claude Code plugin installed"))
				} else {
					fmt.Fprintln(w, styles.RenderStatus(false, "Claude Code plugin may not have installed correctly"))
					fmt.Fprintln(w, styles.Muted.Render("  Run: basecamp doctor"))
				}
			}
		}
	}

	// Always attempt skill link repair (handles "plugin ok, link broken" case)
	if _, _, err := linkSkillToClaude(); err != nil {
		fmt.Fprintln(w, styles.Warning.Render(fmt.Sprintf("  Claude skill symlink failed: %s", err)))
	}

	return nil
}

// wizardAgents offers to set up detected coding agents.
// Replaces the old wizardClaude() — works for any registered agent.
func wizardAgents(cmd *cobra.Command, styles *tui.Styles) error {
	agents := harness.DetectedAgents()
	if len(agents) == 0 {
		return nil
	}

	w := cmd.OutOrStdout()

	// Check if all detected agents are already fully set up
	// (agent checks pass AND baseline skill is installed)
	allGood := baselineSkillInstalled() && len(harness.StalePluginKeys()) == 0
	if allGood {
		for _, a := range agents {
			if a.Checks == nil {
				continue
			}
			for _, c := range a.Checks() {
				if c.Status != "pass" {
					allGood = false
					break
				}
			}
			if !allGood {
				break
			}
		}
	}

	if allGood {
		for _, a := range agents {
			fmt.Fprintln(w, styles.RenderStatus(true, a.Name+" plugin installed"))
		}
		fmt.Fprintln(w)
		return nil
	}

	fmt.Fprintln(w, styles.Heading.Render("  Step 5: Coding Agent Setup"))
	fmt.Fprintln(w)

	// Show detected agents
	var names []string
	for _, a := range agents {
		names = append(names, a.Name)
	}
	fmt.Fprintln(w, styles.Body.Render(fmt.Sprintf("  Detected: %s", joinNames(names))))
	fmt.Fprintln(w)

	// Build numbered list of what will happen
	fmt.Fprintln(w, styles.Body.Render("  This will:"))
	step := 1
	fmt.Fprintln(w, styles.Muted.Render(fmt.Sprintf("    %d. Install Basecamp agent skill to ~/.agents/skills/basecamp/", step)))
	step++
	for _, a := range agents {
		handler, ok := agentSetupHandlers[a.ID]
		if !ok {
			continue
		}
		for _, label := range handler.Labels {
			fmt.Fprintln(w, styles.Muted.Render(fmt.Sprintf("    %d. %s", step, label)))
			step++
		}
	}
	fmt.Fprintln(w)

	install, confirmErr := tui.Confirm("  Set up Basecamp for your coding agents?", true)
	if confirmErr != nil || !install {
		fmt.Fprintln(w)
		fmt.Fprintln(w, styles.Muted.Render("  You can set up agents later:"))
		for _, a := range agents {
			if _, ok := agentSetupHandlers[a.ID]; ok {
				fmt.Fprintln(w, styles.Bold.Render(fmt.Sprintf("    basecamp setup %s", a.ID)))
			}
		}
		fmt.Fprintln(w)
		return nil //nolint:nilerr // Treat confirm error as skip (user canceled)
	}

	fmt.Fprintln(w)

	// Install baseline skill (always, for any agent)
	if _, err := installSkillFiles(); err != nil {
		fmt.Fprintln(w, styles.Warning.Render(fmt.Sprintf("  Skill install failed: %s", err)))
	} else {
		fmt.Fprintln(w, styles.RenderStatus(true, "Agent skill installed"))
	}

	// Run each detected agent's handler
	for _, a := range agents {
		handler, ok := agentSetupHandlers[a.ID]
		if !ok {
			continue
		}
		if err := handler.Run(cmd, styles); err != nil {
			return err
		}
	}

	fmt.Fprintln(w)
	return nil
}

// runClaudeSetupNonInteractive attempts plugin install without prompts (for --json/--agent mode).
func runClaudeSetupNonInteractive(cmd *cobra.Command) error {
	var errs []string

	// Clean up stale plugin entries from old marketplaces before checking status.
	var reinstallScopes []string
	if stalePlugins := harness.StalePluginKeys(); len(stalePlugins) > 0 {
		if claudePath := harness.FindClaudeBinary(); claudePath != "" {
			_, reinstallScopes = removeStaleClaudePlugins(cmd.Context(), claudePath, stalePlugins)
		}
	}

	// Reinstall at scopes removed from stale entries (preserves project/local installs).
	if len(reinstallScopes) > 0 {
		if claudePath := harness.FindClaudeBinary(); claudePath != "" {
			ctx := cmd.Context()
			w := cmd.ErrOrStderr()
			mktCmd := exec.CommandContext(ctx, claudePath, "plugin", "marketplace", "add", harness.ClaudeMarketplaceSource) //nolint:gosec // G204: claudePath from FindClaudeBinary
			mktCmd.Stderr = w
			_ = mktCmd.Run()

			for _, scope := range reinstallScopes {
				args := []string{"plugin", "install", harness.ClaudeExpectedPluginKey, "--scope", scope}
				installCmd := exec.CommandContext(ctx, claudePath, args...) //nolint:gosec // G204: claudePath from FindClaudeBinary
				installCmd.Stderr = w
				if err := installCmd.Run(); err != nil {
					errs = append(errs, fmt.Sprintf("plugin install (scope %s): %s", scope, err))
				}
			}
		}
	}

	// If the plugin is still not installed, do a fresh default install.
	if check := harness.CheckClaudePlugin(); check.Status != "pass" {
		claudePath := harness.FindClaudeBinary()
		if claudePath == "" {
			// Can't install without binary — not an error, just nothing to do
		} else {
			ctx := cmd.Context()
			w := cmd.ErrOrStderr()

			// Best-effort marketplace registration
			marketplaceCmd := exec.CommandContext(ctx, claudePath, "plugin", "marketplace", "add", harness.ClaudeMarketplaceSource) //nolint:gosec // G204: claudePath from exec.LookPath
			marketplaceCmd.Stderr = w
			_ = marketplaceCmd.Run()

			// Install the plugin
			installCmd := exec.CommandContext(ctx, claudePath, "plugin", "install", harness.ClaudeExpectedPluginKey) //nolint:gosec // G204: claudePath from exec.LookPath
			installCmd.Stderr = w
			if err := installCmd.Run(); err != nil {
				errs = append(errs, fmt.Sprintf("plugin install: %s", err))
			}
		}
	}

	// Always attempt skill link repair
	if _, _, err := linkSkillToClaude(); err != nil {
		errs = append(errs, fmt.Sprintf("skill link: %s", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// removeStaleClaudePlugins uninstalls plugin entries from old/dead marketplaces.
// When scope information is available, each scope is uninstalled explicitly.
// Otherwise, we retry uninstall until it fails (entry gone) or a safety cap of
// 10 iterations is reached.
func removeStaleClaudePlugins(ctx context.Context, claudePath string, plugins []harness.StalePlugin) ([]string, []string) {
	var removed []string
	scopeSeen := map[string]bool{}
	var scopes []string
	for _, p := range plugins {
		if len(p.Scopes) > 0 {
			anyRemoved := false
			for _, scope := range p.Scopes {
				c := exec.CommandContext(ctx, claudePath, "plugin", "uninstall", p.Key, "--scope", scope) //nolint:gosec // G204: claudePath from FindClaudeBinary
				if err := c.Run(); err == nil {
					anyRemoved = true
					if !scopeSeen[scope] {
						scopeSeen[scope] = true
						scopes = append(scopes, scope)
					}
				}
			}
			if anyRemoved {
				removed = append(removed, p.Key)
			}
		} else {
			n := 0
			for i := 0; i < 10; i++ {
				c := exec.CommandContext(ctx, claudePath, "plugin", "uninstall", p.Key) //nolint:gosec // G204: claudePath from FindClaudeBinary
				if err := c.Run(); err != nil {
					break
				}
				n++
			}
			if n > 0 {
				removed = append(removed, p.Key)
			}
		}
	}
	return removed, scopes
}

// claudeManualInstallHint returns the two-line manual install instructions.
func claudeManualInstallHint(styles *tui.Styles) (string, string) {
	return styles.Bold.Render(fmt.Sprintf("    claude plugin marketplace add %s", harness.ClaudeMarketplaceSource)),
		styles.Bold.Render(fmt.Sprintf("    claude plugin install %s", harness.ClaudeExpectedPluginKey))
}

// newSetupAgentCmds generates `setup <agent>` subcommands from the registry.
func newSetupAgentCmds() []*cobra.Command {
	var cmds []*cobra.Command
	for _, a := range harness.AllAgents() {
		agent := a // capture for closure
		handler, ok := agentSetupHandlers[agent.ID]
		if !ok {
			continue
		}
		h := handler // capture
		cmds = append(cmds, &cobra.Command{
			Use:   agent.ID,
			Short: fmt.Sprintf("Install the Basecamp plugin for %s", agent.Name),
			Long:  fmt.Sprintf("Set up the %s integration so %s can access Basecamp.", agent.Name, agent.Name),
			RunE: func(cmd *cobra.Command, args []string) error {
				app := appctx.FromContext(cmd.Context())
				if app == nil {
					return fmt.Errorf("app not initialized")
				}

				// Always install baseline skill (interactive and non-interactive)
				_, skillErr := installSkillFiles()

				var setupErrors []string
				if skillErr != nil {
					setupErrors = append(setupErrors, fmt.Sprintf("skill install: %s", skillErr))
				}

				if !app.IsInteractive() {
					if h.RunNonInteractive != nil {
						if err := h.RunNonInteractive(cmd); err != nil {
							setupErrors = append(setupErrors, err.Error())
						}
					}
				} else {
					styles := tui.NewStylesWithTheme(tui.ResolveTheme(tui.DetectDark()))
					w := cmd.OutOrStdout()

					if skillErr != nil {
						fmt.Fprintln(w, styles.Warning.Render(fmt.Sprintf("  Skill install failed: %s", skillErr)))
					} else {
						fmt.Fprintln(w, styles.RenderStatus(true, "Agent skill installed"))
					}

					if err := h.Run(cmd, styles); err != nil {
						return err
					}

					fmt.Fprintln(w, styles.Muted.Render("  Start a new "+agent.Name+" session to use Basecamp commands."))
				}

				// Build structured result (re-check after potential install)
				detected := agent.Detect != nil && agent.Detect()
				installed := false
				if detected && agent.Checks != nil {
					checks := agent.Checks()
					installed = len(checks) > 0
					for _, c := range checks {
						if c.Status != "pass" {
							installed = false
							break
						}
					}
				}

				summary := agent.Name + " plugin installed"
				if !detected {
					summary = agent.Name + " not detected"
				} else if !installed {
					summary = agent.Name + " plugin not installed"
				}

				result := map[string]any{
					"plugin_installed": installed,
					"agent_detected":   detected,
				}
				if len(setupErrors) > 0 {
					result["errors"] = setupErrors
					// If setup had errors, don't claim installed even if checks pass
					if installed {
						result["plugin_installed"] = false
						summary = agent.Name + " plugin not installed"
					}
				}

				return app.OK(result,
					output.WithSummary(summary),
					output.WithBreadcrumbs(
						output.Breadcrumb{Action: "doctor", Cmd: "basecamp doctor", Description: "Check CLI health"},
					),
				)
			},
		})
	}
	return cmds
}

// baselineSkillInstalled returns true if ~/.agents/skills/basecamp/SKILL.md exists.
func baselineSkillInstalled() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(home, ".agents", "skills", "basecamp", "SKILL.md"))
	return err == nil
}

// joinNames joins names with commas and "and".
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1] //nolint:gosec // G602: len==2 guaranteed by switch
	default:
		result := ""
		for i, n := range names {
			if i == len(names)-1 {
				result += "and " + n
			} else {
				result += n + ", "
			}
		}
		return result
	}
}
