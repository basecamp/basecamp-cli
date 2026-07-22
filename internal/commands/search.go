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

// defaultSearchLimit caps results when neither --all nor an explicit --limit is
// given. The SDK treats Limit==0 as "fetch every page" and follows Link-header
// pagination unbounded, which can hang for 90s+ on a broad query (#470); a
// bounded default keeps the common case fast while --all preserves opt-in
// exhaustive fetches.
const defaultSearchLimit = 20

// NewSearchCmd creates the search command for full-text search.
func NewSearchCmd() *cobra.Command {
	var sortBy string
	var limit int
	var all bool

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search across Basecamp content",
		Long: `Search across all Basecamp content.

Uses the Basecamp search API to find content matching your query.
Results are capped at 20 by default; pass --all to fetch every match.
Use 'basecamp search metadata' to inspect the search metadata the API reports.`,
		Example: `  basecamp search "quarterly goals"
  basecamp search "bug report" --sort recency
  basecamp search "design review" --limit 5
  basecamp search "meeting notes" --all`,
		Annotations: map[string]string{"agent_notes": "Use search for keyword queries, use recordings for browsing by type/status without a search term"},
		Args:        cobra.MinimumNArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			// Handle "metadata" subcommand
			if len(args) > 0 && (args[0] == "metadata" || args[0] == "types") {
				return runSearchMetadata(cmd, app)
			}

			// Show help when invoked with no query
			if len(args) == 0 {
				return missingArg(cmd, "<query>")
			}

			query := args[0]

			// The global --project/--in flag is accepted but the pinned SDK's
			// SearchParams exposes only q/sort — no bucket_ids[]. Reject explicit
			// project scoping rather than silently returning unscoped results.
			// Ambient config.ProjectID is never rejected; only an explicit flag
			// signals search-scoping intent.
			// Follow-up (post-SDK-bump that absorbs bucket_ids[], BC3 #12361 merged
			// server-side): resolve via app.Names.ResolveProject and pass the bucket
			// ID. See spec/api-gaps/search-filter-additions.md in the SDK repo.
			if app.Flags.Project != "" {
				return output.ErrUsageHint(
					"project-scoped search is not supported yet",
					"The Basecamp API now accepts bucket_ids[] (BC3 #12361), but no published SDK build exposes it. Re-run the search without --project/--in for now.",
				)
			}

			sort, err := normalizeSearchSort(sortBy)
			if err != nil {
				return err
			}

			limitChanged := cmd.Flags().Changed("limit")
			if all && limitChanged {
				return output.ErrUsage("--all and --limit are mutually exclusive")
			}

			effectiveLimit := defaultSearchLimit
			switch {
			case all:
				effectiveLimit = 0 // unbounded: follow pagination to the end
			case limitChanged:
				if limit <= 0 {
					return output.ErrUsage("--limit must be a positive number; use --all to fetch every result")
				}
				effectiveLimit = limit
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			opts := &basecamp.SearchOptions{
				Sort:  sort,
				Limit: effectiveLimit,
			}

			searchResult, err := app.Account().Search().Search(cmd.Context(), query, opts)
			if err != nil {
				return convertSDKError(err)
			}

			results := searchResult.Results
			summary := fmt.Sprintf("%d results for \"%s\"", len(results), query)

			// Humanize for styled terminal output; preserve raw SDK structs
			// for machine-readable formats (--json, --agent, --md)
			var data any = results
			if app.Output.EffectiveFormat() == output.FormatStyled {
				data = humanizeSearchResults(results)
			}

			respOpts := []output.ResponseOption{
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp show <id> --project <project_id>",
						Description: "Show result details",
					},
				),
			}

			if notice := output.TruncationNoticeWithTotal(len(results), searchResult.Meta.TotalCount); notice != "" {
				respOpts = append(respOpts, output.WithNotice(notice))
			}

			return app.OK(data, respOpts...)
		},
	}

	cmd.Flags().StringVarP(&sortBy, "sort", "s", "", "Sort order: relevance (default) or recency")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of results to fetch (default 20; use --all for every result)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all results (no limit)")

	cmd.AddCommand(newSearchMetadataCmd())

	return cmd
}

// normalizeSearchSort maps the user-facing --sort vocabulary onto the values the
// Basecamp search API accepts. Empty/relevance normalizes to best_match (BC3's
// default, pinned explicitly for deterministic output); recency and its
// deprecated created_at/updated_at aliases normalize to recency (newest-first).
// BC3 treats any non-blank, non-best_match sort as created-at descending, so
// recency works today regardless of the search-filter release. Unknown values
// are a usage error.
func normalizeSearchSort(sort string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(sort)) {
	case "", "relevance", "best_match":
		return "best_match", nil
	case "recency", "newest", "created_at", "updated_at":
		return "recency", nil
	default:
		return "", output.ErrUsage(fmt.Sprintf("invalid --sort value %q; valid values are relevance or recency", sort))
	}
}

func newSearchMetadataCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "metadata",
		Aliases: []string{"types"},
		Short:   "Show search metadata reported by the API",
		Long: `Display the search metadata returned by the Basecamp API.

Note: this SDK build surfaces only project scopes; the API also returns
recording/file search types and labels that are not yet modeled.`,
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
	if metadata == nil {
		metadata = &basecamp.SearchMetadata{}
	}

	summary := "Search metadata (limited SDK coverage)"
	if len(metadata.Projects) > 0 {
		summary = fmt.Sprintf("Search metadata: %d project scopes", len(metadata.Projects))
	}

	respOpts := []output.ResponseOption{
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "search",
				Cmd:         "basecamp search <query>",
				Description: "Search content",
			},
		),
	}

	// The pinned SDK models only `projects`, but /searches/metadata.json now also
	// returns recording/file search types and labels. An empty projects list is
	// SDK schema drift, not an empty account — surface a notice rather than the
	// former fatal "Search metadata not available" error (#546). Reserve errors
	// for genuine transport/API failures (handled by convertSDKError above).
	if len(metadata.Projects) == 0 {
		respOpts = append(respOpts, output.WithNotice(
			"This SDK build surfaces only project scopes; the Basecamp API also returns recording/file search types and labels that are not yet modeled.",
		))
	}

	return app.OK(metadata, respOpts...)
}
