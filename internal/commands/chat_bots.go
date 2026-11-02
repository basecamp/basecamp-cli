package commands

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// chatRoomScope resolves the project and the chat a chat subcommand acts on:
// the project from --in, the global flag or config (asking when none is set
// and a prompt can be answered), and the chat from --room or, failing that,
// the project's one chat. Chatbot routes name both, so the project is
// resolved even when --room is given.
func chatRoomScope(cmd *cobra.Command, app *appctx.App, project, chatID string) (string, int64, error) {
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return "", 0, err
		}
		projectID = app.Config.ProjectID
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return "", 0, err
	}

	if chatID == "" {
		chatID, err = getChatID(cmd, app, resolvedProjectID)
		if err != nil {
			return "", 0, err
		}
	}
	roomID, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return "", 0, output.ErrUsage("Invalid chat room ID")
	}
	return resolvedProjectID, roomID, nil
}

func newChatUploadsCmd(project, chatID *string) *cobra.Command {
	var limit, page int
	var all bool

	cmd := &cobra.Command{
		Use:   "uploads",
		Short: "List files posted in a chat",
		Long: `List the files posted in a chat, oldest first.

Each one is the chat line that carried the file, with the file in its
attachments. 'basecamp chat upload <file>' posts one.

Up to 100 come back by default: use --limit for another number, --all for
every one, or --page for a single page.`,
		Example: `  basecamp chat uploads --in my-project
  basecamp chat uploads --room 789 --in my-project --all`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit < 0 {
				return output.ErrUsage("--limit must not be negative")
			}
			if all && limit > 0 {
				return output.ErrUsage("--all and --limit are mutually exclusive")
			}
			if page < 0 {
				return output.ErrUsage("--page must not be negative")
			}
			if page > 0 && (all || limit > 0) {
				return output.ErrUsage("--page cannot be combined with --all or --limit")
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			projectID, roomID, err := chatRoomScope(cmd, app, *project, *chatID)
			if err != nil {
				return err
			}

			opts := &basecamp.CampfireUploadListOptions{Page: page}
			switch {
			case all:
				opts.Limit = -1 // the SDK reads -1 as no limit
			case limit > 0:
				opts.Limit = limit
			}

			result, err := app.Account().Campfires().ListUploads(cmd.Context(), roomID, opts)
			if err != nil {
				return convertSDKError(err)
			}
			lines := result.Lines
			if lines == nil {
				lines = []basecamp.CampfireLine{}
			}

			nav := chatRoomNav(strconv.FormatInt(roomID, 10), projectID)
			respOpts := []output.ResponseOption{
				output.WithSummary(fmt.Sprintf("%d files posted in chat", len(lines))),
				output.WithEntity("chat_line"),
				output.WithDisplayData(chatLinesDisplayData(lines)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "upload",
						Cmd:         fmt.Sprintf("basecamp chat upload <file> --room %d --in %s", roomID, projectID),
						Description: "Post a file",
					},
					output.Breadcrumb{
						Action:      "messages",
						Cmd:         nav.messages(0),
						Description: "View messages",
					},
				),
			}
			if notice := output.TruncationNoticeWithTotal(len(lines), result.Meta.TotalCount); notice != "" {
				respOpts = append(respOpts, output.WithNotice(notice))
			}
			return app.OK(lines, respOpts...)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of files to fetch (0 = default 100)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch every file (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")

	return cmd
}

const chatBotsAgentNotes = "Chatbots belong to the account, not to one chat: deleting one removes it from every chat\n" +
	"Creating, changing and deleting chatbots takes an account administrator\n" +
	"lines_url carries the chatbot's key: anyone holding it can post as the bot, so treat it like a password\n" +
	"Posting as a chatbot uses lines_url, not these commands"

func newChatBotsCmd(project, chatID *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "bots",
		Aliases: []string{"bot"},
		Short:   "Manage chatbots",
		Long: `Manage the chatbots that post in chats.

A chatbot belongs to the account, not to one chat: deleting one removes it
from every chat. Creating, changing and deleting chatbots takes an account
administrator.

A chatbot's lines_url is how it posts into the chat you named: a POST to it
adds a line as the bot, with no other credential. It carries the bot's key,
so treat it like a password. Its command_url, when set, is the HTTPS address
Basecamp calls when someone addresses the bot in a chat.

Use 'basecamp chat bots list' to see a chat's chatbots.
Use 'basecamp chat bots create <name>' to add one.`,
		Annotations: map[string]string{"agent_notes": chatBotsAgentNotes},
	}

	cmd.AddCommand(
		newChatBotsListCmd(project, chatID),
		newChatBotsShowCmd(project, chatID),
		newChatBotsCreateCmd(project, chatID),
		newChatBotsUpdateCmd(project, chatID),
		newChatBotsDeleteCmd(project, chatID),
	)
	return cmd
}

// chatBotID parses a chatbot ID argument.
func chatBotID(arg string) (int64, error) {
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil || id <= 0 {
		return 0, output.ErrUsage("Invalid chatbot ID: " + arg)
	}
	return id, nil
}

// chatBotScope is where a chatbot command continues: the chat's room and
// project, carried into each follow-up.
func chatBotScope(roomID int64, projectID string) string {
	return fmt.Sprintf("--room %d --in %s", roomID, projectID)
}

func chatBotBreadcrumbs(bot *basecamp.Chatbot, roomID int64, projectID string) []output.Breadcrumb {
	scope := chatBotScope(roomID, projectID)
	return []output.Breadcrumb{
		{
			Action:      "update",
			Cmd:         fmt.Sprintf("basecamp chat bots update %d --name <name> %s", bot.ID, scope),
			Description: "Rename the chatbot or change its command URL",
		},
		{
			Action:      "list",
			Cmd:         "basecamp chat bots list " + scope,
			Description: "List chatbots",
		},
	}
}

func newChatBotsListCmd(project, chatID *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List a chat's chatbots",
		Long: `List the chatbots, with the lines_url each posts into this chat with.

The answer arrives in one unpaginated response.`,
		Example: `  basecamp chat bots list --in my-project`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			projectID, roomID, err := chatRoomScope(cmd, app, *project, *chatID)
			if err != nil {
				return err
			}
			bucketID, err := strconv.ParseInt(projectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			result, err := app.Account().Campfires().ListChatbots(cmd.Context(), bucketID, roomID, nil)
			if err != nil {
				return convertSDKError(err)
			}
			bots := result.Chatbots
			if bots == nil {
				bots = []basecamp.Chatbot{}
			}

			scope := chatBotScope(roomID, projectID)
			return app.OK(bots,
				output.WithSummary(fmt.Sprintf("%d chatbots", len(bots))),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp chat bots show <id> " + scope,
						Description: "Show a chatbot",
					},
					output.Breadcrumb{
						Action:      "create",
						Cmd:         "basecamp chat bots create <name> " + scope,
						Description: "Add a chatbot",
					},
				),
			)
		},
	}
}

func newChatBotsShowCmd(project, chatID *string) *cobra.Command {
	return &cobra.Command{
		Use:     "show <id>",
		Short:   "Show a chatbot",
		Long:    "Show a chatbot: its name, command URL, and the lines_url it posts into this chat with.",
		Example: `  basecamp chat bots show 42 --in my-project`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			botID, err := chatBotID(args[0])
			if err != nil {
				return err
			}
			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			projectID, roomID, err := chatRoomScope(cmd, app, *project, *chatID)
			if err != nil {
				return err
			}
			bucketID, err := strconv.ParseInt(projectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			bot, err := app.Account().Campfires().GetChatbot(cmd.Context(), bucketID, roomID, botID)
			if err != nil {
				return convertSDKError(err)
			}
			return app.OK(bot,
				output.WithSummary(fmt.Sprintf("Chatbot #%d: %s", bot.ID, bot.ServiceName)),
				output.WithBreadcrumbs(chatBotBreadcrumbs(bot, roomID, projectID)...),
			)
		},
	}
}

func newChatBotsCreateCmd(project, chatID *string) *cobra.Command {
	var commandURL string

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Add a chatbot",
		Long: `Add a chatbot to the account, answering with the lines_url it posts into
this chat with.

The name is how people address the bot in a chat: letters, digits and
underscores, with no spaces or emoji. --command-url is the HTTPS address
Basecamp calls when someone addresses it; leave it out for a bot that only
posts.

The lines_url in the answer carries the bot's key: anyone holding it can post
as the bot. Takes an account administrator.`,
		Example: `  basecamp chat bots create deploys --in my-project
  basecamp chat bots create tally --command-url https://example.com/tally --in my-project`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<name>")
			}
			if len(args) > 1 {
				return output.ErrUsageHint("chat bots create takes one name",
					"A chatbot's name has no spaces: basecamp chat bots create deploys")
			}
			if args[0] == "" {
				return output.ErrUsage("chatbot name cannot be empty")
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			projectID, roomID, err := chatRoomScope(cmd, app, *project, *chatID)
			if err != nil {
				return err
			}
			bucketID, err := strconv.ParseInt(projectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			bot, err := app.Account().Campfires().CreateChatbot(cmd.Context(), bucketID, roomID,
				&basecamp.CreateChatbotRequest{ServiceName: args[0], CommandURL: commandURL})
			if err != nil {
				return convertSDKError(err)
			}
			return app.OK(bot,
				output.WithSummary(fmt.Sprintf("Created chatbot #%d: %s", bot.ID, bot.ServiceName)),
				output.WithBreadcrumbs(chatBotBreadcrumbs(bot, roomID, projectID)...),
			)
		},
	}

	cmd.Flags().StringVar(&commandURL, "command-url", "", "HTTPS address Basecamp calls when the bot is addressed")
	return cmd
}

func newChatBotsUpdateCmd(project, chatID *string) *cobra.Command {
	var name, commandURL string

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Rename a chatbot or change its command URL",
		Long: `Rename a chatbot or change its command URL.

What you leave out keeps its current value: the chatbot is read first and
sent back with your changes, since Basecamp takes its name on every update.
A command URL can be changed but not removed here.

Takes an account administrator.`,
		Example: `  basecamp chat bots update 42 --name deploy_bot --in my-project
  basecamp chat bots update 42 --command-url https://example.com/v2 --in my-project`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			botID, err := chatBotID(args[0])
			if err != nil {
				return err
			}
			nameSet := cmd.Flags().Changed("name")
			urlSet := cmd.Flags().Changed("command-url")
			if !nameSet && !urlSet {
				return output.ErrUsageHint("Nothing to update",
					"Pass --name, --command-url or both: basecamp chat bots update "+args[0]+" --name <name>")
			}
			if nameSet && name == "" {
				return output.ErrUsage("chatbot name cannot be empty")
			}
			if urlSet && commandURL == "" {
				return output.ErrUsage("--command-url cannot be empty: a command URL can be changed but not removed here")
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			projectID, roomID, err := chatRoomScope(cmd, app, *project, *chatID)
			if err != nil {
				return err
			}
			bucketID, err := strconv.ParseInt(projectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			current, err := app.Account().Campfires().GetChatbot(cmd.Context(), bucketID, roomID, botID)
			if err != nil {
				return convertSDKError(err)
			}
			req := &basecamp.UpdateChatbotRequest{ServiceName: current.ServiceName, CommandURL: current.CommandURL}
			if nameSet {
				req.ServiceName = name
			}
			if urlSet {
				req.CommandURL = commandURL
			}

			bot, err := app.Account().Campfires().UpdateChatbot(cmd.Context(), bucketID, roomID, botID, req)
			if err != nil {
				return convertSDKError(err)
			}
			return app.OK(bot,
				output.WithSummary(fmt.Sprintf("Updated chatbot #%d: %s", bot.ID, bot.ServiceName)),
				output.WithBreadcrumbs(chatBotBreadcrumbs(bot, roomID, projectID)...),
			)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "New name (letters, digits and underscores)")
	cmd.Flags().StringVar(&commandURL, "command-url", "", "New HTTPS address Basecamp calls when the bot is addressed")
	return cmd
}

func newChatBotsDeleteCmd(project, chatID *string) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a chatbot",
		Long: `Delete a chatbot from the account.

It is removed from every chat, not just this one, and is not moved to the
trash: its lines_url stops working and it cannot be restored.

Deleting asks for confirmation. Where nothing can answer that, --force is
required instead: any machine-output mode (--json, --agent, --quiet),
BASECAMP_NONINTERACTIVE, or no terminal on stdin and stderr.

Takes an account administrator.`,
		Example: `  basecamp chat bots delete 42 --in my-project
  basecamp chat bots delete 42 --in my-project --json --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			botID, err := chatBotID(args[0])
			if err != nil {
				return err
			}
			// Permanent, so it happens only with --force or a confirmation
			// somebody can answer; refused before any lookup.
			if err := ensureDeleteConfirmable(cmd, force); err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}
			projectID, roomID, err := chatRoomScope(cmd, app, *project, *chatID)
			if err != nil {
				return err
			}
			bucketID, err := strconv.ParseInt(projectID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid project ID")
			}

			if !force {
				confirmed, err := tui.ConfirmDangerous("Permanently delete this chatbot from every chat?")
				switch {
				case errors.Is(err, tui.ErrCanceled):
					return nil
				case err != nil:
					return fmt.Errorf("confirming the delete: %w", err)
				case !confirmed:
					return nil
				}
			}

			if err := app.Account().Campfires().DeleteChatbot(cmd.Context(), bucketID, roomID, botID); err != nil {
				return convertSDKError(err)
			}
			return app.OK(map[string]any{"id": botID, "deleted": true},
				output.WithSummary(fmt.Sprintf("Deleted chatbot #%d", botID)),
				output.WithBreadcrumbs(output.Breadcrumb{
					Action:      "list",
					Cmd:         "basecamp chat bots list " + chatBotScope(roomID, projectID),
					Description: "List chatbots",
				}),
			)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation prompt")
	return cmd
}
