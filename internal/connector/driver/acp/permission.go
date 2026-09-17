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
// what the agent asked for. onRequest is the only place a permission is
// decided. Two paths answer one without deciding it, and both record the
// refusal they are: a request past the connection's handler bound is
// answered busy (onBusy), and past even the queue of those the session ends,
// which answers every request it had outstanding.
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
// What is not, because an adapter can write anything:
//
//   - The option ids and labels. The answer is chosen by kind — allow_once,
//     never allow_always, so no answer outlives its request — and a list
//     that gives one id to two options selects nothing at all.
//   - The call's title and raw input. Neither is kept, and neither names a
//     tool on its own: they are read only to corroborate codex-acp's MCP
//     calls, which arrive with no name, and only where the adapter's own
//     marking, the title and the input agree. What claude-agent-acp names in
//     _meta or in name is taken as it gives it — the adapter's word for its
//     own tool — and in either case only in a form the policy can key on
//     (plainName), never one made plain by dropping what is not.
//   - The locations. They are the agent's paths, cut to what a path can be,
//     and are kept against the call — and so reach a later request about it —
//     only while the session could be asked about that call at all
//     (mayAskLocked).
//
// The policy may take its time, so the conditions are rechecked before an
// allow is sent: a session canceled, ended or found unsafe while it decided
// allows nothing more.
// mayAskLocked reports whether the session could be asked to decide something
// for turn t right now: t is the turn in flight, the agent has not answered
// it, no history is replaying, the mode is confirmed, and the session is
// neither unsafe nor closed. It is the one condition on which a request is
// put to the policy and the one on which evidence about a tool call is kept,
// so an update the session could not be asked about cannot describe a call
// that a later request is decided on.
func (s *session) mayAskLocked(t *turn) bool {
	return t != nil && s.turn == t && !t.settling && !s.replaying &&
		s.verified && s.unsafe == nil && !s.closed
}

// onRequest answers the agent's requests. The client offers no fs and no
// terminal, so a permission is the only request it serves.
func (s *session) onRequest(id json.RawMessage, method string, params json.RawMessage, claim any) {
	defer s.release(claim)
	if method != "session/request_permission" {
		s.conn.replyError(id, codeMethodNotFound, "method not supported by this client")
		return
	}
	// The turn the request was read in, not whatever turn is in flight by
	// the time this goroutine runs.
	t := turnOf(claim)
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
	askable := s.mayAskLocked(t) && s.id != "" && p.SessionID == s.id
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
func (s *session) onBusy(method string, params json.RawMessage, claim any) {
	if method != "session/request_permission" {
		return
	}
	var p struct {
		ToolCall json.RawMessage `json:"toolCall"`
	}
	_ = json.Unmarshal(params, &p)
	call, _ := decodeUpdate(p.ToolCall)
	req := driver.PermissionRequest{ToolCallID: call.ToolCallID, Tool: toolName(call), Kind: toolKind(call.Kind)}
	// On the turn the request was read in: this refusal is answered off the
	// reading goroutine, so by now a later turn may be in flight.
	s.record(req, turnOf(claim))
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

// chooseOption selects by kind, never by id or label (invariant 3). A list
// that gives one id to two options says nothing about which the agent will
// act on, so nothing is selected from it and the request is answered as
// canceled.
func chooseOption(options []driver.PermissionOption, allow bool) string {
	seen := make(map[string]bool, len(options))
	for _, o := range options {
		if o.ID == "" {
			continue
		}
		if seen[o.ID] {
			return ""
		}
		seen[o.ID] = true
	}
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

// toolInfo is what is known of one tool call.
type toolInfo struct {
	name      string
	kind      driver.ToolKind
	locations []string
}

// noteTool merges what u says about its tool call into what the session
// knows of it, and returns the result. A later message fills in what an
// earlier one left out; it never blanks what was known.
//
// What it keeps is evidence a permission decision may rest on, so it is kept
// only on the condition a request is put to the policy at all: an update read
// outside a turn, or while a load replays a session's history, says what it
// says of itself and leaves nothing behind for a later request to inherit.
func (s *session) noteTool(u sessionUpdate) toolInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.mayAskLocked(s.turn) {
		info := toolInfo{name: toolName(u), kind: toolKind(u.Kind), locations: slices.Clone(u.Locations)}
		if len(info.locations) > maxLocations {
			info.locations = info.locations[:maxLocations]
		}
		if info.kind == "" {
			info.kind = driver.ToolOther
		}
		return info
	}
	info := s.tools[u.ToolCallID]
	if name := toolName(u); name != "" {
		info.name = name
	}
	if u.Kind != "" {
		info.kind = toolKind(u.Kind)
	}
	if info.kind == "" {
		info.kind = driver.ToolOther
	}
	if len(u.Locations) > 0 {
		info.locations = slices.Clone(u.Locations)
		if len(info.locations) > maxLocations {
			info.locations = info.locations[:maxLocations]
		}
	}
	if u.ToolCallID == "" || len(u.ToolCallID) > maxToolCallID {
		return info
	}
	switch toolStatus(u.Status) {
	case driver.ToolCompleted, driver.ToolFailed:
		delete(s.tools, u.ToolCallID)
	default:
		if _, known := s.tools[u.ToolCallID]; known || len(s.tools) < maxTools {
			s.tools[u.ToolCallID] = info
		}
	}
	return info
}
