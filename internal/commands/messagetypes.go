package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

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

	if err := app.RequireAccount(); err != nil {
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

	bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid project ID")
	}

	types, err := app.Account().MessageTypes().List(cmd.Context(), bucketID)
	if err != nil {
		return convertSDKError(err)
	}

	return app.OK(types,
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

			if err := app.RequireAccount(); err != nil {
				return err
			}

			typeIDStr := args[0]
			typeID, err := strconv.ParseInt(typeIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid message type ID")
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

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			msgType, err := app.Account().MessageTypes().Get(cmd.Context(), bucketID, typeID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(msgType,
				output.WithSummary(fmt.Sprintf("%s %s", msgType.Icon, msgType.Name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("bcq messagetypes update %s --name \"New Name\" --in %s", typeIDStr, resolvedProjectID),
						Description: "Update message type",
					},
					output.Breadcrumb{
						Action:      "delete",
						Cmd:         fmt.Sprintf("bcq messagetypes delete %s --in %s", typeIDStr, resolvedProjectID),
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

			if err := app.RequireAccount(); err != nil {
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

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			req := &basecamp.CreateMessageTypeRequest{
				Name: name,
				Icon: icon,
			}

			msgType, err := app.Account().MessageTypes().Create(cmd.Context(), bucketID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(msgType,
				output.WithSummary(fmt.Sprintf("Created message type #%d: %s %s", msgType.ID, icon, name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq messagetypes show %d --in %s", msgType.ID, resolvedProjectID),
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

			if err := app.RequireAccount(); err != nil {
				return err
			}

			typeIDStr := args[0]
			typeID, err := strconv.ParseInt(typeIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid message type ID")
			}

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

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			req := &basecamp.UpdateMessageTypeRequest{
				Name: name,
				Icon: icon,
			}

			msgType, err := app.Account().MessageTypes().Update(cmd.Context(), bucketID, typeID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(msgType,
				output.WithSummary(fmt.Sprintf("Updated message type #%s", typeIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq messagetypes show %s --in %s", typeIDStr, resolvedProjectID),
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

			if err := app.RequireAccount(); err != nil {
				return err
			}

			typeIDStr := args[0]
			typeID, err := strconv.ParseInt(typeIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid message type ID")
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

			bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			err = app.Account().MessageTypes().Delete(cmd.Context(), bucketID, typeID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"deleted": true},
				output.WithSummary(fmt.Sprintf("Deleted message type #%s", typeIDStr)),
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
