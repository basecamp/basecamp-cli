package mcpserver

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/basecamp/mcp/catalog"
	"github.com/basecamp/mcp/gateway"

	"github.com/basecamp/basecamp-cli/internal/connector"
)

// The basecamp_connect domain: how a worker the connector started pulls its
// instruction and reports on it, served from the connector's ledger.
//
// It exists only on a server started for one task — a connector state
// directory and that task's token — and every action is bound to the task
// the token names. There is no listing: a worker never reads other tasks.
// Nothing it returns carries the token, a feed position or a route; the
// instruction is an allowlist of fields (connector.Instruction).
const (
	connectDomainKey   = "connect"
	connectToolName    = "basecamp_connect"
	getDispatchAction  = "get_dispatch"
	ackDispatchAction  = "ack_dispatch"
	completeDispatch   = "complete_dispatch"
	connectDomainBlurb = "Your dispatch from the Basecamp agent connector: pull the instruction you were started for, acknowledge it, and report its outcome. Bound to this task; there is no listing."
)

// Dispatch is the task-bound ledger the connect domain serves.
// *connector.TaskDispatch satisfies it.
type Dispatch interface {
	Get(ctx context.Context, eventID int64) (connector.Instruction, bool, error)
	Ack(ctx context.Context, eventID int64, ackID *int64) (connector.Receipt, error)
	Complete(ctx context.Context, eventID int64, c connector.Completion) (connector.Receipt, error)
}

var _ Dispatch = (*connector.TaskDispatch)(nil)

func connectDomain() *catalog.Domain {
	object := func(required []any, properties map[string]any) map[string]any {
		body := map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
		if len(required) > 0 {
			body["required"] = required
		}
		return body
	}
	eventID := func(description string) map[string]any { return idSchema(description) }
	return &catalog.Domain{
		Key:   connectDomainKey,
		Tool:  connectToolName,
		Blurb: connectDomainBlurb,
		Operations: []*catalog.Operation{
			{
				ID:     "AckDispatch",
				Action: ackDispatchAction,
				Tag:    "Connect",
				Summary: syntheticSummaryTag + "acknowledge an instruction you were handed. Marks it delivered and records your own acknowledgement (the boost or comment you posted). " +
					"Safe to retry: a repeat answers the same receipt.",
				Idempotent:   true,
				BodyRequired: true,
				Body: object([]any{"event_id"}, map[string]any{
					"event_id": eventID("The event get_dispatch returned."),
					"ack_id":   idSchema("The id of the boost or comment you acknowledged with, when you posted one."),
				}),
			},
			{
				ID:     "CompleteDispatch",
				Action: completeDispatch,
				Tag:    "Connect",
				Summary: syntheticSummaryTag + "report the outcome of an instruction: succeeded or failed, with your reply's id and any links (a pull request, a card). Also acknowledges it. " +
					"A repeat of the same report answers the same receipt; a different report is refused, because a reported outcome stands.",
				Idempotent:   true,
				BodyRequired: true,
				Body: object([]any{"event_id", "outcome"}, map[string]any{
					"event_id": eventID("The event get_dispatch returned."),
					"outcome":  map[string]any{"type": "string", "enum": []any{"succeeded", "failed"}},
					"links": map[string]any{
						"type": "array", "maxItems": 20,
						"items":       map[string]any{"type": "string", "maxLength": 2048},
						"description": "http(s) URLs to what the work produced.",
					},
					"reply_id": idSchema("The id of the comment or chat line you replied with."),
				}),
			},
			{
				ID:     "GetDispatch",
				Action: getDispatchAction,
				Tag:    "Connect",
				Summary: syntheticSummaryTag + "the instruction for an event on your task, or the earliest you have not acknowledged. " +
					"Returns the recording, where to reply, who asked, the instruction's content with your own mention removed, whether to acknowledge, and whether the connector already acknowledged for you. " +
					"Calling it records that you were handed the instruction; it does not acknowledge. A repeat returns the same instruction.",
				Idempotent: true,
				Body: object(nil, map[string]any{
					"event_id": eventID("An event on your task. Omit for the earliest not yet acknowledged."),
				}),
			},
		},
	}
}

// connectHandler serves one connect action.
type connectHandler func(ctx context.Context, d Dispatch, params map[string]any) (*mcp.CallToolResult, error)

var connectHandlers = map[string]connectHandler{
	getDispatchAction: handleGetDispatch,
	ackDispatchAction: handleAckDispatch,
	completeDispatch:  handleCompleteDispatch,
}

func (d dispatcher) handleConnect(ctx context.Context, op *catalog.Operation, params map[string]any) (*mcp.CallToolResult, error) {
	handler, ok := connectHandlers[op.Action]
	if !ok || d.connect == nil {
		return gateway.ErrorResult("internal error: connect action %q is not served", op.Action), nil
	}
	known, _ := op.Body["properties"].(map[string]any)
	for name := range params {
		if _, ok := known[name]; !ok {
			return gateway.ErrorResult("unknown parameter %q for action %q (describe the action for its schema)", name, op.Action), nil
		}
	}
	return handler(ctx, d.connect, params)
}

func handleGetDispatch(ctx context.Context, d Dispatch, params map[string]any) (*mcp.CallToolResult, error) {
	var eventID int64
	if _, given := params["event_id"]; given {
		id, err := requiredID(params, "event_id")
		if err != nil {
			return gateway.ErrorResult("%v", err), nil
		}
		eventID = id
	}
	instruction, ok, err := d.Get(ctx, eventID)
	if err != nil {
		return connectFailure(err), nil
	}
	if !ok {
		return gateway.JSONResult(map[string]any{"instruction": nil, "message": "Nothing on this task is waiting to be acknowledged."})
	}
	return gateway.JSONResult(map[string]any{"instruction": instruction})
}

func handleAckDispatch(ctx context.Context, d Dispatch, params map[string]any) (*mcp.CallToolResult, error) {
	eventID, err := requiredID(params, "event_id")
	if err != nil {
		return gateway.ErrorResult("%v", err), nil
	}
	ackID, err := optionalID(params, "ack_id")
	if err != nil {
		return gateway.ErrorResult("%v", err), nil
	}
	receipt, err := d.Ack(ctx, eventID, ackID)
	if err != nil {
		return connectFailure(err), nil
	}
	return gateway.JSONResult(receipt)
}

func handleCompleteDispatch(ctx context.Context, d Dispatch, params map[string]any) (*mcp.CallToolResult, error) {
	eventID, err := requiredID(params, "event_id")
	if err != nil {
		return gateway.ErrorResult("%v", err), nil
	}
	outcome, err := optionalString(params, "outcome")
	if err != nil {
		return gateway.ErrorResult("%v", err), nil
	}
	if outcome == "" {
		return gateway.ErrorResult("missing required parameter %q (describe the action for its schema)", "outcome"), nil
	}
	replyID, err := optionalID(params, "reply_id")
	if err != nil {
		return gateway.ErrorResult("%v", err), nil
	}
	var links []string
	if raw, ok := params["links"]; ok && raw != nil {
		items, ok := raw.([]any)
		if !ok {
			return gateway.ErrorResult("parameter %q must be an array of strings", "links"), nil
		}
		for _, item := range items {
			link, ok := item.(string)
			if !ok {
				return gateway.ErrorResult("parameter %q must be an array of strings", "links"), nil
			}
			links = append(links, link)
		}
	}
	receipt, err := d.Complete(ctx, eventID, connector.Completion{Outcome: connector.Outcome(outcome), Links: links, ReplyID: replyID})
	if err != nil {
		return connectFailure(err), nil
	}
	return gateway.JSONResult(receipt)
}

func optionalID(params map[string]any, name string) (*int64, error) {
	if raw, ok := params[name]; !ok || raw == nil {
		return nil, nil
	}
	id, err := requiredID(params, name)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// connectFailure names a refusal by what it means to the worker. A fault the
// worker cannot act on is reported without the ledger's own detail.
func connectFailure(err error) *mcp.CallToolResult {
	for _, known := range []struct {
		err  error
		kind string
	}{
		{connector.ErrTaskTokenRefused, "task_token_refused"},
		{connector.ErrNotOnTask, "not_on_task"},
		{connector.ErrNotExposed, "not_exposed"},
		{connector.ErrReportConflict, "report_conflict"},
		{connector.ErrNotDispatchable, "not_dispatchable"},
		{connector.ErrInvalidReport, "invalid_report"},
	} {
		if errors.Is(err, known.err) {
			message := known.err.Error()
			if known.err == connector.ErrInvalidReport {
				// What is wrong with the report is the worker's to fix.
				message = strings.TrimPrefix(err.Error(), "connector: ")
			}
			result, encodeErr := gateway.JSONResult(map[string]any{"error": known.kind, "message": message})
			if encodeErr != nil || result == nil {
				return gateway.ErrorResult("%s", known.kind)
			}
			result.IsError = true
			return result
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return gateway.ErrorResult("the call was canceled")
	}
	// Anything else is the ledger failing, not the worker: its detail stays
	// out of the model's transcript.
	return gateway.ErrorResult("the connector ledger could not answer; try again, and report it if it persists")
}
