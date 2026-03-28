package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/urlarg"
)

// NewShowCmd creates the show command for viewing any recording.
func NewShowCmd() *cobra.Command {
	var recordType string
	var cf *commentFlags
	var dlDir *string

	cmd := &cobra.Command{
		Use:   "show [type] <id|url>",
		Short: "Show any item by ID or URL",
		Long: `Show details of any Basecamp item by ID or URL.

Common types: todo, todolist, message, comment, card, card-table,
  document, schedule-entry, checkin, forward, upload, vault, chat,
  line, people, boosts

Also accepts URL path types directly (e.g. inbox_forwards,
question_answers, card_tables, columns, steps, todosets).

If no type specified, uses generic lookup.
URLs with #__recording_ fragments resolve the referenced recording.

You can also pass a Basecamp URL directly:
  basecamp show https://3.basecamp.com/123/buckets/456/todos/789
  basecamp show todo 789`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			// Parse positional args: [type] <id|url>
			var id string
			if len(args) == 1 {
				id = args[0]
			} else {
				// Two args: type and id
				if recordType == "" {
					recordType = args[0]
				}
				id = args[1]
			}

			// Capture whether the user performed an untyped lookup before
			// URL parsing can modify recordType. Fragment URLs intentionally
			// clear recordType, so checking after parsing would misclassify
			// them as untyped. This drives the 204 error message: untyped
			// lookups get a "type required" hint, typed ones get "not found".
			untypedLookup := recordType == "" || recordType == "recording" || recordType == "recordings"

			// Check if the id is a URL and extract components
			var occurrenceDate string
			if parsed := urlarg.Parse(id); parsed != nil {
				if parsed.CommentID != "" {
					// Fragment URL (#__recording_N) — resolve the referenced
					// recording directly instead of the parent resource.
					// Intentionally clears recordType even if --type was provided,
					// because the fragment identifies the specific recording and
					// its type will be auto-detected from the API response.
					id = parsed.CommentID
					recordType = ""
					untypedLookup = false // user targeted a specific recording via fragment
				} else if parsed.IsCollection {
					// Collection URL (e.g. .../todosets/777/todolists) — the ID
					// belongs to the parent container, not a child resource.
					return output.ErrUsageHint(
						"This URL points to a list, not an individual item",
						"Paste the URL of a specific item from this list to show it",
					)
				} else if parsed.RecordingID != "" {
					id = parsed.RecordingID
					// Auto-detect type from URL if not specified
					if recordType == "" && parsed.Type != "" {
						recordType = parsed.Type
						untypedLookup = false // URL carries the type
					}
					occurrenceDate = parsed.OccurrenceDate
				} else if parsed.ProjectID != "" && parsed.Type == "project" {
					// Project URL — redirect to "projects show"
					return output.ErrUsageHint(
						"Use 'projects show' for project URLs",
						fmt.Sprintf("basecamp projects show %s", parsed.ProjectID),
					)
				} else if parsed.Type != "" && parsed.RecordingID == "" && parsed.ProjectID != "" {
					// Type-level URL (e.g. .../todolists, .../messages) — structural
					// match with no specific item ID.
					return output.ErrUsageHint(
						"This URL points to a list, not an individual item",
						"Paste the URL of a specific item from this list to show it",
					)
				} else {
					// URL was recognized but has no recording ID (e.g. circle URLs
					// which represent people groups, not viewable items).
					return output.ErrUsageHint(
						"This URL type cannot be shown",
						"Try pasting the URL of a specific item instead",
					)
				}
			}

			// Validate type early (before account check) for better error messages
			if !isValidRecordType(recordType) {
				return output.ErrUsageHint(
					fmt.Sprintf("Unknown type: %s", recordType),
					"Supported types: todo, todolist, message, comment, card, card-table, "+
						"document, schedule-entry, checkin, forward, upload, vault, chat, "+
						"line, replies, people, boosts, or any Basecamp URL",
				)
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Determine endpoint based on type. Types without a dedicated
			// shortcut endpoint go through /recordings/ and need a refetch
			// to get full content.
			var (
				endpoint     string
				needsRefetch bool
			)
			switch recordType {
			case "todo", "todos":
				endpoint = fmt.Sprintf("/todos/%s.json", id)
			case "todolist", "todolists":
				endpoint = fmt.Sprintf("/todolists/%s.json", id)
			case "message", "messages":
				endpoint = fmt.Sprintf("/messages/%s.json", id)
			case "comment", "comments":
				endpoint = fmt.Sprintf("/comments/%s.json", id)
			case "card", "cards":
				endpoint = fmt.Sprintf("/card_tables/cards/%s.json", id)
			case "card-table", "card_table", "cardtable", "card_tables":
				endpoint = fmt.Sprintf("/card_tables/%s.json", id)
			case "document", "documents":
				endpoint = fmt.Sprintf("/documents/%s.json", id)
			case "schedule-entry", "schedule_entry", "schedule_entries":
				if occurrenceDate != "" {
					endpoint = fmt.Sprintf("/schedule_entries/%s/occurrences/%s.json", id, occurrenceDate)
				} else {
					endpoint = fmt.Sprintf("/schedule_entries/%s.json", id)
				}
			case "checkin", "check-in", "check_in", "questions":
				endpoint = fmt.Sprintf("/questions/%s.json", id)
			case "question_answers":
				endpoint = fmt.Sprintf("/question_answers/%s.json", id)
			case "forward", "forwards", "inbox_forwards":
				endpoint = fmt.Sprintf("/forwards/%s.json", id)
			case "upload", "uploads":
				endpoint = fmt.Sprintf("/uploads/%s.json", id)
			case "vault", "vaults":
				endpoint = fmt.Sprintf("/vaults/%s.json", id)
			case "chat", "chats", "campfire", "campfires":
				endpoint = fmt.Sprintf("/chats/%s.json", id)
			case "people":
				endpoint = fmt.Sprintf("/people/%s.json", id)
			case "boosts":
				endpoint = fmt.Sprintf("/boosts/%s.json", id)
			case "columns":
				endpoint = fmt.Sprintf("/card_tables/columns/%s.json", id)
			case "steps":
				endpoint = fmt.Sprintf("/card_tables/steps/%s.json", id)
			case "todosets":
				endpoint = fmt.Sprintf("/todosets/%s.json", id)
			case "message_boards":
				endpoint = fmt.Sprintf("/message_boards/%s.json", id)
			case "schedules":
				endpoint = fmt.Sprintf("/schedules/%s.json", id)
			case "questionnaires":
				endpoint = fmt.Sprintf("/questionnaires/%s.json", id)
			case "inboxes":
				endpoint = fmt.Sprintf("/inboxes/%s.json", id)
			case "line", "lines":
				// Lines require a parent chat ID for the dedicated endpoint
				// (/chats/{id}/lines/{id}), which we don't have from a plain
				// "show line 123" invocation. Use generic recording lookup.
				endpoint = fmt.Sprintf("/recordings/%s.json", id)
				needsRefetch = true
			case "replies":
				// Replies require a parent forward ID for the dedicated endpoint
				// (/inbox_forwards/{id}/replies/{id}), which we don't have from
				// a plain "show replies 123" invocation. Use generic recording lookup.
				endpoint = fmt.Sprintf("/recordings/%s.json", id)
				needsRefetch = true
			case "", "recording", "recordings":
				endpoint = fmt.Sprintf("/recordings/%s.json", id)
				needsRefetch = true
			default:
				// isValidRecordType guards against this; unreachable in practice.
				return fmt.Errorf("internal: unhandled record type %q", recordType)
			}

			resp, err := app.Account().Get(cmd.Context(), endpoint)
			if err != nil {
				return convertSDKError(err)
			}

			// Check for empty response (204 No Content)
			if resp.StatusCode == http.StatusNoContent {
				if untypedLookup {
					return output.ErrUsageHint(
						fmt.Sprintf("Item %s not found or type required", id),
						"Specify a type: basecamp show todo|todolist|message|comment|card|document <id>",
					)
				}
				return output.ErrNotFound("item", id)
			}

			// Parse response for summary. UseNumber preserves integer
			// precision so IDs survive the map round-trip when we inject
			// attachment metadata below.
			var data map[string]any
			dec := json.NewDecoder(bytes.NewReader(resp.Data))
			dec.UseNumber()
			if err := dec.Decode(&data); err != nil {
				return err
			}

			// For generic recording lookups, re-fetch using the type-specific
			// endpoint to get full content (the /recordings/ endpoint returns
			// sparse data). The endpoint is derived from the response's type
			// field — never from the url field, which could point off-origin.
			if needsRefetch {
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

			// Skip comment fetch for non-commentable types.
			enrichment := &commentEnrichment{}
			if isCommentableShowType(recordType, data) {
				enrichment = fetchRecordingComments(cmd.Context(), app, id, data, cf)
				if enrichment.Comments != nil {
					data["comments"] = enrichment.Comments
				}
			}

			// Extract title from various fields
			title := ""
			for _, key := range []string{"title", "name", "content", "subject"} {
				if v, ok := data[key].(string); ok && v != "" {
					title = v
					break
				}
			}
			if len(title) > 60 {
				title = title[:57] + "..."
			}

			itemType := "Item"
			if t, ok := data["type"].(string); ok && t != "" {
				itemType = t
			}

			summary := fmt.Sprintf("%s #%s: %s", itemType, id, title)
			if enrichment.CountLabel != "" {
				summary += fmt.Sprintf(" (%s)", enrichment.CountLabel)
			}
			breadcrumbs := make([]output.Breadcrumb, 0, 1+len(enrichment.Breadcrumbs))
			breadcrumbs = append(breadcrumbs, output.Breadcrumb{
				Action:      "comment",
				Cmd:         fmt.Sprintf("basecamp comment %s <text>", id),
				Description: "Add comment",
			})
			breadcrumbs = append(breadcrumbs, enrichment.Breadcrumbs...)

			opts := []output.ResponseOption{
				output.WithSummary(summary),
				output.WithBreadcrumbs(breadcrumbs...),
			}

			// Check for attachments in content and description fields independently
			contentStr, _ := data["content"].(string)
			contentAtts := downloadableAttachments(richtext.ParseAttachments(contentStr))

			descStr, _ := data["description"].(string)
			descAtts := downloadableAttachments(richtext.ParseAttachments(descStr))

			resultData := any(data)
			attachmentNotice := ""
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
					data["content_attachments"] = attachmentMeta(contentAtts, contentDL)
				}
				if len(descAtts) > 0 {
					data["description_attachments"] = attachmentMeta(descAtts, descDL)
				}
				resultData = data

				attachmentNotice = fmt.Sprintf("%d attachment(s) — download: basecamp attachments download %s", total, id)
				if dl != nil && dl.Notice != "" {
					attachmentNotice += "; " + dl.Notice
				}
				opts = append(opts, output.WithBreadcrumbs(attachmentBreadcrumb(id, total)))
			}

			opts = append(opts, enrichment.applyNotices(attachmentNotice)...)

			return app.OK(resultData, opts...)
		},
	}

	cmd.Flags().StringVarP(&recordType, "type", "t", "", "Content type (e.g. todo, message, comment, card, document, vault, chat)")
	cf = addCommentFlags(cmd, true)
	dlDir = addDownloadAttachmentsFlag(cmd)

	return cmd
}

// recordingTypeEndpoint maps a recording's canonical API "type" field to the
// type-specific endpoint path. Type names are the namespaced forms returned by
// the Basecamp API (e.g. "Kanban::Card", "Schedule::Entry"), matching the SDK
// constants in basecamp.RecordingType*. Returns "" for unrecognized types,
// causing the caller to fall through to sparse recording data (no regression).
func recordingTypeEndpoint(data map[string]any, id string) string {
	t, _ := data["type"].(string)

	// Chat lines have multiple subtypes (Chat::Lines::Text, Chat::Lines::Attachment, …).
	// They require the parent chat ID for the dedicated endpoint.
	if strings.HasPrefix(t, "Chat::Lines::") {
		if parentID := parentRecordingID(data); parentID != "" {
			return fmt.Sprintf("/chats/%s/lines/%s.json", parentID, id)
		}
		return ""
	}

	switch t {
	case "Todo", "Todolist::Todo":
		return fmt.Sprintf("/todos/%s.json", id)
	case "Todolist":
		return fmt.Sprintf("/todolists/%s.json", id)
	case "Message":
		return fmt.Sprintf("/messages/%s.json", id)
	case "Comment":
		return fmt.Sprintf("/comments/%s.json", id)
	case "Kanban::Card":
		return fmt.Sprintf("/card_tables/cards/%s.json", id)
	case "Document", "Vault::Document":
		return fmt.Sprintf("/documents/%s.json", id)
	case "Schedule::Entry":
		return fmt.Sprintf("/schedule_entries/%s.json", id)
	case "Question":
		return fmt.Sprintf("/questions/%s.json", id)
	case "Question::Answer":
		return fmt.Sprintf("/question_answers/%s.json", id)
	case "Inbox::Forward":
		return fmt.Sprintf("/forwards/%s.json", id)
	case "Upload":
		return fmt.Sprintf("/uploads/%s.json", id)
	case "Vault":
		return fmt.Sprintf("/vaults/%s.json", id)
	case "Chat::Transcript":
		return fmt.Sprintf("/chats/%s.json", id)
	case "Todoset":
		return fmt.Sprintf("/todosets/%s.json", id)
	case "Message::Board":
		return fmt.Sprintf("/message_boards/%s.json", id)
	case "Schedule":
		return fmt.Sprintf("/schedules/%s.json", id)
	case "Questionnaire":
		return fmt.Sprintf("/questionnaires/%s.json", id)
	case "Inbox":
		return fmt.Sprintf("/inboxes/%s.json", id)
	case "Kanban::Column":
		return fmt.Sprintf("/card_tables/columns/%s.json", id)
	case "Kanban::Step":
		return fmt.Sprintf("/card_tables/steps/%s.json", id)
	case "Inbox::Forward::Reply":
		if parentID := parentRecordingID(data); parentID != "" {
			return fmt.Sprintf("/inbox_forwards/%s/replies/%s.json", parentID, id)
		}
		return ""
	default:
		return ""
	}
}

// parentRecordingID extracts the parent recording's ID from the "parent"
// object in a recording response. Returns "" if absent.
func parentRecordingID(data map[string]any) string {
	parent, ok := data["parent"].(map[string]any)
	if !ok {
		return ""
	}
	// Handle both json.Number (UseNumber) and float64 (standard decode).
	switch id := parent["id"].(type) {
	case json.Number:
		return id.String()
	case float64:
		return fmt.Sprintf("%.0f", id)
	default:
		return ""
	}
}

// isCommentableShowType returns true when the record type supports comments.
// Checks the CLI type alias first; for generic lookups (empty recordType) falls
// back to the API response's "type" field.
func isCommentableShowType(recordType string, data map[string]any) bool {
	// Non-commentable CLI type aliases.
	switch recordType {
	case "people", "boosts", "comment", "comments":
		return false
	}

	// For generic lookups, check the API response type.
	if recordType == "" || recordType == "recording" || recordType == "recordings" {
		apiType, _ := data["type"].(string)
		switch apiType {
		case "Person", "Boost", "Comment":
			return false
		}
	}

	return true
}

// isValidRecordType checks if the given type is a valid recording type.
func isValidRecordType(t string) bool {
	switch t {
	case "", "todo", "todos", "todolist", "todolists", "message", "messages",
		"comment", "comments", "card", "cards",
		"card-table", "card_table", "cardtable", "card_tables",
		"document", "documents", "recording", "recordings",
		"schedule-entry", "schedule_entry", "schedule_entries",
		"checkin", "check-in", "check_in", "questions", "question_answers",
		"forward", "forwards", "inbox_forwards", "upload", "uploads",
		"vault", "vaults", "chat", "chats", "campfire", "campfires",
		"line", "lines", "replies", "columns", "steps",
		"todosets", "message_boards", "schedules", "questionnaires", "inboxes",
		"people", "boosts":
		return true
	default:
		return false
	}
}

func recordingCommentsCount(data map[string]any) (int, bool) {
	count, ok := data["comments_count"]
	if !ok {
		count, ok = data["comment_count"]
		if !ok {
			return 0, false
		}
	}

	switch v := count.(type) {
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n), true
		}
		if n, err := v.Float64(); err == nil {
			return int(n), true
		}
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	}

	return 0, false
}

func pluralizeComments(count int) string {
	if count == 1 {
		return "1 comment"
	}
	return fmt.Sprintf("%d comments", count)
}

func commentsTruncationNotice(count, total int) string {
	if total <= 0 || count >= total {
		return ""
	}
	return fmt.Sprintf("Showing %d of %d comments — use --all-comments for the full discussion", count, total)
}

func joinShowNotices(parts ...string) string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, "; ")
}

func commentsFetchFailedNotice(count int, id string) string {
	return fmt.Sprintf("%s available, but fetching them failed — view: basecamp comments list --all %s", pluralizeComments(count), id)
}
