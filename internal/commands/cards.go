package commands

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/completion"
	"github.com/basecamp/basecamp-cli/internal/dateparse"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// NewCardsCmd creates the cards command group.
func NewCardsCmd() *cobra.Command {
	var project string
	var cardTable string

	cmd := &cobra.Command{
		Use:         "cards",
		Short:       "Manage cards in Card Tables",
		Long:        "List, show, create, and manage cards in Card Tables (Kanban boards).",
		Annotations: map[string]string{"agent_notes": "Cards do NOT support --assignee filtering like todos — fetch all and filter client-side\nIf a project has multiple card tables, you must specify --card-table <id>\nAssign/unassign shortcuts work on cards: basecamp assign <card_id> --to <person>\nCross-project cards: basecamp recordings cards --json"},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.PersistentFlags().StringVar(&cardTable, "card-table", "", "Card table ID (required if project has multiple)")

	cmd.AddCommand(
		newCardsListCmd(&project, &cardTable),
		newCardsShowCmd(),
		newCardsCreateCmd(&project, &cardTable),
		newCardsUpdateCmd(),
		newCardsMoveCmd(&project, &cardTable),
		newCardsColumnsCmd(&project, &cardTable),
		newCardsColumnCmd(&project, &cardTable),
		newCardsStepsCmd(&project),
		newCardsStepCmd(&project),
		newRecordableTrashCmd("card"),
		newRecordableArchiveCmd("card"),
		newRecordableRestoreCmd("card"),
	)

	return cmd
}

func newCardsListCmd(project, cardTable *string) *cobra.Command {
	var column string
	var limit int
	var page int
	var all bool
	var sortField string
	var reverse bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List cards",
		Long:  "List all cards in a project's card table.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCardsList(cmd, *project, column, *cardTable, limit, page, all, sortField, reverse)
		},
	}

	cmd.Flags().StringVarP(&column, "column", "c", "", "Filter by column ID or name")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of cards to fetch (0 = all)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all cards (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")
	cmd.Flags().StringVar(&sortField, "sort", "", "Sort by field (title, created, updated, position, due)")
	cmd.Flags().BoolVar(&reverse, "reverse", false, "Reverse sort order")

	return cmd
}

func runCardsList(cmd *cobra.Command, project, column, cardTable string, limit, page int, all bool, sortField string, reverse bool) error {
	app := appctx.FromContext(cmd.Context())

	// Validate flag combinations
	if all && limit > 0 {
		return output.ErrUsage("--all and --limit are mutually exclusive")
	}
	if page > 0 && (all || limit > 0) {
		return output.ErrUsage("--page cannot be combined with --all or --limit")
	}
	if page > 1 {
		return output.ErrUsage("only --page 1 is supported; use --all to fetch everything")
	}
	if sortField != "" {
		// Validate against the superset of all allowed fields early, before any
		// API calls. Context-specific restrictions (e.g. no position in aggregate)
		// are enforced at each branch below.
		if err := validateSortField(sortField, []string{"title", "created", "updated", "position", "due"}); err != nil {
			return err
		}
	}

	// Pagination flags only make sense when listing a single column
	// When aggregating across columns, pagination is per-column which is confusing
	if column == "" && (page > 0 || limit > 0 || all) {
		return output.ErrUsageHint(
			"Pagination flags require --column",
			"When listing all columns, pagination applies per-column. Use --column <id> to paginate a single column.",
		)
	}

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

	// Column name (non-numeric) requires --card-table for resolution
	// Numeric column IDs can be used directly without discovery
	if column != "" && !isNumericID(column) && cardTable == "" {
		return output.ErrUsage("--card-table is required when using --column with a name")
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	// Build pagination options
	opts := &basecamp.CardListOptions{}
	if all {
		opts.Limit = -1 // SDK treats -1 as "fetch all"
	} else if limit > 0 {
		opts.Limit = limit
	}
	if page > 0 {
		opts.Page = page
	}

	// Optimization: If column is a numeric ID, skip card table discovery
	// and fetch cards directly from the column endpoint
	if column != "" && isNumericID(column) {
		columnID, err := strconv.ParseInt(column, 10, 64)
		if err != nil {
			return output.ErrUsage("Invalid column ID")
		}

		cardsResult, err := app.Account().Cards().List(cmd.Context(), columnID, opts)
		if err != nil {
			return convertSDKError(err)
		}

		if sortField != "" {
			sortCards(cardsResult.Cards, sortField, reverse)
		}

		return app.OK(cardsResult.Cards,
			output.WithSummary(fmt.Sprintf("%d cards", len(cardsResult.Cards))),
			output.WithBreadcrumbs(cardsListBreadcrumbs(resolvedProjectID)...),
		)
	}

	// Get card table ID from project dock
	cardTableID, err := getCardTableID(cmd, app, resolvedProjectID, cardTable)
	if err != nil {
		return err
	}

	cardTableIDInt, err := strconv.ParseInt(cardTableID, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid card table ID")
	}

	// Get card table with embedded columns (lists)
	cardTableData, err := app.Account().CardTables().Get(cmd.Context(), cardTableIDInt)
	if err != nil {
		return convertSDKError(err)
	}

	// Get cards from all columns or specific column
	var allCards []basecamp.Card
	if column != "" {
		// Find column by ID or name
		columnID := resolveColumn(cardTableData.Lists, column)
		if columnID == 0 {
			return output.ErrUsageHint(
				fmt.Sprintf("Column '%s' not found", column),
				"Use column ID or exact name",
			)
		}
		cardsResult, err := app.Account().Cards().List(cmd.Context(), columnID, opts)
		if err != nil {
			return convertSDKError(err)
		}
		allCards = cardsResult.Cards

		if sortField != "" {
			sortCards(allCards, sortField, reverse)
		}
	} else {
		// No position in aggregate — it's only meaningful within a single column
		if sortField == "position" {
			return output.ErrUsage("--sort position requires --column (position is per-column)")
		}

		// Get cards from all columns (no pagination - already validated above)
		for _, col := range cardTableData.Lists {
			cardsResult, err := app.Account().Cards().List(cmd.Context(), col.ID, nil)
			if err != nil {
				continue // Skip columns with errors
			}
			allCards = append(allCards, cardsResult.Cards...)
		}

		if sortField != "" {
			sortCards(allCards, sortField, reverse)
		}
	}

	return app.OK(allCards,
		output.WithSummary(fmt.Sprintf("%d cards", len(allCards))),
		output.WithBreadcrumbs(append(cardsListBreadcrumbs(resolvedProjectID),
			output.Breadcrumb{
				Action:      "columns",
				Cmd:         fmt.Sprintf("basecamp cards columns --in %s", resolvedProjectID),
				Description: "List columns with IDs",
			},
		)...),
	)
}

func cardsListBreadcrumbs(resolvedProjectID string) []output.Breadcrumb {
	return []output.Breadcrumb{
		{Action: "create", Cmd: fmt.Sprintf("basecamp card --title <title> --in %s", resolvedProjectID), Description: "Create card"},
		{Action: "show", Cmd: "basecamp cards show <id>", Description: "Show card details"},
		{Action: "archived", Cmd: fmt.Sprintf("basecamp recordings cards --status archived --in %s", resolvedProjectID), Description: "Browse archived cards"},
	}
}

func newCardsShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show card details",
		Long: `Display detailed information about a card.

You can pass either a card ID or a Basecamp URL:
  basecamp cards show 789
  basecamp cards show https://3.basecamp.com/123/buckets/456/card_tables/cards/789`,
		Args: cobra.ExactArgs(1),
	}

	dlDir := addDownloadAttachmentsFlag(cmd)
	cf := addCommentFlags(cmd, false)

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		app := appctx.FromContext(cmd.Context())

		if err := ensureAccount(cmd, app); err != nil {
			return err
		}

		// Extract ID from URL if provided
		cardIDStr := extractID(args[0])

		cardID, err := strconv.ParseInt(cardIDStr, 10, 64)
		if err != nil {
			return output.ErrUsage("Invalid card ID")
		}

		card, err := app.Account().Cards().Get(cmd.Context(), cardID)
		if err != nil {
			return convertSDKError(err)
		}

		enrichment := fetchCommentsForRecording(cmd.Context(), app, cardIDStr, cf)

		opts := []output.ResponseOption{
			output.WithSummary(fmt.Sprintf("Card #%s: %s", cardIDStr, card.Title)),
			output.WithBreadcrumbs(
				output.Breadcrumb{
					Action:      "comment",
					Cmd:         fmt.Sprintf("basecamp comment %s <text>", cardIDStr),
					Description: "Add comment",
				},
			),
		}

		data := any(card)
		attachmentNotice := ""
		contentAtts := downloadableAttachments(richtext.ParseAttachments(card.Content))
		descAtts := downloadableAttachments(richtext.ParseAttachments(card.Description))
		total := len(contentAtts) + len(descAtts)
		if total > 0 {
			allAtts := append(contentAtts, descAtts...)
			dl := runDownloadAttachments(cmd, app, allAtts, dlDir)
			var contentDL, descDL []attachmentResult
			if dl != nil {
				contentDL = dl.Results[:len(contentAtts)]
				descDL = dl.Results[len(contentAtts):]
			}
			if len(contentAtts) > 0 {
				data = withAttachmentMeta(card, "content", contentAtts, contentDL)
			}
			if len(descAtts) > 0 {
				data = withAttachmentMeta(data, "description", descAtts, descDL)
			}
			attachmentNotice = fmt.Sprintf("%d attachment(s) — download: basecamp attachments download %s",
				total, cardIDStr)
			if dl != nil && dl.Notice != "" {
				attachmentNotice += "; " + dl.Notice
			}
			opts = append(opts,
				output.WithBreadcrumbs(attachmentBreadcrumb(cardIDStr, total)),
			)
		}

		data, extraOpts := enrichment.apply(data, attachmentNotice)
		opts = append(opts, extraOpts...)

		return app.OK(data, opts...)
	}

	return cmd
}

func resolveAssigneeID(ctx context.Context, app *appctx.App, input string) (int64, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return 0, output.ErrUsage("Assignee cannot be empty")
	}

	if id, err := strconv.ParseInt(input, 10, 64); err == nil {
		if id <= 0 {
			return 0, output.ErrUsage("Assignee ID must be a positive number")
		}
		return id, nil
	}
	resolvedID, _, err := app.Names.ResolvePerson(ctx, input)
	if err != nil {
		return 0, fmt.Errorf("failed to resolve assignee '%s': %w", input, err)
	}
	id, err := strconv.ParseInt(resolvedID, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid resolved ID '%s': %w", resolvedID, err)
	}
	if id <= 0 {
		return 0, fmt.Errorf("resolved assignee ID for '%s' is not valid: %d", input, id)
	}
	return id, nil
}

func newCardsCreateCmd(project, cardTable *string) *cobra.Command {
	var column string
	var assignee string
	var attachFiles []string

	cmd := &cobra.Command{
		Use:   "create <title> [body]",
		Short: "Create a new card",
		Long:  "Create a new card in a project's card table.",
		Example: `  basecamp cards create "My card" --in myproject
  basecamp cards create --in myproject -- "--title with dashes"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked with no title
			if len(args) == 0 {
				return missingArg(cmd, "<title>")
			}

			title := args[0]
			if strings.TrimSpace(title) == "" {
				return cmd.Help()
			}
			var content string
			if len(args) > 1 {
				content = args[1]
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Column name (non-numeric) requires --card-table for resolution
			// Numeric column IDs can be used directly without card table discovery
			if column != "" && !isNumericID(column) && *cardTable == "" {
				return output.ErrUsage("--card-table is required when using --column with a name")
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

			// If column is a numeric ID, use it directly without card table discovery
			var columnID int64
			var cardTableIDVal string
			if column != "" && isNumericID(column) {
				columnID, err = strconv.ParseInt(column, 10, 64)
				if err != nil {
					return output.ErrUsage("Invalid column ID")
				}
				cardTableIDVal = "" // Not needed for numeric column ID
			} else {
				// Need to discover card table and resolve column
				cardTableIDVal, err = getCardTableID(cmd, app, resolvedProjectID, *cardTable)
				if err != nil {
					return err
				}

				cardTableIDInt, err := strconv.ParseInt(cardTableIDVal, 10, 64)
				if err != nil {
					return output.ErrUsage("Invalid card table ID")
				}

				// Get card table with embedded columns (lists)
				cardTableData, err := app.Account().CardTables().Get(cmd.Context(), cardTableIDInt)
				if err != nil {
					return convertSDKError(err)
				}

				// Find target column
				if column != "" {
					columnID = resolveColumn(cardTableData.Lists, column)
					if columnID == 0 {
						return output.ErrUsageHint(
							fmt.Sprintf("Column '%s' not found", column),
							"Use column ID or exact name",
						)
					}
				} else {
					// Use first column
					if len(cardTableData.Lists) == 0 {
						return output.ErrNotFound("columns", resolvedProjectID)
					}
					columnID = cardTableData.Lists[0].ID
				}
			}

			// Pre-resolve assignee before side-effectful work (fail early on bad input)
			var assigneeID int64
			if cmd.Flags().Changed("assignee") || cmd.Flags().Changed("to") {
				assigneeID, err = resolveAssigneeID(cmd.Context(), app, assignee)
				if err != nil {
					return err
				}
			}

			// Convert content through rich text pipeline
			var mentionNotice string
			if content != "" {
				content = richtext.MarkdownToHTML(content)
				content, err = resolveLocalImages(cmd, app, content)
				if err != nil {
					return err
				}
				mentionResult, mentionErr := resolveMentions(cmd.Context(), app.Names, content)
				if mentionErr != nil {
					return mentionErr
				}
				content = mentionResult.HTML
				mentionNotice = unresolvedMentionWarning(mentionResult.Unresolved)
			}

			// Upload explicit --attach files and embed
			if len(attachFiles) > 0 {
				refs, attachErr := uploadAttachments(cmd, app, attachFiles)
				if attachErr != nil {
					return attachErr
				}
				content = richtext.EmbedAttachments(content, refs)
			}

			// Build request
			req := &basecamp.CreateCardRequest{
				Title:   title,
				Content: content,
			}

			card, err := app.Account().Cards().Create(cmd.Context(), columnID, req)
			if err != nil {
				return convertSDKError(err)
			}

			if assigneeID != 0 {
				createdCardID := card.ID
				card, err = app.Account().Cards().Update(cmd.Context(), createdCardID, &basecamp.UpdateCardRequest{
					AssigneeIDs: []int64{assigneeID},
				})
				if err != nil {
					sdkErr := convertSDKError(err)
					var e *output.Error
					if errors.As(sdkErr, &e) {
						e.Message = fmt.Sprintf("card %d created but assignment failed: %s", createdCardID, e.Message)
						return e
					}
					return fmt.Errorf("card %d created but assignment failed: %w", createdCardID, sdkErr)
				}
			}

			// Build breadcrumbs - only include --card-table when known
			breadcrumbs := []output.Breadcrumb{
				{
					Action:      "view",
					Cmd:         fmt.Sprintf("basecamp cards show %d", card.ID),
					Description: "View card",
				},
			}
			if cardTableIDVal != "" {
				breadcrumbs = append(breadcrumbs, output.Breadcrumb{
					Action:      "move",
					Cmd:         fmt.Sprintf("basecamp cards move %d --to <column> --card-table %s --in %s", card.ID, cardTableIDVal, resolvedProjectID),
					Description: "Move card",
				})
			} else {
				// When using numeric column ID, move command can also use numeric column ID
				breadcrumbs = append(breadcrumbs, output.Breadcrumb{
					Action:      "move",
					Cmd:         fmt.Sprintf("basecamp cards move %d --to <column-id> --in %s", card.ID, resolvedProjectID),
					Description: "Move card",
				})
			}
			breadcrumbs = append(breadcrumbs, output.Breadcrumb{
				Action:      "list",
				Cmd:         fmt.Sprintf("basecamp cards --in %s", resolvedProjectID),
				Description: "List cards",
			})

			respOpts := []output.ResponseOption{
				output.WithSummary(fmt.Sprintf("Created card #%d", card.ID)),
				output.WithBreadcrumbs(breadcrumbs...),
			}
			if mentionNotice != "" {
				respOpts = append(respOpts, output.WithDiagnostic(mentionNotice))
			}
			return app.OK(card, respOpts...)
		},
	}

	cmd.Flags().StringVarP(&column, "column", "c", "", "Column ID or name (defaults to first column)")
	cmd.Flags().StringVar(&assignee, "assignee", "", "Assignee ID or name")
	cmd.Flags().StringVar(&assignee, "to", "", "Assignee (alias for --assignee)")
	cmd.Flags().StringArrayVar(&attachFiles, "attach", nil, "Attach file (repeatable)")

	completer := completion.NewCompleter(nil)
	_ = cmd.RegisterFlagCompletionFunc("assignee", completer.PeopleNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("to", completer.PeopleNameCompletion())

	return cmd
}

func newCardsUpdateCmd() *cobra.Command {
	var title string
	var content string
	var due string
	var assignee string
	var attachFiles []string

	cmd := &cobra.Command{
		Use:   "update <id|url>",
		Short: "Update a card",
		Long: `Update an existing card.

You can pass either a card ID or a Basecamp URL:
  basecamp cards update 789 --title "new title"
  basecamp cards update 789 --body "new body"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(title) == "" && strings.TrimSpace(content) == "" && due == "" && !cmd.Flags().Changed("assignee") && len(attachFiles) == 0 {
				return noChanges(cmd)
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			cardIDStr := extractID(args[0])

			cardID, err := strconv.ParseInt(cardIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid card ID")
			}

			req := &basecamp.UpdateCardRequest{}
			if title != "" {
				req.Title = title
			}
			var mentionNotice string
			var html string
			if content != "" {
				html = richtext.MarkdownToHTML(content)
				html, err = resolveLocalImages(cmd, app, html)
				if err != nil {
					return err
				}
				mentionResult, mentionErr := resolveMentions(cmd.Context(), app.Names, html)
				if mentionErr != nil {
					return mentionErr
				}
				html = mentionResult.HTML
				mentionNotice = unresolvedMentionWarning(mentionResult.Unresolved)
			}

			// Upload explicit --attach files and embed
			if len(attachFiles) > 0 {
				refs, attachErr := uploadAttachments(cmd, app, attachFiles)
				if attachErr != nil {
					return attachErr
				}
				html = richtext.EmbedAttachments(html, refs)
			}

			if html != "" {
				req.Content = html
			}
			if due != "" {
				req.DueOn = dateparse.Parse(due)
			}
			if cmd.Flags().Changed("assignee") {
				assigneeID, err := resolveAssigneeID(cmd.Context(), app, assignee)
				if err != nil {
					return err
				}
				req.AssigneeIDs = []int64{assigneeID}
			}

			card, err := app.Account().Cards().Update(cmd.Context(), cardID, req)
			if err != nil {
				return convertSDKError(err)
			}

			respOpts := []output.ResponseOption{
				output.WithSummary(fmt.Sprintf("Updated card #%s", cardIDStr)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp cards show %s", cardIDStr),
						Description: "View card",
					},
				),
			}
			if mentionNotice != "" {
				respOpts = append(respOpts, output.WithDiagnostic(mentionNotice))
			}
			return app.OK(card, respOpts...)
		},
	}

	cmd.Flags().StringVarP(&title, "title", "t", "", "New title")
	cmd.Flags().StringVarP(&content, "body", "b", "", "New body content")
	cmd.Flags().StringVarP(&due, "due", "d", "", "Due date (natural language or YYYY-MM-DD)")
	cmd.Flags().StringVar(&assignee, "assignee", "", "Assignee ID or name")
	cmd.Flags().StringArrayVar(&attachFiles, "attach", nil, "Attach file (repeatable)")

	// Register tab completion for assignee flag
	completer := completion.NewCompleter(nil)
	_ = cmd.RegisterFlagCompletionFunc("assignee", completer.PeopleNameCompletion())

	return cmd
}

func newCardsMoveCmd(project, cardTable *string) *cobra.Command {
	var targetColumn string
	var position int
	var onHold bool

	cmd := &cobra.Command{
		Use:   "move <id|url>",
		Short: "Move a card to another column",
		Long: `Move a card to a different column in the card table.

You can pass either a card ID or a Basecamp URL:
  basecamp cards move 789 --to "Done" --in my-project
  basecamp cards move https://3.basecamp.com/123/buckets/456/card_tables/cards/789 --to "Done"
  basecamp cards move 789 --to "Done" --position 1 --in my-project
  basecamp cards move 789 --on-hold --in my-project
  basecamp cards move 789 --to 456 --on-hold --in my-project`,
		Args:    cobra.ExactArgs(1),
		Aliases: []string{"mv"},
		Annotations: map[string]string{
			"agent_notes": "When --on-hold is used without --to, the card moves to the on-hold section of its current column. " +
				"When --on-hold is used with --to, the card moves to the on-hold section of the target column. " +
				"--position cannot be combined with --on-hold.",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if targetColumn == "" && !onHold {
				return missingArg(cmd, "--to")
			}

			positionSet := cmd.Flags().Changed("position") || cmd.Flags().Changed("pos")
			if positionSet && position <= 0 {
				return output.ErrUsage("--position must be a positive integer (1-indexed)")
			}
			if positionSet && onHold {
				return output.ErrUsage("--position cannot be used with --on-hold")
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			cardIDStr, urlProjectID := extractWithProject(args[0])

			cardID, err := strconv.ParseInt(cardIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid card ID")
			}

			isNumericColumn := targetColumn != "" && isNumericID(targetColumn)
			if targetColumn != "" && !isNumericColumn && *cardTable == "" {
				return output.ErrUsage("--card-table is required when --to is a column name")
			}

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

			// --on-hold: move card to on-hold section of current or target column
			if onHold {
				return moveCardOnHold(cmd, app, cardID, cardIDStr, resolvedProjectID, targetColumn, *cardTable)
			}

			var columnID int64
			var cardTableIDVal string
			if isNumericColumn {
				columnID, err = strconv.ParseInt(targetColumn, 10, 64)
				if err != nil {
					return output.ErrUsage("Invalid column ID")
				}
			} else {
				cardTableIDVal, err = getCardTableID(cmd, app, resolvedProjectID, *cardTable)
				if err != nil {
					return err
				}

				cardTableIDInt, err := strconv.ParseInt(cardTableIDVal, 10, 64)
				if err != nil {
					return output.ErrUsage("Invalid card table ID")
				}

				cardTableData, err := app.Account().CardTables().Get(cmd.Context(), cardTableIDInt)
				if err != nil {
					return convertSDKError(err)
				}

				columnID = resolveColumn(cardTableData.Lists, targetColumn)
				if columnID == 0 {
					return output.ErrUsageHint(
						fmt.Sprintf("Column '%s' not found", targetColumn),
						"Use column ID or exact name",
					)
				}
			}

			if positionSet && position > 0 && cardTableIDVal == "" {
				cardTableIDVal, err = getCardTableID(cmd, app, resolvedProjectID, *cardTable)
				if err != nil {
					return err
				}
			}

			if positionSet && position > 0 {
				cardTableIDInt, parseErr := strconv.ParseInt(cardTableIDVal, 10, 64)
				if parseErr != nil {
					return output.ErrUsage("Invalid card table ID")
				}
				err = app.Account().CardColumns().Move(cmd.Context(), cardTableIDInt, &basecamp.MoveColumnRequest{
					SourceID: cardID,
					TargetID: columnID,
					Position: position,
				})
			} else {
				err = app.Account().Cards().Move(cmd.Context(), cardID, columnID, nil)
			}
			if err != nil {
				return convertSDKError(err)
			}

			breadcrumbs := []output.Breadcrumb{
				{
					Action:      "view",
					Cmd:         fmt.Sprintf("basecamp cards show %s --in %s", cardIDStr, resolvedProjectID),
					Description: "View card",
				},
			}
			if cardTableIDVal != "" {
				breadcrumbs = append(breadcrumbs, output.Breadcrumb{
					Action:      "list",
					Cmd:         fmt.Sprintf("basecamp cards --in %s --card-table %s --column \"%s\"", resolvedProjectID, cardTableIDVal, targetColumn),
					Description: "List cards in column",
				})
			}

			result := map[string]any{
				"id":     cardIDStr,
				"status": "moved",
				"column": targetColumn,
			}
			summary := fmt.Sprintf("Moved card #%s to '%s'", cardIDStr, targetColumn)
			if positionSet && position > 0 {
				result["position"] = position
				summary = fmt.Sprintf("Moved card #%s to '%s' at position %d", cardIDStr, targetColumn, position)
			}

			return app.OK(result,
				output.WithSummary(summary),
				output.WithBreadcrumbs(breadcrumbs...),
			)
		},
	}

	cmd.Flags().StringVarP(&targetColumn, "to", "t", "", "Target column ID or name (optional with --on-hold)")
	cmd.Flags().IntVar(&position, "position", 0, "Position in column (1-indexed)")
	cmd.Flags().IntVar(&position, "pos", 0, "Position in column (alias for --position)")
	cmd.Flags().BoolVar(&onHold, "on-hold", false, "Move card to the on-hold section of its current (or target) column")

	return cmd
}

func moveCardOnHold(cmd *cobra.Command, app *appctx.App, cardID int64, cardIDStr, projectID, targetColumn, cardTableFlag string) error {
	var column *basecamp.CardColumn

	if targetColumn != "" && isNumericID(targetColumn) {
		columnID, err := strconv.ParseInt(targetColumn, 10, 64)
		if err != nil {
			return output.ErrUsage("Invalid column ID")
		}
		col, err := app.Account().CardColumns().Get(cmd.Context(), columnID)
		if err != nil {
			return convertSDKError(err)
		}
		column = col
	} else if targetColumn == "" {
		card, err := app.Account().Cards().Get(cmd.Context(), cardID)
		if err != nil {
			return convertSDKError(err)
		}
		if card.Parent == nil {
			return output.ErrUsageHint(
				"Card has no parent column",
				fmt.Sprintf("Specify the target column: basecamp cards move %s --to <column-id> --on-hold", cardIDStr),
			)
		}
		col, err := app.Account().CardColumns().Get(cmd.Context(), card.Parent.ID)
		if err != nil {
			return convertSDKError(err)
		}
		column = col
	} else {
		cardTableIDVal, err := getCardTableID(cmd, app, projectID, cardTableFlag)
		if err != nil {
			return err
		}
		cardTableIDInt, err := strconv.ParseInt(cardTableIDVal, 10, 64)
		if err != nil {
			return output.ErrUsage("Invalid card table ID")
		}
		cardTableData, err := app.Account().CardTables().Get(cmd.Context(), cardTableIDInt)
		if err != nil {
			return convertSDKError(err)
		}
		colID := resolveColumn(cardTableData.Lists, targetColumn)
		if colID == 0 {
			return output.ErrUsageHint(
				fmt.Sprintf("Column '%s' not found", targetColumn),
				"Use column ID or exact name",
			)
		}
		for i := range cardTableData.Lists {
			if cardTableData.Lists[i].ID == colID {
				column = &cardTableData.Lists[i]
				break
			}
		}
	}

	if column.OnHold == nil || column.OnHold.ID == 0 {
		return output.ErrUsageHint(
			fmt.Sprintf("Column '%s' does not have an on-hold section", column.Title),
			fmt.Sprintf("Enable on-hold with: basecamp cards column on-hold %d", column.ID),
		)
	}

	err := app.Account().Cards().Move(cmd.Context(), cardID, column.OnHold.ID, nil)
	if err != nil {
		return convertSDKError(err)
	}

	result := map[string]any{
		"id":      cardIDStr,
		"status":  "moved",
		"column":  column.Title,
		"on_hold": true,
	}
	summary := fmt.Sprintf("Moved card #%s to on-hold in '%s'", cardIDStr, column.Title)

	return app.OK(result,
		output.WithSummary(summary),
		output.WithBreadcrumbs(output.Breadcrumb{
			Action:      "view",
			Cmd:         fmt.Sprintf("basecamp cards show %s --in %s", cardIDStr, projectID),
			Description: "View card",
		}),
	)
}

func newCardsColumnsCmd(project, cardTable *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "columns",
		Short: "List columns",
		Long:  "List all columns in a project's card table with their IDs.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
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

			// Get card table ID from project dock
			cardTableID, err := getCardTableID(cmd, app, resolvedProjectID, *cardTable)
			if err != nil {
				return err
			}

			cardTableIDInt, err := strconv.ParseInt(cardTableID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid card table ID")
			}

			// Get card table with embedded columns (lists)
			cardTableData, err := app.Account().CardTables().Get(cmd.Context(), cardTableIDInt)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(cardTableData.Lists,
				output.WithSummary(fmt.Sprintf("%d columns", len(cardTableData.Lists))),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "cards",
						Cmd:         fmt.Sprintf("basecamp cards --in %s --card-table %s --column <id>", resolvedProjectID, cardTableID),
						Description: "List cards in column",
					},
					output.Breadcrumb{
						Action:      "create",
						Cmd:         fmt.Sprintf("basecamp card --title <title> --in %s --card-table %s --column <id>", resolvedProjectID, cardTableID),
						Description: "Create card in column",
					},
				),
			)
		},
	}
	return cmd
}

// NewCardCmd creates the card command (shortcut for creating cards).
func NewCardCmd() *cobra.Command {
	var project string
	var column string
	var cardTable string
	var assignee string
	var attachFiles []string

	cmd := &cobra.Command{
		Use:   "card <title> [body]",
		Short: "Create a new card",
		Long:  "Create a card in a project's card table. Shortcut for 'basecamp cards create'.",
		Example: `  basecamp card "My card" --in myproject
  basecamp card --in myproject -- "--title with dashes"`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked with no title
			if len(args) == 0 {
				return missingArg(cmd, "<title>")
			}

			title := args[0]
			if strings.TrimSpace(title) == "" {
				return cmd.Help()
			}
			var content string
			if len(args) > 1 {
				content = args[1]
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Column name (non-numeric) requires --card-table for resolution
			// Numeric column IDs can be used directly without card table discovery
			if column != "" && !isNumericID(column) && cardTable == "" {
				return output.ErrUsage("--card-table is required when using --column with a name")
			}

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

			// If column is a numeric ID, use it directly without card table discovery
			var columnID int64
			var cardTableIDVal string
			if column != "" && isNumericID(column) {
				columnID, err = strconv.ParseInt(column, 10, 64)
				if err != nil {
					return output.ErrUsage("Invalid column ID")
				}
				cardTableIDVal = "" // Not needed for numeric column ID
			} else {
				// Need to discover card table and resolve column
				cardTableIDVal, err = getCardTableID(cmd, app, resolvedProjectID, cardTable)
				if err != nil {
					return err
				}

				cardTableIDInt, err := strconv.ParseInt(cardTableIDVal, 10, 64)
				if err != nil {
					return output.ErrUsage("Invalid card table ID")
				}

				// Get card table with embedded columns (lists)
				cardTableData, err := app.Account().CardTables().Get(cmd.Context(), cardTableIDInt)
				if err != nil {
					return convertSDKError(err)
				}

				// Find target column
				if column != "" {
					columnID = resolveColumn(cardTableData.Lists, column)
					if columnID == 0 {
						return output.ErrUsageHint(
							fmt.Sprintf("Column '%s' not found", column),
							"Use column ID or exact name",
						)
					}
				} else {
					// Use first column
					if len(cardTableData.Lists) == 0 {
						return output.ErrNotFound("columns", resolvedProjectID)
					}
					columnID = cardTableData.Lists[0].ID
				}
			}

			// Pre-resolve assignee before side-effectful work (fail early on bad input)
			var assigneeID int64
			if cmd.Flags().Changed("assignee") || cmd.Flags().Changed("to") {
				assigneeID, err = resolveAssigneeID(cmd.Context(), app, assignee)
				if err != nil {
					return err
				}
			}

			// Convert content through rich text pipeline
			var mentionNotice string
			if content != "" {
				content = richtext.MarkdownToHTML(content)
				content, err = resolveLocalImages(cmd, app, content)
				if err != nil {
					return err
				}
				mentionResult, mentionErr := resolveMentions(cmd.Context(), app.Names, content)
				if mentionErr != nil {
					return mentionErr
				}
				content = mentionResult.HTML
				mentionNotice = unresolvedMentionWarning(mentionResult.Unresolved)
			}

			// Upload explicit --attach files and embed
			if len(attachFiles) > 0 {
				refs, attachErr := uploadAttachments(cmd, app, attachFiles)
				if attachErr != nil {
					return attachErr
				}
				content = richtext.EmbedAttachments(content, refs)
			}

			// Build request
			req := &basecamp.CreateCardRequest{
				Title:   title,
				Content: content,
			}

			card, err := app.Account().Cards().Create(cmd.Context(), columnID, req)
			if err != nil {
				return convertSDKError(err)
			}

			if assigneeID != 0 {
				createdCardID := card.ID
				card, err = app.Account().Cards().Update(cmd.Context(), createdCardID, &basecamp.UpdateCardRequest{
					AssigneeIDs: []int64{assigneeID},
				})
				if err != nil {
					sdkErr := convertSDKError(err)
					var e *output.Error
					if errors.As(sdkErr, &e) {
						e.Message = fmt.Sprintf("card %d created but assignment failed: %s", createdCardID, e.Message)
						return e
					}
					return fmt.Errorf("card %d created but assignment failed: %w", createdCardID, sdkErr)
				}
			}

			// Build breadcrumbs - only include --card-table when known
			cardBreadcrumbs := []output.Breadcrumb{
				{
					Action:      "view",
					Cmd:         fmt.Sprintf("basecamp cards show %d", card.ID),
					Description: "View card",
				},
			}
			if cardTableIDVal != "" {
				cardBreadcrumbs = append(cardBreadcrumbs, output.Breadcrumb{
					Action:      "move",
					Cmd:         fmt.Sprintf("basecamp cards move %d --to <column> --card-table %s --in %s", card.ID, cardTableIDVal, resolvedProjectID),
					Description: "Move card",
				})
			} else {
				cardBreadcrumbs = append(cardBreadcrumbs, output.Breadcrumb{
					Action:      "move",
					Cmd:         fmt.Sprintf("basecamp cards move %d --to <column-id> --in %s", card.ID, resolvedProjectID),
					Description: "Move card",
				})
			}
			cardBreadcrumbs = append(cardBreadcrumbs, output.Breadcrumb{
				Action:      "list",
				Cmd:         fmt.Sprintf("basecamp cards --in %s", resolvedProjectID),
				Description: "List cards",
			})

			respOpts := []output.ResponseOption{
				output.WithSummary(fmt.Sprintf("Created card #%d", card.ID)),
				output.WithBreadcrumbs(cardBreadcrumbs...),
			}
			if mentionNotice != "" {
				respOpts = append(respOpts, output.WithDiagnostic(mentionNotice))
			}
			return app.OK(card, respOpts...)
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.Flags().StringVarP(&column, "column", "c", "", "Column ID or name (defaults to first column)")
	cmd.Flags().StringVar(&assignee, "assignee", "", "Assignee ID or name")
	cmd.Flags().StringVar(&assignee, "to", "", "Assignee (alias for --assignee)")
	cmd.PersistentFlags().StringVar(&cardTable, "card-table", "", "Card table ID (required if project has multiple)")
	cmd.Flags().StringArrayVar(&attachFiles, "attach", nil, "Attach file (repeatable)")

	cardCompleter := completion.NewCompleter(nil)
	_ = cmd.RegisterFlagCompletionFunc("assignee", cardCompleter.PeopleNameCompletion())
	_ = cmd.RegisterFlagCompletionFunc("to", cardCompleter.PeopleNameCompletion())

	cmd.AddCommand(
		newCardsUpdateCmd(),
		newCardsMoveCmd(&project, &cardTable),
	)

	return cmd
}

// newCardsColumnCmd creates the column management subcommand.
func newCardsColumnCmd(project, cardTable *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "column",
		Short: "Manage columns",
		Long:  "Show, create, and manage card table columns.",
	}

	cmd.AddCommand(
		newCardsColumnShowCmd(project),
		newCardsColumnCreateCmd(project, cardTable),
		newCardsColumnUpdateCmd(),
		newCardsColumnMoveCmd(project, cardTable),
		newCardsColumnWatchCmd(),
		newCardsColumnUnwatchCmd(),
		newCardsColumnOnHoldCmd(),
		newCardsColumnNoOnHoldCmd(),
		newCardsColumnColorCmd(),
	)

	return cmd
}

func newCardsColumnShowCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show column details",
		Long: `Display detailed information about a column.

You can pass either a column ID or a Basecamp URL:
  basecamp cards column show 789 --in my-project
  basecamp cards column show https://3.basecamp.com/123/buckets/456/card_tables/columns/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID and project from URL if provided
			columnIDStr, urlProjectID := extractWithProject(args[0])
			columnID, err := strconv.ParseInt(columnIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid column ID")
			}

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

			col, err := app.Account().CardColumns().Get(cmd.Context(), columnID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(col,
				output.WithSummary(fmt.Sprintf("%s (%d cards)", col.Title, col.CardsCount)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("basecamp cards column update %s --in %s", columnIDStr, resolvedProjectID),
						Description: "Update column",
					},
					output.Breadcrumb{
						Action:      "columns",
						Cmd:         fmt.Sprintf("basecamp cards columns --in %s", resolvedProjectID),
						Description: "List all columns",
					},
				),
			)
		},
	}
	return cmd
}

func newCardsColumnCreateCmd(project, cardTable *string) *cobra.Command {
	var description string

	cmd := &cobra.Command{
		Use:   "create <title>",
		Short: "Create a column",
		Long:  "Create a new column in the card table.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked with no title
			if len(args) == 0 {
				return missingArg(cmd, "<title>")
			}

			title := args[0]

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
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

			// Get card table ID
			cardTableID, err := getCardTableID(cmd, app, resolvedProjectID, *cardTable)
			if err != nil {
				return err
			}

			cardTableIDInt, err := strconv.ParseInt(cardTableID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid card table ID")
			}

			req := &basecamp.CreateColumnRequest{
				Title:       title,
				Description: description,
			}

			col, err := app.Account().CardColumns().Create(cmd.Context(), cardTableIDInt, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(col,
				output.WithSummary(fmt.Sprintf("Created column: %s", col.Title)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "column",
						Cmd:         fmt.Sprintf("basecamp cards column show %d --in %s", col.ID, resolvedProjectID),
						Description: "View column",
					},
					output.Breadcrumb{
						Action:      "columns",
						Cmd:         fmt.Sprintf("basecamp cards columns --in %s", resolvedProjectID),
						Description: "List columns",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&description, "description", "d", "", "Column description")

	return cmd
}

func newCardsColumnUpdateCmd() *cobra.Command {
	var title string
	var description string

	cmd := &cobra.Command{
		Use:   "update <id|url>",
		Short: "Update a column",
		Long: `Update an existing card table column.

You can pass either a column ID or a Basecamp URL:
  basecamp cards column update 789 --title "new name"
  basecamp cards column update 789 --description "new desc"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if title == "" && description == "" {
				return noChanges(cmd)
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			columnIDStr := extractID(args[0])
			columnID, err := strconv.ParseInt(columnIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid column ID")
			}

			req := &basecamp.UpdateColumnRequest{
				Title:       title,
				Description: description,
			}

			col, err := app.Account().CardColumns().Update(cmd.Context(), columnID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(col,
				output.WithSummary(fmt.Sprintf("Updated column #%s", columnIDStr)),
			)
		},
	}

	cmd.Flags().StringVarP(&title, "title", "t", "", "New title")
	cmd.Flags().StringVarP(&description, "description", "d", "", "New description")

	return cmd
}

func newCardsColumnMoveCmd(project, cardTable *string) *cobra.Command {
	var position int

	cmd := &cobra.Command{
		Use:   "move <id|url>",
		Short: "Move a column",
		Long: `Reposition a column within the card table.

You can pass either a column ID or a Basecamp URL:
  basecamp cards column move 789 --position 2 --in my-project
  basecamp cards column move https://3.basecamp.com/123/buckets/456/card_tables/columns/789 --position 2`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID and project from URL if provided
			columnIDStr, urlProjectID := extractWithProject(args[0])
			columnID, err := strconv.ParseInt(columnIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Column ID must be numeric")
			}

			if position <= 0 {
				return output.ErrUsage("--position required (1-indexed)")
			}

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

			// Get card table ID
			cardTableID, err := getCardTableID(cmd, app, resolvedProjectID, *cardTable)
			if err != nil {
				return err
			}

			cardTableIDInt, err := strconv.ParseInt(cardTableID, 10, 64)
			if err != nil {
				return output.ErrUsage("Card table ID must be numeric")
			}

			req := &basecamp.MoveColumnRequest{
				SourceID: columnID,
				TargetID: cardTableIDInt,
				Position: position,
			}

			err = app.Account().CardColumns().Move(cmd.Context(), cardTableIDInt, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{
				"moved":    true,
				"id":       columnIDStr,
				"position": position,
			}, output.WithSummary(fmt.Sprintf("Moved column #%s to position %d", columnIDStr, position)))
		},
	}

	cmd.Flags().IntVar(&position, "position", 0, "Target position (1-indexed)")
	cmd.Flags().IntVar(&position, "pos", 0, "Target position (alias for --position)")

	return cmd
}

func newCardsColumnWatchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch <id|url>",
		Short: "Watch a column",
		Long: `Subscribe to updates for a column.

You can pass either a column ID or a Basecamp URL:
  basecamp cards column watch 789 --in my-project
  basecamp cards column watch https://3.basecamp.com/123/buckets/456/card_tables/columns/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			columnIDStr := extractID(args[0])
			columnID, err := strconv.ParseInt(columnIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid column ID")
			}

			_, err = app.Account().CardColumns().Watch(cmd.Context(), columnID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{
				"watching": true,
				"id":       columnIDStr,
			}, output.WithSummary(fmt.Sprintf("Now watching column #%s", columnIDStr)))
		},
	}
	return cmd
}

func newCardsColumnUnwatchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unwatch <id|url>",
		Short: "Unwatch a column",
		Long: `Unsubscribe from updates for a column.

You can pass either a column ID or a Basecamp URL:
  basecamp cards column unwatch 789 --in my-project
  basecamp cards column unwatch https://3.basecamp.com/123/buckets/456/card_tables/columns/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			columnIDStr := extractID(args[0])
			columnID, err := strconv.ParseInt(columnIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid column ID")
			}

			err = app.Account().CardColumns().Unwatch(cmd.Context(), columnID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{
				"watching": false,
				"id":       columnIDStr,
			}, output.WithSummary(fmt.Sprintf("Stopped watching column #%s", columnIDStr)))
		},
	}
	return cmd
}

func newCardsColumnOnHoldCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "on-hold <id|url>",
		Short: "Enable on-hold section",
		Long: `Enable on-hold section for a column.

You can pass either a column ID or a Basecamp URL:
  basecamp cards column on-hold 789 --in my-project
  basecamp cards column on-hold https://3.basecamp.com/123/buckets/456/card_tables/columns/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			columnIDStr := extractID(args[0])
			columnID, err := strconv.ParseInt(columnIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid column ID")
			}

			col, err := app.Account().CardColumns().EnableOnHold(cmd.Context(), columnID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(col,
				output.WithSummary(fmt.Sprintf("Enabled on-hold for column #%s", columnIDStr)),
			)
		},
	}
	return cmd
}

func newCardsColumnNoOnHoldCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "no-on-hold <id|url>",
		Short: "Disable on-hold section",
		Long: `Disable on-hold section for a column.

You can pass either a column ID or a Basecamp URL:
  basecamp cards column no-on-hold 789 --in my-project
  basecamp cards column no-on-hold https://3.basecamp.com/123/buckets/456/card_tables/columns/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			columnIDStr := extractID(args[0])
			columnID, err := strconv.ParseInt(columnIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid column ID")
			}

			col, err := app.Account().CardColumns().DisableOnHold(cmd.Context(), columnID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(col,
				output.WithSummary(fmt.Sprintf("Disabled on-hold for column #%s", columnIDStr)),
			)
		},
	}
	return cmd
}

func newCardsColumnColorCmd() *cobra.Command {
	var color string

	cmd := &cobra.Command{
		Use:   "color <id|url>",
		Short: "Set column color",
		Long: `Set the color for a column.

You can pass either a column ID or a Basecamp URL:
  basecamp cards column color 789 --color blue --in my-project
  basecamp cards column color https://3.basecamp.com/123/buckets/456/card_tables/columns/789 --color blue`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked with no color flag
			if color == "" {
				return missingArg(cmd, "--color")
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			columnIDStr := extractID(args[0])
			columnID, err := strconv.ParseInt(columnIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid column ID")
			}

			col, err := app.Account().CardColumns().SetColor(cmd.Context(), columnID, color)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(col,
				output.WithSummary(fmt.Sprintf("Set column #%s color to %s", columnIDStr, color)),
			)
		},
	}

	cmd.Flags().StringVarP(&color, "color", "c", "", "Column color")

	return cmd
}

// newCardsStepsCmd creates the steps listing subcommand.
func newCardsStepsCmd(project *string) *cobra.Command {
	var cardID string

	cmd := &cobra.Command{
		Use:   "steps <card-id|url>",
		Short: "List steps on a card",
		Long:  "Display all steps (checklist items) on a card.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Accept card ID as positional arg or flag
			if len(args) > 0 {
				cardID = args[0]
			}
			if cardID == "" {
				return missingArg(cmd, "<card-id|url>")
			}

			cardIDInt, err := strconv.ParseInt(cardID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid card ID")
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

			// Get card with steps
			card, err := app.Account().Cards().Get(cmd.Context(), cardIDInt)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(card.Steps,
				output.WithSummary(fmt.Sprintf("%d steps on card #%s", len(card.Steps), cardID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "create",
						Cmd:         fmt.Sprintf("basecamp cards step create --title <title> --card %s --in %s", cardID, resolvedProjectID),
						Description: "Add step",
					},
					output.Breadcrumb{
						Action:      "card",
						Cmd:         fmt.Sprintf("basecamp cards show %s --in %s", cardID, resolvedProjectID),
						Description: "View card",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&cardID, "card", "c", "", "Card ID")

	return cmd
}

// newCardsStepCmd creates the step management subcommand.
func newCardsStepCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "step",
		Short: "Manage steps",
		Long:  "Create, complete, and manage card steps.",
	}

	cmd.AddCommand(
		newCardsStepCreateCmd(project),
		newCardsStepUpdateCmd(),
		newCardsStepCompleteCmd(),
		newCardsStepUncompleteCmd(),
		newCardsStepMoveCmd(),
		newCardsStepDeleteCmd(),
	)

	return cmd
}

func newCardsStepCreateCmd(project *string) *cobra.Command {
	var cardID string
	var dueOn string
	var assignees string

	cmd := &cobra.Command{
		Use:   "create <title>",
		Short: "Create a step",
		Long:  "Add a new step (checklist item) to a card.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked with no title
			if len(args) == 0 {
				return missingArg(cmd, "<title>")
			}

			title := args[0]

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			if cardID == "" {
				return output.ErrUsage("--card is required")
			}

			cardIDInt, err := strconv.ParseInt(cardID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid card ID")
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

			req := &basecamp.CreateStepRequest{
				Title: title,
			}
			if dueOn != "" {
				req.DueOn = dateparse.Parse(dueOn)
			}
			if assignees != "" {
				assigneeIDs, err := resolveAssigneeIDs(cmd.Context(), app, assignees)
				if err != nil {
					return err
				}
				req.AssigneeIDs = assigneeIDs
			}

			step, err := app.Account().CardSteps().Create(cmd.Context(), cardIDInt, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(step,
				output.WithSummary(fmt.Sprintf("Created step: %s", step.Title)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "complete",
						Cmd:         fmt.Sprintf("basecamp cards step complete %d --in %s", step.ID, resolvedProjectID),
						Description: "Complete step",
					},
					output.Breadcrumb{
						Action:      "steps",
						Cmd:         fmt.Sprintf("basecamp cards steps %s --in %s", cardID, resolvedProjectID),
						Description: "List steps",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&cardID, "card", "c", "", "Card ID (required)")
	cmd.Flags().StringVarP(&dueOn, "due", "d", "", "Due date (natural language or YYYY-MM-DD)")
	cmd.Flags().StringVar(&assignees, "assignees", "", "Assignees (IDs or names, comma-separated)")

	return cmd
}

func newCardsStepUpdateCmd() *cobra.Command {
	var dueOn string
	var assignees string

	cmd := &cobra.Command{
		Use:   "update <step_id|url> [title]",
		Short: "Update a step",
		Long: `Update an existing step on a card.

You can pass either a step ID or a Basecamp URL:
  basecamp cards step update 789 "new title"
  basecamp cards step update https://3.basecamp.com/123/buckets/456/card_tables/cards/steps/789 "new title"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked with no args
			if len(args) == 0 {
				return missingArg(cmd, "<step_id|url>")
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			stepIDStr := extractID(args[0])
			stepID, err := strconv.ParseInt(stepIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid step ID")
			}

			var title string
			if len(args) > 1 {
				title = args[1]
			}

			if len(args) < 2 && dueOn == "" && assignees == "" {
				return noChanges(cmd)
			}

			req := &basecamp.UpdateStepRequest{}
			if title != "" {
				req.Title = title
			}
			if dueOn != "" {
				req.DueOn = dateparse.Parse(dueOn)
			}
			if assignees != "" {
				assigneeIDs, err := resolveAssigneeIDs(cmd.Context(), app, assignees)
				if err != nil {
					return err
				}
				req.AssigneeIDs = assigneeIDs
			}

			step, err := app.Account().CardSteps().Update(cmd.Context(), stepID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(step,
				output.WithSummary(fmt.Sprintf("Updated step #%s", stepIDStr)),
			)
		},
	}

	cmd.Flags().StringVarP(&dueOn, "due", "d", "", "Due date (natural language or YYYY-MM-DD)")
	cmd.Flags().StringVar(&assignees, "assignees", "", "Assignees (IDs or names, comma-separated)")

	return cmd
}

func newCardsStepCompleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "complete <step_id|url>",
		Short: "Complete a step",
		Long: `Mark a step as completed.

You can pass either a step ID or a Basecamp URL:
  basecamp cards step complete 789 --in my-project
  basecamp cards step complete https://3.basecamp.com/123/buckets/456/card_tables/cards/steps/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			stepIDStr := extractID(args[0])
			stepID, err := strconv.ParseInt(stepIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid step ID")
			}

			step, err := app.Account().CardSteps().Complete(cmd.Context(), stepID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(step,
				output.WithSummary(fmt.Sprintf("Completed step #%s", stepIDStr)),
			)
		},
	}
	return cmd
}

func newCardsStepUncompleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uncomplete <step_id|url>",
		Short: "Uncomplete a step",
		Long: `Mark a step as not completed.

You can pass either a step ID or a Basecamp URL:
  basecamp cards step uncomplete 789 --in my-project
  basecamp cards step uncomplete https://3.basecamp.com/123/buckets/456/card_tables/cards/steps/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			stepIDStr := extractID(args[0])
			stepID, err := strconv.ParseInt(stepIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid step ID")
			}

			step, err := app.Account().CardSteps().Uncomplete(cmd.Context(), stepID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(step,
				output.WithSummary(fmt.Sprintf("Uncompleted step #%s", stepIDStr)),
			)
		},
	}
	return cmd
}

func newCardsStepMoveCmd() *cobra.Command {
	var cardID string
	var position int

	cmd := &cobra.Command{
		Use:   "move <step_id|url>",
		Short: "Move a step",
		Long: `Reposition a step within a card (0-indexed).

You can pass either a step ID or a Basecamp URL:
  basecamp cards step move 789 --card 456 --position 0 --in my-project
  basecamp cards step move https://3.basecamp.com/123/buckets/456/card_tables/cards/steps/789 --card 456 --position 0`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked with no card flag
			if cardID == "" {
				return missingArg(cmd, "--card")
			}

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			stepIDStr := extractID(args[0])
			stepID, err := strconv.ParseInt(stepIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Step ID must be numeric")
			}
			if position < 0 {
				return output.ErrUsage("--position is required (0-indexed)")
			}

			cardIDInt, err := strconv.ParseInt(cardID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid card ID")
			}

			err = app.Account().CardSteps().Reposition(cmd.Context(), cardIDInt, stepID, position)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{
				"moved":    true,
				"id":       stepIDStr,
				"position": position,
			}, output.WithSummary(fmt.Sprintf("Moved step #%s to position %d", stepIDStr, position)))
		},
	}

	cmd.Flags().StringVarP(&cardID, "card", "c", "", "Card ID (required)")
	cmd.Flags().IntVar(&position, "position", -1, "Target position (0-indexed)")
	cmd.Flags().IntVar(&position, "pos", -1, "Target position (alias for --position)")

	return cmd
}

func newCardsStepDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <step_id|url>",
		Short: "Delete a step",
		Long: `Permanently delete a step from a card.

You can pass either a step ID or a Basecamp URL:
  basecamp cards step delete 789 --in my-project
  basecamp cards step delete https://3.basecamp.com/123/buckets/456/card_tables/cards/steps/789`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Extract ID from URL if provided
			stepIDStr := extractID(args[0])
			stepID, err := strconv.ParseInt(stepIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid step ID")
			}

			err = app.Account().CardSteps().Delete(cmd.Context(), stepID)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(map[string]any{"deleted": true},
				output.WithSummary(fmt.Sprintf("Deleted step #%s", stepIDStr)),
			)
		},
	}

	return cmd
}

// getCardTableID retrieves the card table ID from a project's dock.
// If the project has multiple card tables and no explicit cardTableID is provided,
// an error is returned with the available card table IDs.
func getCardTableID(cmd *cobra.Command, app *appctx.App, projectID, explicitCardTableID string) (string, error) {
	path := fmt.Sprintf("/projects/%s.json", projectID)
	resp, err := app.Account().Get(cmd.Context(), path)
	if err != nil {
		return "", convertSDKError(err)
	}

	var project struct {
		Dock []struct {
			Name  string `json:"name"`
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"dock"`
	}
	if err := resp.UnmarshalData(&project); err != nil {
		return "", fmt.Errorf("failed to parse project: %w", err)
	}

	// Collect all card tables from dock
	var cardTables []struct {
		ID    int64
		Title string
	}
	for _, item := range project.Dock {
		if item.Name == "kanban_board" {
			cardTables = append(cardTables, struct {
				ID    int64
				Title string
			}{ID: item.ID, Title: item.Title})
		}
	}

	if len(cardTables) == 0 {
		return "", output.ErrNotFound("card table", projectID)
	}

	// If explicit card table ID provided, validate it exists
	if explicitCardTableID != "" {
		var idInt int64
		if _, err := fmt.Sscanf(explicitCardTableID, "%d", &idInt); err == nil {
			for _, ct := range cardTables {
				if ct.ID == idInt {
					return explicitCardTableID, nil
				}
			}
		}
		return "", output.ErrUsageHint(
			fmt.Sprintf("Card table '%s' not found", explicitCardTableID),
			fmt.Sprintf("Available card tables: %s", formatCardTableIDs(cardTables)),
		)
	}

	// Single card table - return it
	if len(cardTables) == 1 {
		return fmt.Sprintf("%d", cardTables[0].ID), nil
	}

	// Multiple card tables - error with available IDs
	lines := make([]string, 0, len(cardTables))
	for _, ct := range cardTables {
		title := ct.Title
		if title == "" {
			title = "card table"
		}
		lines = append(lines, fmt.Sprintf("  %d  %s", ct.ID, title))
	}
	return "", &output.Error{
		Code:    output.CodeAmbiguous,
		Message: "Multiple card tables found",
		Hint:    fmt.Sprintf("Specify one with --card-table <id>:\n%s", strings.Join(lines, "\n")),
	}
}

// formatCardTableIDs formats card table IDs for error messages.
func formatCardTableIDs(cardTables []struct {
	ID    int64
	Title string
}) string {
	ids := make([]string, len(cardTables))
	for i, ct := range cardTables {
		if ct.Title != "" {
			ids[i] = fmt.Sprintf("%d (%s)", ct.ID, ct.Title)
		} else {
			ids[i] = fmt.Sprintf("%d", ct.ID)
		}
	}
	return fmt.Sprintf("%v", ids)
}

// isNumericID checks if a string consists only of digits (matches bash: [[ "$s" =~ ^[0-9]+$ ]]).
func isNumericID(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// resolveColumn finds a column by ID or name.
func resolveColumn(columns []basecamp.CardColumn, identifier string) int64 {
	// Try by ID first
	idInt, err := strconv.ParseInt(identifier, 10, 64)
	if err == nil {
		for _, col := range columns {
			if col.ID == idInt {
				return col.ID
			}
		}
	}

	// Fall back to name match
	for _, col := range columns {
		if col.Title == identifier {
			return col.ID
		}
	}

	return 0
}

func resolveAssigneeIDs(ctx context.Context, app *appctx.App, input string) ([]int64, error) {
	parts := strings.Split(input, ",")
	ids := make([]int64, 0, len(parts))

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		id, err := resolveAssigneeID(ctx, app, part)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}

	if len(ids) == 0 {
		return nil, output.ErrUsage("No valid assignees provided")
	}

	return ids, nil
}
