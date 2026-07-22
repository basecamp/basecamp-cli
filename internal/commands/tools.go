package commands

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewToolsCmd creates the tools command for managing project dock tools.
func NewToolsCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:   "tools [action]",
		Short: "Manage project dock tools",
		Long: `Manage project dock tools (Chat, Schedule, Docs & Files, etc.).

Every project has a "dock" with tools like Message Board, To-dos, Docs & Files,
Chat, Schedule, etc. Tool IDs can be found in the project's dock array
(see 'basecamp projects show <id>').

Tools are created by type (e.g., add a second chat with --type chat).
Disabling a tool hides it from the dock but preserves its content.`,
		Annotations: map[string]string{"agent_notes": fmt.Sprintf(
			"Dock tools are the sidebar navigation items in a project\n"+
				"Enable/disable controls visibility without deleting\n"+
				"Create by type with --type: %s (create-by-type is BC5-only)",
			strings.Join(toolTypeFriendlyNames(), ", "))},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name (for breadcrumbs)")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID or name (alias for --project)")

	cmd.AddCommand(
		newToolsShowCmd(&project),
		newToolsCreateCmd(&project),
		newToolsUpdateCmd(&project),
		newToolsTrashCmd(&project),
		newToolsEnableCmd(&project),
		newToolsDisableCmd(&project),
		newToolsRepositionCmd(&project),
	)

	return cmd
}

// resolveToolsProject optionally resolves a project for breadcrumb display.
// Returns the resolved project ID string, or "" if no project was specified.
// If the user explicitly provided a project via flag (--in/--project) that
// cannot be resolved, the error is returned. Config defaults are best-effort:
// resolution failures are silently ignored since the user didn't ask for
// project context on this invocation.
func resolveToolsProject(cmd *cobra.Command, app *appctx.App, project string) (string, error) {
	explicit := project != ""

	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
		if projectID != "" {
			explicit = true
		}
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		return "", nil
	}

	resolved, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		if explicit {
			return "", err
		}
		return "", nil
	}
	return resolved, nil
}

// toolBreadcrumbFlag returns " --in <id>" if projectID is non-empty, or "".
func toolBreadcrumbFlag(projectID string) string {
	if projectID == "" {
		return ""
	}
	return " --in " + projectID
}

func newToolsShowCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show tool details",
		Long:  "Display detailed information about a dock tool.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			toolID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid tool ID")
			}

			resolvedProjectID, err := resolveToolsProject(cmd, app, *project)
			if err != nil {
				return err
			}
			inFlag := toolBreadcrumbFlag(resolvedProjectID)

			tool, err := app.Account().Tools().Get(cmd.Context(), toolID)
			if err != nil {
				return convertSDKError(err)
			}

			posStr := "disabled"
			if tool.Position != nil {
				posStr = fmt.Sprintf("%d", *tool.Position)
			}
			summary := fmt.Sprintf("%s (%s) at position %s", tool.Title, tool.Name, posStr)

			crumbs := []output.Breadcrumb{
				{
					Action:      "rename",
					Cmd:         fmt.Sprintf("basecamp tools update %d \"New Name\"%s", toolID, inFlag),
					Description: "Rename tool",
				},
				{
					Action:      "reposition",
					Cmd:         fmt.Sprintf("basecamp tools reposition %d --position 1%s", toolID, inFlag),
					Description: "Move tool",
				},
			}
			if resolvedProjectID != "" {
				crumbs = append(crumbs, output.Breadcrumb{
					Action:      "project",
					Cmd:         fmt.Sprintf("basecamp projects show %s", resolvedProjectID),
					Description: "View project",
				})
			}

			return app.OK(tool,
				output.WithSummary(summary),
				output.WithBreadcrumbs(crumbs...),
			)
		},
	}
}

// toolTypes is the single source of truth for the closed set of dock tool
// types create-by-type accepts. Its order is deterministic and drives three
// things that must never drift: normalization, --type completion, and the
// unknown-type error listing. friendly is the primary dock noun surfaced in
// completion and errors; canonical is the Rails class-name sent to the API.
var toolTypes = []struct {
	canonical string   // Rails class → the tool_type posted to the API
	friendly  string   // primary dock noun (completion + error list)
	aliases   []string // extra accepted forms (matched after collapse)
}{
	{"Chat::Transcript", "chat", []string{"campfire"}},
	{"Inbox", "inbox", []string{"forwards", "email"}},
	{"Kanban::Board", "kanban_board", []string{"kanban", "cardtable", "cards", "card"}},
	{"Message::Board", "message_board", []string{"messageboard", "messages", "message"}},
	{"Questionnaire", "questionnaire", []string{"questions", "checkin", "checkins", "automaticcheckins"}},
	{"Schedule", "schedule", []string{"calendar"}},
	{"Todoset", "todoset", []string{"todosets", "todos", "todo", "todolist"}},
	{"Vault", "vault", []string{"docs", "doc", "documents", "files"}},
}

// collapseToolType reduces an input to a comparison key: lowercased with all
// separators (::, -, _, spaces) removed. This lets "Message::Board",
// "message_board", and "message-board" all match the same entry.
func collapseToolType(input string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(input) {
		switch r {
		case ':', '-', '_', ' ':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// normalizeToolType maps a friendly noun, canonical class-name, or accepted
// degenerate spelling to the canonical tool_type. Strict: unknown input returns
// ok=false. The closed 8-set plus the API's opaque 400 on a bad value justify
// rejecting rather than passing input through (unlike normalizeRecordingType).
func normalizeToolType(input string) (canonical string, ok bool) {
	key := collapseToolType(input)
	if key == "" {
		return "", false
	}
	for _, t := range toolTypes {
		if key == collapseToolType(t.canonical) || key == collapseToolType(t.friendly) {
			return t.canonical, true
		}
		for _, alias := range t.aliases {
			if key == collapseToolType(alias) {
				return t.canonical, true
			}
		}
	}
	return "", false
}

// toolTypeFriendlyNames returns the friendly nouns in stable slice order, used
// for both --type completion and the unknown-type error listing.
func toolTypeFriendlyNames() []string {
	names := make([]string, len(toolTypes))
	for i, t := range toolTypes {
		names[i] = t.friendly
	}
	return names
}

// resolveToolBucketID resolves the numeric destination bucket for create.
// Unlike the breadcrumb-only resolveToolsProject, this bucket is sent to the
// API, so a project is required. Precedence: --in/--project > --project global
// flag > config default > interactive. Returns the bucket ID and the resolved
// project ID string (for breadcrumbs).
func resolveToolBucketID(cmd *cobra.Command, app *appctx.App, project string) (int64, string, error) {
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return 0, "", err
		}
		projectID = app.Config.ProjectID
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return 0, "", err
	}

	bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return 0, "", output.ErrUsage("Project ID must be numeric")
	}
	return bucketID, resolvedProjectID, nil
}

func newToolsCreateCmd(project *string) *cobra.Command {
	var toolType string

	cmd := &cobra.Command{
		Use:   "create [title]",
		Short: "Create a new dock tool by type",
		Long: fmt.Sprintf(`Create a new dock tool by type in a project's dock.

For example, add a second chat with --type chat, or a Message Board with
--type message_board. An optional title may be given; without one, Basecamp
assigns the default title for the type.

Accepted types: %s. Create-by-type is BC5-only.`,
			strings.Join(toolTypeFriendlyNames(), ", ")),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if toolType == "" {
				return output.ErrUsage("--type is required")
			}

			canonicalType, ok := normalizeToolType(toolType)
			if !ok {
				if collapseToolType(toolType) == "board" {
					return output.ErrUsage(fmt.Sprintf(
						"Ambiguous --type %q — use message_board or kanban_board (accepted: %s)",
						toolType, strings.Join(toolTypeFriendlyNames(), ", ")))
				}
				return output.ErrUsage(fmt.Sprintf("Unknown --type %q (accepted: %s)",
					toolType, strings.Join(toolTypeFriendlyNames(), ", ")))
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			title := ""
			if len(args) > 0 {
				title = args[0]
			}

			if title != "" {
				if n := utf8.RuneCountInString(title); n > 64 {
					return output.ErrUsage(fmt.Sprintf("Tool name too long (%d characters, max 64)", n))
				}
			}

			bucketID, resolvedProjectID, err := resolveToolBucketID(cmd, app, *project)
			if err != nil {
				return err
			}
			inFlag := toolBreadcrumbFlag(resolvedProjectID)

			var opts *basecamp.CreateToolOptions
			if title != "" {
				opts = &basecamp.CreateToolOptions{Title: title}
			}

			created, err := app.Account().Tools().Create(cmd.Context(), bucketID, canonicalType, opts)
			if err != nil {
				return convertSDKError(err)
			}

			crumbs := []output.Breadcrumb{
				{
					Action:      "tool",
					Cmd:         fmt.Sprintf("basecamp tools show %d%s", created.ID, inFlag),
					Description: "View tool",
				},
				{
					Action:      "project",
					Cmd:         fmt.Sprintf("basecamp projects show %s", resolvedProjectID),
					Description: "View project",
				},
			}

			return app.OK(created,
				output.WithSummary(fmt.Sprintf("Created: %s", created.Title)),
				output.WithBreadcrumbs(crumbs...),
			)
		},
	}

	cmd.Flags().StringVarP(&toolType, "type", "t", "", "Tool type to create (required)")
	_ = cmd.RegisterFlagCompletionFunc("type", func(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		return toolTypeFriendlyNames(), cobra.ShellCompDirectiveNoFileComp
	})

	return cmd
}

func newToolsUpdateCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "update <id> <title>",
		Aliases: []string{"rename"},
		Short:   "Rename a dock tool",
		Long:    "Update a dock tool's title.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked with insufficient arguments
			if len(args) == 0 {
				return missingArg(cmd, "<id>")
			}
			if len(args) < 2 {
				return missingArg(cmd, "<title>")
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			toolID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid tool ID")
			}

			title := args[1]

			if n := utf8.RuneCountInString(title); n > 64 {
				return output.ErrUsage(fmt.Sprintf("Tool name too long (%d characters, max 64)", n))
			}

			resolvedProjectID, err := resolveToolsProject(cmd, app, *project)
			if err != nil {
				return err
			}
			inFlag := toolBreadcrumbFlag(resolvedProjectID)

			tool, err := app.Account().Tools().Update(cmd.Context(), toolID, title)
			if err != nil {
				return convertSDKError(err)
			}

			crumbs := []output.Breadcrumb{
				{
					Action:      "tool",
					Cmd:         fmt.Sprintf("basecamp tools show %d%s", toolID, inFlag),
					Description: "View tool",
				},
			}
			if resolvedProjectID != "" {
				crumbs = append(crumbs, output.Breadcrumb{
					Action:      "project",
					Cmd:         fmt.Sprintf("basecamp projects show %s", resolvedProjectID),
					Description: "View project",
				})
			}

			return app.OK(tool,
				output.WithSummary(fmt.Sprintf("Renamed to: %s", tool.Title)),
				output.WithBreadcrumbs(crumbs...),
			)
		},
	}

	return cmd
}

func newToolsTrashCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "trash <id>",
		Aliases: []string{"delete"},
		Short:   "Permanently trash a dock tool",
		Long: `Permanently trash a dock tool.

WARNING: This permanently removes the tool and all its content.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			toolID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid tool ID")
			}

			resolvedProjectID, err := resolveToolsProject(cmd, app, *project)
			if err != nil {
				return err
			}

			err = app.Account().Tools().Delete(cmd.Context(), toolID)
			if err != nil {
				return convertSDKError(err)
			}

			var crumbs []output.Breadcrumb
			if resolvedProjectID != "" {
				crumbs = append(crumbs, output.Breadcrumb{
					Action:      "project",
					Cmd:         fmt.Sprintf("basecamp projects show %s", resolvedProjectID),
					Description: "View project",
				})
			}

			return app.OK(map[string]any{"trashed": true},
				output.WithSummary(fmt.Sprintf("Tool %d trashed", toolID)),
				output.WithBreadcrumbs(crumbs...),
			)
		},
	}

	return cmd
}

func newToolsEnableCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "enable <id>",
		Short: "Enable a tool in the dock",
		Long:  "Enable a tool to make it visible in the project dock.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			toolID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid tool ID")
			}

			resolvedProjectID, err := resolveToolsProject(cmd, app, *project)
			if err != nil {
				return err
			}
			inFlag := toolBreadcrumbFlag(resolvedProjectID)

			err = app.Account().Tools().Enable(cmd.Context(), toolID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"enabled": true},
				output.WithSummary(fmt.Sprintf("Tool %d enabled in dock", toolID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "tool",
						Cmd:         fmt.Sprintf("basecamp tools show %d%s", toolID, inFlag),
						Description: "View tool",
					},
				),
			)
		},
	}
}

func newToolsDisableCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "disable <id>",
		Short: "Disable a tool (hide from dock)",
		Long: `Disable a tool to hide it from the project dock.

The tool is not deleted - just hidden. Use 'basecamp tools enable' to restore.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			toolID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid tool ID")
			}

			resolvedProjectID, err := resolveToolsProject(cmd, app, *project)
			if err != nil {
				return err
			}
			inFlag := toolBreadcrumbFlag(resolvedProjectID)

			err = app.Account().Tools().Disable(cmd.Context(), toolID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"disabled": true},
				output.WithSummary(fmt.Sprintf("Tool %d disabled (hidden from dock)", toolID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "enable",
						Cmd:         fmt.Sprintf("basecamp tools enable %d%s", toolID, inFlag),
						Description: "Re-enable tool",
					},
				),
			)
		},
	}
}

func newToolsRepositionCmd(project *string) *cobra.Command {
	var position int

	cmd := &cobra.Command{
		Use:     "reposition <id>",
		Aliases: []string{"move"},
		Short:   "Change a tool's position in the dock",
		Long:    "Move a tool to a different position in the project dock.",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if position == 0 {
				return output.ErrUsage("--position is required (1-based)")
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			toolID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid tool ID")
			}

			resolvedProjectID, err := resolveToolsProject(cmd, app, *project)
			if err != nil {
				return err
			}
			inFlag := toolBreadcrumbFlag(resolvedProjectID)

			err = app.Account().Tools().Reposition(cmd.Context(), toolID, position)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"repositioned": true, "position": position},
				output.WithSummary(fmt.Sprintf("Tool %d moved to position %d", toolID, position)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "tool",
						Cmd:         fmt.Sprintf("basecamp tools show %d%s", toolID, inFlag),
						Description: "View tool",
					},
				),
			)
		},
	}

	cmd.Flags().IntVar(&position, "position", 0, "New position, 1-based (required)")
	cmd.Flags().IntVar(&position, "pos", 0, "New position (alias)")

	return cmd
}
