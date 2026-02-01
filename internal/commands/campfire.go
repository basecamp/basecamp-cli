package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

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
			if err := ensureAccount(cmd, app); err != nil {
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
			if err := ensureAccount(cmd, app); err != nil {
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
		result, err := app.Account().Campfires().List(cmd.Context())
		if err != nil {
			return err
		}
		campfires := result.Campfires

		summary := fmt.Sprintf("%d campfires", len(campfires))

		return app.OK(campfires,
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
		if err := ensureProject(cmd, app); err != nil {
			return err
		}
		projectID = app.Config.ProjectID
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	// Get campfire from project dock
	campfireIDStr, err := getCampfireID(cmd, app, resolvedProjectID)
	if err != nil {
		return err
	}

	bucketID, _ := strconv.ParseInt(resolvedProjectID, 10, 64)
	campfireIDInt, _ := strconv.ParseInt(campfireIDStr, 10, 64)

	// Get campfire details using SDK
	campfire, err := app.Account().Campfires().Get(cmd.Context(), bucketID, campfireIDInt)
	if err != nil {
		return err
	}

	title := "Campfire"
	if campfire.Title != "" {
		title = campfire.Title
	}

	// Return as array for consistency
	result := []any{campfire}
	summary := fmt.Sprintf("Campfire: %s", title)

	return app.OK(result,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "messages",
				Cmd:         fmt.Sprintf("bcq campfire %s messages --in %s", campfireIDStr, resolvedProjectID),
				Description: "View messages",
			},
			output.Breadcrumb{
				Action:      "post",
				Cmd:         fmt.Sprintf("bcq campfire %s post \"message\" --in %s", campfireIDStr, resolvedProjectID),
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
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			return runCampfireMessages(cmd, app, *campfireID, *project, limit)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 25, "Number of messages to show")

	return cmd
}

func runCampfireMessages(cmd *cobra.Command, app *appctx.App, campfireID, project string, limit int) error {
	// Resolve project, with interactive fallback
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return err
		}
		projectID = app.Config.ProjectID
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

	bucketID, _ := strconv.ParseInt(resolvedProjectID, 10, 64)
	campfireIDInt, _ := strconv.ParseInt(campfireID, 10, 64)

	// Get recent messages (lines) using SDK
	result, err := app.Account().Campfires().ListLines(cmd.Context(), bucketID, campfireIDInt)
	if err != nil {
		return err
	}
	lines := result.Lines

	// Take last N messages
	if limit > 0 && len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}

	summary := fmt.Sprintf("%d messages", len(lines))

	return app.OK(lines,
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

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			return runCampfirePost(cmd, app, *campfireID, *project, messageContent)
		},
	}

	cmd.Flags().StringVar(&content, "content", "", "Message content")

	return cmd
}

func runCampfirePost(cmd *cobra.Command, app *appctx.App, campfireID, project, content string) error {
	// Resolve project, with interactive fallback
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return err
		}
		projectID = app.Config.ProjectID
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

	bucketID, _ := strconv.ParseInt(resolvedProjectID, 10, 64)
	campfireIDInt, _ := strconv.ParseInt(campfireID, 10, 64)

	// Post message using SDK
	// Content is plain text (API forces text-only lines) - do not wrap in HTML
	line, err := app.Account().Campfires().CreateLine(cmd.Context(), bucketID, campfireIDInt, content)
	if err != nil {
		return err
	}

	summary := fmt.Sprintf("Posted message #%d", line.ID)

	return app.OK(line,
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
		Use:     "line <id|url>",
		Aliases: []string{"show"},
		Short:   "Show a specific message",
		Long: `Show details of a specific message line.

You can pass either a line ID or a Basecamp line URL:
  bcq campfire line 789 --in my-project
  bcq campfire line https://3.basecamp.com/123/buckets/456/chats/789/lines/111`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID and project from URL if provided
			lineID, urlProjectID := extractWithProject(args[0])

			// Resolve project - use URL > flag > config, with interactive fallback
			projectID := *project
			if projectID == "" && urlProjectID != "" {
				projectID = urlProjectID
			}
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
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

			bucketID, _ := strconv.ParseInt(resolvedProjectID, 10, 64)
			campfireIDInt, _ := strconv.ParseInt(effectiveCampfireID, 10, 64)
			lineIDInt, _ := strconv.ParseInt(lineID, 10, 64)

			// Get line using SDK
			line, err := app.Account().Campfires().GetLine(cmd.Context(), bucketID, campfireIDInt, lineIDInt)
			if err != nil {
				return err
			}

			creatorName := ""
			if line.Creator != nil {
				creatorName = line.Creator.Name
			}
			summary := fmt.Sprintf("Line #%s by %s", lineID, creatorName)

			return app.OK(line,
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
		Use:   "delete <id|url>",
		Short: "Delete a message",
		Long: `Delete a message line from a Campfire.

You can pass either a line ID or a Basecamp line URL:
  bcq campfire delete 789 --in my-project
  bcq campfire delete https://3.basecamp.com/123/buckets/456/chats/789/lines/111`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID and project from URL if provided
			lineID, urlProjectID := extractWithProject(args[0])

			// Resolve project - use URL > flag > config, with interactive fallback
			projectID := *project
			if projectID == "" && urlProjectID != "" {
				projectID = urlProjectID
			}
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
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

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}
			campfireIDInt, err := strconv.ParseInt(effectiveCampfireID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid campfire ID")
			}
			lineIDInt, err := strconv.ParseInt(lineID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid line ID")
			}

			// Delete line using SDK
			err = app.Account().Campfires().DeleteLine(cmd.Context(), bucketID, campfireIDInt, lineIDInt)
			if err != nil {
				return err
			}

			summary := fmt.Sprintf("Deleted line #%s", lineID)

			return app.OK(map[string]any{},
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
