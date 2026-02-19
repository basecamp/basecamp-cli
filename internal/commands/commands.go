package commands

import (
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// CommandInfo describes a CLI command.
type CommandInfo struct {
	Name        string   `json:"name"`
	Category    string   `json:"category"`
	Description string   `json:"description"`
	Actions     []string `json:"actions,omitempty"`
}

// CommandCategory groups commands by category.
type CommandCategory struct {
	Name     string        `json:"name"`
	Commands []CommandInfo `json:"commands"`
}

// commandCategories returns all command categories for the catalog.
func commandCategories() []CommandCategory {
	return []CommandCategory{
		{
			Name: "Core Commands",
			Commands: []CommandInfo{
				{Name: "projects", Category: "core", Description: "Manage projects", Actions: []string{"list", "show", "create", "update", "delete"}},
				{Name: "todos", Category: "core", Description: "Manage to-dos", Actions: []string{"list", "show", "create", "complete", "uncomplete", "position"}},
				{Name: "todolists", Category: "core", Description: "Manage to-do lists", Actions: []string{"list", "show", "create", "update"}},
				{Name: "todosets", Category: "core", Description: "View to-do set containers", Actions: []string{"show"}},
				{Name: "todolistgroups", Category: "core", Description: "Manage to-do list groups", Actions: []string{"list", "show", "create", "update", "position"}},
				{Name: "messages", Category: "core", Description: "Manage messages", Actions: []string{"list", "show", "create", "update", "pin", "unpin"}},
				{Name: "campfire", Category: "core", Description: "Chat in Campfire rooms", Actions: []string{"list", "messages", "post", "line", "delete"}},
				{Name: "cards", Category: "core", Description: "Manage Kanban cards", Actions: []string{"list", "show", "create", "update", "move", "columns", "steps"}},
			},
		},
		{
			Name: "Shortcut Commands",
			Commands: []CommandInfo{
				{Name: "todo", Category: "shortcut", Description: "Create a to-do"},
				{Name: "done", Category: "shortcut", Description: "Complete a to-do"},
				{Name: "reopen", Category: "shortcut", Description: "Uncomplete a to-do"},
				{Name: "message", Category: "shortcut", Description: "Post a message"},
				{Name: "card", Category: "shortcut", Description: "Create a card"},
				{Name: "comment", Category: "shortcut", Description: "Add a comment"},
				{Name: "assign", Category: "shortcut", Description: "Assign a recording"},
				{Name: "unassign", Category: "shortcut", Description: "Remove assignment"},
				{Name: "clock", Category: "shortcut", Description: "Log time"},
				{Name: "react", Category: "shortcut", Description: "Boost a recording"},
			},
		},
		{
			Name: "Files & Docs",
			Commands: []CommandInfo{
				{Name: "files", Category: "files", Description: "Manage files, documents, and folders", Actions: []string{"list", "show", "create", "update"}},
				{Name: "uploads", Category: "files", Description: "List and manage uploads", Actions: []string{"list", "show"}},
				{Name: "vaults", Category: "files", Description: "Manage folders (vaults)", Actions: []string{"list", "show", "create"}},
				{Name: "docs", Category: "files", Description: "Manage documents", Actions: []string{"list", "show", "create", "update"}},
			},
		},
		{
			Name: "Scheduling & Time",
			Commands: []CommandInfo{
				{Name: "schedule", Category: "scheduling", Description: "Manage schedule entries", Actions: []string{"show", "entries", "create", "update"}},
				{Name: "timesheet", Category: "scheduling", Description: "Manage time tracking", Actions: []string{"report", "project", "recording", "entry show", "entry create", "entry update", "entry trash"}},
				{Name: "checkins", Category: "scheduling", Description: "View automatic check-ins", Actions: []string{"questions", "question", "answers", "answer"}},
				{Name: "timeline", Category: "scheduling", Description: "View activity timelines", Actions: []string{}},
				{Name: "reports", Category: "scheduling", Description: "View reports", Actions: []string{"assignable", "assigned", "overdue", "schedule"}},
			},
		},
		{
			Name: "Organization",
			Commands: []CommandInfo{
				{Name: "people", Category: "organization", Description: "Manage people and access", Actions: []string{"list", "show", "pingable", "add", "remove"}},
				{Name: "templates", Category: "organization", Description: "Manage project templates", Actions: []string{"list", "show", "create", "update", "delete", "construct"}},
				{Name: "webhooks", Category: "organization", Description: "Manage webhooks", Actions: []string{"list", "show", "create", "update", "delete"}},
				{Name: "lineup", Category: "organization", Description: "Manage lineup markers", Actions: []string{"create", "update", "delete"}},
			},
		},
		{
			Name: "Communication",
			Commands: []CommandInfo{
				{Name: "messageboards", Category: "communication", Description: "View message boards", Actions: []string{"show"}},
				{Name: "messagetypes", Category: "communication", Description: "Manage message categories", Actions: []string{"list", "show", "create", "update", "delete"}},
				{Name: "forwards", Category: "communication", Description: "Manage email forwards (inbox)", Actions: []string{"list", "show", "inbox", "replies", "reply"}},
				{Name: "subscriptions", Category: "communication", Description: "Manage notification subscriptions", Actions: []string{"show", "subscribe", "unsubscribe", "add", "remove"}},
				{Name: "comments", Category: "communication", Description: "Manage comments", Actions: []string{"list", "show", "update"}},
				{Name: "boost", Category: "communication", Description: "Manage boosts (reactions)", Actions: []string{"list", "show", "create", "delete"}},
			},
		},
		{
			Name: "Search & Browse",
			Commands: []CommandInfo{
				{Name: "search", Category: "search", Description: "Search across projects"},
				{Name: "recordings", Category: "search", Description: "Browse recordings by type/status", Actions: []string{"list", "trash", "archive", "restore", "visibility"}},
				{Name: "show", Category: "search", Description: "Show any recording by ID"},
				{Name: "events", Category: "search", Description: "View recording change history"},
				{Name: "url", Category: "search", Description: "Open Basecamp URL in browser"},
			},
		},
		{
			Name: "Auth & Config",
			Commands: []CommandInfo{
				{Name: "auth", Category: "auth", Description: "Authenticate with Basecamp", Actions: []string{"login", "logout", "status", "refresh"}},
				{Name: "config", Category: "auth", Description: "Manage configuration", Actions: []string{"show", "init", "set", "unset", "project"}},
				{Name: "me", Category: "auth", Description: "Show current user profile"},
				{Name: "setup", Category: "auth", Description: "Interactive first-time setup"},
				{Name: "quick-start", Category: "auth", Description: "Show getting started guide"},
				{Name: "doctor", Category: "auth", Description: "Check CLI health and diagnose issues"},
				{Name: "migrate", Category: "auth", Description: "Migrate data from legacy bcq installation"},
			},
		},
		{
			Name: "Additional Commands",
			Commands: []CommandInfo{
				{Name: "commands", Category: "additional", Description: "List all commands"},
				{Name: "completion", Category: "additional", Description: "Generate shell completions", Actions: []string{"bash", "zsh", "fish", "powershell", "refresh", "status"}},
				{Name: "mcp", Category: "additional", Description: "MCP server integration", Actions: []string{"server"}},
				{Name: "tools", Category: "additional", Description: "Manage project dock tools", Actions: []string{"show", "create", "update", "trash", "enable", "disable", "reposition"}},
				{Name: "tui", Category: "additional", Description: "Launch the Basecamp workspace"},
				{Name: "api", Category: "additional", Description: "Raw API access"},
				{Name: "help", Category: "additional", Description: "Show help"},
				{Name: "version", Category: "additional", Description: "Show version"},
			},
		},
	}
}

// CatalogCommandNames returns all command names from the catalog.
// Used by tests to verify catalog matches registered commands.
func CatalogCommandNames() []string {
	categories := commandCategories()
	// Count total commands for preallocation
	total := 0
	for _, cat := range categories {
		total += len(cat.Commands)
	}
	names := make([]string, 0, total)
	for _, cat := range categories {
		for _, cmd := range cat.Commands {
			names = append(names, cmd.Name)
		}
	}
	return names
}

// NewCommandsCmd creates the commands listing command.
func NewCommandsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "commands",
		Aliases: []string{"cmds"},
		Short:   "List all available commands",
		Long:    "List all available basecamp commands organized by category.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			return app.OK(commandCategories(),
				output.WithSummary("All available basecamp commands"),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "help",
						Cmd:         "basecamp --help",
						Description: "View help",
					},
				),
			)
		},
	}
}
