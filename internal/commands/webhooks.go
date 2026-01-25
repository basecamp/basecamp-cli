package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

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
	if err := app.API.RequireAccount(); err != nil {
		return err
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
		return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/buckets/%s/webhooks.json", resolvedProjectID)
	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var webhooks []any
	if err := resp.UnmarshalData(&webhooks); err != nil {
		return fmt.Errorf("failed to parse webhooks: %w", err)
	}

	return app.Output.OK(webhooks,
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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			webhookID := args[0]

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

			path := fmt.Sprintf("/buckets/%s/webhooks/%s.json", resolvedProjectID, webhookID)
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var data struct {
				PayloadURL string `json:"payload_url"`
			}
			_ = json.Unmarshal(resp.Data, &data) // Best-effort

			summary := fmt.Sprintf("Webhook #%s: %s", webhookID, data.PayloadURL)

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("bcq webhooks update %s --in %s", webhookID, resolvedProjectID),
						Description: "Update webhook",
					},
					output.Breadcrumb{
						Action:      "delete",
						Cmd:         fmt.Sprintf("bcq webhooks delete %s --in %s", webhookID, resolvedProjectID),
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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			if url == "" {
				return output.ErrUsage("--url is required")
			}

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

			body := map[string]any{
				"payload_url": url,
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
				body["types"] = typeArray
			}

			path := fmt.Sprintf("/buckets/%s/webhooks.json", resolvedProjectID)
			resp, err := app.API.Post(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			var webhook struct {
				ID int64 `json:"id"`
			}
			_ = json.Unmarshal(resp.Data, &webhook) // Best-effort

			return app.Output.OK(json.RawMessage(resp.Data),
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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			webhookID := args[0]

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

			body := make(map[string]any)

			if url != "" {
				body["payload_url"] = url
			}

			if active {
				body["active"] = true
			} else if inactive {
				body["active"] = false
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
				body["types"] = typeArray
			}

			if len(body) == 0 {
				return output.ErrUsage("at least one of --url, --types, --active, or --inactive is required")
			}

			path := fmt.Sprintf("/buckets/%s/webhooks/%s.json", resolvedProjectID, webhookID)
			resp, err := app.API.Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Updated webhook #%s", webhookID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq webhooks show %s --in %s", webhookID, resolvedProjectID),
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
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			webhookID := args[0]

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

			path := fmt.Sprintf("/buckets/%s/webhooks/%s.json", resolvedProjectID, webhookID)
			_, err = app.API.Delete(cmd.Context(), path)
			if err != nil {
				return err
			}

			result := map[string]any{
				"deleted": true,
				"id":      webhookID,
			}

			return app.Output.OK(result,
				output.WithSummary(fmt.Sprintf("Deleted webhook #%s", webhookID)),
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
