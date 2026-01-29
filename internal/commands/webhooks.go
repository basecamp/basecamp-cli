package commands

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// NewWebhooksCmd creates the webhooks command group.
func NewWebhooksCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:     "webhooks",
		Aliases: []string{"webhook"},
		Short:   "Manage webhooks",
		Long: `Manage webhooks for project notifications.

Event types: Todo, Todolist, Message, Comment, Document, Upload,
Vault, Schedule::Entry, Kanban::Card, Question, Question::Answer`,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if app == nil {
				return fmt.Errorf("app not initialized")
			}
			return ensureAccount(cmd, app)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Check for unknown subcommand
			if len(args) > 0 {
				return output.ErrUsageHint(
					"Unknown webhooks action: "+args[0],
					"Run 'bcq webhooks -h' for available commands",
				)
			}
			// Default to list when called without subcommand
			return runWebhooksList(cmd, project)
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	cmd.AddCommand(
		newWebhooksListCmd(&project),
		newWebhooksShowCmd(&project),
		newWebhooksCreateCmd(&project),
		newWebhooksUpdateCmd(&project),
		newWebhooksDeleteCmd(&project),
	)

	return cmd
}

func newWebhooksListCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List webhooks",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWebhooksList(cmd, *project)
		},
	}
}

func runWebhooksList(cmd *cobra.Command, project string) error {
	app := appctx.FromContext(cmd.Context())

	// Resolve account (enables interactive prompt if needed)
	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Resolve project from CLI flags and config, with interactive fallback
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}

	// If no project specified, try interactive resolution
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

	bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}

	webhooks, err := app.Account().Webhooks().List(cmd.Context(), bucketID)
	if err != nil {
		return convertSDKError(err)
	}

	return app.OK(webhooks,
		output.WithSummary(fmt.Sprintf("%d webhooks", len(webhooks))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("bcq webhooks show <id> --in %s", resolvedProjectID),
				Description: "Show webhook details",
			},
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("bcq webhooks create --url <url> --in %s", resolvedProjectID),
				Description: "Create webhook",
			},
		),
	)
}

func newWebhooksShowCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show webhook details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			webhookIDStr := args[0]
			webhookID, err := strconv.ParseInt(webhookIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid webhook ID")
			}

			// Resolve project, with interactive fallback
			projectID := *project
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

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			webhook, err := app.Account().Webhooks().Get(cmd.Context(), bucketID, webhookID)
			if err != nil {
				return convertSDKError(err)
			}

			summary := fmt.Sprintf("Webhook #%s: %s", webhookIDStr, webhook.PayloadURL)

			return app.OK(webhook,
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("bcq webhooks update %s --in %s", webhookIDStr, resolvedProjectID),
						Description: "Update webhook",
					},
					output.Breadcrumb{
						Action:      "delete",
						Cmd:         fmt.Sprintf("bcq webhooks delete %s --in %s", webhookIDStr, resolvedProjectID),
						Description: "Delete webhook",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("bcq webhooks --in %s", resolvedProjectID),
						Description: "Back to webhooks",
					},
				),
			)
		},
	}
}

func newWebhooksCreateCmd(project *string) *cobra.Command {
	var url string
	var types string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new webhook",
		Long: `Create a new webhook for project notifications.

Event types: Todo, Todolist, Message, Comment, Document, Upload,
Vault, Schedule::Entry, Kanban::Card, Question, Question::Answer`,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if url == "" {
				return output.ErrUsage("--url is required")
			}

			// Resolve project, with interactive fallback
			projectID := *project
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

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			// Build type array from comma-separated string if specified
			// If not specified, leave nil to let server use its defaults
			var typeArray []string
			if types != "" {
				typeParts := strings.Split(types, ",")
				typeArray = make([]string, 0, len(typeParts))
				for _, t := range typeParts {
					t = strings.TrimSpace(t)
					if t != "" {
						typeArray = append(typeArray, t)
					}
				}
			}

			req := &basecamp.CreateWebhookRequest{
				PayloadURL: url,
				Types:      typeArray, // nil = server defaults
			}

			webhook, err := app.Account().Webhooks().Create(cmd.Context(), bucketID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(webhook,
				output.WithSummary(fmt.Sprintf("Created webhook #%d", webhook.ID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq webhooks show %d --in %s", webhook.ID, resolvedProjectID),
						Description: "View webhook",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("bcq webhooks --in %s", resolvedProjectID),
						Description: "List webhooks",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&url, "url", "", "Webhook payload URL (must be HTTPS)")
	cmd.Flags().StringVar(&types, "types", "", "Comma-separated event types (default: all)")
	_ = cmd.MarkFlagRequired("url")

	return cmd
}

func newWebhooksUpdateCmd(project *string) *cobra.Command {
	var url string
	var types string
	var active bool
	var inactive bool

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a webhook",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			webhookIDStr := args[0]
			webhookID, err := strconv.ParseInt(webhookIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid webhook ID")
			}

			// Resolve project, with interactive fallback
			projectID := *project
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

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			// Build update request
			req := &basecamp.UpdateWebhookRequest{}
			hasChanges := false

			if url != "" {
				req.PayloadURL = url
				hasChanges = true
			}

			if active {
				activeVal := true
				req.Active = &activeVal
				hasChanges = true
			} else if inactive {
				activeVal := false
				req.Active = &activeVal
				hasChanges = true
			}

			if types != "" {
				typeParts := strings.Split(types, ",")
				typeArray := make([]string, 0, len(typeParts))
				for _, t := range typeParts {
					t = strings.TrimSpace(t)
					if t != "" {
						typeArray = append(typeArray, t)
					}
				}
				req.Types = typeArray
				hasChanges = true
			}

			if !hasChanges {
				return output.ErrUsage("at least one of --url, --types, --active, or --inactive is required")
			}

			webhook, err := app.Account().Webhooks().Update(cmd.Context(), bucketID, webhookID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(webhook,
				output.WithSummary(fmt.Sprintf("Updated webhook #%s", webhookIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq webhooks show %s --in %s", webhookIDStr, resolvedProjectID),
						Description: "View webhook",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("bcq webhooks --in %s", resolvedProjectID),
						Description: "List webhooks",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&url, "url", "", "New webhook payload URL")
	cmd.Flags().StringVar(&types, "types", "", "New comma-separated event types")
	cmd.Flags().BoolVar(&active, "active", false, "Enable webhook")
	cmd.Flags().BoolVar(&inactive, "inactive", false, "Disable webhook")

	return cmd
}

func newWebhooksDeleteCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a webhook",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			webhookIDStr := args[0]
			webhookID, err := strconv.ParseInt(webhookIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid webhook ID")
			}

			// Resolve project, with interactive fallback
			projectID := *project
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

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			err = app.Account().Webhooks().Delete(cmd.Context(), bucketID, webhookID)
			if err != nil {
				return convertSDKError(err)
			}

			result := map[string]any{
				"deleted": true,
				"id":      webhookIDStr,
			}

			return app.OK(result,
				output.WithSummary(fmt.Sprintf("Deleted webhook #%s", webhookIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("bcq webhooks --in %s", resolvedProjectID),
						Description: "List webhooks",
					},
				),
			)
		},
	}
}
