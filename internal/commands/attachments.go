package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/urlarg"
)

// NewAttachmentsCmd creates the attachments command group (list).
func NewAttachmentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "attachments",
		Short:       "List attachments on items",
		Long:        "List file attachments embedded in Basecamp items (todos, messages, cards, comments, etc.).",
		Annotations: map[string]string{"agent_notes": "Parses <bc-attachment> tags from item content\nWorks on any recording type — todos, messages, cards, comments\nUse --type to skip the generic recording lookup when you know the type"},
	}

	cmd.AddCommand(newAttachmentsListCmd())

	return cmd
}

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

	id := extractID(arg)

	// Auto-detect type from URL if not specified (mirrors show.go).
	// Prefer CommentID when present and type is compatible (comment.go convention).
	if parsed := urlarg.Parse(arg); parsed != nil {
		if parsed.CommentID != "" && (recordType == "" || recordType == "comment" || recordType == "comments") {
			id = parsed.CommentID
			if recordType == "" {
				recordType = "comment"
			}
		} else if parsed.RecordingID != "" {
			id = parsed.RecordingID
		}
		if recordType == "" && parsed.Type != "" {
			recordType = parsed.Type
		}
	}

	// Validate type early (before account check) for better error messages
	if !isGenericType(recordType) && typeToEndpoint(recordType, id) == "" {
		return output.ErrUsageHint(
			fmt.Sprintf("Unknown type: %s", recordType),
			"Supported: todo, todolist, message, comment, card, card-table, document, schedule-entry, checkin, answer, forward, upload",
		)
	}

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Fetch the item — same two-step pattern as show.go:
	// 1. Generic recording lookup to discover type
	// 2. Re-fetch via type-specific endpoint for full content
	content, err := fetchItemContent(cmd, app, id, recordType)
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
		return nil
	}

	// JSON/agent output
	showCmd := fmt.Sprintf("basecamp show %s", id)
	if showType := normalizeShowType(recordType); showType != "" {
		showCmd = fmt.Sprintf("basecamp show %s --type %s", id, showType)
	}

	breadcrumbs := []output.Breadcrumb{
		{Action: "show", Cmd: showCmd, Description: "Show item"},
	}
	// Only suggest commenting when the target isn't itself a comment.
	if recordType != "comment" && recordType != "comments" {
		breadcrumbs = append(breadcrumbs, output.Breadcrumb{
			Action:      "comment",
			Cmd:         fmt.Sprintf("basecamp comment %s <text>", id),
			Description: "Add comment",
		})
	}

	respOpts := []output.ResponseOption{
		output.WithEntity("attachment"),
		output.WithSummary(fmt.Sprintf("%d attachment(s) on #%s", len(attachments), id)),
		output.WithBreadcrumbs(breadcrumbs...),
	}

	return app.OK(attachments, respOpts...)
}

// isGenericType returns true when the record type should use the generic
// recordings endpoint (with auto-discovery re-fetch), mirroring show.go.
func isGenericType(recordType string) bool {
	return recordType == "" || recordType == "recording" || recordType == "recordings"
}

// fetchItemContent retrieves the HTML content field from a Basecamp item.
// Uses the same recording-type discovery pattern as show.go.
func fetchItemContent(cmd *cobra.Command, app *appctx.App, id, recordType string) (string, error) {
	// If type is provided, go directly to the type-specific endpoint
	var endpoint string
	if !isGenericType(recordType) {
		endpoint = typeToEndpoint(recordType, id)
		if endpoint == "" {
			return "", output.ErrUsageHint(
				fmt.Sprintf("Unknown type: %s", recordType),
				"Supported: todo, todolist, message, comment, card, card-table, document, schedule-entry, checkin, answer, forward, upload",
			)
		}
	} else {
		// Generic recording lookup first
		endpoint = fmt.Sprintf("/recordings/%s.json", id)
	}

	resp, err := app.Account().Get(cmd.Context(), endpoint)
	if err != nil {
		return "", convertSDKError(err)
	}
	if resp.StatusCode == http.StatusNoContent {
		if isGenericType(recordType) {
			return "", output.ErrUsageHint(
				fmt.Sprintf("Item %s not found or type required", id),
				"Specify a type: basecamp attachments list <id> --type todo|todolist|message|comment|card|card-table|document|schedule-entry|checkin|answer|forward|upload",
			)
		}
		return "", output.ErrNotFound("item", id)
	}

	var data map[string]any
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	// If we used generic recording, re-fetch via type-specific endpoint for full content
	if isGenericType(recordType) {
		if refetchEndpoint := recordingTypeEndpoint(data, id); refetchEndpoint != "" {
			refetchResp, refetchErr := app.Account().Get(cmd.Context(), refetchEndpoint)
			if refetchErr == nil && refetchResp.StatusCode != http.StatusNoContent {
				var richer map[string]any
				if json.Unmarshal(refetchResp.Data, &richer) == nil {
					data = richer
				}
			}
		}
	}

	// Extract content — different item types use different field names
	return extractContentField(data), nil
}

// extractContentField tries multiple field names to find the HTML content.
// Todos use "description", most others use "content".
func extractContentField(data map[string]any) string {
	var parts []string
	for _, key := range []string{"content", "description"} {
		if v, ok := data[key].(string); ok && v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, "\n")
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
		return "checkin"
	case "questions":
		return "checkin"
	case "forwards":
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
// Kept in sync with show.go's isValidRecordType / recordingTypeEndpoint.
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
	case "forward", "forwards":
		return fmt.Sprintf("/forwards/%s.json", id)
	case "upload", "uploads":
		return fmt.Sprintf("/uploads/%s.json", id)
	default:
		return ""
	}
}
