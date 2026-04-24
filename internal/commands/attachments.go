package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/urlarg"
)

// genericLookupTypeHint is the usage hint emitted when the generic
// /recordings/<id>.json lookup cannot resolve the recording — either
// because the endpoint returns 204 (some recording types) or 404 (cards,
// and any other type whose recording is not addressable without bucket
// scope). Callers need to specify --type (or pass a URL, from which the
// type is parsed) so the typed endpoint can be used instead.
//
// Kept command-agnostic: fetchItemContent is shared by `attachments list`
// and `attachments download`, so the hint must not tell a download caller
// to re-run the list command (or vice versa).
const genericLookupTypeHint = "Re-run with --type todo|todolist|message|comment|card|card-table|document|schedule-entry|checkin|answer|forward|upload, or pass a URL (which encodes the type)"

// NewAttachmentsCmd creates the attachments command group.
func NewAttachmentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attachments",
		Short: "List and download attachments",
		Long: `List and download file attachments embedded in Basecamp items.

Attachments are files embedded in rich text content via <bc-attachment>
elements. Use 'list' to inspect them or 'download' to save them locally.`,
		Annotations: map[string]string{"agent_notes": "Parses <bc-attachment> tags from item content\nWorks on any recording type — todos, messages, cards, comments\nUse --type to skip the generic recording lookup when you know the type\nSupports --out - for stdout streaming (single file only)"},
	}

	cmd.AddCommand(
		newAttachmentsListCmd(),
		newAttachmentsDownloadCmd(),
	)

	return cmd
}

// ---------------------------------------------------------------------------
// attachments list
// ---------------------------------------------------------------------------

func newAttachmentsListCmd() *cobra.Command {
	var recordType string

	cmd := &cobra.Command{
		Use:   "list <id|url>",
		Short: "List attachments on an item",
		Long: `List all file attachments embedded in an item's content.

Attachments are extracted from the item's rich text content (HTML). This works
on any item type: todos, messages, cards, comments, documents, etc.

You can pass either an ID or a Basecamp URL:
  basecamp attachments list 789
  basecamp attachments list https://3.basecamp.com/123/buckets/456/todos/789`,
		Example: `  basecamp attachments list 789
  basecamp attachments list 789 --type todo
  basecamp attachments list https://3.basecamp.com/123/buckets/456/todos/789 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<id|url>")
			}
			return runAttachmentsList(cmd, args[0], recordType)
		},
	}

	cmd.Flags().StringVarP(&recordType, "type", "t", "", "Item type hint (todo, todolist, message, comment, card, card-table, document, schedule-entry, checkin, answer, forward, upload)")

	return cmd
}

func runAttachmentsList(cmd *cobra.Command, arg, recordType string) error {
	app := appctx.FromContext(cmd.Context())

	id, resolvedType := resolveAttachmentTarget(arg, recordType)

	// Validate type early (before account check) for better error messages
	if !isGenericType(resolvedType) && typeToEndpoint(resolvedType, id) == "" {
		return output.ErrUsageHint(
			fmt.Sprintf("Unknown type: %s", resolvedType),
			"Supported: todo, todolist, message, comment, card, card-table, document, schedule-entry, checkin, answer, forward, upload",
		)
	}

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Fetch the item — same two-step pattern as show.go:
	// 1. Generic recording lookup to discover type
	// 2. Re-fetch via type-specific endpoint for full content
	content, err := fetchItemContent(cmd, app, id, resolvedType)
	if err != nil {
		return err
	}

	attachments := richtext.ParseAttachments(content)

	// Styled TTY output
	if app.Output.EffectiveFormat() == output.FormatStyled {
		w := cmd.OutOrStdout()
		if len(attachments) == 0 {
			fmt.Fprintln(w, "No attachments found.")
			return nil
		}

		bold := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#888"))
		muted := lipgloss.NewStyle().Foreground(lipgloss.Color("#888"))
		fmt.Fprintln(w, bold.Render("Attachments:"))

		for i, a := range attachments {
			icon := "\U0001F4CE" // 📎
			if a.IsImage() {
				icon = "\U0001F5BC\uFE0F" // 🖼️
			}

			line := fmt.Sprintf("  %d. %s %s", i+1, icon, a.DisplayName())
			if a.IsImage() && a.Width != "" && a.Height != "" {
				line += fmt.Sprintf(" (%s\u00D7%s)", a.Width, a.Height)
			}
			fmt.Fprintln(w, line)

			if url := a.DisplayURL(); url != "" {
				fmt.Fprintf(w, "     URL: %s\n", url)
			}
		}

		// Download hint
		downloadable := 0
		for _, a := range attachments {
			if a.DisplayURL() != "" {
				downloadable++
			}
		}
		if downloadable > 0 {
			fmt.Fprintln(w)
			fmt.Fprintln(w, muted.Render(fmt.Sprintf("Download: basecamp attachments download %s", id)))
		}
		return nil
	}

	// JSON/agent output
	showCmd := fmt.Sprintf("basecamp show %s", id)
	if showType := normalizeShowType(resolvedType); showType != "" {
		showCmd = fmt.Sprintf("basecamp show %s --type %s", id, showType)
	}

	breadcrumbs := []output.Breadcrumb{
		{Action: "show", Cmd: showCmd, Description: "Show item"},
	}
	// Only suggest commenting when the target isn't itself a comment.
	if resolvedType != "comment" && resolvedType != "comments" {
		breadcrumbs = append(breadcrumbs, output.Breadcrumb{
			Action:      "comment",
			Cmd:         fmt.Sprintf("basecamp comment %s <text>", id),
			Description: "Add comment",
		})
	}
	downloadable := downloadableAttachments(attachments)
	if len(downloadable) > 0 {
		breadcrumbs = append(breadcrumbs, attachmentBreadcrumb(id, len(downloadable)))
	}

	respOpts := []output.ResponseOption{
		output.WithEntity("attachment"),
		output.WithSummary(fmt.Sprintf("%d attachment(s) on #%s", len(attachments), id)),
		output.WithBreadcrumbs(breadcrumbs...),
	}

	return app.OK(attachments, respOpts...)
}

// ---------------------------------------------------------------------------
// attachments download
// ---------------------------------------------------------------------------

func newAttachmentsDownloadCmd() *cobra.Command {
	var outDir string
	var filename string
	var recordType string
	var index int

	cmd := &cobra.Command{
		Use:   "download <id|url>",
		Short: "Download attachments from a recording",
		Long: `Download file attachments from any Basecamp recording.

Fetches the recording's rich text content, extracts <bc-attachment> elements,
and downloads the referenced files in parallel.

You can pass a recording ID or URL:
  basecamp attachments download 789
  basecamp attachments download https://3.basecamp.com/123/buckets/456/messages/789

Options:
  --out <dir>     Output directory (default: current directory)
  --out -         Stream a single file to stdout (requires single selection)
  --file <name>   Download only the named file
  --index <n>     Select attachment by 1-based index (disambiguates duplicate names)
  --type <type>   Recording type hint (todo, todolist, message, comment, card, card-table, document, schedule-entry, checkin, answer, forward, upload)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Resolve target — reuse the same URL/comment parsing as list
			idStr, resolvedType := resolveAttachmentTarget(args[0], recordType)

			// Validate type early (same guard as list)
			if !isGenericType(resolvedType) && typeToEndpoint(resolvedType, idStr) == "" {
				return output.ErrUsageHint(
					fmt.Sprintf("Unknown type: %s", resolvedType),
					"Supported: todo, todolist, message, comment, card, card-table, document, schedule-entry, checkin, answer, forward, upload",
				)
			}

			// Fetch recording content
			content, err := fetchItemContent(cmd, app, idStr, resolvedType)
			if err != nil {
				return err
			}

			// Parse all attachments (same parser as list — consistent indexes)
			allAttachments := richtext.ParseAttachments(content)
			if len(allAttachments) == 0 {
				return output.ErrUsageHint(
					"No downloadable attachments found",
					"This recording has no embedded file attachments",
				)
			}

			// Apply --index over the full set (matches list numbering)
			if index < 0 {
				return output.ErrUsage("--index must be a positive integer (1-based)")
			}
			if index > 0 {
				if index > len(allAttachments) {
					return output.ErrUsageHint(
						fmt.Sprintf("Index %d out of range (have %d attachments)", index, len(allAttachments)),
						"Use --index 1 through --index "+fmt.Sprintf("%d", len(allAttachments)),
					)
				}
				selected := allAttachments[index-1]
				if selected.DisplayURL() == "" {
					return output.ErrUsageHint(
						fmt.Sprintf("Attachment %d (%s) has no download URL", index, selected.DisplayName()),
						"This attachment has metadata but no downloadable URL",
					)
				}
				allAttachments = []richtext.ParsedAttachment{selected}
			}

			// Filter by filename if specified. Match against the full list
			// first so we can distinguish "name doesn't exist" from
			// "name exists but has no download URL".
			attachments := allAttachments
			if filename != "" {
				allMatches := filterParsedAttachments(attachments, filename)
				if len(allMatches) == 0 {
					names := parsedAttachmentFilenames(downloadableAttachments(attachments))
					return output.ErrUsageHint(
						fmt.Sprintf("No attachment matching %q", filename),
						fmt.Sprintf("Available: %s", strings.Join(names, ", ")),
					)
				}
				downloadable := downloadableAttachments(allMatches)
				if len(downloadable) == 0 {
					return output.ErrUsageHint(
						fmt.Sprintf("Attachment %q has no download URL", filename),
						"This attachment has metadata but no downloadable URL",
					)
				}
				attachments = downloadable
			}

			// After filtering/selection, verify at least one attachment
			// has a download URL. Without this, recordings whose
			// <bc-attachment> tags have metadata but no url/href would
			// proceed to download, mark everything as "skipped", and
			// exit 0 — making automation treat a no-op as success.
			if len(downloadableAttachments(attachments)) == 0 {
				return output.ErrUsageHint(
					"No downloadable attachments found",
					"This recording has attachment metadata but no downloadable URLs",
				)
			}

			// Stdout streaming: --out -
			if outDir == "-" {
				downloadable := downloadableAttachments(attachments)
				if len(downloadable) == 0 {
					return output.ErrUsageHint(
						"No downloadable attachments found",
						"This recording has no embedded file attachments with download URLs",
					)
				}
				if len(downloadable) > 1 {
					return output.ErrUsageHint(
						"Multiple attachments match",
						"Use --index <n> to select one when streaming to stdout",
					)
				}
				att := downloadable[0]
				result, err := app.Account().DownloadURL(cmd.Context(), att.DisplayURL())
				if err != nil {
					return convertSDKError(err)
				}
				defer result.Body.Close()
				_, err = io.Copy(cmd.OutOrStdout(), result.Body)
				return err
			}

			// Progress writer: stderr for TTY, nil for machine output
			var progress io.Writer
			if !app.IsMachineOutput() {
				progress = cmd.ErrOrStderr()
			}

			// Download all attachments (skips non-downloadable with status)
			results := downloadParsedAttachments(cmd.Context(), app, attachments, outDir, progress)

			var downloaded, skipped, failed int
			var errors []string
			for _, r := range results {
				switch r.Status {
				case "downloaded":
					downloaded++
				case "skipped":
					skipped++
				case "error":
					failed++
					errors = append(errors, fmt.Sprintf("%s: %s", r.Filename, r.Error))
				}
			}

			// If all operations failed, return an error for automation
			if downloaded == 0 && failed > 0 {
				return fmt.Errorf("all %d download(s) failed: %s", failed, strings.Join(errors, "; "))
			}

			parts := []string{fmt.Sprintf("%d downloaded", downloaded)}
			if skipped > 0 {
				parts = append(parts, fmt.Sprintf("%d skipped", skipped))
			}
			if failed > 0 {
				parts = append(parts, fmt.Sprintf("%d failed", failed))
			}
			summary := strings.Join(parts, ", ")

			opts := []output.ResponseOption{
				output.WithSummary(summary),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp show %s", idStr),
						Description: "View recording",
					},
				),
			}
			if len(errors) > 0 {
				opts = append(opts, output.WithNotice("Some downloads failed: "+strings.Join(errors, "; ")))
			}

			return app.OK(results, opts...)
		},
	}

	cmd.Flags().StringVarP(&outDir, "out", "o", "", "Output directory (default: current directory), use - for stdout")
	cmd.Flags().StringVar(&filename, "file", "", "Download only the named file")
	cmd.Flags().IntVar(&index, "index", 0, "Select attachment by 1-based index")
	cmd.Flags().StringVarP(&recordType, "type", "t", "", "Recording type hint (todo, todolist, message, comment, card, card-table, document, schedule-entry, checkin, answer, forward, upload)")

	return cmd
}

// ---------------------------------------------------------------------------
// Shared helpers — content fetching
// ---------------------------------------------------------------------------

// isGenericType returns true when the record type should use the generic
// recordings endpoint (with auto-discovery re-fetch), mirroring show.go.
// Includes types that require a parent ID for their dedicated endpoint
// (lines need chat ID, replies need forward ID) — the generic path
// discovers the parent via recordingTypeEndpoint.
func isGenericType(recordType string) bool {
	switch recordType {
	case "", "recording", "recordings", "line", "lines", "replies":
		return true
	default:
		return false
	}
}

// shouldSuggestType reports whether the --type usage hint is appropriate
// for the current recordType. It fires only when no explicit type was
// provided. The "line"/"lines"/"replies" aliases route through the same
// generic /recordings lookup for parent discovery, but the user *did*
// pass --type — telling those callers to "specify --type" is misleading.
func shouldSuggestType(recordType string) bool {
	switch recordType {
	case "", "recording", "recordings":
		return true
	default:
		return false
	}
}

// fetchItemContent retrieves the HTML content field from a Basecamp item.
// Uses the same recording-type discovery pattern as show.go.
func fetchItemContent(cmd *cobra.Command, app *appctx.App, id, recordType string) (string, error) {
	// If type is provided, go directly to the type-specific endpoint.
	// Caller validates the type before calling, so typeToEndpoint won't return "".
	var endpoint string
	if !isGenericType(recordType) {
		endpoint = typeToEndpoint(recordType, id)
	} else {
		// Generic recording lookup first
		endpoint = fmt.Sprintf("/recordings/%s.json", id)
	}

	resp, err := app.Account().Get(cmd.Context(), endpoint)
	if err != nil {
		// The generic /recordings/<id>.json endpoint returns 404 for
		// recording types that require bucket scope (e.g. Kanban::Card).
		// Convert to the same usage hint the 204 branch below emits so
		// the user is told to pass --type instead of seeing a bare
		// "Resource not found". Only fires when no explicit type was
		// provided — a 404 under --type line|replies means the ID is
		// wrong, not that --type is missing.
		if shouldSuggestType(recordType) {
			var sdkErr *basecamp.Error
			if errors.As(err, &sdkErr) && sdkErr.Code == basecamp.CodeNotFound {
				return "", output.ErrUsageHint(
					fmt.Sprintf("Item %s not found or type required", id),
					genericLookupTypeHint,
				)
			}
		}
		return "", convertSDKError(err)
	}
	if resp.StatusCode == http.StatusNoContent {
		if shouldSuggestType(recordType) {
			return "", output.ErrUsageHint(
				fmt.Sprintf("Item %s not found or type required", id),
				genericLookupTypeHint,
			)
		}
		return "", output.ErrNotFound("item", id)
	}

	// UseNumber so parentRecordingID sees json.Number for large IDs
	// (lines/replies need parent.id to build the refetch endpoint).
	var data map[string]any
	dec := json.NewDecoder(bytes.NewReader(resp.Data))
	dec.UseNumber()
	if err := dec.Decode(&data); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	// If we used generic recording, re-fetch via type-specific endpoint for full content
	if isGenericType(recordType) {
		if refetchEndpoint := recordingTypeEndpoint(data, id); refetchEndpoint != "" {
			refetchResp, refetchErr := app.Account().Get(cmd.Context(), refetchEndpoint)
			if refetchErr == nil && refetchResp.StatusCode != http.StatusNoContent {
				var richer map[string]any
				refetchDec := json.NewDecoder(bytes.NewReader(refetchResp.Data))
				refetchDec.UseNumber()
				if refetchDec.Decode(&richer) == nil {
					data = richer
				}
			}
		}
	}

	// Extract content — different item types use different field names
	return extractContentField(data), nil
}

// extractContentField extracts the rich-text HTML content from a recording's
// data map. Both "content" and "description" are inspected: the field that
// contains HTML is preferred, because some recording types (e.g. todos) store
// a plain-text title in "content" and the rich body in "description".
// When both contain HTML, both are concatenated so all attachments are found.
func extractContentField(data map[string]any) string {
	content, _ := data["content"].(string)
	description, _ := data["description"].(string)

	if content == "" {
		return description
	}
	if description == "" {
		return content
	}

	// Both present: if only one is HTML, return it.
	// If both are HTML, concatenate to capture attachments in either field.
	contentIsHTML := richtext.IsHTML(content)
	descIsHTML := richtext.IsHTML(description)
	if descIsHTML && !contentIsHTML {
		return description
	}
	if contentIsHTML && descIsHTML {
		return content + "\n" + description
	}
	return content
}

// resolveAttachmentTarget extracts the recording ID and type from a raw
// argument (plain ID or Basecamp URL) and an optional explicit type hint.
// Prefers CommentID when present and type is compatible.
func resolveAttachmentTarget(arg, recordType string) (id, resolvedType string) {
	id = extractID(arg)
	resolvedType = recordType

	if parsed := urlarg.Parse(arg); parsed != nil {
		if parsed.CommentID != "" && (resolvedType == "" || resolvedType == "comment" || resolvedType == "comments") {
			id = parsed.CommentID
			if resolvedType == "" {
				resolvedType = "comment"
			}
		} else if parsed.RecordingID != "" {
			id = parsed.RecordingID
		}
		if resolvedType == "" && parsed.Type != "" {
			resolvedType = parsed.Type
		}
	}

	return id, resolvedType
}

// normalizeShowType maps attachment type aliases to canonical types that
// basecamp show accepts. Returns "" for generic types (no --type needed).
func normalizeShowType(recordType string) string {
	switch recordType {
	case "":
		return ""
	case "todos":
		return "todo"
	case "todolists":
		return "todolist"
	case "messages":
		return "message"
	case "comments":
		return "comment"
	case "cards":
		return "card"
	case "card_tables":
		return "card-table"
	case "documents":
		return "document"
	case "schedule_entries":
		return "schedule-entry"
	case "answer", "question_answers":
		// show has no type alias for question_answers; omit --type
		// so generic recording lookup finds the right endpoint.
		return ""
	case "questions":
		return "checkin"
	case "forwards", "inbox_forwards":
		return "forward"
	case "uploads":
		return "upload"
	case "recording", "recordings":
		return ""
	default:
		return recordType
	}
}

// typeToEndpoint maps a user-provided type string to the API endpoint.
// Superset of show.go's types — includes URL path segment aliases
// (e.g. question_answers, schedule_entries) that show.go doesn't accept.
func typeToEndpoint(recordType, id string) string {
	switch recordType {
	case "todo", "todos":
		return fmt.Sprintf("/todos/%s.json", id)
	case "todolist", "todolists":
		return fmt.Sprintf("/todolists/%s.json", id)
	case "message", "messages":
		return fmt.Sprintf("/messages/%s.json", id)
	case "comment", "comments":
		return fmt.Sprintf("/comments/%s.json", id)
	case "card", "cards":
		return fmt.Sprintf("/card_tables/cards/%s.json", id)
	case "card-table", "card_table", "cardtable", "card_tables":
		return fmt.Sprintf("/card_tables/%s.json", id)
	case "document", "documents":
		return fmt.Sprintf("/documents/%s.json", id)
	case "schedule-entry", "schedule_entry", "schedule_entries":
		return fmt.Sprintf("/schedule_entries/%s.json", id)
	case "answer", "question_answers":
		return fmt.Sprintf("/question_answers/%s.json", id)
	case "checkin", "check-in", "check_in", "questions":
		return fmt.Sprintf("/questions/%s.json", id)
	case "forward", "forwards", "inbox_forwards":
		return fmt.Sprintf("/forwards/%s.json", id)
	case "upload", "uploads":
		return fmt.Sprintf("/uploads/%s.json", id)
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// Shared helpers — download
// ---------------------------------------------------------------------------

// filterParsedAttachments returns attachments whose Filename matches the given name.
func filterParsedAttachments(atts []richtext.ParsedAttachment, name string) []richtext.ParsedAttachment {
	var result []richtext.ParsedAttachment
	for _, a := range atts {
		if a.Filename == name {
			result = append(result, a)
		}
	}
	return result
}

// parsedAttachmentFilenames returns unique filenames from the attachment list.
func parsedAttachmentFilenames(atts []richtext.ParsedAttachment) []string {
	seen := make(map[string]bool)
	var names []string
	for _, a := range atts {
		name := a.Filename
		if name == "" {
			name = "(unnamed)"
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// attachmentResult holds the outcome of a single attachment download.
type attachmentResult struct {
	URL         string `json:"url"`
	Filename    string `json:"filename"`
	Status      string `json:"status"`
	Path        string `json:"path,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	ByteSize    int64  `json:"byte_size,omitempty"`
	Error       string `json:"error,omitempty"`
}

// downloadParsedAttachments processes ALL attachments — downloadable entries
// are fetched in parallel, non-downloadable ones are marked as skipped.
// Progress lines are written to progress (nil suppresses output).
func downloadParsedAttachments(ctx context.Context, app *appctx.App, attachments []richtext.ParsedAttachment, outDir string, progress io.Writer) []attachmentResult {
	dir := outDir
	if dir == "" {
		dir = "."
	}

	total := len(attachments)
	results := make([]attachmentResult, total)
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	var mu sync.Mutex
	used := make(map[string]bool)
	var completed atomic.Int32

	for i, att := range attachments {
		dlURL := att.DisplayURL()
		fname := att.Filename
		if fname == "" {
			fname = att.DisplayName()
		}

		if dlURL == "" {
			seq := completed.Add(1)
			results[i] = attachmentResult{
				Filename: fname,
				Status:   "skipped",
			}
			if progress != nil {
				fmt.Fprintf(progress, "  [%d/%d] Skipped: %s (no download URL)\n", seq, total, fname)
			}
			continue
		}

		wg.Add(1)
		go func(i int, att richtext.ParsedAttachment, dlURL, fname string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			mu.Lock()
			name := uniqueFilename(dir, used, fname)
			used[name] = true
			mu.Unlock()

			dl, err := app.Account().DownloadURL(ctx, dlURL)
			if err != nil {
				seq := completed.Add(1)
				errMsg := convertSDKError(err).Error()
				results[i] = attachmentResult{URL: dlURL, Filename: name, Status: "error", Error: errMsg}
				if progress != nil {
					fmt.Fprintf(progress, "  [%d/%d] Error: %s — %s\n", seq, total, name, errMsg)
				}
				return
			}
			defer dl.Body.Close()

			path, bytes, writeErr := writeBodyToFile(dl.Body, dir, name)
			if writeErr != nil {
				seq := completed.Add(1)
				results[i] = attachmentResult{URL: dlURL, Filename: name, Status: "error", Error: writeErr.Error()}
				if progress != nil {
					fmt.Fprintf(progress, "  [%d/%d] Error: %s — %s\n", seq, total, name, writeErr.Error())
				}
				return
			}

			seq := completed.Add(1)
			results[i] = attachmentResult{
				URL:         dlURL,
				Filename:    name,
				Status:      "downloaded",
				Path:        path,
				ContentType: att.ContentType,
				ByteSize:    bytes,
			}
			if progress != nil {
				fmt.Fprintf(progress, "  [%d/%d] Downloaded %s (%s)\n", seq, total, path, humanSize(bytes))
			}
		}(i, att, dlURL, fname)
	}
	wg.Wait()
	return results
}

// uniqueFilename returns a filename that is unique within the output directory
// and the used set. Appends -1, -2, etc. suffixes to avoid collisions.
func uniqueFilename(dir string, used map[string]bool, name string) string {
	if name == "" {
		name = "download"
	}
	name = filepath.Base(name)
	if name == "." || name == "" {
		name = "download"
	}
	candidate := name
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; used[candidate] || fileExists(filepath.Join(dir, candidate)); i++ {
		candidate = fmt.Sprintf("%s-%d%s", base, i, ext)
	}
	return candidate
}

// fileExists returns true if the path exists on disk.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// writeBodyToFile writes an io.Reader to dir/name using the exact filename
// provided (no SDK override). Returns the output path and bytes written.
func writeBodyToFile(body io.Reader, dir, name string) (outputPath string, written int64, err error) {
	outputPath = filepath.Join(dir, name)

	// Verify the resolved path is within dir to prevent traversal attacks
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", 0, fmt.Errorf("failed to resolve output directory: %w", err)
	}
	absPath, err := filepath.Abs(outputPath)
	if err != nil {
		return "", 0, fmt.Errorf("failed to resolve output path: %w", err)
	}
	expectedPrefix := absDir
	if !strings.HasSuffix(expectedPrefix, string(filepath.Separator)) {
		expectedPrefix += string(filepath.Separator)
	}
	if !strings.HasPrefix(absPath, expectedPrefix) {
		return "", 0, output.ErrUsage("Invalid filename: path traversal detected")
	}

	outFile, err := createFile(outputPath)
	if err != nil {
		return "", 0, fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()

	written, err = io.Copy(outFile, body)
	if err != nil {
		return "", 0, fmt.Errorf("failed to write file: %w", err)
	}
	return outputPath, written, nil
}

// ---------------------------------------------------------------------------
// Shared helpers — show command integration
// ---------------------------------------------------------------------------

// withAttachmentMeta adds a "<field>_attachments" field to data by
// JSON-marshaling the struct to a map and inserting the field. Falls back
// to the original data on any marshal error. When dlResults is non-nil,
// each attachment's metadata gains path/status/error fields from the
// corresponding download result.
//
// The field parameter names the source rich-text attribute (e.g. "content",
// "description") so recordings with multiple rich-text fields produce
// distinct attachment collections (content_attachments, description_attachments).
//
// Can be called sequentially on the same data — the second call operates on
// the map returned by the first, preserving both keys.
func withAttachmentMeta(data any, field string, atts []richtext.ParsedAttachment, dlResults []attachmentResult) any {
	if len(atts) == 0 {
		return data
	}

	// If data is already a map (from a prior call), reuse it directly.
	if m, ok := data.(map[string]any); ok {
		m[field+"_attachments"] = attachmentMeta(atts, dlResults)
		return m
	}

	b, err := json.Marshal(data)
	if err != nil {
		return data
	}
	// Decode with UseNumber to preserve integer precision (IDs > 2^53).
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return data
	}
	m[field+"_attachments"] = attachmentMeta(atts, dlResults)
	return m
}

// attachmentMeta converts ParsedAttachment slice to JSON-friendly metadata.
// When dlResults is non-nil, each entry gains "path", "download_status",
// and (on failure) "download_error" fields from the corresponding result.
func attachmentMeta(atts []richtext.ParsedAttachment, dlResults []attachmentResult) []map[string]string {
	result := make([]map[string]string, len(atts))
	for i, a := range atts {
		m := map[string]string{}
		if url := a.DisplayURL(); url != "" {
			m["url"] = url
		}
		if a.Filename != "" {
			m["filename"] = a.Filename
		}
		if a.ContentType != "" {
			m["content_type"] = a.ContentType
		}
		if a.Filesize != "" {
			m["filesize"] = a.Filesize
		}
		if a.SGID != "" {
			m["sgid"] = a.SGID
		}
		if a.Width != "" {
			m["width"] = a.Width
		}
		if a.Height != "" {
			m["height"] = a.Height
		}
		if dlResults != nil && i < len(dlResults) {
			r := dlResults[i]
			if r.Path != "" {
				m["path"] = r.Path
			}
			if r.Status != "" {
				m["download_status"] = r.Status
			}
			if r.Error != "" {
				m["download_error"] = r.Error
			}
		}
		result[i] = m
	}
	return result
}

// downloadableAttachments returns only attachments that have a download URL.
func downloadableAttachments(atts []richtext.ParsedAttachment) []richtext.ParsedAttachment {
	var result []richtext.ParsedAttachment
	for _, a := range atts {
		if a.DisplayURL() != "" {
			result = append(result, a)
		}
	}
	return result
}

// attachmentBreadcrumb returns a breadcrumb hinting at the download command.
func attachmentBreadcrumb(id string, n int) output.Breadcrumb {
	desc := "Download attachment"
	if n > 1 {
		desc = fmt.Sprintf("Download %d attachments", n)
	}
	return output.Breadcrumb{
		Action:      "download",
		Cmd:         fmt.Sprintf("basecamp attachments download %s", id),
		Description: desc,
	}
}

// addDownloadAttachmentsFlag registers --download-attachments on a command.
// Returns a pointer to the flag value. Use cmd.Flags().Changed() to detect use.
func addDownloadAttachmentsFlag(cmd *cobra.Command) *string {
	var dir string
	cmd.Flags().StringVar(&dir, "download-attachments", "", "Download attachments to `dir` (default: OS temp dir)")
	// NoOptDefVal must be non-empty for pflag to treat the value as optional.
	// Use the OS temp dir so --download-attachments (bare) works as documented.
	cmd.Flags().Lookup("download-attachments").NoOptDefVal = os.TempDir()
	return &dir
}

// downloadAttachmentResults holds the outcome of runDownloadAttachments.
type downloadAttachmentResults struct {
	Results []attachmentResult
	Notice  string // non-empty when some downloads failed
}

// runDownloadAttachments downloads attachments when --download-attachments is set.
// Returns nil when the flag was not set, otherwise the full per-attachment results
// with status, path, and error information.
func runDownloadAttachments(cmd *cobra.Command, app *appctx.App, attachments []richtext.ParsedAttachment, dirFlag *string) *downloadAttachmentResults {
	if !cmd.Flags().Changed("download-attachments") {
		return nil
	}

	dir := *dirFlag
	if dir == "" {
		dir = os.TempDir()
	}

	var progress io.Writer
	if !app.IsMachineOutput() {
		progress = cmd.ErrOrStderr()
		fmt.Fprintf(progress, "Downloading attachments to %s\n", dir)
	}

	results := downloadParsedAttachments(cmd.Context(), app, attachments, dir, progress)

	var errors []string
	for _, r := range results {
		if r.Status == "error" {
			errors = append(errors, fmt.Sprintf("%s: %s", r.Filename, r.Error))
		}
	}
	var notice string
	if len(errors) > 0 {
		notice = "Some downloads failed: " + strings.Join(errors, "; ")
	}

	return &downloadAttachmentResults{Results: results, Notice: notice}
}
