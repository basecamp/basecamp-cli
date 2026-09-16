package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/basecamp/mcp/catalog"
	"github.com/basecamp/mcp/gateway"
)

// Composite actions: the basecamp-sdk helpers that compose several typed
// reads into one answer, served here as catalog actions even though no
// single Basecamp endpoint backs them.
//
// Everything else in this catalog is a model operation — one row of
// basecamp-sdk's OpenAPI export, dispatched as one HTTP request by
// buildRequest. These two are not, and the catalog says so: their
// operations carry no method and no path, and their summaries open with
// "Synthetic:". They exist because every account event feed row is ids
// only — no title, body, URL or names — so a worker that wants to reason
// about the recording a row points at must refetch it, and the refetch is
// type-dependent. Implementing that once in the SDK and wiring it into both
// MCP servers is what keeps the two servers answering the same way.
//
//   - recordings.summarize resolves a feed pointer (bucket id, recording
//     id, and the event or recording type) into the compact projection,
//     through RecordingsService.Summarize.
//   - messages.create_comment grows a mentions parameter, expanded into
//     bc-attachment markup through CommentsService.ExpandMentions before
//     the model operation posts the comment.
//
// Naming note: the spec writes the first as recordings.describe, which this
// catalog cannot serve — "describe" is the gateway's own reserved action,
// the one that hands back per-action schemas, and gateway.New refuses a
// domain that registers an operation under that name. "summarize" is the
// SDK helper's own name, so the action, the helper and the projection type
// (RecordingSummary) all agree.
const (
	recordingsDomainKey  = "recordings"
	recordingsToolName   = "basecamp_recordings"
	summarizeAction      = "summarize"
	messagesDomainKey    = "messages"
	createCommentAction  = "create_comment"
	mentionsParam        = "mentions"
	syntheticSummaryTag  = "Synthetic: "
	recordingsDomainTag  = "Recordings"
	recordingsDomainDesc = "Recordings: the compact projection of one recording, resolved from the pointer an account event feed row carries."
)

// maxExactJSONInteger is the largest integer a JSON number carries without
// loss. Ids arrive as float64, so anything past this is refused rather than
// silently rounded into a different recording or a different person.
const maxExactJSONInteger = 1<<53 - 1

// compositeHandler serves one composite action from the SDK client.
type compositeHandler func(ctx context.Context, api API, params map[string]any) (*mcp.CallToolResult, error)

// compositeHandlers is keyed "<domain>.<action>", the same pair the
// dispatcher holds when a call arrives.
var compositeHandlers = map[string]compositeHandler{
	recordingsDomainKey + "." + summarizeAction: handleSummarize,
}

// recordingsDomain is the hand-built domain carrying the composite reads.
// It is appended to the loaded catalog, so describe, the action enum, the
// read-only filter and the catalog snapshot all pick it up the way they do
// a model domain.
func recordingsDomain() *catalog.Domain {
	return &catalog.Domain{
		Key:   recordingsDomainKey,
		Tool:  recordingsToolName,
		Blurb: recordingsDomainDesc,
		Operations: []*catalog.Operation{
			{
				ID:      "SummarizeRecording",
				Action:  summarizeAction,
				Tag:     recordingsDomainTag,
				Summary: syntheticSummaryTag + "resolve a recording pointer into a compact projection.",
				Doc: "Composed in basecamp-sdk from one typed read per recording type — no single Basecamp endpoint serves it, so this action carries no method and no path.\n\n" +
					"Takes the pointer an account event feed row carries: bucket_id, recording_id, and either event_type (\"comment.created\", \"card.assignment_changed\", \"chat.line.created\") or recording_type (\"Comment\", \"Kanban::Card\", \"Chat::Lines::Text\"). recording_type wins when both are given.\n\n" +
					"Returns type, status, title, app URL, parent, bucket, creator, assignees, the person ids the content mentions, the content in full, and updated_at. A chat line also carries the campfire_id it was found under, which is the reply destination for a chat trigger: the pointer does not carry it, so it is discovered from the bucket's project dock first and the account-wide Campfire listing second.\n\n" +
					"Failures keep their shapes apart. A read that answered 401, 403 or 5xx is that read's own error. \"campfire_discovery_incomplete\" means candidates were left unsearched, so nothing can be reported absent. \"recording_unresolved\" means every visible Campfire answered 404 — block and retry rather than treat it as an outage.",
				ReadOnly:     true,
				Idempotent:   true,
				BodyRequired: true,
				Body: map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []any{"bucket_id", "recording_id"},
					"properties": map[string]any{
						"bucket_id": map[string]any{
							"type":        "integer",
							"description": "The project the recording lives in. The read is checked against it, so a pointer from one project can never resolve to a recording in another.",
						},
						"recording_id": map[string]any{
							"type":        "integer",
							"description": "The recording's id.",
						},
						"event_type": map[string]any{
							"type":        "string",
							"description": "The account event feed type that named the recording, e.g. \"comment.created\". The segment before the action names the recording type. Used when recording_type is absent.",
						},
						"recording_type": map[string]any{
							"type":        "string",
							"description": "The recording's own type as Basecamp spells it, e.g. \"Kanban::Card\". Takes precedence over event_type, being the more exact of the two.",
						},
					},
				},
			},
		},
	}
}

// installComposites adds the composite surface to a loaded catalog: the
// recordings domain, and the mentions parameter on messages.create_comment.
// It runs after rescopeToAccount, which walks model paths the composite
// operations deliberately do not have.
func installComposites(cat *catalog.Catalog) error {
	for _, d := range cat.Domains {
		if d.Key == recordingsDomainKey {
			return fmt.Errorf("the model already serves a %q domain; the composite domain would shadow it", recordingsDomainKey)
		}
	}
	cat.Domains = append(cat.Domains, recordingsDomain())
	return installMentionsParam(cat)
}

// installMentionsParam declares the mentions parameter on
// messages.create_comment. The body schema is stamped strict by the
// toolkit, and the dispatcher refuses parameters an operation does not
// declare, so without this the parameter would be unreachable — and with it
// the parameter is advertised by describe like any other.
//
// The parameter never reaches Basecamp: the dispatcher consumes it, reads
// each person for the attachable_sgid only Basecamp can vouch for, and
// folds the mentions into content before the model operation posts it.
func installMentionsParam(cat *catalog.Catalog) error {
	op, err := findOperation(cat, messagesDomainKey, createCommentAction)
	if err != nil {
		return err
	}
	properties, ok := op.Body["properties"].(map[string]any)
	if !ok {
		return fmt.Errorf("operation %q has no body properties to add %q to", op.ID, mentionsParam)
	}
	if _, taken := properties[mentionsParam]; taken {
		return fmt.Errorf("operation %q already declares %q", op.ID, mentionsParam)
	}
	properties[mentionsParam] = map[string]any{
		"type":        "array",
		"items":       map[string]any{"type": "integer"},
		"description": syntheticSummaryTag + "person ids to mention. Expanded client-side into the bc-attachment markup Basecamp reads as a mention, and prepended to content. Every id is read for its attachable_sgid before anything is posted, so an id that is not a person in this account fails the call and posts nothing.",
	}
	op.Summary += " Accepts a synthetic mentions parameter."
	return nil
}

func findOperation(cat *catalog.Catalog, domainKey, action string) (*catalog.Operation, error) {
	for _, d := range cat.Domains {
		if d.Key != domainKey {
			continue
		}
		for _, op := range d.Operations {
			if op.Action == action {
				return op, nil
			}
		}
		return nil, fmt.Errorf("domain %q serves no %q action", domainKey, action)
	}
	return nil, fmt.Errorf("catalog serves no %q domain", domainKey)
}

// handleSummarize serves recordings.summarize.
func handleSummarize(ctx context.Context, api API, params map[string]any) (*mcp.CallToolResult, error) {
	ref, err := summarizeRef(params)
	if err != nil {
		return gateway.ErrorResult("%v", err), nil
	}
	summary, err := api.Recordings().Summarize(ctx, ref)
	if err != nil {
		return summarizeFailure(ref, err), nil
	}
	return gateway.JSONResult(summary)
}

func summarizeRef(params map[string]any) (basecamp.RecordingRef, error) {
	var ref basecamp.RecordingRef
	known := map[string]bool{"bucket_id": true, "recording_id": true, "event_type": true, "recording_type": true}
	for name := range params {
		if !known[name] {
			return ref, fmt.Errorf("unknown parameter %q for action %q (describe the action for its schema)", name, summarizeAction)
		}
	}

	var err error
	if ref.BucketID, err = requiredID(params, "bucket_id"); err != nil {
		return ref, err
	}
	if ref.RecordingID, err = requiredID(params, "recording_id"); err != nil {
		return ref, err
	}
	if ref.EventType, err = optionalString(params, "event_type"); err != nil {
		return ref, err
	}
	if ref.RecordingType, err = optionalString(params, "recording_type"); err != nil {
		return ref, err
	}
	if strings.TrimSpace(ref.EventType) == "" && strings.TrimSpace(ref.RecordingType) == "" {
		return ref, fmt.Errorf("action %q needs event_type or recording_type: the recording's type is what names the read", summarizeAction)
	}
	return ref, nil
}

// summarizeFailure renders the composite's own verdicts as in-band errors
// that name the identity, not a coarse code.
//
// The identities are the vocabulary basecamp-sdk's conformance fixture
// pins (conformance/tests/recording_summary.json), so every SDK spells them
// the same way and a caller can branch on them. The coarse error code each
// SDK derives from them is being settled separately; this surface therefore
// reports the identity and the message and names no code, so it cannot
// disagree with whatever that settles on.
func summarizeFailure(ref basecamp.RecordingRef, err error) *mcp.CallToolResult {
	payload := map[string]any{"message": err.Error()}

	var unresolved *basecamp.UnresolvedRecordingError
	var incomplete *basecamp.CampfireDiscoveryIncompleteError
	var mismatch *basecamp.BucketMismatchError

	switch {
	case errors.As(err, &unresolved):
		payload["type"] = "recording_unresolved"
		payload["campfire_ids"] = unresolved.CampfireIDs
		payload["refreshed"] = unresolved.Refreshed
		if len(unresolved.StaleCampfireIDs) > 0 {
			payload["stale_campfire_ids"] = unresolved.StaleCampfireIDs
		}
	case errors.As(err, &incomplete):
		payload["type"] = "campfire_discovery_incomplete"
		payload["reason"] = incomplete.Reason
	case errors.As(err, &mismatch):
		payload["type"] = "bucket_mismatch"
		payload["found_in_bucket_id"] = mismatch.BucketID
	case errors.Is(err, basecamp.ErrNoRecordingType):
		payload["type"] = "no_recording_type"
	case errors.Is(err, basecamp.ErrUnknownRecordingType):
		payload["type"] = "unknown_recording_type"
	default:
		// One constituent read's own answer — a 401, a 403, a 404, a 5xx,
		// a transport failure. It is returned as itself so it is never
		// confused with a verdict the composite reached.
		return gateway.ErrorResult("%v", err)
	}

	payload["bucket_id"] = ref.BucketID
	payload["recording_id"] = ref.RecordingID

	result, marshalErr := gateway.JSONResult(map[string]any{"error": payload})
	if marshalErr != nil || result == nil {
		return gateway.ErrorResult("%v", err)
	}
	result.IsError = true
	return result
}

// expandMentions consumes the synthetic mentions parameter before the model
// operation is dispatched, folding the people it names into content. The
// person reads happen before the write, so a lookup that fails posts
// nothing.
func expandMentions(ctx context.Context, api API, domainKey, action string, params map[string]any) error {
	if domainKey != messagesDomainKey || action != createCommentAction {
		return nil
	}
	raw, present := params[mentionsParam]
	if !present {
		return nil
	}
	delete(params, mentionsParam)

	ids, err := personIDs(raw)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}

	content, err := optionalString(params, "content")
	if err != nil {
		return err
	}
	expanded, err := api.Comments().ExpandMentions(ctx, content, ids)
	if err != nil {
		return err
	}
	params["content"] = expanded
	return nil
}

func personIDs(raw any) ([]int64, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("parameter %q must be an array of person ids, got %T", mentionsParam, raw)
	}
	ids := make([]int64, 0, len(items))
	for i, item := range items {
		id, err := exactID(item)
		if err != nil {
			return nil, fmt.Errorf("parameter %q[%d]: %w", mentionsParam, i, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func requiredID(params map[string]any, name string) (int64, error) {
	raw, ok := params[name]
	if !ok {
		return 0, fmt.Errorf("missing required parameter %q (describe the action for its schema)", name)
	}
	id, err := exactID(raw)
	if err != nil {
		return 0, fmt.Errorf("parameter %q: %w", name, err)
	}
	return id, nil
}

// exactID reads an id off a JSON value without rounding it. A JSON number
// arrives as float64: a value with a fraction, or past the exact-integer
// range, would name a different record than the caller wrote, so it is
// refused rather than truncated.
func exactID(raw any) (int64, error) {
	switch v := raw.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) {
			return 0, fmt.Errorf("must be a whole number, got %v", v)
		}
		if v > maxExactJSONInteger || v < -maxExactJSONInteger {
			return 0, fmt.Errorf("is past the range a JSON number carries exactly (%d)", int64(maxExactJSONInteger))
		}
		return int64(v), nil
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case string:
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("must be a whole number, got %q", v)
		}
		return id, nil
	default:
		return 0, fmt.Errorf("must be a whole number, got %T", raw)
	}
}

func optionalString(params map[string]any, name string) (string, error) {
	raw, ok := params[name]
	if !ok || raw == nil {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("parameter %q must be a string, got %T", name, raw)
	}
	return value, nil
}
