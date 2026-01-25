package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// CampfireLine represents a line (message) in a Campfire chat.
type CampfireLine struct {
	ID        int64  `json:"id"`
	Content   string `json:"content"`
	Creator   Person `json:"creator"`
	CreatedAt string `json:"created_at"`
}

// NewCampfireCmd creates the campfire command for real-time chat.
func NewCampfireCmd() *cobra.Command {
	var project string
	var campfireID string

	cmd := &cobra.Command{
		Use:     "campfire [action]",
		Aliases: []string{"chat"},
		Short:   "Interact with Campfire chat",
		Long: `Interact with Campfire (real-time chat).

Use 'bcq campfire list' to see campfires in a project.
Use 'bcq campfire messages' to view recent messages.
Use 'bcq campfire post "message"' to post a message.`,
		Args: cobra.MinimumNArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			// Handle numeric ID as first arg: bcq campfire 123 messages
			if len(args) > 0 && isNumeric(args[0]) {
				campfireID = args[0]
				if len(args) > 1 {
					// Dispatch to subcommand
					switch args[1] {
					case "messages":
						return runCampfireMessages(cmd, app, campfireID, project, 25)
					case "post":
						if len(args) > 2 {
							return runCampfirePost(cmd, app, campfireID, project, args[2])
						}
						return output.ErrUsage("Message content required")
					default:
						return runCampfireMessages(cmd, app, campfireID, project, 25)
					}
				}
				return runCampfireMessages(cmd, app, campfireID, project, 25)
			}

			// Default to list
			return runCampfireList(cmd, app, project, false)
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.PersistentFlags().StringVarP(&campfireID, "campfire", "c", "", "Campfire ID")

	cmd.AddCommand(
		newCampfireListCmd(&project),
		newCampfireMessagesCmd(&project, &campfireID),
		newCampfirePostCmd(&project, &campfireID),
		newCampfireLineShowCmd(&project, &campfireID),
		newCampfireLineDeleteCmd(&project, &campfireID),
	)

	return cmd
}

func newCampfireListCmd(project *string) *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List campfires",
		Long:  "List campfires in a project or account-wide with --all.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}
			return runCampfireList(cmd, app, *project, all)
		},
	}

	cmd.Flags().BoolVarP(&all, "all", "A", false, "List all campfires across account")

	return cmd
}

func runCampfireList(cmd *cobra.Command, app *appctx.App, project string, all bool) error {
	// Account-wide campfire listing
	if all {
		resp, err := app.API.Get(cmd.Context(), "/chats.json")
		if err != nil {
			return err
		}

		var campfires []json.RawMessage
		if err := resp.UnmarshalData(&campfires); err != nil {
			return fmt.Errorf("failed to parse campfires: %w", err)
		}

		summary := fmt.Sprintf("%d campfires", len(campfires))

		return app.Output.OK(campfires,
			output.WithSummary(summary),
			output.WithBreadcrumbs(
				output.Breadcrumb{
					Action:      "messages",
					Cmd:         "bcq campfire <id> messages --in <project>",
					Description: "View messages",
				},
				output.Breadcrumb{
					Action:      "post",
					Cmd:         "bcq campfire <id> post \"message\" --in <project>",
					Description: "Post message",
				},
			),
		)
	}

	// Resolve project
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		return output.ErrUsageHint(
			"--project is required",
			"Use --in <project>, --all for account-wide, or set in .basecamp/config.json",
		)
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	// Get campfire from project dock
	campfireID, err := getCampfireID(cmd, app, resolvedProjectID)
	if err != nil {
		return err
	}

	// Get campfire details
	path := fmt.Sprintf("/buckets/%s/chats/%s.json", resolvedProjectID, campfireID)
	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var campfire map[string]any
	if err := json.Unmarshal(resp.Data, &campfire); err != nil {
		return err
	}

	title := "Campfire"
	if t, ok := campfire["title"].(string); ok && t != "" {
		title = t
	}

	// Return as array for consistency
	result := []json.RawMessage{resp.Data}
	summary := fmt.Sprintf("Campfire: %s", title)

	return app.Output.OK(result,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "messages",
				Cmd:         fmt.Sprintf("bcq campfire %s messages --in %s", campfireID, resolvedProjectID),
				Description: "View messages",
			},
			output.Breadcrumb{
				Action:      "post",
				Cmd:         fmt.Sprintf("bcq campfire %s post \"message\" --in %s", campfireID, resolvedProjectID),
				Description: "Post message",
			},
		),
	)
}

func newCampfireMessagesCmd(project, campfireID *string) *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "messages",
		Short: "View recent messages",
		Long:  "View recent messages from a Campfire.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}
			return runCampfireMessages(cmd, app, *campfireID, *project, limit)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 25, "Number of messages to show")

	return cmd
}

func runCampfireMessages(cmd *cobra.Command, app *appctx.App, campfireID, project string, limit int) error {
	// Resolve project
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		return output.ErrUsage("--project is required")
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	// Get campfire ID from project if not specified
	if campfireID == "" {
		campfireID, err = getCampfireID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	}

	// Get recent messages (lines)
	path := fmt.Sprintf("/buckets/%s/chats/%s/lines.json", resolvedProjectID, campfireID)
	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var messages []json.RawMessage
	if err := resp.UnmarshalData(&messages); err != nil {
		return fmt.Errorf("failed to parse messages: %w", err)
	}

	// Take last N messages
	if limit > 0 && len(messages) > limit {
		messages = messages[len(messages)-limit:]
	}

	summary := fmt.Sprintf("%d messages", len(messages))

	return app.Output.OK(messages,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "post",
				Cmd:         fmt.Sprintf("bcq campfire %s post \"message\" --in %s", campfireID, resolvedProjectID),
				Description: "Post message",
			},
			output.Breadcrumb{
				Action:      "more",
				Cmd:         fmt.Sprintf("bcq campfire %s messages --limit 50 --in %s", campfireID, resolvedProjectID),
				Description: "Load more",
			},
		),
	)
}

func newCampfirePostCmd(project, campfireID *string) *cobra.Command {
	var content string

	cmd := &cobra.Command{
		Use:   "post [message]",
		Short: "Post a message",
		Long:  "Post a message to a Campfire.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			// Validate user input first, before checking account
			messageContent := content
			if len(args) > 0 {
				messageContent = args[0]
			}

			if messageContent == "" {
				return output.ErrUsage("Message content required")
			}

			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			return runCampfirePost(cmd, app, *campfireID, *project, messageContent)
		},
	}

	cmd.Flags().StringVar(&content, "content", "", "Message content")

	return cmd
}

func runCampfirePost(cmd *cobra.Command, app *appctx.App, campfireID, project, content string) error {
	// Resolve project
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		return output.ErrUsage("--project is required")
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	// Get campfire ID from project if not specified
	if campfireID == "" {
		campfireID, err = getCampfireID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	}

	// Post message
	body := map[string]string{
		"content": content,
	}

	path := fmt.Sprintf("/buckets/%s/chats/%s/lines.json", resolvedProjectID, campfireID)
	resp, err := app.API.Post(cmd.Context(), path, body)
	if err != nil {
		return err
	}

	var line struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(resp.Data, &line); err != nil {
		return err
	}

	summary := fmt.Sprintf("Posted message #%d", line.ID)

	return app.Output.OK(json.RawMessage(resp.Data),
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "messages",
				Cmd:         fmt.Sprintf("bcq campfire %s messages --in %s", campfireID, resolvedProjectID),
				Description: "View messages",
			},
			output.Breadcrumb{
				Action:      "post",
				Cmd:         fmt.Sprintf("bcq campfire %s post \"reply\" --in %s", campfireID, resolvedProjectID),
				Description: "Post another",
			},
		),
	)
}

func newCampfireLineShowCmd(project, campfireID *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "line <id>",
		Aliases: []string{"show"},
		Short:   "Show a specific message",
		Long:    "Show details of a specific message line.",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			lineID := args[0]

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Get campfire ID from project if not specified
			effectiveCampfireID := *campfireID
			if effectiveCampfireID == "" {
				effectiveCampfireID, err = getCampfireID(cmd, app, resolvedProjectID)
				if err != nil {
					return err
				}
			}

			path := fmt.Sprintf("/buckets/%s/chats/%s/lines/%s.json", resolvedProjectID, effectiveCampfireID, lineID)
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var line struct {
				Creator struct {
					Name string `json:"name"`
				} `json:"creator"`
			}
			if err := json.Unmarshal(resp.Data, &line); err != nil {
				return err
			}

			summary := fmt.Sprintf("Line #%s by %s", lineID, line.Creator.Name)

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "delete",
						Cmd:         fmt.Sprintf("bcq campfire delete %s --campfire %s --in %s", lineID, effectiveCampfireID, resolvedProjectID),
						Description: "Delete line",
					},
					output.Breadcrumb{
						Action:      "messages",
						Cmd:         fmt.Sprintf("bcq campfire %s messages --in %s", effectiveCampfireID, resolvedProjectID),
						Description: "Back to messages",
					},
				),
			)
		},
	}
	return cmd
}

func newCampfireLineDeleteCmd(project, campfireID *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a message",
		Long:  "Delete a message line from a Campfire.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			lineID := args[0]

			// Resolve project
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				return output.ErrUsage("--project is required")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Get campfire ID from project if not specified
			effectiveCampfireID := *campfireID
			if effectiveCampfireID == "" {
				effectiveCampfireID, err = getCampfireID(cmd, app, resolvedProjectID)
				if err != nil {
					return err
				}
			}

			path := fmt.Sprintf("/buckets/%s/chats/%s/lines/%s.json", resolvedProjectID, effectiveCampfireID, lineID)
			_, err = app.API.Delete(cmd.Context(), path)
			if err != nil {
				return err
			}

			summary := fmt.Sprintf("Deleted line #%s", lineID)

			return app.Output.OK(map[string]any{},
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "messages",
						Cmd:         fmt.Sprintf("bcq campfire %s messages --in %s", effectiveCampfireID, resolvedProjectID),
						Description: "Back to messages",
					},
				),
			)
		},
	}
	return cmd
}

// getCampfireID retrieves the campfire ID from a project's dock, handling multi-dock projects.
func getCampfireID(cmd *cobra.Command, app *appctx.App, projectID string) (string, error) {
	return getDockToolID(cmd.Context(), app, projectID, "chat", "", "campfire")
}
