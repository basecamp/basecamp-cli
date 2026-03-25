package commands

import (
	"fmt"
	"io"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// CommandInfo describes a CLI command.
type CommandInfo struct {
	Name         string   `json:"name"`
	Category     string   `json:"category"`
	Description  string   `json:"description"`
	Actions      []string `json:"actions,omitempty"`
	Experimental bool     `json:"experimental,omitempty"`
	DevOnly      bool     `json:"dev_only,omitempty"`
}

// CommandCategory groups commands by category.
type CommandCategory struct {
	Name     string        `json:"name"`
	Commands []CommandInfo `json:"commands"`
}

// CommandCategories returns all command categories for the catalog.
func CommandCategories() []CommandCategory {
	return []CommandCategory{
		{
			Name: "Core Commands",
			Commands: []CommandInfo{
				{Name: "projects", Category: "core", Description: "Manage projects", Actions: []string{"list", "show", "create", "update", "delete"}},
				{Name: "todos", Category: "core", Description: "Manage to-dos", Actions: []string{"list", "show", "create", "update", "complete", "uncomplete", "position", "trash", "archive", "restore"}},
				{Name: "todolists", Category: "core", Description: "Manage to-do lists", Actions: []string{"list", "show", "create", "update", "trash", "archive", "restore"}},
				{Name: "todosets", Category: "core", Description: "Manage to-do set containers", Actions: []string{"list", "show"}},
				{Name: "hillcharts", Category: "core", Description: "Manage hill charts", Actions: []string{"show", "track", "untrack"}},
				{Name: "gauges", Category: "core", Description: "Manage gauges", Actions: []string{"list", "needles", "needle", "create", "update", "delete", "enable", "disable"}},
				{Name: "todolistgroups", Category: "core", Description: "Manage to-do list groups", Actions: []string{"list", "show", "create", "update", "position"}},
				{Name: "messages", Category: "core", Description: "Manage messages", Actions: []string{"list", "show", "create", "update", "publish", "pin", "unpin", "trash", "archive", "restore"}},
				{Name: "chat", Category: "core", Description: "Chat in real-time", Actions: []string{"list", "messages", "post", "upload", "line", "delete"}},
				{Name: "cards", Category: "core", Description: "Manage Kanban cards", Actions: []string{"list", "show", "create", "update", "move", "columns", "steps", "trash", "archive", "restore"}},
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
				{Name: "assign", Category: "shortcut", Description: "Assign someone to a to-do, card, or step"},
				{Name: "unassign", Category: "shortcut", Description: "Remove assignment from a to-do, card, or step"},
				{Name: "react", Category: "shortcut", Description: "React with an emoji"},
				{Name: "attach", Category: "shortcut", Description: "Upload and stage an attachment"},
				{Name: "upload", Category: "shortcut", Description: "Upload a file to Docs & Files"},
			},
		},
		{
			Name: "Files & Docs",
			Commands: []CommandInfo{
				{Name: "files", Category: "files", Description: "Manage files, documents, and folders", Actions: []string{"list", "show", "download", "update", "trash", "archive", "restore"}},
				{Name: "uploads", Category: "files", Description: "List and manage uploads", Actions: []string{"list", "show", "download", "update", "trash", "archive", "restore"}},
				{Name: "vaults", Category: "files", Description: "Manage folders (vaults)", Actions: []string{"list", "show", "download", "update", "trash", "archive", "restore"}},
				{Name: "docs", Category: "files", Description: "Manage documents", Actions: []string{"list", "show", "download", "update", "trash", "archive", "restore"}},
			},
		},
		{
			Name: "Scheduling & Time",
			Commands: []CommandInfo{
				{Name: "schedule", Category: "scheduling", Description: "Manage schedule entries", Actions: []string{"show", "entries", "create", "update"}},
				{Name: "timesheet", Category: "scheduling", Description: "Manage time tracking", Actions: []string{"report", "project", "item"}},
				{Name: "checkins", Category: "scheduling", Description: "View automatic check-ins", Actions: []string{"questions", "question", "answers", "answer"}},
				{Name: "timeline", Category: "scheduling", Description: "View activity timelines", Actions: []string{}},
				{Name: "reports", Category: "scheduling", Description: "View reports", Actions: []string{"assignable", "assigned", "overdue", "schedule"}},
				{Name: "assignments", Category: "scheduling", Description: "View my assignments", Actions: []string{"list", "completed", "due"}},
			},
		},
		{
			Name: "Organization",
			Commands: []CommandInfo{
				{Name: "people", Category: "organization", Description: "Manage people and access", Actions: []string{"list", "show", "pingable", "add", "remove"}},
				{Name: "templates", Category: "organization", Description: "Manage project templates", Actions: []string{"list", "show", "create", "update", "delete", "construct"}},
				{Name: "webhooks", Category: "organization", Description: "Manage webhooks", Actions: []string{"list", "show", "create", "update", "delete"}},
				{Name: "lineup", Category: "organization", Description: "Manage lineup markers", Actions: []string{"list", "create", "update", "delete"}},
			},
		},
		{
			Name: "Communication",
			Commands: []CommandInfo{
				{Name: "messageboards", Category: "communication", Description: "View message boards", Actions: []string{"show"}},
				{Name: "messagetypes", Category: "communication", Description: "Manage message categories", Actions: []string{"list", "show", "create", "update", "delete"}},
				{Name: "forwards", Category: "communication", Description: "Manage email forwards (inbox)", Actions: []string{"list", "show", "inbox", "replies", "reply"}},
				{Name: "subscriptions", Category: "communication", Description: "Manage notification subscriptions", Actions: []string{"show", "subscribe", "unsubscribe", "add", "remove"}},
				{Name: "comments", Category: "communication", Description: "Manage comments", Actions: []string{"create", "list", "show", "update", "trash", "archive", "restore"}},
				{Name: "attachments", Category: "communication", Description: "List attachments on items", Actions: []string{"list"}},
				{Name: "boost", Category: "communication", Description: "Manage boosts (reactions)", Actions: []string{"list", "show", "create", "delete"}},
				{Name: "notifications", Category: "communication", Description: "View and manage notifications", Actions: []string{"list", "read"}},
			},
		},
		{
			Name: "Search & Browse",
			Commands: []CommandInfo{
				{Name: "search", Category: "search", Description: "Search across projects"},
				{Name: "recordings", Category: "search", Description: "Browse content by type across projects", Actions: []string{"list", "trash", "archive", "restore", "visibility"}},
				{Name: "show", Category: "search", Description: "Show any item by ID"},
				{Name: "events", Category: "search", Description: "View change history"},
				{Name: "url", Category: "search", Description: "Parse Basecamp URLs"},
			},
		},
		{
			Name: "Auth & Config",
			Commands: []CommandInfo{
				{Name: "accounts", Category: "auth", Description: "Manage accounts", Actions: []string{"list", "use", "show", "update", "logo"}},
				{Name: "auth", Category: "auth", Description: "Authenticate with Basecamp", Actions: []string{"login", "logout", "status", "refresh"}},
				{Name: "login", Category: "auth", Description: "Authenticate with Basecamp"},
				{Name: "logout", Category: "auth", Description: "Remove stored credentials"},
				{Name: "config", Category: "auth", Description: "Manage configuration", Actions: []string{"show", "init", "set", "unset", "project", "trust", "untrust"}},
				{Name: "me", Category: "auth", Description: "Show current user profile"},
				{Name: "setup", Category: "auth", Description: "Interactive first-time setup"},
				{Name: "quick-start", Category: "auth", Description: "Show getting started guide"},
				{Name: "doctor", Category: "auth", Description: "Check CLI health and diagnose issues"},
				{Name: "upgrade", Category: "auth", Description: "Upgrade to the latest version"},
				{Name: "migrate", Category: "auth", Description: "Migrate data from legacy bcq installation"},
				{Name: "profile", Category: "auth", Description: "Manage named profiles", Actions: []string{"list", "show", "create", "delete", "set-default"}},
			},
		},
		{
			Name: "Additional Commands",
			Commands: []CommandInfo{
				{Name: "commands", Category: "additional", Description: "List all commands"},
				{Name: "completion", Category: "additional", Description: "Generate shell completions", Actions: []string{"bash", "zsh", "fish", "powershell", "refresh", "status"}},
				{Name: "tools", Category: "additional", Description: "Manage project dock tools", Actions: []string{"show", "create", "update", "trash", "enable", "disable", "reposition"}},
				{Name: "skill", Category: "additional", Description: "Manage the embedded agent skill file", Actions: []string{"install"}},
				{Name: "tui", Category: "additional", Description: "Launch the Basecamp workspace", Experimental: true, DevOnly: true},
				{Name: "bonfire", Category: "additional", Description: "Multi-chat orchestration", Actions: []string{"split", "layout"}, Experimental: true, DevOnly: true},
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
	categories := CommandCategories()
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
			categories := CommandCategories()

			// For styled terminal output, render grouped columns directly
			if app.Output.EffectiveFormat() == output.FormatStyled {
				renderCommandsStyled(cmd.OutOrStdout(), categories)
				return nil
			}

			return app.OK(categories,
				output.WithSummary("All available basecamp commands"),
			)
		},
	}
}

// renderCommandsStyled writes a grouped command listing with aligned columns.
func renderCommandsStyled(w io.Writer, categories []CommandCategory) {
	bold := lipgloss.NewStyle().Bold(true)
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("#888"))

	devPrefix := "[dev] "
	experimentalPrefix := "[experimental] "

	// Find max widths across all categories for alignment,
	// accounting for badge prefixes in the description column.
	maxName := 0
	maxDesc := 0
	for _, cat := range categories {
		for _, cmd := range cat.Commands {
			if len(cmd.Name) > maxName {
				maxName = len(cmd.Name)
			}
			descWidth := len(cmd.Description)
			if cmd.DevOnly {
				descWidth += len(devPrefix)
			} else if cmd.Experimental {
				descWidth += len(experimentalPrefix)
			}
			if descWidth > maxDesc {
				maxDesc = descWidth
			}
		}
	}

	for i, cat := range categories {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, bold.Render(cat.Name))
		for _, cmd := range cat.Commands {
			actions := ""
			if len(cmd.Actions) > 0 {
				actions = strings.Join(cmd.Actions, ", ")
			}
			desc := cmd.Description
			if cmd.DevOnly {
				desc = devPrefix + desc
			} else if cmd.Experimental {
				desc = experimentalPrefix + desc
			}
			line := fmt.Sprintf("  %-*s  %-*s", maxName, cmd.Name, maxDesc, desc)
			if actions != "" {
				line += "  " + muted.Render(actions)
			}
			fmt.Fprintln(w, line)
		}
	}
}
