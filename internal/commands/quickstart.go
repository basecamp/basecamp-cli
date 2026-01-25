package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
	"github.com/basecamp/bcq/internal/version"
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

// RunQuickStartDefault is called when bcq is run with no args.
func RunQuickStartDefault(cmd *cobra.Command, args []string) error {
	return runQuickStart(cmd, args)
}

func runQuickStart(cmd *cobra.Command, args []string) error {
	app := appctx.FromContext(cmd.Context())

	// Determine auth status
	authInfo := AuthInfo{Status: "unauthenticated"}
	if app.Auth.IsAuthenticated() {
		authInfo.Status = "authenticated"
		// Try to get account ID from config (name isn't stored)
		if app.Config.AccountID != "" {
			authInfo.Account = app.Config.AccountID
		}
	}

	// Build context info
	contextInfo := ContextInfo{}
	if app.Config.ProjectID != "" {
		// Try to resolve project name
		projectID, projectName, err := app.Names.ResolveProject(cmd.Context(), app.Config.ProjectID)
		if err == nil {
			var id int64
			_, _ = fmt.Sscanf(projectID, "%d", &id) //nolint:gosec // G104: ID validated
			contextInfo.ProjectID = &id
			if projectName != "" {
				contextInfo.ProjectName = &projectName
			}
		}
	}

	// Commands info
	commandsInfo := CommandsInfo{
		QuickStart: []string{"bcq projects", "bcq todos", "bcq search \"query\""},
		Common:     []string{"bcq todo \"content\"", "bcq done <id>", "bcq comment \"text\" <id>"},
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
			summary = fmt.Sprintf("bcq v%s - logged in @ %s", version.Version, authInfo.Account)
		} else {
			summary = fmt.Sprintf("bcq v%s - logged in", version.Version)
		}
	} else {
		summary = fmt.Sprintf("bcq v%s - not logged in", version.Version)
	}

	// Build breadcrumbs
	breadcrumbs := []output.Breadcrumb{
		{Action: "list_projects", Cmd: "bcq projects", Description: "List projects"},
		{Action: "list_todos", Cmd: "bcq todos", Description: "List todos"},
	}
	if authInfo.Status == "unauthenticated" {
		breadcrumbs = append(breadcrumbs, output.Breadcrumb{
			Action: "authenticate", Cmd: "bcq auth login", Description: "Login",
		})
	}

	return app.Output.OK(json.RawMessage(data),
		output.WithSummary(summary),
		output.WithBreadcrumbs(breadcrumbs...),
	)
}
