package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// NewCampfireCmd creates the campfire command for real-time chat.
func NewCampfireCmd() *cobra.Command {
	var project string
	var campfireID string
	var contentType string

	cmd := &cobra.Command{
		Use:     "campfire [action]",
		Aliases: []string{"chat"},
		Short:   "Interact with Campfire chat",
		Long: `Interact with Campfire (real-time chat).

Use 'basecamp campfire list' to see campfires in a project.
Use 'basecamp campfire messages' to view recent messages.
Use 'basecamp campfire post "message"' to post a message.`,
		Annotations: map[string]string{"agent_notes": "Projects may have multiple campfires; use `campfire list` to see them\nContent supports Markdown — converted to HTML automatically\nCampfire is project-scoped, no cross-project campfire queries"},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.PersistentFlags().StringVarP(&campfireID, "campfire", "c", "", "Campfire ID")
	cmd.AddCommand(
		newCampfireListCmd(&project, &campfireID),
		newCampfireMessagesCmd(&project, &campfireID),
		newCampfirePostCmd(&project, &campfireID, &contentType),
		newCampfireUploadCmd(&project, &campfireID),
		newCampfireLineShowCmd(&project, &campfireID),
		newCampfireLineDeleteCmd(&project, &campfireID),
	)

	return cmd
}

func newCampfireListCmd(project, campfireID *string) *cobra.Command {
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
			return runCampfireList(cmd, app, *project, *campfireID, all)
		},
	}

	cmd.Flags().BoolVarP(&all, "all", "A", false, "List all campfires across account")

	return cmd
}

func runCampfireList(cmd *cobra.Command, app *appctx.App, project, campfireID string, all bool) error {
	// Account-wide campfire listing
	if all {
		result, err := app.Account().Campfires().List(cmd.Context(), nil)
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
					Cmd:         "basecamp campfire <id> messages --in <project>",
					Description: "View messages",
				},
				output.Breadcrumb{
					Action:      "post",
					Cmd:         "basecamp campfire <id> post \"message\" --in <project>",
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

	// If a specific campfire ID was given via -c, fetch just that one
	if campfireID != "" {
		campfireIDInt, parseErr := strconv.ParseInt(campfireID, 10, 64)
		if parseErr != nil {
			return output.ErrUsage("Invalid campfire ID")
		}

		campfire, getErr := app.Account().Campfires().Get(cmd.Context(), campfireIDInt)
		if getErr != nil {
			return getErr
		}

		return app.OK([]*basecamp.Campfire{campfire},
			output.WithSummary(fmt.Sprintf("Campfire: %s", campfireTitle(campfire))),
			output.WithBreadcrumbs(campfireListBreadcrumbs(campfireID, resolvedProjectID)...),
		)
	}

	// Fetch project dock and find all chat entries (supports multi-campfire projects)
	path := fmt.Sprintf("/projects/%s.json", resolvedProjectID)
	resp, err := app.Account().Get(cmd.Context(), path)
	if err != nil {
		return convertSDKError(err)
	}

	var projectData struct {
		Dock []DockTool `json:"dock"`
	}
	if err := json.Unmarshal(resp.Data, &projectData); err != nil {
		return fmt.Errorf("failed to parse project: %w", err)
	}

	// Collect enabled chat dock entries and fetch full campfire details
	var campfires []*basecamp.Campfire
	var hasDisabled bool
	for _, tool := range projectData.Dock {
		if tool.Name != "chat" {
			continue
		}
		if !tool.Enabled {
			hasDisabled = true
			continue
		}
		campfire, getErr := app.Account().Campfires().Get(cmd.Context(), tool.ID)
		if getErr != nil {
			return getErr
		}
		campfires = append(campfires, campfire)
	}

	if len(campfires) == 0 {
		hint := "Project has no campfire"
		if hasDisabled {
			hint = "Campfire is disabled for this project"
		}
		return output.ErrNotFoundHint("campfire", resolvedProjectID, hint)
	}

	summary := fmt.Sprintf("%d campfire(s)", len(campfires))

	return app.OK(campfires,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "messages",
				Cmd:         fmt.Sprintf("basecamp campfire messages -c <id> --in %s", resolvedProjectID),
				Description: "View messages",
			},
			output.Breadcrumb{
				Action:      "post",
				Cmd:         fmt.Sprintf("basecamp campfire post \"message\" -c <id> --in %s", resolvedProjectID),
				Description: "Post message",
			},
		),
	)
}

func campfireTitle(c *basecamp.Campfire) string {
	if c.Title != "" {
		return c.Title
	}
	return "Campfire"
}

func campfireListBreadcrumbs(campfireID, projectID string) []output.Breadcrumb {
	return []output.Breadcrumb{
		{
			Action:      "messages",
			Cmd:         fmt.Sprintf("basecamp campfire messages -c %s --in %s", campfireID, projectID),
			Description: "View messages",
		},
		{
			Action:      "post",
			Cmd:         fmt.Sprintf("basecamp campfire post \"message\" -c %s --in %s", campfireID, projectID),
			Description: "Post message",
		},
	}
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

	campfireIDInt, err := strconv.ParseInt(campfireID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid campfire ID")
	}

	// Get recent messages (lines) using SDK
	result, err := app.Account().Campfires().ListLines(cmd.Context(), campfireIDInt, nil)
	if err != nil {
		return err
	}
	lines := result.Lines

	// Take last N messages (newest)
	if limit > 0 && len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}

	summary := fmt.Sprintf("%d messages", len(lines))

	return app.OK(lines,
		output.WithSummary(summary),
		output.WithEntity("campfire_line"),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "post",
				Cmd:         fmt.Sprintf("basecamp campfire %s post \"message\" --in %s", campfireID, resolvedProjectID),
				Description: "Post message",
			},
			output.Breadcrumb{
				Action:      "more",
				Cmd:         fmt.Sprintf("basecamp campfire %s messages --limit 50 --in %s", campfireID, resolvedProjectID),
				Description: "Load more",
			},
		),
	)
}

func newCampfirePostCmd(project, campfireID, contentType *string) *cobra.Command {
	var content string

	cmd := &cobra.Command{
		Use:   "post <message>",
		Short: "Post a message",
		Long: `Post a message to a Campfire.

By default, messages are sent as plain text. Use --content-type text/html
for rich text (HTML) messages.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			// Validate user input first, before checking account
			messageContent := content
			if len(args) > 0 {
				messageContent = args[0]
			}

			// Show help when invoked with no message content
			if strings.TrimSpace(messageContent) == "" {
				return missingArg(cmd, "<message>")
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			return runCampfirePost(cmd, app, *campfireID, *project, messageContent, *contentType)
		},
	}

	cmd.Flags().StringVar(&content, "content", "", "Message content")
	cmd.Flags().StringVar(contentType, "content-type", "", "Content type (text/html for rich text)")

	return cmd
}

func runCampfirePost(cmd *cobra.Command, app *appctx.App, campfireID, project, content, contentType string) error {
	// Resolve project only when needed (campfire ID not provided, or for breadcrumbs)
	var resolvedProjectID string
	if campfireID == "" {
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

		var err error
		resolvedProjectID, _, err = app.Names.ResolveProject(cmd.Context(), projectID)
		if err != nil {
			return err
		}

		campfireID, err = getCampfireID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	}

	campfireIDInt, err := strconv.ParseInt(campfireID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid campfire ID")
	}

	// Post message using SDK
	var opts *basecamp.CreateLineOptions
	if contentType != "" {
		opts = &basecamp.CreateLineOptions{ContentType: contentType}
	}
	line, err := app.Account().Campfires().CreateLine(cmd.Context(), campfireIDInt, content, opts)
	if err != nil {
		return err
	}

	summary := fmt.Sprintf("Posted message #%d", line.ID)

	// Build breadcrumbs — include project context if resolved
	var breadcrumbs []output.Breadcrumb
	if resolvedProjectID != "" {
		breadcrumbs = append(breadcrumbs,
			output.Breadcrumb{
				Action:      "messages",
				Cmd:         fmt.Sprintf("basecamp campfire %s messages --in %s", campfireID, resolvedProjectID),
				Description: "View messages",
			},
			output.Breadcrumb{
				Action:      "post",
				Cmd:         fmt.Sprintf("basecamp campfire %s post \"reply\" --in %s", campfireID, resolvedProjectID),
				Description: "Post another",
			},
		)
	} else {
		breadcrumbs = append(breadcrumbs,
			output.Breadcrumb{
				Action:      "messages",
				Cmd:         fmt.Sprintf("basecamp campfire %s messages", campfireID),
				Description: "View messages",
			},
			output.Breadcrumb{
				Action:      "post",
				Cmd:         fmt.Sprintf("basecamp campfire %s post \"reply\"", campfireID),
				Description: "Post another",
			},
		)
	}

	return app.OK(line,
		output.WithSummary(summary),
		output.WithEntity("campfire_line"),
		output.WithBreadcrumbs(breadcrumbs...),
	)
}

func newCampfireUploadCmd(project, campfireID *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "upload <file>",
		Short: "Upload a file to Campfire",
		Long: `Upload a file directly to a Campfire chat room.

The file is uploaded as a campfire line (chat message with an attachment).`,
		Example: `  basecamp campfire upload ./screenshot.png --in my-project`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			return runCampfireUpload(cmd, app, *campfireID, *project, args[0])
		},
	}
	return cmd
}

func runCampfireUpload(cmd *cobra.Command, app *appctx.App, campfireID, project, filePath string) error {
	// Normalize drag/paste paths and validate
	filePath = richtext.NormalizeDragPath(filePath)
	if err := richtext.ValidateFile(filePath); err != nil {
		return fmt.Errorf("%s: %w", filePath, err)
	}

	// Resolve project — required when campfire ID not provided, optional for breadcrumbs
	var resolvedProjectID string
	if campfireID == "" {
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

		var err error
		resolvedProjectID, _, err = app.Names.ResolveProject(cmd.Context(), projectID)
		if err != nil {
			return err
		}

		campfireID, err = getCampfireID(cmd, app, resolvedProjectID)
		if err != nil {
			return err
		}
	} else if project != "" {
		// Campfire ID provided directly — still resolve project for breadcrumbs
		var err error
		resolvedProjectID, _, err = app.Names.ResolveProject(cmd.Context(), project)
		if err != nil {
			return err
		}
	}

	campfireIDInt, err := strconv.ParseInt(campfireID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid campfire ID")
	}

	contentType := richtext.DetectMIME(filePath)
	filename := filepath.Base(filePath)

	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("%s: %w", filePath, err)
	}
	defer f.Close()

	line, err := app.Account().Campfires().CreateUpload(cmd.Context(), campfireIDInt, filename, contentType, f)
	if err != nil {
		return convertSDKError(err)
	}

	// Build breadcrumbs
	var breadcrumbs []output.Breadcrumb
	if resolvedProjectID != "" {
		breadcrumbs = append(breadcrumbs,
			output.Breadcrumb{
				Action:      "messages",
				Cmd:         fmt.Sprintf("basecamp campfire %s messages --in %s", campfireID, resolvedProjectID),
				Description: "View messages",
			},
		)
	} else {
		breadcrumbs = append(breadcrumbs,
			output.Breadcrumb{
				Action:      "messages",
				Cmd:         fmt.Sprintf("basecamp campfire %s messages", campfireID),
				Description: "View messages",
			},
		)
	}

	return app.OK(line,
		output.WithSummary(fmt.Sprintf("Uploaded %s (#%d)", filename, line.ID)),
		output.WithBreadcrumbs(breadcrumbs...),
	)
}

func newCampfireLineShowCmd(project, campfireID *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "line <id|url>",
		Aliases: []string{"show"},
		Short:   "Show a specific message",
		Long: `Show details of a specific message line.

You can pass either a line ID or a Basecamp line URL:
  basecamp campfire line 789 --in my-project
  basecamp campfire line https://3.basecamp.com/123/buckets/456/chats/789/lines/111`,
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

			campfireIDInt, err := strconv.ParseInt(effectiveCampfireID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid campfire ID")
			}
			lineIDInt, err := strconv.ParseInt(lineID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid line ID")
			}

			// Get line using SDK
			line, err := app.Account().Campfires().GetLine(cmd.Context(), campfireIDInt, lineIDInt)
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
				output.WithEntity("campfire_line"),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "delete",
						Cmd:         fmt.Sprintf("basecamp campfire delete %s --campfire %s --in %s", lineID, effectiveCampfireID, resolvedProjectID),
						Description: "Delete line",
					},
					output.Breadcrumb{
						Action:      "messages",
						Cmd:         fmt.Sprintf("basecamp campfire %s messages --in %s", effectiveCampfireID, resolvedProjectID),
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
  basecamp campfire delete 789 --in my-project
  basecamp campfire delete https://3.basecamp.com/123/buckets/456/chats/789/lines/111`,
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

			campfireIDInt, err := strconv.ParseInt(effectiveCampfireID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid campfire ID")
			}
			lineIDInt, err := strconv.ParseInt(lineID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid line ID")
			}

			// Delete line using SDK
			err = app.Account().Campfires().DeleteLine(cmd.Context(), campfireIDInt, lineIDInt)
			if err != nil {
				return err
			}

			summary := fmt.Sprintf("Deleted line #%s", lineID)

			return app.OK(map[string]any{},
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "messages",
						Cmd:         fmt.Sprintf("basecamp campfire %s messages --in %s", effectiveCampfireID, resolvedProjectID),
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
