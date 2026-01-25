package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// NewMessagetypesCmd creates the messagetypes command for managing message types.
func NewMessagetypesCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:   "messagetypes",
		Short: "Manage message types (categories)",
		Long: `Manage message types (categories) for the message board.

Message types categorize messages on the message board. Each type has a name
and an emoji icon that appears alongside messages of that type.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMessagetypesList(cmd, project)
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")

	cmd.AddCommand(
		newMessagetypesListCmd(&project),
		newMessagetypesShowCmd(&project),
		newMessagetypesCreateCmd(&project),
		newMessagetypesUpdateCmd(&project),
		newMessagetypesDeleteCmd(&project),
	)

	return cmd
}

func newMessagetypesListCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List message types",
		Long:  "List all message types in a project.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMessagetypesList(cmd, *project)
		},
	}
}

func runMessagetypesList(cmd *cobra.Command, project string) error {
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
		return output.ErrUsage("--project is required")
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/buckets/%s/categories.json", resolvedProjectID)
	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return err
	}

	var types []json.RawMessage
	if err := json.Unmarshal(resp.Data, &types); err != nil {
		return fmt.Errorf("failed to parse message types: %w", err)
	}

	return app.Output.OK(json.RawMessage(resp.Data),
		output.WithSummary(fmt.Sprintf("%d message types", len(types))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         fmt.Sprintf("bcq messagetypes show <id> --in %s", resolvedProjectID),
				Description: "View message type",
			},
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("bcq messagetypes create \"Name\" --icon \"emoji\" --in %s", resolvedProjectID),
				Description: "Create message type",
			},
		),
	)
}

func newMessagetypesShowCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show message type details",
		Long:  "Display detailed information about a message type.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			typeID := args[0]

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

			path := fmt.Sprintf("/buckets/%s/categories/%s.json", resolvedProjectID, typeID)
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var msgType struct {
				Name string `json:"name"`
				Icon string `json:"icon"`
			}
			if err := json.Unmarshal(resp.Data, &msgType); err != nil {
				return fmt.Errorf("failed to parse message type: %w", err)
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("%s %s", msgType.Icon, msgType.Name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("bcq messagetypes update %s --name \"New Name\" --in %s", typeID, resolvedProjectID),
						Description: "Update message type",
					},
					output.Breadcrumb{
						Action:      "delete",
						Cmd:         fmt.Sprintf("bcq messagetypes delete %s --in %s", typeID, resolvedProjectID),
						Description: "Delete message type",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("bcq messagetypes --in %s", resolvedProjectID),
						Description: "List message types",
					},
				),
			)
		},
	}
}

func newMessagetypesCreateCmd(project *string) *cobra.Command {
	var name string
	var icon string

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new message type",
		Long:  "Create a new message type with a name and emoji icon.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			// Name from positional arg or flag
			if len(args) > 0 && name == "" {
				name = args[0]
			}

			if name == "" {
				return output.ErrUsage("Name is required")
			}

			if icon == "" {
				return output.ErrUsage("--icon is required")
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

			body := map[string]string{
				"name": name,
				"icon": icon,
			}

			path := fmt.Sprintf("/buckets/%s/categories.json", resolvedProjectID)
			resp, err := app.API.Post(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			var created struct {
				ID int64 `json:"id"`
			}
			if err := json.Unmarshal(resp.Data, &created); err != nil {
				return fmt.Errorf("failed to parse response: %w", err)
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Created message type #%d: %s %s", created.ID, icon, name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq messagetypes show %d --in %s", created.ID, resolvedProjectID),
						Description: "View message type",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("bcq messagetypes --in %s", resolvedProjectID),
						Description: "List message types",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Message type name")
	cmd.Flags().StringVar(&icon, "icon", "", "Message type icon (emoji)")

	return cmd
}

func newMessagetypesUpdateCmd(project *string) *cobra.Command {
	var name string
	var icon string

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a message type",
		Long:  "Update an existing message type's name or icon.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			typeID := args[0]

			if name == "" && icon == "" {
				return output.ErrUsage("Use --name or --icon to update")
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

			body := map[string]string{}
			if name != "" {
				body["name"] = name
			}
			if icon != "" {
				body["icon"] = icon
			}

			path := fmt.Sprintf("/buckets/%s/categories/%s.json", resolvedProjectID, typeID)
			resp, err := app.API.Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Updated message type #%s", typeID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq messagetypes show %s --in %s", typeID, resolvedProjectID),
						Description: "View message type",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "New name")
	cmd.Flags().StringVar(&icon, "icon", "", "New icon (emoji)")

	return cmd
}

func newMessagetypesDeleteCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a message type",
		Long:  "Delete an existing message type.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			typeID := args[0]

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

			path := fmt.Sprintf("/buckets/%s/categories/%s.json", resolvedProjectID, typeID)
			_, err = app.API.Delete(cmd.Context(), path)
			if err != nil {
				return err
			}

			return app.Output.OK(map[string]any{"deleted": true},
				output.WithSummary(fmt.Sprintf("Deleted message type #%s", typeID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("bcq messagetypes --in %s", resolvedProjectID),
						Description: "List message types",
					},
				),
			)
		},
	}
}
