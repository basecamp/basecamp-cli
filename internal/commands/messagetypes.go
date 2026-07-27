package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewMessagetypesCmd creates the messagetypes command for managing message types.
func NewMessagetypesCmd() *cobra.Command {
	var project string

	cmd := &cobra.Command{
		Use:   "messagetypes",
		Short: "Manage message types (categories)",
		Long: `Manage message types (categories) for a project's message board.

Message types categorize messages on the message board. Each type has a name
and an emoji icon that appears alongside messages of that type. Message types
are per-project: every command takes --in/--project to pick the project.

  basecamp messagetypes list --in MyProject`,
		Annotations: map[string]string{
			"agent_notes": "Message types (categories) are per-project — every action needs --in <project>.\n" +
				"Each type has a name and an emoji icon shown alongside messages of that type.",
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

// resolveMessagetypesBucket resolves the project into the numeric bucket ID
// required by the bucket-scoped categories API. Returns the bucket ID and the
// resolved project ID string (for breadcrumbs).
func resolveMessagetypesBucket(cmd *cobra.Command, app *appctx.App, project string) (int64, string, error) {
	resolvedProjectID, err := resolveProjectID(cmd, app, project)
	if err != nil {
		return 0, "", err
	}

	bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return 0, "", output.ErrUsage("Invalid project ID")
	}
	return bucketID, resolvedProjectID, nil
}

func newMessagetypesListCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List message types",
		Long: `List all message types in a project.

  basecamp messagetypes list --in MyProject`,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			bucketID, resolvedProjectID, err := resolveMessagetypesBucket(cmd, app, *project)
			if err != nil {
				return err
			}

			typesResult, err := app.Account().MessageTypes().List(cmd.Context(), bucketID, nil)
			if err != nil {
				return convertSDKError(err)
			}
			types := typesResult.MessageTypes

			return app.OK(types,
				output.WithSummary(fmt.Sprintf("%d message types", len(types))),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp messagetypes show <id> --in %s", resolvedProjectID),
						Description: "View message type",
					},
					output.Breadcrumb{
						Action:      "create",
						Cmd:         fmt.Sprintf("basecamp messagetypes create \"Name\" --icon \"emoji\" --in %s", resolvedProjectID),
						Description: "Create message type",
					},
				),
			)
		},
	}
}

func newMessagetypesShowCmd(project *string) *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show message type details",
		Long: `Display detailed information about a message type.

  basecamp messagetypes show 12345 --in MyProject`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			typeIDStr := args[0]
			typeID, err := strconv.ParseInt(typeIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid message type ID")
			}

			bucketID, resolvedProjectID, err := resolveMessagetypesBucket(cmd, app, *project)
			if err != nil {
				return err
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
						Cmd:         fmt.Sprintf("basecamp messagetypes update %s --name \"New Name\" --in %s", typeIDStr, resolvedProjectID),
						Description: "Update message type",
					},
					output.Breadcrumb{
						Action:      "delete",
						Cmd:         fmt.Sprintf("basecamp messagetypes delete %s --in %s", typeIDStr, resolvedProjectID),
						Description: "Delete message type",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("basecamp messagetypes list --in %s", resolvedProjectID),
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
		Use:   "create <name>",
		Short: "Create a new message type",
		Long: `Create a new message type with a name and emoji icon.

  basecamp messagetypes create "Announcement" --icon "📣" --in MyProject`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Name from positional arg or flag
			if len(args) > 0 && name == "" {
				name = args[0]
			}

			// Show help when invoked with no arguments
			if name == "" {
				return missingArg(cmd, "<name>")
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if icon == "" {
				return output.ErrUsage("--icon is required")
			}

			bucketID, resolvedProjectID, err := resolveMessagetypesBucket(cmd, app, *project)
			if err != nil {
				return err
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
						Cmd:         fmt.Sprintf("basecamp messagetypes show %d --in %s", msgType.ID, resolvedProjectID),
						Description: "View message type",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("basecamp messagetypes list --in %s", resolvedProjectID),
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
		Long: `Update an existing message type's name or icon.

  basecamp messagetypes update 12345 --name "New Name" --in MyProject`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			typeIDStr := args[0]
			typeID, err := strconv.ParseInt(typeIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid message type ID")
			}

			if name == "" && icon == "" {
				return noChanges(cmd)
			}

			bucketID, resolvedProjectID, err := resolveMessagetypesBucket(cmd, app, *project)
			if err != nil {
				return err
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
						Cmd:         fmt.Sprintf("basecamp messagetypes show %s --in %s", typeIDStr, resolvedProjectID),
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
		Long: `Delete an existing message type.

  basecamp messagetypes delete 12345 --in MyProject`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			typeIDStr := args[0]
			typeID, err := strconv.ParseInt(typeIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid message type ID")
			}

			bucketID, resolvedProjectID, err := resolveMessagetypesBucket(cmd, app, *project)
			if err != nil {
				return err
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
						Cmd:         fmt.Sprintf("basecamp messagetypes list --in %s", resolvedProjectID),
						Description: "List message types",
					},
				),
			)
		},
	}
}
