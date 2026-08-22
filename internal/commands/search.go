package commands

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/completion"
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
	var typeName string
	var since string
	var creator string
	var fileType string
	var excludeChat bool

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search across Basecamp content",
		Long: `Search across all Basecamp content.

Uses the Basecamp search API to find content matching your query.
Results are capped at 20 by default; pass --all to fetch every match.

Filter with --project/--in, --type, --creator, --since, --file-type, and
--exclude-chat. Use 'basecamp search metadata' to inspect the recording and
file types the API accepts.`,
		Example: `  basecamp search "quarterly goals"
  basecamp search "bug report" --sort recency
  basecamp search "design review" --limit 5
  basecamp search "bug" --project Marketing --type todo --since last_30_days
  basecamp search "invoice" --file-type pdf --creator me
  basecamp search "meeting notes" --all`,
		Annotations: map[string]string{"agent_notes": "Use search for keyword queries, use recordings for browsing by type/status without a search term"},
		// At most one positional: the query (0 shows help). More than one is a
		// usage error rather than silently searching only the first word — an
		// unquoted `search foo bar` should not quietly drop "bar".
		Args: cobra.MaximumNArgs(1),
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

			sort, err := normalizeSearchSort(sortBy)
			if err != nil {
				return err
			}

			canonicalType, err := normalizeSearchType(typeName)
			if err != nil {
				return err
			}

			canonicalFileType, err := normalizeSearchFileType(fileType)
			if err != nil {
				return err
			}

			canonicalSince, err := normalizeSearchSince(since)
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
				Sort:        sort,
				Limit:       effectiveLimit,
				FileType:    canonicalFileType,
				ExcludeChat: excludeChat,
				Since:       canonicalSince,
			}

			// Only explicit --project/--in scopes the search; ambient
			// config.ProjectID stays ignored because search is account-wide.
			// Send both the plural (BucketIds) and deprecated singular (BucketID)
			// forms: BC5 honors the plural, older clients fall back to the
			// singular, and since the flag is single-valued they mirror each other.
			if app.Flags.Project != "" {
				resolved, _, err := app.Names.ResolveProject(cmd.Context(), app.Flags.Project)
				if err != nil {
					return err
				}
				id, err := strconv.ParseInt(resolved, 10, 64)
				if err != nil {
					return output.ErrNotFound("Project", app.Flags.Project)
				}
				opts.BucketIds = []int64{id}
				opts.BucketID = id
			}

			if creator != "" {
				resolved, _, err := app.Names.ResolvePerson(cmd.Context(), creator)
				if err != nil {
					return err
				}
				id, err := strconv.ParseInt(resolved, 10, 64)
				if err != nil {
					return output.ErrNotFound("Person", creator)
				}
				opts.CreatorIds = []int64{id}
				opts.CreatorID = id
			}

			if canonicalType != "" {
				opts.TypeNames = []string{canonicalType}
				opts.Type = canonicalType
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
	cmd.Flags().StringVarP(&typeName, "type", "t", "", "Filter by content type (todo, message, card, ping, chat, event, ...)")
	cmd.Flags().StringVar(&since, "since", "", "Restrict to a time range: last_7_days, last_30_days, last_90_days, last_12_months, forever (BC5-only)")
	cmd.Flags().StringVar(&creator, "creator", "", "Filter by creator (name, email, ID, or 'me')")
	cmd.Flags().StringVar(&fileType, "file-type", "", "Filter attachments by type: image, audio, video, pdf")
	cmd.Flags().BoolVar(&excludeChat, "exclude-chat", false, "Exclude chat/campfire results")

	_ = cmd.RegisterFlagCompletionFunc("type", func(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		return searchTypeFriendlyNames(), cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("since", func(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		return searchSinceValues(), cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("file-type", func(_ *cobra.Command, _ []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		return []cobra.Completion{"image", "audio", "video", "pdf"}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("creator", completion.NewCompleter(nil).PeopleCompletion())

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

// searchTypeAliases maps friendly names and canonical Keys onto the canonical
// Search::Type Keys BC3 accepts (app/models/search/type.rb). Search uses a
// different vocabulary than recordings: upload/file → Attachment (recordings
// uses Upload), ping → Circle, check-in → Question, event → Schedule::Entry,
// folder → Vault, chat → Chat::Transcript, forward → Inbox::Forward. BC3
// silently discards an unrecognized type and returns unfiltered results, so we
// reject unknown values rather than passing them through.
var searchTypeAliases = map[string]string{
	"todo":                   "Todo",
	"message":                "Message",
	"document":               "Document",
	"comment":                "Comment",
	"card":                   "Kanban::Card",
	"kanban::card":           "Kanban::Card",
	"file":                   "Attachment",
	"upload":                 "Attachment",
	"attachment":             "Attachment",
	"ping":                   "Circle",
	"circle":                 "Circle",
	"chat":                   "Chat::Transcript",
	"chat::transcript":       "Chat::Transcript",
	"check-in":               "Question",
	"checkin":                "Question",
	"question":               "Question",
	"event":                  "Schedule::Entry",
	"schedule::entry":        "Schedule::Entry",
	"folder":                 "Vault",
	"vault":                  "Vault",
	"forward":                "Inbox::Forward",
	"inbox::forward":         "Inbox::Forward",
	"client":                 "Client::Correspondence",
	"client::correspondence": "Client::Correspondence",
}

// searchTypeFriendlyNames lists the friendly --type values for help, error
// messages, and completion, in a stable presentation order.
func searchTypeFriendlyNames() []string {
	return []string{
		"todo", "message", "document", "comment", "card", "file",
		"ping", "chat", "check-in", "event", "folder", "forward", "client",
	}
}

// normalizeSearchType maps a friendly alias or canonical Key onto the canonical
// Search::Type Key. Empty input leaves the filter unset. Unknown values are a
// usage error — BC3 would silently drop them and return unfiltered results.
func normalizeSearchType(input string) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", nil
	}
	if canonical, ok := searchTypeAliases[strings.ToLower(trimmed)]; ok {
		return canonical, nil
	}
	return "", output.ErrUsage(fmt.Sprintf(
		"invalid --type value %q; valid values are %s",
		input, strings.Join(searchTypeFriendlyNames(), ", "),
	))
}

// normalizeSearchFileType maps a case-insensitive file-type name onto BC3's
// capitalized Blob::TYPES membership (Image, Audio, Video, PDF). BC3 filters
// after a case-sensitive check, so "image" would silently disable the filter;
// we normalize casing and reject unknown values.
func normalizeSearchFileType(input string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "":
		return "", nil
	case "image":
		return "Image", nil
	case "audio":
		return "Audio", nil
	case "video":
		return "Video", nil
	case "pdf":
		return "PDF", nil
	default:
		return "", output.ErrUsage(fmt.Sprintf(
			"invalid --file-type value %q; valid values are image, audio, video, pdf", input,
		))
	}
}

// searchSinceValues lists the accepted --since ranges for help and completion.
func searchSinceValues() []string {
	return []string{"last_7_days", "last_30_days", "last_90_days", "last_12_months", "forever"}
}

// normalizeSearchSince validates the --since range against the values BC5
// accepts. Hyphens normalize to underscores; empty input leaves it unset.
// Unknown values are a usage error. There is no BC4 equivalent — --since is
// BC5-only.
func normalizeSearchSince(input string) (string, error) {
	normalized := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(input)), "-", "_")
	if normalized == "" {
		return "", nil
	}
	for _, v := range searchSinceValues() {
		if normalized == v {
			return normalized, nil
		}
	}
	return "", output.ErrUsage(fmt.Sprintf(
		"invalid --since value %q; valid values are %s",
		input, strings.Join(searchSinceValues(), ", "),
	))
}

func newSearchMetadataCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "metadata",
		Aliases: []string{"types"},
		Short:   "Show the recording and file types search accepts",
		Long: `Display the search filter options returned by the Basecamp API.

Lists the recording types (--type) and file types (--file-type) you can filter
by, along with the default labels the API reports for each filter.`,
		Args: cobra.NoArgs,
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
		created := ""
		if r.CreatedAt != nil {
			created = relativeTime(*r.CreatedAt)
		}
		row := map[string]any{
			"id":      r.ID,
			"title":   title,
			"type":    simplifyType(r.Type),
			"project": project,
			"created": created,
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

	// Count only the selectable (non-default) options. Each list carries a
	// leading key:null entry — the "Everything"/"All files" default — that must
	// not be counted as a filterable type.
	recordingOptions := countSelectableTypes(metadata.RecordingSearchTypes)
	fileOptions := countSelectableTypes(metadata.FileSearchTypes)
	summary := fmt.Sprintf("Search filters: %d %s, %d %s",
		recordingOptions, pluralize(recordingOptions, "recording type", "recording types"),
		fileOptions, pluralize(fileOptions, "file type", "file types"))

	respOpts := []output.ResponseOption{
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "search",
				Cmd:         "basecamp search <query> --type <type>",
				Description: "Search content, optionally filtered by type",
			},
		),
	}

	return app.OK(metadata, respOpts...)
}

// countSelectableTypes counts search options with a non-nil Key. A nil Key is
// the default "Everything"/"All files" entry, which is not a real filter value.
func countSelectableTypes(types []basecamp.SearchType) int {
	n := 0
	for _, t := range types {
		if t.Key != nil {
			n++
		}
	}
	return n
}
