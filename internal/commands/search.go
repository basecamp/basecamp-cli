package commands

import (
	"fmt"
	"strings"
	"time"

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
		Example: `  basecamp search "quarterly goals"
  basecamp search "bug report" --sort created_at
  basecamp search "design review" --limit 5`,
		Annotations: map[string]string{"agent_notes": "Use search for keyword queries, use recordings for browsing by type/status without a search term"},
		Args:        cobra.MinimumNArgs(0),
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

			// Humanize for styled terminal output; preserve raw SDK structs
			// for machine-readable formats (--json, --agent, --md)
			var data any = results
			if app.Output.EffectiveFormat() == output.FormatStyled {
				data = humanizeSearchResults(results)
			}

			return app.OK(data,
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

// humanizeSearchResults transforms raw SDK results into clean maps for display.
func humanizeSearchResults(results []basecamp.SearchResult) []map[string]any {
	out := make([]map[string]any, 0, len(results))
	for _, r := range results {
		title := r.Title
		if title == "" {
			title = r.Subject
		}
		if runes := []rune(title); len(runes) > 60 {
			title = string(runes[:57]) + "…"
		}
		project := ""
		if r.Bucket != nil {
			project = r.Bucket.Name
		}
		row := map[string]any{
			"id":      r.ID,
			"title":   title,
			"type":    simplifyType(r.Type),
			"project": project,
			"created": relativeTime(r.CreatedAt),
		}
		out = append(out, row)
	}
	return out
}

// simplifyType strips module prefixes and lowercases Basecamp type names.
// "Chat::Lines::RichText" → "chat", "Todo" → "todo", "Message::Board" → "message"
func simplifyType(t string) string {
	parts := strings.Split(t, "::")
	// Use first segment as the primary type
	s := parts[0]
	s = strings.ToLower(s)
	// Normalize common variants
	switch s {
	case "inbox":
		return "forward"
	case "question":
		return "check-in"
	}
	return s
}

// relativeTime formats a timestamp as a human-readable relative duration.
func relativeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		return fmt.Sprintf("%dm ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		return fmt.Sprintf("%dh ago", h)
	case d < 30*24*time.Hour:
		days := int(d.Hours() / 24)
		return fmt.Sprintf("%dd ago", days)
	case d < 365*24*time.Hour:
		months := int(d.Hours() / 24 / 30)
		return fmt.Sprintf("%dmo ago", months)
	default:
		years := int(d.Hours() / 24 / 365)
		return fmt.Sprintf("%dy ago", years)
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
