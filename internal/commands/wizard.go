package commands

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
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
	return &cobra.Command{
		Use:   "setup",
		Short: "Interactive first-time setup",
		Long:  "Walk through authentication, account selection, and project configuration.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			return runWizard(cmd, app)
		},
	}
}

// runWizard runs the interactive first-run setup wizard.
// It walks the user through authentication, account selection, and project selection.
func runWizard(cmd *cobra.Command, app *appctx.App) error {
	if app == nil {
		return fmt.Errorf("app not initialized")
	}

	styles := tui.NewStylesWithTheme(tui.ResolveTheme())

	// Step 1: Welcome
	if err := showWelcome(styles); err != nil {
		return err
	}

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
	result.ConfigScope = wizardSaveConfig(styles, accountID, projectID)

	// Step 6: Summary with next steps
	showSuccess(styles, result)

	return app.OK(result,
		output.WithSummary(wizardSummaryLine(result)),
		output.WithBreadcrumbs(wizardBreadcrumbs(result)...),
	)
}

// showWelcome displays the welcome screen.
func showWelcome(styles *tui.Styles) error {
	title := styles.Title.Render("Welcome to basecamp")
	body := fmt.Sprintf(
		"%s\n\n%s\n\n%s",
		styles.Body.Render(fmt.Sprintf("The command-line interface for Basecamp (v%s).", version.Version)),
		styles.Body.Render("Let's get you set up. This will only take a moment."),
		styles.Muted.Render("Press Enter to continue, or Ctrl+C to exit."),
	)
	return tui.Note(title, body)
}

// wizardAuth handles the authentication flow with scope selection.
func wizardAuth(cmd *cobra.Command, app *appctx.App, styles *tui.Styles) error {
	if app.Auth.IsAuthenticated() {
		fmt.Println(styles.Success.Render("  Already authenticated."))
		fmt.Println()
		return nil
	}

	fmt.Println(styles.Heading.Render("  Step 1: Authentication"))
	fmt.Println()

	// Let user choose scope (map to OAuth scopes: "read" or "write")
	scope, err := tui.Select("  What access level do you need?", []tui.SelectOption{
		{Value: "read", Label: "Read-only (recommended for browsing)"},
		{Value: "write", Label: "Full access (read + write)"},
	})
	if err != nil {
		return fmt.Errorf("scope selection canceled: %w", err)
	}

	fmt.Println()
	fmt.Println(styles.Muted.Render("  Opening browser for Basecamp login..."))
	fmt.Println()

	if err := app.Auth.Login(cmd.Context(), auth.LoginOptions{
		Scope:  scope,
		Logger: func(msg string) { fmt.Println("  " + msg) },
	}); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	// Try to fetch user profile for a friendly greeting
	resp, err := app.SDK.Get(cmd.Context(), "/my/profile.json")
	if err == nil {
		var profile struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}
		if err := resp.UnmarshalData(&profile); err == nil {
			_ = app.Auth.SetUserID(fmt.Sprintf("%d", profile.ID))
			fmt.Println(styles.Success.Render(fmt.Sprintf("  Logged in as %s.", profile.Name)))
		}
	} else {
		fmt.Println(styles.Success.Render("  Authentication successful."))
	}
	fmt.Println()

	return nil
}

// wizardAccount resolves the account using the existing interactive picker.
func wizardAccount(cmd *cobra.Command, app *appctx.App, styles *tui.Styles) (string, error) {
	fmt.Println(styles.Heading.Render("  Step 2: Select Account"))
	fmt.Println()

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
	fmt.Println(styles.Heading.Render("  Step 3: Default Project (optional)"))
	fmt.Println()

	wantProject, err := tui.Confirm("  Set a default project?", true)
	if err != nil {
		return "", nil //nolint:nilerr // Treat confirm error as skip (user canceled)
	}
	if !wantProject {
		fmt.Println(styles.Muted.Render("  Skipped. Use --project or run: basecamp config project"))
		fmt.Println()
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
func wizardSaveConfig(styles *tui.Styles, accountID, projectID string) string {
	if accountID == "" {
		return ""
	}

	fmt.Println(styles.Heading.Render("  Step 4: Save Configuration"))
	fmt.Println()

	scope, err := tui.Select("  Where should defaults be saved?", []tui.SelectOption{
		{Value: "global", Label: "Global (~/.config/basecamp/config.json) - applies everywhere"},
		{Value: "local", Label: "Local (.basecamp/config.json) - this directory only"},
		{Value: "skip", Label: "Don't save - I'll use flags each time"},
	})
	if err != nil || scope == "skip" {
		fmt.Println(styles.Muted.Render("  Skipped. Use --account and --project flags."))
		fmt.Println()
		return ""
	}

	saved := false
	if err := resolve.PersistValue("account_id", accountID, scope); err != nil {
		fmt.Println(styles.Warning.Render(fmt.Sprintf("  Could not save account_id: %s", err)))
	} else {
		fmt.Println(styles.Success.Render(fmt.Sprintf("  Saved account_id = %s (%s)", accountID, scope)))
		saved = true
	}

	if projectID != "" {
		if err := resolve.PersistValue("project_id", projectID, scope); err != nil {
			fmt.Println(styles.Warning.Render(fmt.Sprintf("  Could not save project_id: %s", err)))
		} else {
			fmt.Println(styles.Success.Render(fmt.Sprintf("  Saved project_id = %s (%s)", projectID, scope)))
			saved = true
		}
	}

	fmt.Println()
	if !saved {
		return "" // Don't report scope if nothing was actually saved
	}
	return scope
}

// showSuccess displays the completion summary with example commands.
func showSuccess(styles *tui.Styles, result WizardResult) {
	divider := styles.Muted.Render("─────────────────────────────────")

	fmt.Println(divider)
	fmt.Println(styles.Success.Render("  Setup complete!"))
	fmt.Println(divider)
	fmt.Println()

	// Status checklist
	fmt.Println(styles.RenderStatus(true, "Authenticated"))
	if result.AccountName != "" {
		fmt.Println(styles.RenderStatus(true, fmt.Sprintf("Account: %s", result.AccountName)))
	} else {
		fmt.Println(styles.RenderStatus(true, fmt.Sprintf("Account: #%s", result.AccountID)))
	}
	if result.ProjectName != "" {
		fmt.Println(styles.RenderStatus(true, fmt.Sprintf("Project: %s", result.ProjectName)))
	} else if result.ProjectID != "" {
		fmt.Println(styles.RenderStatus(true, fmt.Sprintf("Project: #%s", result.ProjectID)))
	}
	if result.ConfigScope != "" {
		fmt.Println(styles.RenderStatus(true, fmt.Sprintf("Config saved (%s)", result.ConfigScope)))
	}
	fmt.Println()

	// Example commands
	fmt.Println(styles.Body.Render("  Try these commands:"))
	fmt.Println()

	cmdStyle := lipgloss.NewStyle().Bold(true).Foreground(styles.Theme().Primary)
	descStyle := styles.Muted

	examples := []struct{ cmd, desc string }{
		{"basecamp projects", "List your projects"},
		{"basecamp todos", "List to-dos"},
		{"basecamp todo -c \"Buy milk\"", "Create a to-do"},
		{"basecamp search \"quarterly\"", "Search across Basecamp"},
	}
	for _, ex := range examples {
		fmt.Printf("    %s  %s\n", cmdStyle.Render(ex.cmd), descStyle.Render(ex.desc))
	}
	fmt.Println()
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
// Checks: no stored credentials, no BASECAMP_TOKEN env, interactive TTY.
func isFirstRun(app *appctx.App) bool {
	if app.Auth.IsAuthenticated() {
		return false
	}
	if os.Getenv("BASECAMP_TOKEN") != "" {
		return false
	}
	return app.IsInteractive()
}
