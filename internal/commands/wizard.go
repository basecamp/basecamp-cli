package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/stdinarg"
	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/resolve"
)

// WizardResult holds the outcome of the first-run wizard, for showSuccess to
// render. It carries no json tags: the wizard only runs interactively, so the
// structured envelope it once emitted is unreachable and nothing serializes
// this.
type WizardResult struct {
	Status          string // "complete" or "incomplete"
	AuthenticatedAs string
	AccountID       string
	AccountName     string
	ProjectID       string
	ProjectName     string
	ConfigScope     string // "global", "local", or "" if skipped
}

// NewSetupCmd creates the setup command.
func NewSetupCmd() *cobra.Command {
	var customize bool
	var minimal bool

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "First-time setup with recommended defaults",
		Long: "Authenticate with Basecamp, select the first available account, save it globally, " +
			"and connect detected coding agents. Use --customize to choose each setting or --minimal for a concise completion message.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if customize {
				return runWizard(cmd, app)
			}
			return runFastSetup(cmd, app, minimal)
		},
	}
	cmd.Flags().BoolVar(&customize, "customize", false, "Choose the account, default project, config scope, and coding agent setup")
	cmd.Flags().BoolVar(&minimal, "minimal", false, "Show a concise completion message without starter commands")
	cmd.MarkFlagsMutuallyExclusive("customize", "minimal")
	for _, sub := range newSetupAgentCmds() {
		cmd.AddCommand(sub)
	}
	cmd.AddCommand(newSetupAgentsCmd())
	return cmd
}

// runFastSetup authenticates and applies the recommended first-run defaults.
func runFastSetup(cmd *cobra.Command, app *appctx.App, minimal bool) error {
	if app == nil {
		return fmt.Errorf("app not initialized")
	}
	app.SuppressPostRunNotices = true
	if app.Flags.JQFilter != "" {
		return output.ErrJQNotSupported("the setup command")
	}
	if app.Flags.Project != "" {
		return output.ErrUsageHint("choosing a default project uses customized setup", "Run: basecamp setup --customize --project "+app.Flags.Project)
	}
	if !setupCanRun(app) {
		return output.ErrUsageHint("basecamp setup needs an interactive terminal", wizardEscapeHint())
	}

	styles := tui.NewStylesWithTheme(tui.ResolveTheme(tui.DetectDark()))
	waitAnim := showWelcome(cmd.OutOrStdout(), styles)
	waitAnim()

	authenticatedAs, err := wizardAuth(cmd, app, styles, false)
	if err != nil {
		return err
	}

	accountID, accountName, err := automaticAccount(cmd, app)
	if err != nil {
		return err
	}
	if err := persistRecommendedDefaults(app, accountID); err != nil {
		return err
	}

	showFastAuthenticated(cmd.OutOrStdout(), styles, authenticatedAs, accountID, accountName)

	var agentOutcome agentSetupOutcome
	err = tui.RunWithSpinner(cmd.OutOrStdout(), styles.Theme(), "Setting up AI coding agents...", func() error {
		var setupErr error
		agentOutcome, setupErr = automaticAgents(cmd, styles)
		return setupErr
	})
	if err != nil {
		return err
	}
	showFastAgentStatus(cmd.OutOrStdout(), styles, agentOutcome)

	var omarchyOutcome omarchyPluginOutcome
	if detectOmarchy() {
		_ = tui.RunWithSpinner(cmd.OutOrStdout(), styles.Theme(), "Setting up Basecamp for Omarchy...", func() error {
			omarchyOutcome = setupOmarchyPlugin(cmd.Context())
			return nil
		})
	}

	if err := resolve.PersistValue("onboarded", "true", "global"); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to persist onboarding flag: %v\n", err)
	}

	showFastCompletion(cmd.OutOrStdout(), styles, omarchyOutcome, minimal)
	return nil
}

// persistRecommendedDefaults saves account-wide global defaults together.
// Directory and environment project settings remain persisted at their own
// higher-precedence sources and apply to later commands in those contexts.
func persistRecommendedDefaults(app *appctx.App, accountID string) error {
	if err := resolve.PersistValues(map[string]string{"account_id": accountID}, []string{"project_id"}, "global"); err != nil {
		return fmt.Errorf("saving the recommended defaults: %w", err)
	}
	app.Config.AccountID = accountID
	app.Config.ProjectID = ""
	delete(app.Config.Sources, "project_id")
	return nil
}

// runWizard runs the customizable first-run setup wizard.
// It walks the user through authentication, account selection, and project selection.
func runWizard(cmd *cobra.Command, app *appctx.App) error {
	if app == nil {
		return fmt.Errorf("app not initialized")
	}

	// --jq keeps its own, more specific error; it is a usage error either way.
	if app.Flags.JQFilter != "" {
		return output.ErrJQNotSupported("the setup command")
	}

	// Refuse rather than walk into a prompt nothing can answer. The user asked
	// for the wizard by name here, so say so; isFirstRun asks the same question
	// and answers it differently — see wizardCanRun.
	//
	// The gate belongs to this RunE alone: `setup claude`, `setup codex` and
	// `setup agents` are the supported non-interactive paths and must keep
	// working, which a persistent hook here would have broken.
	if !setupCanRun(app) {
		return output.ErrUsageHint("basecamp setup needs an interactive terminal", wizardEscapeHint())
	}

	styles := tui.NewStylesWithTheme(tui.ResolveTheme(tui.DetectDark()))

	// Step 1: Welcome
	waitAnim := showWelcome(cmd.OutOrStdout(), styles)
	waitAnim()

	// Step 2: Auth
	authenticatedAs, err := wizardAuth(cmd, app, styles, true)
	if err != nil {
		return err
	}

	// Step 3: Account selection
	result := WizardResult{Status: "complete", AuthenticatedAs: authenticatedAs}
	accountID, err := wizardAccount(cmd, app, styles)
	if err != nil {
		return err
	}
	result.AccountID = accountID

	// Fetch account name for display
	result.AccountName = fetchAccountName(cmd, app, accountID)
	w := cmd.OutOrStdout()
	if result.AccountName != "" {
		fmt.Fprintln(w, styles.Success.Render(fmt.Sprintf("  Selected account: %s", result.AccountName)))
	} else {
		fmt.Fprintln(w, styles.Success.Render(fmt.Sprintf("  Selected account: #%s", accountID)))
	}
	fmt.Fprintln(w)

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

	// Coding agent integration
	agentOutcome, err := wizardAgents(cmd, styles)
	if err != nil {
		return err
	}
	result.Status = statusFromOutcome(agentOutcome)

	omarchyOutcome := setupOmarchyPlugin(cmd.Context())
	if omarchyOutcome.Detected {
		fmt.Fprintln(w, styles.Heading.Render("  Omarchy Integration"))
		fmt.Fprintln(w)
		showOmarchyPluginStatus(w, styles, omarchyOutcome)
		fmt.Fprintln(w)
		if omarchyOutcome.failed() {
			result.Status = "incomplete"
		}
	}

	// Persist onboarded flag (always global so it applies everywhere)
	if err := resolve.PersistValue("onboarded", "true", "global"); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to persist onboarding flag: %v\n", err)
	}

	// Step 7: Summary with next steps. The gate above already established
	// interactive, non-machine output — the wizard cannot run any other way —
	// so the rich checklist is the only summary this path renders. The
	// structured-envelope branch that used to sit here was reachable only under
	// machine output, which the gate now refuses; it and its two helpers went
	// with it rather than being kept alive for nobody.
	showSuccess(cmd.OutOrStdout(), styles, result, agentOutcome.Checks, agentOutcome.Issues, agentOutcome.Skipped, omarchyOutcome)
	return nil
}

// wizardEscapeHint names the non-interactive paths that cover setup work,
// rather than restating that a terminal is missing. Modeled on stdinEscapeHint:
// point at the real alternatives.
func wizardEscapeHint() string {
	return "Agent setup runs without a terminal: basecamp setup agents (or basecamp setup claude / basecamp setup codex). " +
		"Set defaults directly with basecamp config set account_id <id> (or basecamp accounts use <id>) and basecamp config set project_id <id>. " +
		"Check authentication with basecamp auth status."
}

// showWelcome displays the welcome screen with the current snowglobe mark.
// All output goes to w so the command fully honors its output writer.
func showWelcome(w io.Writer, styles *tui.Styles) func() {
	fmt.Fprintln(w)
	fmt.Fprintln(w, tui.RenderSnowglobe(styles.Theme()))
	fmt.Fprintln(w)
	fmt.Fprintln(w, styles.Title.Render("Basecamp at your command (line)."))
	fmt.Fprintln(w, styles.Body.Render("Let's get you set up. It’ll only take a moment."))
	fmt.Fprintln(w)
	return func() {}
}

func showAuthenticationStart(w io.Writer, styles *tui.Styles, stepByStep bool) string {
	if !stepByStep {
		return ""
	}

	fmt.Fprintln(w, styles.Heading.Render("  Step 1: Authentication"))
	fmt.Fprintln(w)
	return "  "
}

func authenticationLogger(w io.Writer, prefix string) func(string) {
	launchpadOpeningShown := false
	return func(message string) {
		if strings.HasPrefix(message, "Authenticating via launchpad (") {
			message = "Opening browser for Basecamp login..."
			launchpadOpeningShown = true
		} else if launchpadOpeningShown && strings.TrimSpace(message) == "Opening browser for authentication..." {
			return
		}
		fmt.Fprintln(w, prefix+message)
	}
}

// wizardAuth handles authentication. showResult enables the step-by-step
// presentation and renders the authenticated identity immediately.
func wizardAuth(cmd *cobra.Command, app *appctx.App, styles *tui.Styles, showResult bool) (string, error) {
	w := cmd.OutOrStdout()

	if app.Auth.IsAuthenticated() {
		endpoint, epErr := app.Auth.AuthorizationEndpoint(cmd.Context())
		var info *basecamp.AuthorizationInfo
		var err error
		if epErr == nil {
			info, err = app.SDK.Authorization().GetInfo(cmd.Context(), &basecamp.GetInfoOptions{
				Endpoint:      endpoint,
				FilterProduct: "bc3",
			})
		}
		if showResult {
			if epErr == nil && err == nil {
				name := strings.TrimSpace(fmt.Sprintf("%s %s", info.Identity.FirstName, info.Identity.LastName))
				fmt.Fprintln(w, styles.Success.Render(fmt.Sprintf("  Logged in as %s (%s)", name, info.Identity.EmailAddress)))
			} else {
				fmt.Fprintln(w, styles.Success.Render("  Already authenticated."))
			}
			fmt.Fprintln(w)
		}
		if epErr == nil && err == nil {
			return identityLabel(info.Identity.FirstName, info.Identity.LastName, info.Identity.EmailAddress), nil
		}
		return app.Auth.GetUserEmail(), nil
	}

	loggerPrefix := showAuthenticationStart(w, styles, showResult)
	result, err := app.Auth.Login(cmd.Context(), auth.LoginOptions{
		Logger: authenticationLogger(w, loggerPrefix),
	})
	if err != nil {
		return "", fmt.Errorf("authentication failed: %w", err)
	}

	authenticatedAs := app.Auth.GetUserEmail()

	// Try to fetch user profile for a friendly greeting
	resp, profileErr := app.SDK.Get(cmd.Context(), "/my/profile.json")
	if profileErr == nil {
		var profile struct {
			ID    int    `json:"id"`
			Name  string `json:"name"`
			Email string `json:"email_address"`
		}
		if err := resp.UnmarshalData(&profile); err == nil {
			_ = app.Auth.SetUserIdentity(fmt.Sprintf("%d", profile.ID), profile.Email)
			authenticatedAs = strings.TrimSpace(profile.Name)
			if authenticatedAs == "" {
				authenticatedAs = profile.Email
			}
			if showResult {
				fmt.Fprintln(w, styles.Success.Render(fmt.Sprintf("  Logged in as %s.", profile.Name)))
			}
		}
	} else if showResult {
		fmt.Fprintln(w, styles.Success.Render("  Authentication successful."))
	}

	if showResult && result.Scope != "" {
		fmt.Fprintln(w, styles.Muted.Render(fmt.Sprintf("  Access: %s", result.Scope)))
	}
	if showResult {
		fmt.Fprintln(w)
	}

	return authenticatedAs, nil
}

// identityLabel returns the most useful concise identity available.
func identityLabel(firstName, lastName, email string) string {
	name := strings.TrimSpace(strings.TrimSpace(firstName) + " " + strings.TrimSpace(lastName))
	if name != "" {
		return name
	}
	return strings.TrimSpace(email)
}

// automaticAccount selects an explicit account, the OAuth-bound account, an
// existing configured account, or the first authorized account in that order.
func automaticAccount(cmd *cobra.Command, app *appctx.App) (string, string, error) {
	explicitAccountID := app.Flags.Account
	boundAccountID := app.Auth.AccountID()
	configuredAccountID := app.Config.AccountID
	var accounts []basecamp.AuthorizedAccount
	if explicitAccountID == "" && boundAccountID == "" && configuredAccountID == "" {
		var err error
		accounts, err = app.Resolve().ListAccounts(cmd.Context())
		if err != nil {
			return "", "", err
		}
	}

	accountID, accountName, err := chooseAutomaticAccount(explicitAccountID, boundAccountID, configuredAccountID, accounts)
	if err != nil {
		return "", "", err
	}
	if accountName == "" {
		accountName = fetchAccountName(cmd, app, accountID)
	}

	app.Config.AccountID = accountID
	if err := app.RequireAccount(); err != nil {
		return "", "", err
	}
	app.Names.SetAccountID(accountID)
	return accountID, accountName, nil
}

// chooseAutomaticAccount preserves explicit intent while honoring the account
// boundary carried by OAuth credentials. Existing configuration keeps reruns
// stable for unbound multi-account credentials.
func chooseAutomaticAccount(explicitAccountID, boundAccountID, configuredAccountID string, accounts []basecamp.AuthorizedAccount) (string, string, error) {
	if explicitAccountID != "" {
		if boundAccountID != "" && explicitAccountID != boundAccountID {
			return "", "", output.ErrUsageHint(
				fmt.Sprintf("account %s does not match the OAuth-bound account %s", explicitAccountID, boundAccountID),
				"Use --account "+boundAccountID+" or authenticate for the requested account.",
			)
		}
		return explicitAccountID, "", nil
	}
	if boundAccountID != "" {
		return boundAccountID, "", nil
	}
	if configuredAccountID != "" {
		return configuredAccountID, "", nil
	}
	if len(accounts) == 0 {
		return "", "", output.ErrNotFound("account", "any")
	}
	return fmt.Sprintf("%d", accounts[0].ID), accounts[0].Name, nil
}

// wizardAccount resolves the account using the existing interactive picker.
func wizardAccount(cmd *cobra.Command, app *appctx.App, styles *tui.Styles) (string, error) {
	w := cmd.OutOrStdout()
	fmt.Fprintln(w, styles.Heading.Render("  Step 2: Select Account"))
	fmt.Fprintln(w)

	// Clear any existing account so the picker always shows
	app.Config.AccountID = ""

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

	// Declining and failing look the same to a caller that only checks for a
	// non-nil error, so separate them: a dismissal skips the step, anything else
	// is a real failure and says so rather than reporting a choice nobody made.
	wantProject, err := tui.Confirm("  Set a default project?", true)
	if err != nil && !errors.Is(err, tui.ErrCanceled) {
		return "", fmt.Errorf("asking about the default project: %w", err)
	}
	if err != nil || !wantProject {
		fmt.Fprintln(w, styles.Muted.Render("  Skipped. Use --project or run: basecamp config project"))
		fmt.Fprintln(w)
		//nolint:nilerr // err here can only be tui.ErrCanceled — the check above
		// returned anything else — and a cancellation is an answer, not a failure.
		return "", nil
	}

	// Clear any existing project so the picker always shows
	app.Config.ProjectID = ""

	resolved, err := app.Resolve().Project(cmd.Context())
	if err != nil {
		return "", err
	}

	if resolved.Label != "" {
		fmt.Fprintln(w, styles.Success.Render("  Default project: "+resolved.Label))
		fmt.Fprintln(w)
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
	// No error return here, so a genuine failure is surfaced in place instead of
	// being flattened into the same "Skipped." a deliberate choice produces.
	if err != nil && !errors.Is(err, tui.ErrCanceled) {
		fmt.Fprintln(w, styles.Warning.Render(fmt.Sprintf("  Could not ask where to save: %s", err)))
	}
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

// successHeadline returns the completion banner. When the agent-setup step left
// unresolved issues, the banner is honest about it rather than claiming
// "Setup complete!".
func successHeadline(status string, issueCount int) string {
	if status != "incomplete" {
		return "Setup complete!"
	}
	if issueCount == 1 {
		return "Setup finished — 1 step needs attention"
	}
	return fmt.Sprintf("Setup finished — %d steps need attention", issueCount)
}

// showFastAuthenticated displays the authenticated identity and selected account.
func showFastAuthenticated(w io.Writer, styles *tui.Styles, authenticatedAs, accountID, accountName string) {
	authLabel := "Authenticated"
	if authenticatedAs != "" {
		authLabel += " as " + authenticatedAs
	}
	fmt.Fprintln(w, styles.RenderStatus(true, authLabel))
	accountLabel := accountName
	if accountLabel == "" {
		accountLabel = accountID
	}
	if accountLabel != "" {
		fmt.Fprintln(w, styles.RenderStatus(true, "Using account "+accountLabel))
	}
}

// showFastAgentStatus displays the durable result that replaces the coding-agent spinner.
func showFastAgentStatus(w io.Writer, styles *tui.Styles, agents agentSetupOutcome) {
	switch {
	case len(agents.Issues) > 0:
		fmt.Fprintln(w, styles.RenderStatus(false, "AI coding agents need attention — run: basecamp doctor"))
	case agents.Detected == 0:
		fmt.Fprintln(w, styles.Muted.Render("  AI coding agents: none detected"))
	default:
		fmt.Fprintln(w, styles.RenderStatus(true, "AI coding agents set up"))
	}
}

// showFastCompletion displays the Omarchy result and setup next steps.
func showFastCompletion(w io.Writer, styles *tui.Styles, omarchy omarchyPluginOutcome, minimal bool) {
	if omarchy.Detected {
		showOmarchyPluginStatus(w, styles, omarchy)
	}
	fmt.Fprintln(w)

	if minimal {
		fmt.Fprintln(w, fastSetupTitleStyle(styles).Render("SETUP COMPLETE"))
		fmt.Fprintln(w)
		return
	}
	showFastSetupExamples(w, styles)
}

func showOmarchyPluginStatus(w io.Writer, styles *tui.Styles, outcome omarchyPluginOutcome) {
	switch outcome.Status {
	case "installed":
		fmt.Fprintln(w, styles.RenderStatus(true, "Basecamp plugin installed for Omarchy"))
	case "updated":
		fmt.Fprintln(w, styles.RenderStatus(true, "Basecamp plugin updated for Omarchy"))
	case "failed":
		message := "Basecamp plugin setup needs attention"
		if outcome.Manual != "" {
			message += " — run: " + outcome.Manual
		}
		fmt.Fprintln(w, styles.RenderStatus(false, message))
	}
}

func fastSetupTitleStyle(styles *tui.Styles) lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(styles.Theme().Primary)
}

// showFastSetupExamples prints account-wide commands that work with the
// recommended setup's intentionally empty default project.
func showFastSetupExamples(w io.Writer, styles *tui.Styles) {
	titleStyle := fastSetupTitleStyle(styles)
	descStyle := lipgloss.NewStyle().Italic(true)
	examples := []struct{ cmd, desc string }{
		{"basecamp projects list", "List your projects"},
		{"basecamp assignments", "View your assignments"},
		{"basecamp timeline", "See recent activity"},
		{`basecamp search "quarterly planning"`, "Search across Basecamp"},
	}

	fmt.Fprintln(w, titleStyle.Render("Try it out!"))
	fmt.Fprintln(w)

	width := 0
	for _, example := range examples {
		width = max(width, len(example.cmd))
	}
	for _, example := range examples {
		fmt.Fprintf(w, "%s%s  %s\n",
			example.cmd,
			strings.Repeat(" ", width-len(example.cmd)),
			descStyle.Render(example.desc),
		)
	}
	fmt.Fprintln(w)
}

// showSuccess displays the customizable setup summary with example commands.
// checks is the agent-health snapshot rendered as the checklist; issues holds
// every unresolved problem and drives the headline and remediation. When the
// user skipped agent setup the checks are reported as skipped rather than as
// failures.
func showSuccess(w io.Writer, styles *tui.Styles, result WizardResult, checks []agentCheck, issues []agentIssue, skipped bool, omarchy omarchyPluginOutcome) {
	divider := styles.Muted.Render("─────────────────────────────────")

	headlineStyle := styles.Success
	if result.Status == "incomplete" {
		headlineStyle = styles.Warning
	}

	issueCount := len(issues)
	if omarchy.failed() {
		issueCount++
	}

	fmt.Fprintln(w, divider)
	fmt.Fprintln(w, headlineStyle.Render("  "+successHeadline(result.Status, issueCount)))
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
	if skipped {
		fmt.Fprintln(w, styles.Muted.Render("  Coding agent setup skipped — run: basecamp setup agents"))
	} else {
		for _, check := range checks {
			fmt.Fprintln(w, styles.RenderStatus(check.Status == "pass", check.Name))
		}
	}
	if omarchy.Detected {
		showOmarchyPluginStatus(w, styles, omarchy)
	}
	fmt.Fprintln(w)

	// Remediation for anything that did not complete. Each issue carries its own
	// check's hint, so guidance stays agent-specific; Omarchy carries the exact
	// plugin command that completes its setup.
	if len(issues) > 0 || omarchy.failed() {
		fmt.Fprintln(w, styles.Body.Render("  Some steps need attention:"))
		for _, issue := range issues {
			// Check names usually already carry the agent (e.g. "Claude Code
			// Plugin"); only prefix when they don't, to avoid "Claude Code —
			// Claude Code Plugin".
			label := issue.Check
			if issue.Agent != "" && !strings.HasPrefix(issue.Check, issue.Agent) {
				label = issue.Agent + " — " + issue.Check
			}
			line := "    " + label
			if issue.Hint != "" {
				line += ": " + issue.Hint
			}
			fmt.Fprintln(w, styles.Warning.Render(line))
		}
		if omarchy.failed() {
			line := "    Omarchy — " + omarchy.Detail
			if omarchy.Manual != "" {
				line += ": " + omarchy.Manual
			}
			fmt.Fprintln(w, styles.Warning.Render(line))
		}
		if len(issues) > 0 {
			fmt.Fprintln(w, styles.Muted.Render("    Then verify coding agents with: basecamp doctor"))
		}
		fmt.Fprintln(w)
	}

	// Example commands
	fmt.Fprintln(w, styles.Body.Render("  Try these commands:"))
	fmt.Fprintln(w)

	cmdStyle := lipgloss.NewStyle().Bold(true).Foreground(styles.Theme().Primary)
	descStyle := styles.Muted

	examples := []struct{ cmd, desc string }{
		{"basecamp projects list", "List your projects"},
		{"basecamp todos list", "List to-dos"},
		{"basecamp todos create \"Buy milk\"", "Create a to-do"},
		{"basecamp search \"quarterly\"", "Search across Basecamp"},
	}
	for _, ex := range examples {
		fmt.Fprintf(w, "    %s  %s\n", cmdStyle.Render(ex.cmd), descStyle.Render(ex.desc))
	}
	fmt.Fprintln(w)
}

// fetchAccountName attempts to get the account name from the authorization endpoint.
func fetchAccountName(cmd *cobra.Command, app *appctx.App, accountID string) string {
	endpoint, epErr := app.Auth.AuthorizationEndpoint(cmd.Context())
	if epErr != nil {
		return ""
	}
	info, err := app.SDK.Authorization().GetInfo(cmd.Context(), &basecamp.GetInfoOptions{
		Endpoint:      endpoint,
		FilterProduct: "bc3",
	})
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

// setupCanRun reports whether human first-time setup can run safely.
//
// Recommended setup opens browser OAuth, while customized setup also uses huh
// prompts that draw to stderr. The combined checks keep setup in human-output
// terminal contexts: IsInteractive covers stdin/stdout, flags, and
// BASECAMP_NONINTERACTIVE; IsMachineOutput adds config-driven json/quiet formats
// while honoring explicit --styled/--md overrides; InteractivePrompt adds stderr.
//
// Two callers deliberately respond differently. Explicit `basecamp setup`
// refuses out loud. Bare `basecamp` quietly declines first-time setup and falls
// through to help or the quick-start envelope.
func setupCanRun(app *appctx.App) bool {
	return app.IsInteractive() && !app.IsMachineOutput() && stdinarg.InteractivePrompt()
}

// isFirstRun returns true if this appears to be a first-time run.
// Checks: onboarded flag, stored credentials, BASECAMP_TOKEN env, and whether
// first-time setup can run safely.
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
	return setupCanRun(app)
}
