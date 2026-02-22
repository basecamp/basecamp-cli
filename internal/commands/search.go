package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// NewSearchCmd creates the search command for full-text search.
func NewSearchCmd() *cobra.Command {
	var sortBy string
	var limit int

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search across Basecamp content",
		Long: `Search across all Basecamp content.

Uses the Basecamp search API to find content matching your query.
Use 'basecamp search metadata' to see available search scopes.`,
		Args: cobra.MinimumNArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			// Handle "metadata" subcommand
			if len(args) > 0 && (args[0] == "metadata" || args[0] == "types") {
				return runSearchMetadata(cmd, app)
			}

			if len(args) == 0 {
				return output.ErrUsage("Search query required")
			}

			query := args[0]

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Build search options
			var opts *basecamp.SearchOptions
			if sortBy != "" {
				opts = &basecamp.SearchOptions{
					Sort: sortBy,
				}
			}

			results, err := app.Account().Search().Search(cmd.Context(), query, opts)
			if err != nil {
				return convertSDKError(err)
			}

			// Apply client-side limit if specified
			if limit > 0 && len(results) > limit {
				results = results[:limit]
			}

			summary := fmt.Sprintf("%d results for \"%s\"", len(results), query)

			return app.OK(results,
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp show <id> --project <bucket.id>",
						Description: "Show result details",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&sortBy, "sort", "s", "", "Sort by: created_at or updated_at (default: relevance)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Limit number of results")

	cmd.AddCommand(newSearchMetadataCmd())

	return cmd
}

func newSearchMetadataCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "metadata",
		Aliases: []string{"types"},
		Short:   "Show available search scopes",
		Long:    "Display available projects for search scope filtering.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			return runSearchMetadata(cmd, app)
		},
	}
}

func runSearchMetadata(cmd *cobra.Command, app *appctx.App) error {
	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	metadata, err := app.Account().Search().Metadata(cmd.Context())
	if err != nil {
		return convertSDKError(err)
	}

	// Handle empty response
	if metadata == nil || len(metadata.Projects) == 0 {
		return output.ErrUsageHint(
			"Search metadata not available",
			"No projects available for search filtering",
		)
	}

	summary := fmt.Sprintf("Available projects: %d", len(metadata.Projects))

	return app.OK(metadata,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "search",
				Cmd:         "basecamp search <query>",
				Description: "Search content",
			},
		),
	)
}
