package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
	Run               func(cmd *cobra.Command, styles *tui.Styles) error // interactive setup
	RunNonInteractive func(cmd *cobra.Command) error                     // non-interactive setup
}

// agentSetupError is a setup failure with manual remediation commands the
// user (or agent) can run themselves.
type agentSetupError struct {
	Summary string
	Manual  []string
}

func (e *agentSetupError) Error() string { return e.Summary }

// agentSetupHandlers maps agent ID → setup handler.
var agentSetupHandlers = map[string]agentSetupHandler{
	"claude": {
		Labels: []string{
			"Add basecamp/claude-plugins marketplace to Claude Code",
			"Install the basecamp plugin for Claude Code",
		},
		Run:               runClaudeSetup,
		RunNonInteractive: runClaudeSetupNonInteractive,
	},
	"codex": {
		Labels: []string{
			"Add the 37signals marketplace to Codex",
			"Install the basecamp plugin for Codex",
		},
		Run:               runCodexSetup,
		RunNonInteractive: runCodexSetupNonInteractive,
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

	// Nudge: recommend enabling auto-update (only when plugin is actually installed)
	if harness.CheckClaudePlugin().Status == "pass" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, styles.Muted.Render("  Tip: Enable auto-update to stay current with new CLI releases:"))
		fmt.Fprintln(w, styles.Muted.Render("  "+harness.AutoUpdateHint))
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
			// someScopeInvalid tracks whether any scope failed validPluginScope
			// (i.e. no scoped uninstall could be attempted for it). Those entries
			// are unreachable by scoped uninstalls, so the unscoped fallback must
			// run even when another, valid scope was removed successfully. We must
			// NOT fall back just because a VALID scope's uninstall failed at
			// runtime — that would wrongly strip every-scope install when the
			// targeted ones merely errored.
			someScopeInvalid := false
			for _, scope := range p.Scopes {
				// scope comes from installed_plugins.json (not first-party).
				// Whitelist it so a "-"-leading value can't inject a flag into
				// the uninstall argv.
				if !validPluginScope(scope) {
					someScopeInvalid = true
					continue
				}
				c := exec.CommandContext(ctx, claudePath, "plugin", "uninstall", p.Key, "--scope", scope) //nolint:gosec // G204: claudePath from FindClaudeBinary
				if err := c.Run(); err == nil {
					anyRemoved = true
					if !scopeSeen[scope] {
						scopeSeen[scope] = true
						scopes = append(scopes, scope)
					}
				}
			}
			if someScopeInvalid {
				// At least one scope was invalid, so its entry was never targeted
				// by a scoped uninstall. Fall back to the unscoped retry removal
				// so the plugin isn't silently left installed under that scope.
				if uninstallUnscoped(ctx, claudePath, p.Key) {
					anyRemoved = true
					// The unscoped removal strips EVERY install of the key,
					// including valid-scoped ones whose scoped uninstall failed
					// at runtime (and thus were never recorded above). Record
					// those valid scopes so runClaudeSetup reinstalls them
					// instead of silently dropping the user's install.
					for _, scope := range p.Scopes {
						if validPluginScope(scope) && !scopeSeen[scope] {
							scopeSeen[scope] = true
							scopes = append(scopes, scope)
						}
					}
				}
			}
			if anyRemoved {
				removed = append(removed, p.Key)
			}
		} else if uninstallUnscoped(ctx, claudePath, p.Key) {
			removed = append(removed, p.Key)
		}
	}
	return removed, scopes
}

// uninstallUnscoped removes every installation of key by retrying an unscoped
// `claude plugin uninstall` until it fails (entry gone) or a safety cap of 10
// iterations is reached. Reports whether at least one uninstall succeeded.
func uninstallUnscoped(ctx context.Context, claudePath, key string) bool {
	n := 0
	for i := 0; i < 10; i++ {
		c := exec.CommandContext(ctx, claudePath, "plugin", "uninstall", key) //nolint:gosec // G204: claudePath from FindClaudeBinary
		if err := c.Run(); err != nil {
			break
		}
		n++
	}
	return n > 0
}

// validPluginScope reports whether scope is one of Claude's accepted plugin
// scopes. Used to gate untrusted scope values from installed_plugins.json
// before they reach `claude plugin uninstall --scope <scope>`.
//
// `claude plugin uninstall --scope` only accepts user/project/local, so a
// "global"-scoped entry is invalid here: leaving it valid would make a scoped
// uninstall fail silently while suppressing the unscoped fallback, stranding
// the plugin. Treating "global" as invalid sets someScopeInvalid so the
// unscoped fallback removes it.
func validPluginScope(scope string) bool {
	switch scope {
	case "user", "project", "local":
		return true
	default:
		return false
	}
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
				var manualCommands []string
				if skillErr != nil {
					setupErrors = append(setupErrors, fmt.Sprintf("skill install: %s", skillErr))
				}

				if !app.IsInteractive() {
					if h.RunNonInteractive != nil {
						if err := h.RunNonInteractive(cmd); err != nil {
							setupErrors = append(setupErrors, err.Error())
							var setupErr *agentSetupError
							if errors.As(err, &setupErr) {
								manualCommands = setupErr.Manual
							}
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
				if len(manualCommands) > 0 {
					result["manual_commands"] = manualCommands
				}

				breadcrumbs := []output.Breadcrumb{
					{Action: "doctor", Cmd: "basecamp doctor", Description: "Check CLI health"},
				}
				for i, manual := range manualCommands {
					breadcrumbs = append(breadcrumbs, output.Breadcrumb{
						Action:      fmt.Sprintf("manual_step_%d", i+1),
						Cmd:         manual,
						Description: "Manual setup step",
					})
				}

				return app.OK(result,
					output.WithSummary(summary),
					output.WithBreadcrumbs(breadcrumbs...),
				)
			},
		})
	}
	return cmds
}

// agentSetupEnv selects which coding agents `setup agents` targets.
// Values: claude | codex | all | none. Empty (unset) means auto-detect.
const agentSetupEnv = "BASECAMP_SETUP_AGENT"

// newSetupAgentsCmd builds `setup agents`. It always runs non-interactively:
// it installs the baseline skill, connects agents per the BASECAMP_SETUP_AGENT
// selector (or auto-detection), and emits a structured envelope. It never
// prompts, so it is safe for the piped installer and coding-agent shells.
func newSetupAgentsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "agents",
		Short: "Install the Basecamp skill and connect detected coding agents",
		Long: "Install the baseline Basecamp agent skill and attempt to connect coding agents.\n\n" +
			"Selection is controlled by " + agentSetupEnv + ": claude, codex, all, or none. When\n" +
			"unset, a single detected agent is connected; when several are detected none is\n" +
			"guessed — the per-agent `basecamp setup <id>` commands are surfaced instead.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}
			return runNonInteractiveAgentSetup(cmd, app)
		},
	}
}

// agentSetupRecord is the per-agent outcome captured while running handlers.
// errors/manualCommands are stored bare (not id-prefixed); the top-level union
// prefixes errors with the agent id.
type agentSetupRecord struct {
	id, name        string
	detectedBefore  bool
	detectedAfter   bool
	pluginInstalled bool
	binaryAbsent    bool
	errors          []string
	manualCommands  []string
}

// runNonInteractiveAgentSetup installs the baseline skill, resolves the agent
// selector, runs each targeted handler, and returns a structured envelope where
// every safety-critical outcome lives in a top-level flat field (the styled
// renderer skips nested map/[]map, so the `agents` array is JSON-only detail).
func runNonInteractiveAgentSetup(cmd *cobra.Command, app *appctx.App) error {
	// Baseline skill: installed regardless of selector.
	_, skillErr := installSkillFiles()
	skillInstalled := skillErr == nil

	// Pre-run detection snapshot (set-like → sorted by id).
	detectedBefore := detectedAgentIDs()

	selectorRaw := strings.TrimSpace(os.Getenv(agentSetupEnv))
	selector := strings.ToLower(selectorRaw)

	var warnings []string
	var ambiguous bool
	var ambiguousManual []string
	var targets []harness.AgentInfo

	switch selector {
	case "", "auto":
		selector = "auto"
		detected := harness.DetectedAgents()
		switch len(detected) {
		case 0:
			// baseline skill only
		case 1:
			targets = detected
		default:
			ambiguous = true
			ambiguousManual = agentChoiceCommands(detected)
			warnings = append(warnings, "Multiple coding agents detected; installed the baseline skill only. Choose one: "+strings.Join(ambiguousManual, ", "))
		}
	case "all":
		targets = harness.AllAgents()
	case "none":
		// baseline skill only
	case "claude", "codex":
		if a := harness.FindAgent(selector); a != nil {
			targets = []harness.AgentInfo{*a}
		}
	default:
		selector = "invalid"
		warnings = append(warnings, fmt.Sprintf("Unknown %s value %q; installed the baseline skill only (expected claude, codex, all, or none)", agentSetupEnv, selectorRaw))
	}

	// Run handlers in id order so aggregation is deterministic.
	sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
	records := make([]agentSetupRecord, 0, len(targets))
	for _, agent := range targets {
		records = append(records, runAgentSetupHandler(cmd, agent))
	}

	attempted := make([]string, 0, len(records))
	for _, r := range records {
		attempted = append(attempted, r.id)
	}

	// errors: union of baseline + every per-agent error (id-prefixed), stable
	// first-seen dedup, in sorted-agent order. Never string-sorted.
	errUnion := newOrderedStringSet()
	if skillErr != nil {
		errUnion.add(fmt.Sprintf("skill: %s", skillErr))
	}
	for _, r := range records {
		for _, e := range r.errors {
			errUnion.add(fmt.Sprintf("%s: %s", r.id, e))
		}
	}

	// manual_commands: ambiguous → both `setup <id>`; else union of each
	// handler's own ordered sequence plus a synthesized hint for absent
	// binaries. Stable first-seen dedup preserves each handler's order.
	manualUnion := newOrderedStringSet()
	if ambiguous {
		for _, m := range ambiguousManual {
			manualUnion.add(m)
		}
	} else {
		for _, r := range records {
			for _, m := range r.manualCommands {
				manualUnion.add(m)
			}
			if r.binaryAbsent {
				manualUnion.add("basecamp setup " + r.id)
			}
		}
	}

	// warnings: synthesized missing-binary remediation, sorted-agent order.
	// The Claude handler treats a missing binary as no-op success while Codex
	// returns an error, so synthesizing here keeps remediation symmetric.
	for _, r := range records {
		if r.binaryAbsent {
			warnings = append(warnings, fmt.Sprintf("%s: %s binary not found; install %s, then run: basecamp setup %s", r.id, r.name, r.name, r.id))
		}
	}

	agentsDetail := make([]map[string]any, 0, len(records))
	for _, r := range records {
		agentsDetail = append(agentsDetail, map[string]any{
			"id":               r.id,
			"name":             r.name,
			"detected_before":  r.detectedBefore,
			"detected_after":   r.detectedAfter,
			"plugin_installed": r.pluginInstalled,
			"errors":           orEmptyStrings(r.errors),
			"manual_commands":  orEmptyStrings(r.manualCommands),
		})
	}

	result := map[string]any{
		"skill_installed":  skillInstalled,
		"selector":         selector,
		"ambiguous":        ambiguous,
		"detected_before":  detectedBefore,
		"attempted_agents": attempted,
		"errors":           errUnion.slice(),
		"warnings":         orEmptyStrings(warnings),
		"manual_commands":  manualUnion.slice(),
		"agents":           agentsDetail,
	}

	manual := manualUnion.slice()
	breadcrumbs := make([]output.Breadcrumb, 0, 1+len(manual))
	breadcrumbs = append(breadcrumbs, output.Breadcrumb{Action: "doctor", Cmd: "basecamp doctor", Description: "Check CLI health"})
	for i, m := range manual {
		breadcrumbs = append(breadcrumbs, output.Breadcrumb{
			Action:      fmt.Sprintf("manual_step_%d", i+1),
			Cmd:         m,
			Description: "Manual setup step",
		})
	}

	return app.OK(result,
		output.WithSummary(agentSetupSummary(selector, ambiguous, skillInstalled, records)),
		output.WithBreadcrumbs(breadcrumbs...),
	)
}

// runAgentSetupHandler runs one agent's non-interactive handler and captures
// its before/after detection, plugin health, and remediation.
func runAgentSetupHandler(cmd *cobra.Command, agent harness.AgentInfo) agentSetupRecord {
	rec := agentSetupRecord{
		id:             agent.ID,
		name:           agent.Name,
		detectedBefore: agent.Detect != nil && agent.Detect(),
		binaryAbsent:   !agentBinaryPresent(agent.ID),
	}

	if handler, ok := agentSetupHandlers[agent.ID]; ok && handler.RunNonInteractive != nil {
		if err := handler.RunNonInteractive(cmd); err != nil {
			rec.errors = append(rec.errors, err.Error())
			var setupErr *agentSetupError
			if errors.As(err, &setupErr) {
				rec.manualCommands = append(rec.manualCommands, setupErr.Manual...)
			}
		}
	}

	rec.detectedAfter = agent.Detect != nil && agent.Detect()
	rec.pluginInstalled = agentChecksPass(agent)
	return rec
}

// agentBinaryPresent reports whether the agent's executable is on disk.
// Unknown agents are assumed present so no bogus remediation is synthesized.
func agentBinaryPresent(id string) bool {
	switch id {
	case "claude":
		return harness.FindClaudeBinary() != ""
	case "codex":
		return harness.FindCodexBinary() != ""
	default:
		return true
	}
}

// agentChecksPass reports whether every health check for the agent passes.
func agentChecksPass(agent harness.AgentInfo) bool {
	if agent.Checks == nil {
		return false
	}
	checks := agent.Checks()
	if len(checks) == 0 {
		return false
	}
	for _, c := range checks {
		if c.Status != "pass" {
			return false
		}
	}
	return true
}

// detectedAgentIDs returns the ids of currently detected agents, sorted.
func detectedAgentIDs() []string {
	agents := harness.DetectedAgents()
	ids := make([]string, 0, len(agents))
	for _, a := range agents {
		ids = append(ids, a.ID)
	}
	sort.Strings(ids)
	return ids
}

// agentChoiceCommands returns `basecamp setup <id>` for each agent, sorted by id.
func agentChoiceCommands(agents []harness.AgentInfo) []string {
	ids := make([]string, 0, len(agents))
	for _, a := range agents {
		ids = append(ids, a.ID)
	}
	sort.Strings(ids)
	cmds := make([]string, 0, len(ids))
	for _, id := range ids {
		cmds = append(cmds, "basecamp setup "+id)
	}
	return cmds
}

// agentSetupSummary names the resulting state in one line.
func agentSetupSummary(selector string, ambiguous, skillInstalled bool, records []agentSetupRecord) string {
	switch {
	case !skillInstalled:
		return "Baseline skill installation failed"
	case selector == "invalid":
		return "Unknown " + agentSetupEnv + " value; installed baseline skill only"
	case ambiguous:
		return "Multiple coding agents detected; installed baseline skill only"
	case len(records) == 0:
		return "Installed baseline skill; no coding agents connected"
	}
	names := make([]string, 0, len(records))
	connected := 0
	for _, r := range records {
		names = append(names, r.name)
		if r.pluginInstalled {
			connected++
		}
	}
	if connected == len(records) {
		return "Installed baseline skill; connected " + joinNames(names)
	}
	return "Installed baseline skill; attempted " + joinNames(names)
}

// orderedStringSet accumulates strings with stable first-seen dedup.
type orderedStringSet struct {
	seen  map[string]bool
	items []string
}

func newOrderedStringSet() *orderedStringSet {
	return &orderedStringSet{seen: map[string]bool{}, items: []string{}}
}

func (s *orderedStringSet) add(v string) {
	if !s.seen[v] {
		s.seen[v] = true
		s.items = append(s.items, v)
	}
}

func (s *orderedStringSet) slice() []string { return s.items }

// orEmptyStrings replaces a nil slice with a non-nil empty one so JSON renders
// `[]` rather than `null`.
func orEmptyStrings(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
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
