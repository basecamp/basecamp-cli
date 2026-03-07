package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/harness"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/resolve"
	"github.com/basecamp/basecamp-cli/internal/version"
)

// WizardResult holds the outcome of the first-run wizard.
type WizardResult struct {
	Version     string `json:"version"`
	Status      string `json:"status"` // "complete"
	AccountID   string `json:"account_id,omitempty"`
	AccountName string `json:"account_name,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
	ProjectName string `json:"project_name,omitempty"`
	ConfigScope string `json:"config_scope,omitempty"` // "global", "local", or "" if skipped
}

// NewSetupCmd creates the setup command (explicit wizard invocation).
func NewSetupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Interactive first-time setup",
		Long:  "Walk through authentication, account selection, project configuration, and coding agent integration.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			return runWizard(cmd, app)
		},
	}
	for _, sub := range newSetupAgentCmds() {
		cmd.AddCommand(sub)
	}
	return cmd
}

// runWizard runs the interactive first-run setup wizard.
// It walks the user through authentication, account selection, and project selection.
func runWizard(cmd *cobra.Command, app *appctx.App) error {
	if app == nil {
		return fmt.Errorf("app not initialized")
	}

	styles := tui.NewStylesWithTheme(tui.ResolveTheme(tui.DetectDark()))

	// Step 1: Welcome
	waitAnim := showWelcome(cmd.OutOrStdout(), styles)
	waitAnim()

	// Step 2: Auth
	if err := wizardAuth(cmd, app, styles); err != nil {
		return err
	}

	// Step 3: Account selection
	result := WizardResult{Version: version.Version, Status: "complete"}
	accountID, err := wizardAccount(cmd, app, styles)
	if err != nil {
		return err
	}
	result.AccountID = accountID

	// Fetch account name for display
	result.AccountName = fetchAccountName(cmd, app, accountID)

	// Step 4: Project selection (optional)
	projectID, err := wizardProject(cmd, app, styles)
	if err != nil {
		return err
	}
	result.ProjectID = projectID
	if projectID != "" {
		result.ProjectName = fetchProjectName(cmd, app, projectID)
	}

	// Step 5: Save config
	configScope := wizardSaveConfig(cmd.OutOrStdout(), styles, accountID, projectID)
	result.ConfigScope = configScope

	// Step 6: Coding agent integration
	if err := wizardAgents(cmd, styles); err != nil {
		return err
	}

	// Persist onboarded flag (always global so it applies everywhere)
	if err := resolve.PersistValue("onboarded", "true", "global"); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to persist onboarding flag: %v\n", err)
	}

	// Step 7: Summary with next steps
	// Interactive mode shows the rich checklist directly; non-interactive
	// or machine-output mode delegates to app.OK which renders the structured envelope.
	if app.IsInteractive() && !app.IsMachineOutput() {
		showSuccess(cmd.OutOrStdout(), styles, result)
		return nil
	}

	return app.OK(result,
		output.WithSummary(wizardSummaryLine(result)),
		output.WithBreadcrumbs(wizardBreadcrumbs(result)...),
	)
}

// showWelcome displays the welcome screen with animated logo.
// Returns a wait function that must be called before interactive prompts.
// All output goes to w so the command fully honors its output writer.
func showWelcome(w io.Writer, styles *tui.Styles) func() {
	aw, waitAnim := tui.AnimateWordmarkAsync(w, styles.Theme())
	fmt.Fprintln(aw)
	fmt.Fprintln(aw, styles.Title.Render("Welcome to Basecamp"))
	fmt.Fprintln(aw)
	fmt.Fprintln(aw, styles.Body.Render(fmt.Sprintf("The command-line interface for Basecamp (v%s).", version.Version)))
	fmt.Fprintln(aw, styles.Body.Render("Let's get you set up. This will only take a moment."))
	fmt.Fprintln(aw)
	return waitAnim
}

// wizardAuth handles the authentication flow with scope selection.
func wizardAuth(cmd *cobra.Command, app *appctx.App, styles *tui.Styles) error {
	w := cmd.OutOrStdout()

	if app.Auth.IsAuthenticated() {
		info, err := app.SDK.Authorization().GetInfo(cmd.Context(), nil)
		if err == nil {
			name := fmt.Sprintf("%s %s", info.Identity.FirstName, info.Identity.LastName)
			fmt.Fprintln(w, styles.Success.Render(fmt.Sprintf("  Logged in as %s (%s)", name, info.Identity.EmailAddress)))
			if len(info.Accounts) > 0 {
				var lines string
				for _, acct := range info.Accounts {
					lines += fmt.Sprintf("    • %s\n", acct.Name)
				}
				fmt.Fprint(w, styles.Muted.Render(lines))
			}
		} else {
			fmt.Fprintln(w, styles.Success.Render("  Already authenticated."))
		}
		fmt.Fprintln(w)
		return nil
	}

	fmt.Fprintln(w, styles.Heading.Render("  Step 1: Authentication"))
	fmt.Fprintln(w)

	// Let user choose scope (map to OAuth scopes: "read" or "full")
	scope, err := tui.Select("  What access level do you need?", []tui.SelectOption{
		{Value: "read", Label: "Read-only (recommended for browsing)"},
		{Value: "full", Label: "Full access (read + write)"},
	})
	if err != nil {
		return fmt.Errorf("scope selection canceled: %w", err)
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, styles.Muted.Render("  Opening browser for Basecamp login..."))
	fmt.Fprintln(w)

	if err := app.Auth.Login(cmd.Context(), auth.LoginOptions{
		Scope:  scope,
		Logger: func(msg string) { fmt.Fprintln(w, "  "+msg) },
	}); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	// Try to fetch user profile for a friendly greeting
	resp, err := app.SDK.Get(cmd.Context(), "/my/profile.json")
	if err == nil {
		var profile struct {
			ID    int    `json:"id"`
			Name  string `json:"name"`
			Email string `json:"email_address"`
		}
		if err := resp.UnmarshalData(&profile); err == nil {
			_ = app.Auth.SetUserIdentity(fmt.Sprintf("%d", profile.ID), profile.Email)
			fmt.Fprintln(w, styles.Success.Render(fmt.Sprintf("  Logged in as %s.", profile.Name)))
		}
	} else {
		fmt.Fprintln(w, styles.Success.Render("  Authentication successful."))
	}
	fmt.Fprintln(w)

	return nil
}

// wizardAccount resolves the account using the existing interactive picker.
func wizardAccount(cmd *cobra.Command, app *appctx.App, styles *tui.Styles) (string, error) {
	w := cmd.OutOrStdout()
	fmt.Fprintln(w, styles.Heading.Render("  Step 2: Select Account"))
	fmt.Fprintln(w)

	resolved, err := app.Resolve().Account(cmd.Context())
	if err != nil {
		return "", err
	}

	// Update app config for subsequent steps
	app.Config.AccountID = resolved.Value
	if err := app.RequireAccount(); err != nil {
		return "", err
	}
	app.Names.SetAccountID(resolved.Value)

	return resolved.Value, nil
}

// wizardProject offers optional project selection.
func wizardProject(cmd *cobra.Command, app *appctx.App, styles *tui.Styles) (string, error) {
	w := cmd.OutOrStdout()
	fmt.Fprintln(w, styles.Heading.Render("  Step 3: Default Project (optional)"))
	fmt.Fprintln(w)

	wantProject, err := tui.Confirm("  Set a default project?", true)
	if err != nil {
		return "", nil //nolint:nilerr // Treat confirm error as skip (user canceled)
	}
	if !wantProject {
		fmt.Fprintln(w, styles.Muted.Render("  Skipped. Use --project or run: basecamp config project"))
		fmt.Fprintln(w)
		return "", nil
	}

	resolved, err := app.Resolve().Project(cmd.Context())
	if err != nil {
		return "", err
	}

	app.Config.ProjectID = resolved.Value
	return resolved.Value, nil
}

// wizardSaveConfig asks where to persist the selected defaults.
// Returns the chosen scope ("global", "local") or "" if skipped.
func wizardSaveConfig(w io.Writer, styles *tui.Styles, accountID, projectID string) string {
	if accountID == "" {
		return ""
	}

	fmt.Fprintln(w, styles.Heading.Render("  Step 4: Save Configuration"))
	fmt.Fprintln(w)

	scope, err := tui.Select("  Where should defaults be saved?", []tui.SelectOption{
		{Value: "global", Label: "Global (~/.config/basecamp/config.json) - applies everywhere"},
		{Value: "local", Label: "Local (.basecamp/config.json) - this directory only"},
		{Value: "skip", Label: "Don't save - I'll use flags each time"},
	})
	if err != nil || scope == "skip" {
		fmt.Fprintln(w, styles.Muted.Render("  Skipped. Use --account and --project flags."))
		fmt.Fprintln(w)
		return ""
	}

	saved := false
	if err := resolve.PersistValue("account_id", accountID, scope); err != nil {
		fmt.Fprintln(w, styles.Warning.Render(fmt.Sprintf("  Could not save account_id: %s", err)))
	} else {
		fmt.Fprintln(w, styles.Success.Render(fmt.Sprintf("  Saved account_id = %s (%s)", accountID, scope)))
		saved = true
	}

	if projectID != "" {
		if err := resolve.PersistValue("project_id", projectID, scope); err != nil {
			fmt.Fprintln(w, styles.Warning.Render(fmt.Sprintf("  Could not save project_id: %s", err)))
		} else {
			fmt.Fprintln(w, styles.Success.Render(fmt.Sprintf("  Saved project_id = %s (%s)", projectID, scope)))
			saved = true
		}
	}

	fmt.Fprintln(w)
	if !saved {
		return "" // Don't report scope if nothing was actually saved
	}
	return scope
}

// showSuccess displays the completion summary with example commands.
func showSuccess(w io.Writer, styles *tui.Styles, result WizardResult) {
	divider := styles.Muted.Render("─────────────────────────────────")

	fmt.Fprintln(w, divider)
	fmt.Fprintln(w, styles.Success.Render("  Setup complete!"))
	fmt.Fprintln(w, divider)
	fmt.Fprintln(w)

	// Status checklist
	fmt.Fprintln(w, styles.RenderStatus(true, "Authenticated"))
	if result.AccountName != "" {
		fmt.Fprintln(w, styles.RenderStatus(true, fmt.Sprintf("Account: %s", result.AccountName)))
	} else {
		fmt.Fprintln(w, styles.RenderStatus(true, fmt.Sprintf("Account: #%s", result.AccountID)))
	}
	if result.ProjectName != "" {
		fmt.Fprintln(w, styles.RenderStatus(true, fmt.Sprintf("Project: %s", result.ProjectName)))
	} else if result.ProjectID != "" {
		fmt.Fprintln(w, styles.RenderStatus(true, fmt.Sprintf("Project: #%s", result.ProjectID)))
	}
	if result.ConfigScope != "" {
		fmt.Fprintln(w, styles.RenderStatus(true, fmt.Sprintf("Config saved (%s)", result.ConfigScope)))
	}
	for _, agent := range harness.DetectedAgents() {
		if agent.Checks == nil {
			continue
		}
		for _, check := range agent.Checks() {
			fmt.Fprintln(w, styles.RenderStatus(check.Status == "pass", check.Name))
		}
	}
	fmt.Fprintln(w)

	// Example commands
	fmt.Fprintln(w, styles.Body.Render("  Try these commands:"))
	fmt.Fprintln(w)

	cmdStyle := lipgloss.NewStyle().Bold(true).Foreground(styles.Theme().Primary)
	descStyle := styles.Muted

	examples := []struct{ cmd, desc string }{
		{"basecamp projects", "List your projects"},
		{"basecamp todos", "List to-dos"},
		{"basecamp todo -c \"Buy milk\"", "Create a to-do"},
		{"basecamp search \"quarterly\"", "Search across Basecamp"},
	}
	for _, ex := range examples {
		fmt.Fprintf(w, "    %s  %s\n", cmdStyle.Render(ex.cmd), descStyle.Render(ex.desc))
	}
	fmt.Fprintln(w)
}

// fetchAccountName attempts to get the account name from the authorization endpoint.
func fetchAccountName(cmd *cobra.Command, app *appctx.App, accountID string) string {
	info, err := app.SDK.Authorization().GetInfo(cmd.Context(), nil)
	if err != nil {
		return ""
	}
	for _, acct := range info.Accounts {
		if fmt.Sprintf("%d", acct.ID) == accountID {
			return acct.Name
		}
	}
	return ""
}

// fetchProjectName attempts to get the project name from the API.
func fetchProjectName(cmd *cobra.Command, app *appctx.App, projectID string) string {
	resp, err := app.Account().Get(cmd.Context(), fmt.Sprintf("/projects/%s.json", projectID))
	if err != nil {
		return ""
	}
	var project struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(resp.Data, &project); err != nil {
		return ""
	}
	return project.Name
}

// wizardSummaryLine builds a concise summary for the output envelope.
func wizardSummaryLine(result WizardResult) string {
	if result.AccountName != "" {
		return fmt.Sprintf("Setup complete - %s", result.AccountName)
	}
	return "Setup complete"
}

// wizardBreadcrumbs returns next-step breadcrumbs based on wizard outcome.
func wizardBreadcrumbs(result WizardResult) []output.Breadcrumb {
	crumbs := []output.Breadcrumb{
		{Action: "list_projects", Cmd: "basecamp projects", Description: "List projects"},
	}
	if result.ProjectID != "" {
		crumbs = append(crumbs,
			output.Breadcrumb{Action: "list_todos", Cmd: "basecamp todos", Description: "List to-dos"},
			output.Breadcrumb{Action: "search", Cmd: "basecamp search \"query\"", Description: "Search Basecamp"},
		)
	} else {
		crumbs = append(crumbs,
			output.Breadcrumb{Action: "set_project", Cmd: "basecamp config project", Description: "Set default project"},
		)
	}
	return crumbs
}

// isFirstRun returns true if this appears to be a first-time run.
// Checks: onboarded flag, stored credentials, BASECAMP_TOKEN env, interactive TTY.
func isFirstRun(app *appctx.App) bool {
	if app.Config.Onboarded != nil && *app.Config.Onboarded {
		return false
	}
	if app.Auth.IsAuthenticated() {
		return false
	}
	if os.Getenv("BASECAMP_TOKEN") != "" {
		return false
	}
	return app.IsInteractive()
}
