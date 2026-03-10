package commands

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewWebhooksCmd creates the webhooks command group.
func NewWebhooksCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:     "webhooks",
		Aliases: []string{"webhook"},
		Short:   "Manage webhooks",
		Long: `Manage webhooks for notifications.

Event types: Todo, Todolist, Message, Comment, Document, Upload,
Vault, Schedule::Entry, Kanban::Card, Question, Question::Answer`,
		Annotations: map[string]string{"agent_notes": "Event types: Todo, Todolist, Message, Comment, Document, Upload, Vault, Schedule::Entry, Kanban::Card, Question, Question::Answer"},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project name, URL, or ID")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project name, URL, or ID (alias for --project)")

	cmd.AddCommand(
		newWebhooksListCmd(&project),
		newWebhooksShowCmd(),
		newWebhooksCreateCmd(&project),
		newWebhooksUpdateCmd(),
		newWebhooksDeleteCmd(),
	)

	return cmd
}

func newWebhooksListCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List webhooks",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWebhooksList(cmd, project)
		},
	}
}

func runWebhooksList(cmd *cobra.Command, project *string) error {
	app := appctx.FromContext(cmd.Context())

	// Resolve account (enables interactive prompt if needed)
	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Resolve project — required for project-scoped webhook listing
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

	webhooksResult, err := app.Account().Webhooks().List(cmd.Context(), bucketID)
	if err != nil {
		return convertSDKError(err)
	}
	webhooks := webhooksResult.Webhooks

	return app.OK(webhooks,
		output.WithSummary(fmt.Sprintf("%d webhooks", len(webhooks))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         "basecamp webhooks show <id>",
				Description: "Show webhook details",
			},
			output.Breadcrumb{
				Action:      "create",
				Cmd:         "basecamp webhooks create <url>",
				Description: "Create webhook",
			},
		),
	)
}

func newWebhooksShowCmd() *cobra.Command {
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

			webhook, err := app.Account().Webhooks().Get(cmd.Context(), webhookID)
			if err != nil {
				return convertSDKError(err)
			}

			summary := fmt.Sprintf("Webhook #%s: %s", webhookIDStr, webhook.PayloadURL)

			return app.OK(webhook,
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("basecamp webhooks update %s", webhookIDStr),
						Description: "Update webhook",
					},
					output.Breadcrumb{
						Action:      "delete",
						Cmd:         fmt.Sprintf("basecamp webhooks delete %s", webhookIDStr),
						Description: "Delete webhook",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         "basecamp webhooks list",
						Description: "Back to webhooks",
					},
				),
			)
		},
	}
}

func newWebhooksCreateCmd(project *string) *cobra.Command {
	var types string

	cmd := &cobra.Command{
		Use:   "create <url> [flags]",
		Short: "Create a new webhook",
		Long: `Create a new webhook for notifications.

Event types: Todo, Todolist, Message, Comment, Document, Upload,
Vault, Schedule::Entry, Kanban::Card, Question, Question::Answer`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<url>")
			}
			url := args[0]

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Resolve project — required for project-scoped webhook creation
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
						Cmd:         fmt.Sprintf("basecamp webhooks show %d", webhook.ID),
						Description: "View webhook",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         "basecamp webhooks list",
						Description: "List webhooks",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&types, "types", "", "Comma-separated event types (default: all)")

	return cmd
}

func newWebhooksUpdateCmd() *cobra.Command {
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

			// Show help when invoked with no flags
			if !hasChanges {
				return noChanges(cmd)
			}

			webhook, err := app.Account().Webhooks().Update(cmd.Context(), webhookID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(webhook,
				output.WithSummary(fmt.Sprintf("Updated webhook #%s", webhookIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp webhooks show %s", webhookIDStr),
						Description: "View webhook",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         "basecamp webhooks list",
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

func newWebhooksDeleteCmd() *cobra.Command {
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

			err = app.Account().Webhooks().Delete(cmd.Context(), webhookID)
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
						Cmd:         "basecamp webhooks list",
						Description: "List webhooks",
					},
				),
			)
		},
	}
}
