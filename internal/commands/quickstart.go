package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/harness"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/version"
)

// QuickStartResponse is the JSON structure for the quick-start command.
type QuickStartResponse struct {
	Version  string       `json:"version"`
	Auth     AuthInfo     `json:"auth"`
	Context  ContextInfo  `json:"context"`
	Commands CommandsInfo `json:"commands"`
}

// AuthInfo describes the authentication status.
type AuthInfo struct {
	Status  string `json:"status"`
	User    string `json:"user,omitempty"`
	Account string `json:"account,omitempty"`
}

// ContextInfo describes the current context.
type ContextInfo struct {
	ProjectID   *int64  `json:"project_id,omitempty"`
	ProjectName *string `json:"project_name,omitempty"`
}

// CommandsInfo lists suggested commands.
type CommandsInfo struct {
	QuickStart []string `json:"quick_start"`
	Common     []string `json:"common"`
}

// NewQuickStartCmd creates the quick-start command.
func NewQuickStartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "quick-start",
		Short:  "Show quick start guide",
		Long:   "Display a quick start guide with authentication status and suggested commands.",
		Hidden: true, // Hide from help - this is mainly run as default
		RunE:   runQuickStart,
	}
	return cmd
}

// RunQuickStartDefault is called when basecamp is run with no args.
// If this is a first run (unauthenticated, interactive TTY, no BASECAMP_TOKEN),
// it runs the setup wizard. Non-interactive invocations (piped, non-TTY, or
// machine-output modes — whether flag-driven or config-driven) preserve the
// quick-start JSON envelope. Interactive TTY shows help.
func RunQuickStartDefault(cmd *cobra.Command, args []string) error {
	app := appctx.FromContext(cmd.Context())
	if app != nil && isFirstRun(app) {
		return runWizard(cmd, app)
	}
	if app != nil && (!app.IsInteractive() || app.IsMachineOutput()) {
		return runQuickStart(cmd, args)
	}
	return cmd.Help()
}

func runQuickStart(cmd *cobra.Command, args []string) error {
	app := appctx.FromContext(cmd.Context())

	// Show animated wordmark on interactive TTY (not JSON/agent/piped/config-driven machine output)
	var waitAnim func()
	if app != nil && app.IsInteractive() && !app.IsMachineOutput() {
		styles := tui.NewStylesWithTheme(tui.ResolveTheme(tui.DetectDark()))
		dest := cmd.OutOrStdout()
		aw, wait := tui.AnimateWordmarkAsync(dest, styles.Theme())
		fmt.Fprintln(aw)
		waitAnim = wait
		// Only reroute output when animation started (aw is an AnimWriter).
		// When AnimateWordmarkAsync falls back to static (non-TTY dest),
		// it returns the original writer — leave app.Output alone so its
		// format resolution stays tied to the real destination.
		if aw != dest {
			app.Output = output.New(output.Options{
				Format: app.Output.EffectiveFormat(),
				Writer: aw,
			})
		}
	}

	// Determine auth status
	authInfo := AuthInfo{Status: "unauthenticated"}
	if app.Auth.IsAuthenticated() {
		authInfo.Status = "authenticated"
		// Try to get account ID from config (name isn't stored)
		if app.Config.AccountID != "" {
			authInfo.Account = app.Config.AccountID
		}
	}

	// Build context info from config only (no API calls — must be instant)
	contextInfo := ContextInfo{}
	if app.Config.ProjectID != "" {
		var id int64
		_, _ = fmt.Sscanf(app.Config.ProjectID, "%d", &id) //nolint:gosec // G104: ID validated
		if id != 0 {
			contextInfo.ProjectID = &id
		}
	}

	// Commands info
	commandsInfo := CommandsInfo{
		QuickStart: []string{"basecamp projects", "basecamp todos", "basecamp search \"query\""},
		Common:     []string{"basecamp todo \"content\"", "basecamp done <id>", "basecamp comment \"text\" <id>"},
	}

	// Build response
	resp := QuickStartResponse{
		Version:  version.Version,
		Auth:     authInfo,
		Context:  contextInfo,
		Commands: commandsInfo,
	}

	// Marshal to JSON for the data field
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}

	// Build summary based on auth status
	var summary string
	if authInfo.Status == "authenticated" {
		if authInfo.Account != "" {
			summary = fmt.Sprintf("basecamp v%s - logged in @ %s", version.Version, authInfo.Account)
		} else {
			summary = fmt.Sprintf("basecamp v%s - logged in", version.Version)
		}
		if app.Flags.Profile != "" {
			summary += fmt.Sprintf(" (profile: %s)", app.Flags.Profile)
		}
	} else {
		summary = fmt.Sprintf("basecamp v%s - not logged in", version.Version)
	}

	// Build breadcrumbs
	breadcrumbs := []output.Breadcrumb{
		{Action: "list_projects", Cmd: "basecamp projects", Description: "List projects"},
		{Action: "list_todos", Cmd: "basecamp todos", Description: "List todos"},
	}
	if authInfo.Status == "unauthenticated" {
		breadcrumbs = append(breadcrumbs, output.Breadcrumb{
			Action: "authenticate", Cmd: "basecamp auth login", Description: "Login",
		})
	}
	for _, agent := range harness.DetectedAgents() {
		if agent.Checks == nil {
			continue
		}
		for _, c := range agent.Checks() {
			if c.Status != "pass" {
				breadcrumbs = append(breadcrumbs, output.Breadcrumb{
					Action:      "setup_" + agent.ID,
					Cmd:         "basecamp setup " + agent.ID,
					Description: "Connect " + agent.Name + " to Basecamp",
				})
				break
			}
		}
	}

	err = app.OK(json.RawMessage(data),
		output.WithSummary(summary),
		output.WithBreadcrumbs(breadcrumbs...),
	)

	if waitAnim != nil {
		waitAnim()
	}

	return err
}
