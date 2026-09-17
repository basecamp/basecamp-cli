package acp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"slices"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// Who may decide a permission, and on what evidence
//
// The connector's policy decides; the agent's request is evidence only of
// what the agent asked for. Every session/request_permission is answered
// here, in onRequest, and nowhere else.
//
// A request reaches the policy only when all of this holds: it names this
// session's own id, it was read inside a turn that has not been answered
// (the claim taken on the reading goroutine, not whatever turn is in flight
// when this goroutine runs), the asking mode is confirmed, the session is
// neither unsafe nor closed, and fewer than maxDecisions are already at the
// policy. Anything else is refused without a decision — and a refusal is
// this driver's own record, written by record, never read back from the
// agent's stop reason.
//
// What of the request is trusted:
//
//   - sessionId, compared against the id the agent itself gave at
//     session/new. It routes nothing; it is a guard.
//   - options[].kind, matched against ACP's kinds. An option id is carried
//     back to the agent as an opaque value and is never what selects.
//   - toolCall.toolCallId, as an opaque key for the call, cut to
//     maxToolCallID wherever it is kept or shown (the session's tool calls,
//     an update, a refusal) and digested where once-ness is decided.
//
// What is not, because an adapter can write anything: the option ids and
// labels (so the answer is chosen by kind — allow_once, never allow_always,
// so no answer outlives its request), the call's title and raw input (never
// decoded into anything kept), and the tool's name, which is taken only
// where the adapter's own marking, title and input agree (toolName) and only
// in a form the policy can key on (plainName). The locations are the
// agent's, and are kept against the call — and so decide a later request —
// only for a request the session could be asked at all.
//
// The policy may take its time, so the conditions are rechecked before an
// allow is sent: a session canceled, ended or found unsafe while it decided
// allows nothing more.
// onRequest answers the agent's requests. The client offers no fs and no
// terminal, so a permission is the only request it serves.
func (s *session) onRequest(id json.RawMessage, method string, params json.RawMessage, claimed any) {
	if method != "session/request_permission" {
		s.conn.replyError(id, codeMethodNotFound, "method not supported by this client")
		return
	}
	defer func() {
		s.mu.Lock()
		s.deciding--
		s.mu.Unlock()
	}()
	// The turn the request was read in, not whatever turn is in flight by
	// the time this goroutine runs.
	t, _ := claimed.(*turn)
	var p struct {
		SessionID string          `json:"sessionId"`
		ToolCall  json.RawMessage `json:"toolCall"`
		Options   []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		// Unreadable, so nothing is allowed — which is a refusal this driver
		// made, and it is recorded like any other.
		s.record(driver.PermissionRequest{Kind: driver.ToolOther}, t)
		s.emit(driver.Update{Kind: driver.UpdatePermission, ToolKind: driver.ToolOther})
		s.conn.replyError(id, codeInvalidParams, "unreadable permission request")
		return
	}
	call, _ := decodeUpdate(p.ToolCall)

	select {
	case s.decisions <- struct{}{}:
		defer func() { <-s.decisions }()
	default:
		// More at once than a session has any business asking: refused
		// without a decision, and recorded as the refusal it is.
		s.refuse(id, driver.PermissionRequest{ToolCallID: call.ToolCallID, Tool: toolName(call), Kind: toolKind(call.Kind)}, t)
		return
	}

	s.mu.Lock()
	// A turn the agent has already answered asks nothing more.
	askable := t != nil && s.turn == t && !t.settling && s.verified && s.unsafe == nil && !s.closed && s.id != "" && p.SessionID == s.id
	canceled := t != nil && t.canceled
	s.mu.Unlock()

	// Only a request the session can be asked is merged into what it knows
	// of its tool calls: one for another session, or outside a turn, could
	// otherwise name a call that a later request is decided on.
	info := toolInfo{name: toolName(call), kind: toolKind(call.Kind), locations: call.Locations}
	if askable {
		info = s.noteTool(call)
	}
	req := driver.PermissionRequest{
		ToolCallID: call.ToolCallID,
		Tool:       info.name,
		Kind:       info.kind,
		Locations:  slices.Clone(info.locations),
	}
	for _, o := range p.Options {
		req.Options = append(req.Options, driver.PermissionOption{ID: o.OptionID, Kind: driver.PermissionOptionKind(o.Kind)})
	}

	if canceled {
		// A turn being canceled answers its open requests as canceled, as
		// ACP asks of a client. It is still a call this session did not
		// allow, so it is recorded as one.
		s.refuse(id, req, t)
		return
	}
	allow := askable && s.policy.Decide(context.Background(), req).Allow
	if allow {
		// The policy took its time; the session may have been canceled or
		// found unsafe while it did, and neither allows anything more.
		s.mu.Lock()
		allow = s.turn == t && !t.settling && !t.canceled && s.unsafe == nil && !s.closed
		s.mu.Unlock()
	}
	option := chooseOption(req.Options, allow)
	if allow && option == "" {
		// Allowing is only ever allow_once; without it, the answer is no.
		allow = false
		option = chooseOption(req.Options, false)
	}
	if !allow {
		s.record(req, t)
	}
	s.emit(driver.Update{Kind: driver.UpdatePermission, ToolCallID: req.ToolCallID, Tool: req.Tool, ToolKind: req.Kind, Allowed: allow})
	if option == "" {
		s.conn.reply(id, map[string]any{"outcome": map[string]any{"outcome": outcomeCanceled}})
		return
	}
	s.conn.reply(id, map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": option}})
}

// outcomeCanceled is ACP's permission outcome for a request not answered by
// an option.
const outcomeCanceled = "cancelled" //nolint:misspell // ACP's wire value

// onBusy records a permission request refused at the connection's handler
// bound as the refusal it is.
func (s *session) onBusy(method string, params json.RawMessage) {
	if method != "session/request_permission" {
		return
	}
	var p struct {
		ToolCall json.RawMessage `json:"toolCall"`
	}
	_ = json.Unmarshal(params, &p)
	call, _ := decodeUpdate(p.ToolCall)
	req := driver.PermissionRequest{ToolCallID: call.ToolCallID, Tool: toolName(call), Kind: toolKind(call.Kind)}
	s.record(req, nil)
	s.emit(driver.Update{Kind: driver.UpdatePermission, ToolCallID: req.ToolCallID, Tool: req.Tool, ToolKind: req.Kind})
}

// refuse answers a request the session will not put to the policy at all,
// with no option of the agent's, and records it as the refusal it is.
func (s *session) refuse(id json.RawMessage, req driver.PermissionRequest, t *turn) {
	s.record(req, t)
	s.emit(driver.Update{Kind: driver.UpdatePermission, ToolCallID: req.ToolCallID, Tool: req.Tool, ToolKind: req.Kind})
	s.conn.reply(id, map[string]any{"outcome": map[string]any{"outcome": outcomeCanceled}})
}

// record puts a refusal on the turn it belongs to (invariant 4). A turn given
// as nil is looked up: a refusal the session made before it read the turn
// still belongs to the turn in flight.
func (s *session) record(req driver.PermissionRequest, t *turn) {
	id := req.ToolCallID
	if len(id) > maxToolCallID {
		id = id[:maxToolCallID]
	}
	refusal := driver.Refusal{ToolCallID: s.red.Sanitize(id), Tool: s.red.Sanitize(refusalTool(req))}
	// Once-ness is per the id the agent sent, by digest: two ids cut or
	// redacted to the same text are still two calls.
	key := sha256.Sum256([]byte(req.ToolCallID))

	s.mu.Lock()
	// An id the agent did not give cannot be told from another: such a
	// refusal is recorded every time rather than folded into one.
	first := req.ToolCallID == "" || !s.recorded[key]
	if len(s.recorded) < maxRecorded {
		s.recorded[key] = true
	}
	if t == nil {
		t = s.turn
	}
	if t != nil && s.turn == t && len(t.refusals) < maxRefusals && (req.ToolCallID == "" || !t.seen[key]) {
		if t.seen == nil {
			t.seen = map[[sha256.Size]byte]bool{}
		}
		t.seen[key] = true
		t.refusals = append(t.refusals, refusal)
	}
	recorder := s.recorder
	s.mu.Unlock()

	// The ledger, not a session's memory, is where a refusal is kept: a
	// worker that exits before its result, or a turn cut short, ends that
	// memory. Once per tool call id (driver's "Refusals"); the recorder owns
	// what happens when the ledger refuses the write.
	if first && recorder != nil {
		_ = recorder.RecordRefusal(context.Background(), refusal)
	}
}

// chooseOption selects by kind, never by id or label (invariant 3).
func chooseOption(options []driver.PermissionOption, allow bool) string {
	want := []driver.PermissionOptionKind{driver.RejectOnce, driver.RejectAlways}
	if allow {
		want = []driver.PermissionOptionKind{driver.AllowOnce}
	}
	for _, kind := range want {
		for _, o := range options {
			if o.Kind == kind && o.ID != "" {
				return o.ID
			}
		}
	}
	return ""
}

func refusalTool(req driver.PermissionRequest) string {
	if req.Tool != "" {
		return req.Tool
	}
	return string(req.Kind)
}
