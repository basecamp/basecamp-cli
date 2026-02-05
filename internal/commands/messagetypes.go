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
	cmd := &cobra.Command{
		Use:   "messagetypes",
		Short: "Manage message types (categories)",
		Long: `Manage message types (categories) for the message board.

Message types categorize messages on the message board. Each type has a name
and an emoji icon that appears alongside messages of that type.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMessagetypesList(cmd)
		},
	}

	cmd.AddCommand(
		newMessagetypesListCmd(),
		newMessagetypesShowCmd(),
		newMessagetypesCreateCmd(),
		newMessagetypesUpdateCmd(),
		newMessagetypesDeleteCmd(),
	)

	return cmd
}

func newMessagetypesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List message types",
		Long:  "List all message types.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMessagetypesList(cmd)
		},
	}
}

func runMessagetypesList(cmd *cobra.Command) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	typesResult, err := app.Account().MessageTypes().List(cmd.Context())
	if err != nil {
		return convertSDKError(err)
	}
	types := typesResult.MessageTypes

	return app.OK(types,
		output.WithSummary(fmt.Sprintf("%d message types", len(types))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "show",
				Cmd:         "basecamp messagetypes show <id>",
				Description: "View message type",
			},
			output.Breadcrumb{
				Action:      "create",
				Cmd:         "basecamp messagetypes create \"Name\" --icon \"emoji\"",
				Description: "Create message type",
			},
		),
	)
}

func newMessagetypesShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show message type details",
		Long:  "Display detailed information about a message type.",
		Args:  cobra.ExactArgs(1),
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

			msgType, err := app.Account().MessageTypes().Get(cmd.Context(), typeID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(msgType,
				output.WithSummary(fmt.Sprintf("%s %s", msgType.Icon, msgType.Name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("basecamp messagetypes update %s --name \"New Name\"", typeIDStr),
						Description: "Update message type",
					},
					output.Breadcrumb{
						Action:      "delete",
						Cmd:         fmt.Sprintf("basecamp messagetypes delete %s", typeIDStr),
						Description: "Delete message type",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         "basecamp messagetypes",
						Description: "List message types",
					},
				),
			)
		},
	}
}

func newMessagetypesCreateCmd() *cobra.Command {
	var name string
	var icon string

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new message type",
		Long:  "Create a new message type with a name and emoji icon.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
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

			req := &basecamp.CreateMessageTypeRequest{
				Name: name,
				Icon: icon,
			}

			msgType, err := app.Account().MessageTypes().Create(cmd.Context(), req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(msgType,
				output.WithSummary(fmt.Sprintf("Created message type #%d: %s %s", msgType.ID, icon, name)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp messagetypes show %d", msgType.ID),
						Description: "View message type",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         "basecamp messagetypes",
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

func newMessagetypesUpdateCmd() *cobra.Command {
	var name string
	var icon string

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a message type",
		Long:  "Update an existing message type's name or icon.",
		Args:  cobra.ExactArgs(1),
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
				return output.ErrUsage("Use --name or --icon to update")
			}

			req := &basecamp.UpdateMessageTypeRequest{
				Name: name,
				Icon: icon,
			}

			msgType, err := app.Account().MessageTypes().Update(cmd.Context(), typeID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(msgType,
				output.WithSummary(fmt.Sprintf("Updated message type #%s", typeIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp messagetypes show %s", typeIDStr),
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

func newMessagetypesDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a message type",
		Long:  "Delete an existing message type.",
		Args:  cobra.ExactArgs(1),
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

			err = app.Account().MessageTypes().Delete(cmd.Context(), typeID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"deleted": true},
				output.WithSummary(fmt.Sprintf("Deleted message type #%s", typeIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "list",
						Cmd:         "basecamp messagetypes",
						Description: "List message types",
					},
				),
			)
		},
	}
}
