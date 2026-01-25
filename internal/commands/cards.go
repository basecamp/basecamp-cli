package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/dateparse"
	"github.com/basecamp/bcq/internal/output"
)

// Card represents a Basecamp card.
type Card struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Content   string `json:"content,omitempty"`
	DueOn     string `json:"due_on,omitempty"`
	CreatedAt string `json:"created_at"`
	Parent    struct {
		Title string `json:"title"`
	} `json:"parent"`
	Assignees []struct {
		Name string `json:"name"`
	} `json:"assignees"`
}

// CardColumn represents a card table column.
type CardColumn struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	CardsCount int    `json:"cards_count"`
}

// NewCardsCmd creates the cards command group.
func NewCardsCmd() *cobra.Command {
	var project string
	var column string
	var cardTable string

	cmd := &cobra.Command{
		Use:   "cards",
		Short: "Manage cards in Card Tables",
		Long:  "List, show, create, and manage cards in Card Tables (Kanban boards).",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Default to list when called without subcommand
			return runCardsList(cmd, project, column, cardTable)
		},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.PersistentFlags().StringVar(&cardTable, "card-table", "", "Card table ID (required if project has multiple)")
	cmd.Flags().StringVarP(&column, "column", "c", "", "Filter by column ID or name")

	cmd.AddCommand(
		newCardsListCmd(&project, &cardTable),
		newCardsShowCmd(&project),
		newCardsCreateCmd(&project, &cardTable),
		newCardsUpdateCmd(&project),
		newCardsMoveCmd(&project, &cardTable),
		newCardsColumnsCmd(&project, &cardTable),
		newCardsColumnCmd(&project, &cardTable),
		newCardsStepsCmd(&project),
		newCardsStepCmd(&project),
	)

	return cmd
}

func newCardsListCmd(project, cardTable *string) *cobra.Command {
	var column string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List cards",
		Long:  "List all cards in a project's card table.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCardsList(cmd, *project, column, *cardTable)
		},
	}

	cmd.Flags().StringVarP(&column, "column", "c", "", "Filter by column ID or name")

	return cmd
}

func runCardsList(cmd *cobra.Command, project, column, cardTable string) error {
	app := appctx.FromContext(cmd.Context())

	// Resolve project first (validate before account check)
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

	// Column name (non-numeric) requires --card-table for resolution
	// Numeric column IDs can be used directly without discovery
	if column != "" && !isNumericID(column) && cardTable == "" {
		return output.ErrUsage("--card-table is required when using --column with a name")
	}

	if err := app.API.RequireAccount(); err != nil {
		return err
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	// Optimization: If column is a numeric ID, skip card table discovery
	// and fetch cards directly from the column endpoint
	if column != "" && isNumericID(column) {
		var allCards []Card
		cardsPath := fmt.Sprintf("/buckets/%s/card_tables/lists/%s/cards.json", resolvedProjectID, column)
		cardsResp, err := app.API.Get(cmd.Context(), cardsPath)
		if err != nil {
			return err
		}
		if err := cardsResp.UnmarshalData(&allCards); err != nil {
			return fmt.Errorf("failed to parse cards: %w", err)
		}

		return app.Output.OK(allCards,
			output.WithSummary(fmt.Sprintf("%d cards", len(allCards))),
			output.WithBreadcrumbs(
				output.Breadcrumb{
					Action:      "create",
					Cmd:         fmt.Sprintf("bcq card --title <title> --in %s", resolvedProjectID),
					Description: "Create card",
				},
				output.Breadcrumb{
					Action:      "show",
					Cmd:         "bcq cards show <id>",
					Description: "Show card details",
				},
			),
		)
	}

	// Get card table ID from project dock
	cardTableID, err := getCardTableID(cmd, app, resolvedProjectID, cardTable)
	if err != nil {
		return err
	}

	// Get card table with embedded columns (lists)
	cardTablePath := fmt.Sprintf("/buckets/%s/card_tables/%s.json", resolvedProjectID, cardTableID)
	cardTableResp, err := app.API.Get(cmd.Context(), cardTablePath)
	if err != nil {
		return err
	}

	var cardTableData struct {
		Lists []CardColumn `json:"lists"`
	}
	if err := json.Unmarshal(cardTableResp.Data, &cardTableData); err != nil {
		return fmt.Errorf("failed to parse card table: %w", err)
	}

	// Get cards from all columns or specific column
	var allCards []Card
	if column != "" {
		// Find column by ID or name
		columnID := resolveColumn(cardTableData.Lists, column)
		if columnID == "" {
			return output.ErrUsageHint(
				fmt.Sprintf("Column '%s' not found", column),
				"Use column ID or exact name",
			)
		}
		cardsPath := fmt.Sprintf("/buckets/%s/card_tables/lists/%s/cards.json", resolvedProjectID, columnID)
		cardsResp, err := app.API.Get(cmd.Context(), cardsPath)
		if err != nil {
			return err
		}
		if err := cardsResp.UnmarshalData(&allCards); err != nil {
			return fmt.Errorf("failed to parse cards: %w", err)
		}
	} else {
		// Get cards from all columns
		for _, col := range cardTableData.Lists {
			cardsPath := fmt.Sprintf("/buckets/%s/card_tables/lists/%d/cards.json", resolvedProjectID, col.ID)
			cardsResp, err := app.API.Get(cmd.Context(), cardsPath)
			if err != nil {
				continue // Skip columns with errors
			}
			var cards []Card
			if err := cardsResp.UnmarshalData(&cards); err != nil {
				continue
			}
			allCards = append(allCards, cards...)
		}
	}

	return app.Output.OK(allCards,
		output.WithSummary(fmt.Sprintf("%d cards", len(allCards))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "create",
				Cmd:         fmt.Sprintf("bcq card --title <title> --in %s", resolvedProjectID),
				Description: "Create card",
			},
			output.Breadcrumb{
				Action:      "show",
				Cmd:         "bcq cards show <id>",
				Description: "Show card details",
			},
			output.Breadcrumb{
				Action:      "columns",
				Cmd:         fmt.Sprintf("bcq cards columns --in %s", resolvedProjectID),
				Description: "List columns with IDs",
			},
		),
	)
}

func newCardsShowCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show card details",
		Long:  "Display detailed information about a card.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			cardID := args[0]

			// Resolve project
			projectID := *project
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

			path := fmt.Sprintf("/buckets/%s/card_tables/cards/%s.json", resolvedProjectID, cardID)
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var card Card
			if err := json.Unmarshal(resp.Data, &card); err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Card #%s: %s", cardID, card.Title)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "comment",
						Cmd:         fmt.Sprintf("bcq comment --content <text> --on %s", cardID),
						Description: "Add comment",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("bcq cards --in %s", resolvedProjectID),
						Description: "List cards",
					},
				),
			)
		},
	}
	return cmd
}

func newCardsCreateCmd(project, cardTable *string) *cobra.Command {
	var title string
	var content string
	var column string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new card",
		Long:  "Create a new card in a project's card table.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			if title == "" {
				return output.ErrUsage("--title is required")
			}

			// Column name (non-numeric) requires --card-table for resolution
			// Numeric column IDs can be used directly without card table discovery
			if column != "" && !isNumericID(column) && *cardTable == "" {
				return output.ErrUsage("--card-table is required when using --column with a name")
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
				return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// If column is a numeric ID, use it directly without card table discovery
			var columnID string
			var cardTableID string
			if column != "" && isNumericID(column) {
				columnID = column
				cardTableID = "" // Not needed for numeric column ID
			} else {
				// Need to discover card table and resolve column
				cardTableID, err = getCardTableID(cmd, app, resolvedProjectID, *cardTable)
				if err != nil {
					return err
				}

				// Get card table with embedded columns (lists)
				cardTablePath := fmt.Sprintf("/buckets/%s/card_tables/%s.json", resolvedProjectID, cardTableID)
				cardTableResp, err := app.API.Get(cmd.Context(), cardTablePath)
				if err != nil {
					return err
				}

				var cardTableData struct {
					Lists []CardColumn `json:"lists"`
				}
				if err := json.Unmarshal(cardTableResp.Data, &cardTableData); err != nil {
					return fmt.Errorf("failed to parse card table: %w", err)
				}

				// Find target column
				if column != "" {
					columnID = resolveColumn(cardTableData.Lists, column)
					if columnID == "" {
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
					columnID = fmt.Sprintf("%d", cardTableData.Lists[0].ID)
				}
			}

			// Build request body
			body := map[string]string{
				"title": title,
			}
			if content != "" {
				body["content"] = content
			}

			path := fmt.Sprintf("/buckets/%s/card_tables/lists/%s/cards.json", resolvedProjectID, columnID)
			resp, err := app.API.Post(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			var card struct {
				ID int64 `json:"id"`
			}
			if err := json.Unmarshal(resp.Data, &card); err != nil {
				return err
			}

			// Build breadcrumbs - only include --card-table when known
			breadcrumbs := []output.Breadcrumb{
				{
					Action:      "view",
					Cmd:         fmt.Sprintf("bcq cards show %d --in %s", card.ID, resolvedProjectID),
					Description: "View card",
				},
			}
			if cardTableID != "" {
				breadcrumbs = append(breadcrumbs, output.Breadcrumb{
					Action:      "move",
					Cmd:         fmt.Sprintf("bcq cards move %d --to <column> --card-table %s --in %s", card.ID, cardTableID, resolvedProjectID),
					Description: "Move card",
				})
			} else {
				// When using numeric column ID, move command can also use numeric column ID
				breadcrumbs = append(breadcrumbs, output.Breadcrumb{
					Action:      "move",
					Cmd:         fmt.Sprintf("bcq cards move %d --to <column-id> --in %s", card.ID, resolvedProjectID),
					Description: "Move card",
				})
			}
			breadcrumbs = append(breadcrumbs, output.Breadcrumb{
				Action:      "list",
				Cmd:         fmt.Sprintf("bcq cards --in %s", resolvedProjectID),
				Description: "List cards",
			})

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Created card #%d", card.ID)),
				output.WithBreadcrumbs(breadcrumbs...),
			)
		},
	}

	cmd.Flags().StringVarP(&title, "title", "t", "", "Card title (required)")
	cmd.Flags().StringVarP(&content, "content", "b", "", "Card body/description")
	cmd.Flags().StringVar(&content, "body", "", "Card body/description (alias for --content)")
	cmd.Flags().StringVarP(&column, "column", "c", "", "Column ID or name (defaults to first column)")
	_ = cmd.MarkFlagRequired("title")

	return cmd
}

func newCardsUpdateCmd(project *string) *cobra.Command {
	var title string
	var content string
	var due string
	var assignee string

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a card",
		Long:  "Update an existing card.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			cardID := args[0]

			if title == "" && content == "" && due == "" && assignee == "" {
				return output.ErrUsage("At least one field required")
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
				return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			body := map[string]any{}
			if title != "" {
				body["title"] = title
			}
			if content != "" {
				body["content"] = content
			}
			if due != "" {
				body["due_on"] = dateparse.Parse(due)
			}
			if assignee != "" {
				assigneeID, _, err := app.Names.ResolvePerson(cmd.Context(), assignee)
				if err != nil {
					return fmt.Errorf("failed to resolve assignee '%s': %w", assignee, err)
				}
				assigneeIDInt, _ := strconv.ParseInt(assigneeID, 10, 64)
				body["assignee_ids"] = []int64{assigneeIDInt}
			}

			path := fmt.Sprintf("/buckets/%s/card_tables/cards/%s.json", resolvedProjectID, cardID)
			resp, err := app.API.Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Updated card #%s", cardID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("bcq cards show %s --in %s", cardID, resolvedProjectID),
						Description: "View card",
					},
					output.Breadcrumb{
						Action:      "list",
						Cmd:         fmt.Sprintf("bcq cards --in %s", resolvedProjectID),
						Description: "List cards",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&title, "title", "t", "", "Card title")
	cmd.Flags().StringVarP(&content, "content", "b", "", "Card body/description")
	cmd.Flags().StringVar(&content, "body", "", "Card body/description (alias for --content)")
	cmd.Flags().StringVarP(&due, "due", "d", "", "Due date (natural language or YYYY-MM-DD)")
	cmd.Flags().StringVar(&assignee, "assignee", "", "Assignee ID or name")

	return cmd
}

func newCardsMoveCmd(project, cardTable *string) *cobra.Command {
	var targetColumn string

	cmd := &cobra.Command{
		Use:   "move <id>",
		Short: "Move a card to another column",
		Long:  "Move a card to a different column in the card table.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			cardID := args[0]

			if targetColumn == "" {
				return output.ErrUsage("--to is required")
			}

			// Check if --to is a column name (not numeric) - requires --card-table
			// Do this validation early, before any API calls
			// Use isNumericID for full-string check (not partial match like Sscanf)
			isNumericColumn := isNumericID(targetColumn)
			if !isNumericColumn && *cardTable == "" {
				return output.ErrUsage("--card-table is required when --to is a column name")
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
				return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Determine column ID - numeric IDs bypass card table resolution
			var columnID string
			var cardTableID string // Will be empty if using numeric column ID directly
			if isNumericColumn {
				// Numeric column ID - use directly without card table lookup
				columnID = targetColumn
			} else {
				// Column name - need card table to resolve (already validated above)

				// Get card table ID from project dock
				var err error
				cardTableID, err = getCardTableID(cmd, app, resolvedProjectID, *cardTable)
				if err != nil {
					return err
				}

				// Get card table with embedded columns (lists)
				cardTablePath := fmt.Sprintf("/buckets/%s/card_tables/%s.json", resolvedProjectID, cardTableID)
				cardTableResp, err := app.API.Get(cmd.Context(), cardTablePath)
				if err != nil {
					return err
				}

				var cardTableData struct {
					Lists []CardColumn `json:"lists"`
				}
				if err := json.Unmarshal(cardTableResp.Data, &cardTableData); err != nil {
					return fmt.Errorf("failed to parse card table: %w", err)
				}

				// Find target column by name
				columnID = resolveColumn(cardTableData.Lists, targetColumn)
				if columnID == "" {
					return output.ErrUsageHint(
						fmt.Sprintf("Column '%s' not found", targetColumn),
						"Use column ID or exact name",
					)
				}
			}

			// Move card to column
			var columnIDInt int64
			_, _ = fmt.Sscanf(columnID, "%d", &columnIDInt) //nolint:gosec // G104: ID validated by ResolveColumn

			body := map[string]int64{
				"column_id": columnIDInt,
			}

			path := fmt.Sprintf("/buckets/%s/card_tables/cards/%s/moves.json", resolvedProjectID, cardID)
			_, err = app.API.Post(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			// Build breadcrumbs - only include --card-table when known
			breadcrumbs := []output.Breadcrumb{
				{
					Action:      "view",
					Cmd:         fmt.Sprintf("bcq cards show %s --in %s", cardID, resolvedProjectID),
					Description: "View card",
				},
			}
			if cardTableID != "" {
				breadcrumbs = append(breadcrumbs, output.Breadcrumb{
					Action:      "list",
					Cmd:         fmt.Sprintf("bcq cards --in %s --card-table %s --column \"%s\"", resolvedProjectID, cardTableID, targetColumn),
					Description: "List cards in column",
				})
			}

			return app.Output.OK(map[string]string{
				"id":     cardID,
				"status": "moved",
				"column": targetColumn,
			},
				output.WithSummary(fmt.Sprintf("Moved card #%s to '%s'", cardID, targetColumn)),
				output.WithBreadcrumbs(breadcrumbs...),
			)
		},
	}

	cmd.Flags().StringVarP(&targetColumn, "to", "t", "", "Target column ID or name (required)")
	_ = cmd.MarkFlagRequired("to")

	return cmd
}

func newCardsColumnsCmd(project, cardTable *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "columns",
		Short: "List columns",
		Long:  "List all columns in a project's card table with their IDs.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
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
				return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
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

			// Get card table with embedded columns (lists)
			cardTablePath := fmt.Sprintf("/buckets/%s/card_tables/%s.json", resolvedProjectID, cardTableID)
			cardTableResp, err := app.API.Get(cmd.Context(), cardTablePath)
			if err != nil {
				return err
			}

			var cardTableData struct {
				Lists []CardColumn `json:"lists"`
			}
			if err := json.Unmarshal(cardTableResp.Data, &cardTableData); err != nil {
				return fmt.Errorf("failed to parse card table: %w", err)
			}

			return app.Output.OK(cardTableData.Lists,
				output.WithSummary(fmt.Sprintf("%d columns", len(cardTableData.Lists))),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "cards",
						Cmd:         fmt.Sprintf("bcq cards --in %s --card-table %s --column <id>", resolvedProjectID, cardTableID),
						Description: "List cards in column",
					},
					output.Breadcrumb{
						Action:      "create",
						Cmd:         fmt.Sprintf("bcq card --title <title> --in %s --card-table %s --column <id>", resolvedProjectID, cardTableID),
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
	var title string
	var content string
	var project string
	var column string
	var cardTable string

	cmd := &cobra.Command{
		Use:   "card",
		Short: "Create a new card",
		Long:  "Create a card in a project's card table. Shortcut for 'bcq cards create'.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			if title == "" {
				return output.ErrUsage("--title is required")
			}

			// Column name (non-numeric) requires --card-table for resolution
			// Numeric column IDs can be used directly without card table discovery
			if column != "" && !isNumericID(column) && cardTable == "" {
				return output.ErrUsage("--card-table is required when using --column with a name")
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

			// If column is a numeric ID, use it directly without card table discovery
			var columnID string
			var cardTableIDVal string
			if column != "" && isNumericID(column) {
				columnID = column
				cardTableIDVal = "" // Not needed for numeric column ID
			} else {
				// Need to discover card table and resolve column
				cardTableIDVal, err = getCardTableID(cmd, app, resolvedProjectID, cardTable)
				if err != nil {
					return err
				}

				// Get card table with embedded columns (lists)
				cardTablePath := fmt.Sprintf("/buckets/%s/card_tables/%s.json", resolvedProjectID, cardTableIDVal)
				cardTableResp, err := app.API.Get(cmd.Context(), cardTablePath)
				if err != nil {
					return err
				}

				var cardTableData struct {
					Lists []CardColumn `json:"lists"`
				}
				if err := json.Unmarshal(cardTableResp.Data, &cardTableData); err != nil {
					return fmt.Errorf("failed to parse card table: %w", err)
				}

				// Find target column
				if column != "" {
					columnID = resolveColumn(cardTableData.Lists, column)
					if columnID == "" {
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
					columnID = fmt.Sprintf("%d", cardTableData.Lists[0].ID)
				}
			}

			// Build request body
			body := map[string]string{
				"title": title,
			}
			if content != "" {
				body["content"] = content
			}

			path := fmt.Sprintf("/buckets/%s/card_tables/lists/%s/cards.json", resolvedProjectID, columnID)
			resp, err := app.API.Post(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			var card struct {
				ID int64 `json:"id"`
			}
			if err := json.Unmarshal(resp.Data, &card); err != nil {
				return err
			}

			// Build breadcrumbs - only include --card-table when known
			cardBreadcrumbs := []output.Breadcrumb{
				{
					Action:      "view",
					Cmd:         fmt.Sprintf("bcq cards show %d --in %s", card.ID, resolvedProjectID),
					Description: "View card",
				},
			}
			if cardTableIDVal != "" {
				cardBreadcrumbs = append(cardBreadcrumbs, output.Breadcrumb{
					Action:      "move",
					Cmd:         fmt.Sprintf("bcq cards move %d --to <column> --card-table %s --in %s", card.ID, cardTableIDVal, resolvedProjectID),
					Description: "Move card",
				})
			} else {
				cardBreadcrumbs = append(cardBreadcrumbs, output.Breadcrumb{
					Action:      "move",
					Cmd:         fmt.Sprintf("bcq cards move %d --to <column-id> --in %s", card.ID, resolvedProjectID),
					Description: "Move card",
				})
			}
			cardBreadcrumbs = append(cardBreadcrumbs, output.Breadcrumb{
				Action:      "list",
				Cmd:         fmt.Sprintf("bcq cards --in %s", resolvedProjectID),
				Description: "List cards",
			})

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Created card #%d", card.ID)),
				output.WithBreadcrumbs(cardBreadcrumbs...),
			)
		},
	}

	cmd.Flags().StringVarP(&title, "title", "t", "", "Card title (required)")
	cmd.Flags().StringVarP(&content, "content", "b", "", "Card body/description")
	cmd.Flags().StringVar(&content, "body", "", "Card body/description (alias for --content)")
	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.Flags().StringVarP(&column, "column", "c", "", "Column ID or name (defaults to first column)")
	cmd.PersistentFlags().StringVar(&cardTable, "card-table", "", "Card table ID (required if project has multiple)")
	_ = cmd.MarkFlagRequired("title")

	cmd.AddCommand(
		newCardsUpdateCmd(&project),
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
		newCardsColumnUpdateCmd(project),
		newCardsColumnMoveCmd(project, cardTable),
		newCardsColumnWatchCmd(project),
		newCardsColumnUnwatchCmd(project),
		newCardsColumnOnHoldCmd(project),
		newCardsColumnNoOnHoldCmd(project),
		newCardsColumnColorCmd(project),
	)

	return cmd
}

func newCardsColumnShowCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show column details",
		Long:  "Display detailed information about a column.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			columnID := args[0]

			// Resolve project
			projectID := *project
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

			path := fmt.Sprintf("/buckets/%s/card_tables/columns/%s.json", resolvedProjectID, columnID)
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var col struct {
				Title      string `json:"title"`
				CardsCount int    `json:"cards_count"`
			}
			if err := json.Unmarshal(resp.Data, &col); err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("%s (%d cards)", col.Title, col.CardsCount)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("bcq cards column update %s --in %s", columnID, resolvedProjectID),
						Description: "Update column",
					},
					output.Breadcrumb{
						Action:      "columns",
						Cmd:         fmt.Sprintf("bcq cards columns --in %s", resolvedProjectID),
						Description: "List all columns",
					},
				),
			)
		},
	}
	return cmd
}

func newCardsColumnCreateCmd(project, cardTable *string) *cobra.Command {
	var title string
	var description string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a column",
		Long:  "Create a new column in the card table.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			if title == "" {
				return output.ErrUsage("--title is required")
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
				return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
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

			body := map[string]string{"title": title}
			if description != "" {
				body["description"] = description
			}

			path := fmt.Sprintf("/buckets/%s/card_tables/%s/columns.json", resolvedProjectID, cardTableID)
			resp, err := app.API.Post(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			var col struct {
				ID    int64  `json:"id"`
				Title string `json:"title"`
			}
			if err := json.Unmarshal(resp.Data, &col); err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Created column: %s", col.Title)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "column",
						Cmd:         fmt.Sprintf("bcq cards column show %d --in %s", col.ID, resolvedProjectID),
						Description: "View column",
					},
					output.Breadcrumb{
						Action:      "columns",
						Cmd:         fmt.Sprintf("bcq cards columns --in %s", resolvedProjectID),
						Description: "List columns",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&title, "title", "t", "", "Column title (required)")
	cmd.Flags().StringVarP(&description, "description", "d", "", "Column description")
	_ = cmd.MarkFlagRequired("title")

	return cmd
}

func newCardsColumnUpdateCmd(project *string) *cobra.Command {
	var title string
	var description string

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a column",
		Long:  "Update an existing card table column.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			columnID := args[0]

			if title == "" && description == "" {
				return output.ErrUsage("No update fields provided")
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
				return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			body := map[string]string{}
			if title != "" {
				body["title"] = title
			}
			if description != "" {
				body["description"] = description
			}

			path := fmt.Sprintf("/buckets/%s/card_tables/columns/%s.json", resolvedProjectID, columnID)
			resp, err := app.API.Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Updated column #%s", columnID)),
			)
		},
	}

	cmd.Flags().StringVarP(&title, "title", "t", "", "Column title")
	cmd.Flags().StringVarP(&description, "description", "d", "", "Column description")

	return cmd
}

func newCardsColumnMoveCmd(project, cardTable *string) *cobra.Command {
	var position int

	cmd := &cobra.Command{
		Use:   "move <id>",
		Short: "Move a column",
		Long:  "Reposition a column within the card table.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			columnID := args[0]

			if position <= 0 {
				return output.ErrUsage("--position required (1-indexed)")
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
				return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
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

			columnIDInt, err := strconv.ParseInt(columnID, 10, 64)
			if err != nil {
				return output.ErrUsage("Column ID must be numeric")
			}
			cardTableIDInt, err := strconv.ParseInt(cardTableID, 10, 64)
			if err != nil {
				return output.ErrUsage("Card table ID must be numeric")
			}

			body := map[string]any{
				"source_id": columnIDInt,
				"target_id": cardTableIDInt,
				"position":  position,
			}

			path := fmt.Sprintf("/buckets/%s/card_tables/%s/moves.json", resolvedProjectID, cardTableID)
			_, err = app.API.Post(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			return app.Output.OK(map[string]any{
				"moved":    true,
				"id":       columnID,
				"position": position,
			}, output.WithSummary(fmt.Sprintf("Moved column #%s to position %d", columnID, position)))
		},
	}

	cmd.Flags().IntVar(&position, "position", 0, "Target position (1-indexed)")
	cmd.Flags().IntVar(&position, "pos", 0, "Target position (alias for --position)")

	return cmd
}

func newCardsColumnWatchCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch <id>",
		Short: "Watch a column",
		Long:  "Subscribe to updates for a column.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			columnID := args[0]

			// Resolve project
			projectID := *project
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

			path := fmt.Sprintf("/buckets/%s/card_tables/lists/%s/subscription.json", resolvedProjectID, columnID)
			_, err = app.API.Post(cmd.Context(), path, map[string]any{})
			if err != nil {
				return err
			}

			return app.Output.OK(map[string]any{
				"watching": true,
				"id":       columnID,
			}, output.WithSummary(fmt.Sprintf("Now watching column #%s", columnID)))
		},
	}
	return cmd
}

func newCardsColumnUnwatchCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unwatch <id>",
		Short: "Unwatch a column",
		Long:  "Unsubscribe from updates for a column.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			columnID := args[0]

			// Resolve project
			projectID := *project
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

			path := fmt.Sprintf("/buckets/%s/card_tables/lists/%s/subscription.json", resolvedProjectID, columnID)
			_, err = app.API.Delete(cmd.Context(), path)
			if err != nil {
				return err
			}

			return app.Output.OK(map[string]any{
				"watching": false,
				"id":       columnID,
			}, output.WithSummary(fmt.Sprintf("Stopped watching column #%s", columnID)))
		},
	}
	return cmd
}

func newCardsColumnOnHoldCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "on-hold <id>",
		Short: "Enable on-hold section",
		Long:  "Enable on-hold section for a column.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			columnID := args[0]

			// Resolve project
			projectID := *project
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

			path := fmt.Sprintf("/buckets/%s/card_tables/columns/%s/on_hold.json", resolvedProjectID, columnID)
			resp, err := app.API.Post(cmd.Context(), path, map[string]any{})
			if err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Enabled on-hold for column #%s", columnID)),
			)
		},
	}
	return cmd
}

func newCardsColumnNoOnHoldCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "no-on-hold <id>",
		Short: "Disable on-hold section",
		Long:  "Disable on-hold section for a column.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			columnID := args[0]

			// Resolve project
			projectID := *project
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

			path := fmt.Sprintf("/buckets/%s/card_tables/columns/%s/on_hold.json", resolvedProjectID, columnID)
			resp, err := app.API.Delete(cmd.Context(), path)
			if err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Disabled on-hold for column #%s", columnID)),
			)
		},
	}
	return cmd
}

func newCardsColumnColorCmd(project *string) *cobra.Command {
	var color string

	cmd := &cobra.Command{
		Use:   "color <id>",
		Short: "Set column color",
		Long:  "Set the color for a column.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			columnID := args[0]

			if color == "" {
				return output.ErrUsage("--color is required")
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
				return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			body := map[string]string{"color": color}
			path := fmt.Sprintf("/buckets/%s/card_tables/columns/%s/color.json", resolvedProjectID, columnID)
			resp, err := app.API.Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Set column #%s color to %s", columnID, color)),
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
		Use:   "steps",
		Short: "List steps on a card",
		Long:  "Display all steps (checklist items) on a card.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			// Accept card ID as positional arg or flag
			if len(args) > 0 {
				cardID = args[0]
			}
			if cardID == "" {
				return output.ErrUsage("Card ID required (bcq cards steps <card_id>)")
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
				return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Get card with steps
			path := fmt.Sprintf("/buckets/%s/card_tables/cards/%s.json", resolvedProjectID, cardID)
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var card struct {
				Steps []CardStep `json:"steps"`
			}
			if err := json.Unmarshal(resp.Data, &card); err != nil {
				return err
			}

			return app.Output.OK(card.Steps,
				output.WithSummary(fmt.Sprintf("%d steps on card #%s", len(card.Steps), cardID)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "create",
						Cmd:         fmt.Sprintf("bcq cards step create --title <title> --card %s --in %s", cardID, resolvedProjectID),
						Description: "Add step",
					},
					output.Breadcrumb{
						Action:      "card",
						Cmd:         fmt.Sprintf("bcq cards show %s --in %s", cardID, resolvedProjectID),
						Description: "View card",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&cardID, "card", "c", "", "Card ID")

	return cmd
}

// CardStep represents a step on a card.
type CardStep struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	DueOn     string `json:"due_on,omitempty"`
	Completed bool   `json:"completed"`
	Assignees []struct {
		Name string `json:"name"`
	} `json:"assignees,omitempty"`
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
		newCardsStepUpdateCmd(project),
		newCardsStepCompleteCmd(project),
		newCardsStepUncompleteCmd(project),
		newCardsStepMoveCmd(project),
		newCardsStepDeleteCmd(project),
	)

	return cmd
}

func newCardsStepCreateCmd(project *string) *cobra.Command {
	var title string
	var cardID string
	var dueOn string
	var assignees string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a step",
		Long:  "Add a new step (checklist item) to a card.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			if title == "" {
				return output.ErrUsage("--title is required")
			}
			if cardID == "" {
				return output.ErrUsage("--card is required")
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
				return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			body := map[string]string{"title": title}
			if dueOn != "" {
				body["due_on"] = dateparse.Parse(dueOn)
			}
			if assignees != "" {
				assigneesCSV, err := resolveAssigneesCSV(cmd.Context(), app, assignees)
				if err != nil {
					return err
				}
				body["assignees"] = assigneesCSV
			}

			path := fmt.Sprintf("/buckets/%s/card_tables/cards/%s/steps.json", resolvedProjectID, cardID)
			resp, err := app.API.Post(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			var step struct {
				ID    int64  `json:"id"`
				Title string `json:"title"`
			}
			if err := json.Unmarshal(resp.Data, &step); err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Created step: %s", step.Title)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "complete",
						Cmd:         fmt.Sprintf("bcq cards step complete %d --in %s", step.ID, resolvedProjectID),
						Description: "Complete step",
					},
					output.Breadcrumb{
						Action:      "steps",
						Cmd:         fmt.Sprintf("bcq cards steps %s --in %s", cardID, resolvedProjectID),
						Description: "List steps",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&title, "title", "t", "", "Step title (required)")
	cmd.Flags().StringVarP(&cardID, "card", "c", "", "Card ID (required)")
	cmd.Flags().StringVarP(&dueOn, "due", "d", "", "Due date (natural language or YYYY-MM-DD)")
	cmd.Flags().StringVar(&assignees, "assignees", "", "Assignees (IDs or names, comma-separated)")
	_ = cmd.MarkFlagRequired("title")
	_ = cmd.MarkFlagRequired("card")

	return cmd
}

func newCardsStepUpdateCmd(project *string) *cobra.Command {
	var title string
	var dueOn string
	var assignees string

	cmd := &cobra.Command{
		Use:   "update <step_id>",
		Short: "Update a step",
		Long:  "Update an existing step on a card.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			stepID := args[0]

			if title == "" && dueOn == "" && assignees == "" {
				return output.ErrUsage("No update fields provided")
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
				return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			body := map[string]string{}
			if title != "" {
				body["title"] = title
			}
			if dueOn != "" {
				body["due_on"] = dateparse.Parse(dueOn)
			}
			if assignees != "" {
				assigneesCSV, err := resolveAssigneesCSV(cmd.Context(), app, assignees)
				if err != nil {
					return err
				}
				body["assignees"] = assigneesCSV
			}

			path := fmt.Sprintf("/buckets/%s/card_tables/steps/%s.json", resolvedProjectID, stepID)
			resp, err := app.API.Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Updated step #%s", stepID)),
			)
		},
	}

	cmd.Flags().StringVarP(&title, "title", "t", "", "Step title")
	cmd.Flags().StringVarP(&dueOn, "due", "d", "", "Due date (natural language or YYYY-MM-DD)")
	cmd.Flags().StringVar(&assignees, "assignees", "", "Assignees (IDs or names, comma-separated)")

	return cmd
}

func newCardsStepCompleteCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "complete <step_id>",
		Short: "Complete a step",
		Long:  "Mark a step as completed.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			stepID := args[0]

			// Resolve project
			projectID := *project
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

			body := map[string]string{"completion": "on"}
			path := fmt.Sprintf("/buckets/%s/card_tables/steps/%s/completions.json", resolvedProjectID, stepID)
			resp, err := app.API.Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Completed step #%s", stepID)),
			)
		},
	}
	return cmd
}

func newCardsStepUncompleteCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uncomplete <step_id>",
		Short: "Uncomplete a step",
		Long:  "Mark a step as not completed.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			stepID := args[0]

			// Resolve project
			projectID := *project
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

			body := map[string]string{"completion": "off"}
			path := fmt.Sprintf("/buckets/%s/card_tables/steps/%s/completions.json", resolvedProjectID, stepID)
			resp, err := app.API.Put(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			return app.Output.OK(json.RawMessage(resp.Data),
				output.WithSummary(fmt.Sprintf("Uncompleted step #%s", stepID)),
			)
		},
	}
	return cmd
}

func newCardsStepMoveCmd(project *string) *cobra.Command {
	var cardID string
	var position int

	cmd := &cobra.Command{
		Use:   "move <step_id>",
		Short: "Move a step",
		Long:  "Reposition a step within a card (0-indexed).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			stepID := args[0]

			if cardID == "" {
				return output.ErrUsage("--card is required")
			}
			if position < 0 {
				return output.ErrUsage("--position is required (0-indexed)")
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
				return output.ErrUsageHint("No project specified", "Use --project or set in .basecamp/config.json")
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			stepIDInt, err := strconv.ParseInt(stepID, 10, 64)
			if err != nil {
				return output.ErrUsage("Step ID must be numeric")
			}

			body := map[string]any{
				"source_id": stepIDInt,
				"position":  position,
			}

			path := fmt.Sprintf("/buckets/%s/card_tables/cards/%s/positions.json", resolvedProjectID, cardID)
			_, err = app.API.Post(cmd.Context(), path, body)
			if err != nil {
				return err
			}

			return app.Output.OK(map[string]any{
				"moved":    true,
				"id":       stepID,
				"position": position,
			}, output.WithSummary(fmt.Sprintf("Moved step #%s to position %d", stepID, position)))
		},
	}

	cmd.Flags().StringVarP(&cardID, "card", "c", "", "Card ID (required)")
	cmd.Flags().IntVar(&position, "position", -1, "Target position (0-indexed)")
	cmd.Flags().IntVar(&position, "pos", -1, "Target position (alias for --position)")

	return cmd
}

func newCardsStepDeleteCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <step_id>",
		Short: "Delete a step",
		Long:  "Permanently delete a step from a card.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			stepID := args[0]

			// Resolve project
			projectID := *project
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

			path := fmt.Sprintf("/buckets/%s/card_tables/steps/%s.json", resolvedProjectID, stepID)
			_, err = app.API.Delete(cmd.Context(), path)
			if err != nil {
				return err
			}

			return app.Output.OK(map[string]any{"deleted": true},
				output.WithSummary(fmt.Sprintf("Deleted step #%s", stepID)),
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
	resp, err := app.API.Get(cmd.Context(), path)
	if err != nil {
		return "", err
	}

	var project struct {
		Dock []struct {
			Name  string `json:"name"`
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"dock"`
	}
	if err := json.Unmarshal(resp.Data, &project); err != nil {
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
	matches := formatCardTableMatches(cardTables)
	matches = append(matches, "Use --card-table <id> to specify")
	return "", output.ErrAmbiguous("card table", matches)
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

// formatCardTableMatches formats card tables for ambiguous error.
func formatCardTableMatches(cardTables []struct {
	ID    int64
	Title string
}) []string {
	matches := make([]string, len(cardTables))
	for i, ct := range cardTables {
		if ct.Title != "" {
			matches[i] = fmt.Sprintf("%d: %s", ct.ID, ct.Title)
		} else {
			matches[i] = fmt.Sprintf("%d", ct.ID)
		}
	}
	return matches
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
func resolveColumn(columns []CardColumn, identifier string) string {
	// Try by ID first
	var idInt int64
	if _, err := fmt.Sscanf(identifier, "%d", &idInt); err == nil {
		for _, col := range columns {
			if col.ID == idInt {
				return fmt.Sprintf("%d", col.ID)
			}
		}
	}

	// Fall back to name match
	for _, col := range columns {
		if col.Title == identifier {
			return fmt.Sprintf("%d", col.ID)
		}
	}

	return ""
}

func resolveAssigneesCSV(ctx context.Context, app *appctx.App, input string) (string, error) {
	parts := strings.Split(input, ",")
	ids := make([]string, 0, len(parts))

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		if _, err := strconv.ParseInt(part, 10, 64); err == nil {
			ids = append(ids, part)
			continue
		}

		resolvedID, _, err := app.Names.ResolvePerson(ctx, part)
		if err != nil {
			return "", fmt.Errorf("failed to resolve assignee '%s': %w", part, err)
		}
		ids = append(ids, resolvedID)
	}

	if len(ids) == 0 {
		return "", output.ErrUsage("No valid assignees provided")
	}

	return strings.Join(ids, ","), nil
}
