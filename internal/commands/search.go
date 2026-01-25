package commands

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/output"
)

// SearchResult represents a search result from Basecamp.
type SearchResult struct {
	ID               int64  `json:"id"`
	Type             string `json:"type"`
	Title            string `json:"title,omitempty"`
	PlainTextContent string `json:"plain_text_content,omitempty"`
	Bucket           struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"bucket"`
	UpdatedAt string `json:"updated_at"`
}

// NewSearchCmd creates the search command for full-text search.
func NewSearchCmd() *cobra.Command {
	var recordingType string
	var project string
	var creator string
	var limit int

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search across Basecamp content",
		Long: `Search across all Basecamp content.

Uses the Basecamp search API to find content matching your query.
Use 'bcq search metadata' to see available types.`,
		Args: cobra.MinimumNArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}

			// Handle "metadata" subcommand
			if len(args) > 0 && (args[0] == "metadata" || args[0] == "types") {
				return runSearchMetadata(cmd, app)
			}

			if len(args) == 0 {
				return output.ErrUsage("Search query required")
			}

			query := args[0]

			// Build query string
			params := url.Values{}
			params.Set("q", query)

			if recordingType != "" {
				params.Set("type", recordingType)
			}
			if project != "" {
				// Resolve project
				resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), project)
				if err != nil {
					return err
				}
				params.Set("bucket_id", resolvedProjectID)
			}
			if creator != "" {
				// Resolve person
				resolvedPersonID, _, err := app.Names.ResolvePerson(cmd.Context(), creator)
				if err != nil {
					return err
				}
				params.Set("creator_id", resolvedPersonID)
			}

			path := fmt.Sprintf("/search.json?%s", params.Encode())
			resp, err := app.API.Get(cmd.Context(), path)
			if err != nil {
				return err
			}

			var results []json.RawMessage
			if err := resp.UnmarshalData(&results); err != nil {
				return fmt.Errorf("failed to parse search results: %w", err)
			}

			// Apply client-side limit if specified
			if limit > 0 && len(results) > limit {
				results = results[:limit]
			}

			summary := fmt.Sprintf("%d results for \"%s\"", len(results), query)

			return app.Output.OK(results,
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "bcq show <id> --project <bucket.id>",
						Description: "Show result details",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&recordingType, "type", "t", "", "Filter by recording type (run 'bcq search metadata' for valid types)")
	cmd.Flags().StringVarP(&project, "project", "p", "", "Filter by project ID or name")
	cmd.Flags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.Flags().StringVarP(&creator, "creator", "c", "", "Filter by creator person ID or name")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Limit number of results")

	cmd.AddCommand(newSearchMetadataCmd())

	return cmd
}

func newSearchMetadataCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "metadata",
		Aliases: []string{"types"},
		Short:   "Show available search types",
		Long:    "Display available recording types and file types for search filtering.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			if err := app.API.RequireAccount(); err != nil {
				return err
			}
			return runSearchMetadata(cmd, app)
		},
	}
}

func runSearchMetadata(cmd *cobra.Command, app *appctx.App) error {
	resp, err := app.API.Get(cmd.Context(), "/searches/metadata.json")
	if err != nil {
		return err
	}

	// Handle empty response (204 No Content)
	if len(resp.Data) == 0 {
		return output.ErrUsageHint(
			"Search metadata not available",
			"Common types: Todo, Message, Document, Comment, Kanban::Card",
		)
	}

	// Parse to extract types for summary
	var metadata struct {
		RecordingSearchTypes []struct {
			Key string `json:"key"`
		} `json:"recording_search_types"`
		FileSearchTypes []struct {
			Key string `json:"key"`
		} `json:"file_search_types"`
	}
	if err := json.Unmarshal(resp.Data, &metadata); err != nil {
		return err
	}

	var types []string
	for _, t := range metadata.RecordingSearchTypes {
		types = append(types, t.Key)
	}

	summary := "Search metadata"
	if len(types) > 0 {
		summary = fmt.Sprintf("Available types: %d", len(types))
	}

	return app.Output.OK(json.RawMessage(resp.Data),
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "search",
				Cmd:         "bcq search <query> --type <type>",
				Description: "Search with type filter",
			},
		),
	)
}
