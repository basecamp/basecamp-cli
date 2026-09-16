package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/mcp/catalog"
	"github.com/basecamp/mcp/gateway"
)

// The event feed's two poll lanes are the one place this dispatcher does not
// go through the raw account client.
//
// Everywhere else a failed call becomes its message and nothing more, which is
// all the raw path can offer: the SDK's canonical *Error keeps the message,
// the hint and a validation body's field errors, and drops every other member
// the server wrote. For the feed that would throw away the contract. Its
// documented refusals are recovery instructions — a 410 names the epoch to
// re-enter after, or the URL to re-enter at; a 409 names the two filter
// digests that disagree — and a consumer that receives them as prose has lost
// the lane.
//
// basecamp-sdk models exactly that, so these operations run through
// EventFeedService, whose typed errors carry the members back out. The wire
// request is still built by buildRequest from the vendored model, so
// parameters, validation and the describe schema stay the model's, as they are
// for every other operation.
//
// The stream-ticket mint is not here because it is not served at all: the
// sync script drops it by policy (POLICY_EXCLUDED_OPERATIONS), since its
// result is a replayable bearer this surface would hand verbatim to a model
// transcript and could not use — opening the stream needs a WebSocket.
//
// The trade is on the success path: a page comes back through the SDK's
// modeled shapes rather than as the bytes BC3 wrote, so a member BC3 starts
// serving before the SDK regenerates would not reach a caller here.
// basecamp-mcp-server, which reads the wire itself, passes those through. The
// two agree the moment the SDK carries the raw body on its errors — which is
// also the day this file goes away.
//
// The refusals are read through the typed errors basecamp-sdk#912 split them
// into, one per lane where the lanes differ: *FeedPositionGoneError is the
// feed's 410 (an epoch and a resume at it), *InboxPositionGoneError is the
// inbox's (a resume at since=0 and no epoch at all), and *FeedRequestError is
// either lane's 400, with a reason. Each has its own arm below. The types
// unwrap to the canonical *basecamp.Error, so a missing arm is not a compile
// error — the refusal would quietly degrade to request_failed and lose the
// member a consumer recovers from. The tests drive each shape through the real
// SDK for exactly that reason.

// Feed operation ids, as basecamp-sdk names them.
const (
	opPollEvents = "PollEvents"
	opPollInbox  = "PollInbox"
)

// Feed error types: the vocabulary basecamp-mcp-server serves for the same
// operations. The type strings, the envelope and the recovery members are the
// same; what differs, and why, is in the PR that introduced this file
// (basecamp-cli#725).
const (
	feedErrInvalidPosition  = "invalid_position"
	feedErrInvalidFilter    = "invalid_filter"
	feedErrBadRequest       = "bad_request"
	feedErrFilterMismatch   = "filter_mismatch"
	feedErrStalePosition    = "stale_position"
	feedErrAgentsOnly       = "agents_only"
	feedErrInvalidArguments = "invalid_arguments"
	feedErrUnexpected       = "request_failed"
)

// The two sentences BC3 renders for its two 400s. A malformed position is
// recovered by re-entering with since=, a bad filter by fixing the filters, and
// the instructions are opposites. BC3 names the case in a `reason` member
// (bc3#13362), which the SDK carries on *FeedRequestError and which is read
// first. These sentences are the fallback for a deploy that predates the
// member, where the SDK's advice is to surface the 400 as undifferentiated —
// the companion server keeps the same fallback, deliberately, so the two
// answer an older deploy alike.
const (
	feedFilterRemedy   = "a position reset won't help"
	feedPositionRemedy = "Resume with since="
)

// feedBodylessForbidden is what the SDK renders a 403 as when the response
// carried no message of its own (helpers.go: msgOrDefault(serverMsg, ...)).
// It is the only trace of a bodyless 403 that survives into the typed error,
// and the inbox's principal guard is the one refusal that arrives that way.
const feedBodylessForbidden = "access denied"

// feedError is the typed in-band error, shaped as basecamp-mcp-server shapes
// it. Data carries the members the refusal offers for recovery.
type feedError struct {
	Type       string          `json:"type"`
	HTTPStatus int             `json:"http_status,omitempty"`
	Retryable  bool            `json:"retryable"`
	RetryAfter int             `json:"retry_after,omitempty"`
	RequestID  string          `json:"request_id,omitempty"`
	Message    string          `json:"message,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
}

type feedErrorPayload struct {
	Error feedError `json:"error"`
}

// feedLaneParams are the query parameters each lane hands to the SDK, which is
// every one this file knows how to pass on.
//
// The request is assembled by buildRequest from the vendored model, and the
// SDK's options are typed, so the two can drift: a model sync can declare a
// filter before this file learns to copy it across. A parameter the model
// accepts and this file drops is the widest fail-open on the lane — the caller
// asks to narrow and is served the unfiltered feed, and describe even told them
// the filter existed. So a parameter outside this table is refused, and
// TestFeedLaneParamsMatchTheModel fails the build the day the model and this
// table disagree.
var feedLaneParams = map[string][]string{
	opPollEvents: {"since", "position", "types", "buckets", "creators", "performers", "exclude_performers", "actor_types"},
	opPollInbox:  {"since", "position", "reasons", "types", "buckets"},
}

// isFeedOperation reports whether the operation runs through EventFeedService.
func isFeedOperation(id string) bool {
	switch id {
	case opPollEvents, opPollInbox:
		return true
	}
	return false
}

// dispatchFeed runs one feed operation. The path and query come from
// buildRequest, so the caller's parameters were checked against the same model
// schema describe advertises; only the answer is handled differently.
func (d dispatcher) dispatchFeed(ctx context.Context, op *catalog.Operation, params map[string]any) *mcp.CallToolResult {
	// An unknown parameter or a non-scalar value is the caller's mistake in
	// the same sense a filter that narrows nothing is, so it carries the same
	// type. The describe schema makes these rare; rare is not a reason to be
	// the one refusal on this path that arrives untyped.
	path, _, err := buildRequest(op, params)
	if err != nil {
		return feedUsageResult("%v", err)
	}
	// Not the caller's doing: the path was assembled here.
	query, err := feedQuery(path)
	if err != nil {
		return gateway.ErrorResult("internal error: %v", err)
	}

	// Filters are read through a reader that refuses one which normalizes to
	// nothing, because on the wire that means unfiltered — the whole account
	// feed, handed back as if it were the answer to the question that was
	// asked. A malformed filter must narrow to a refusal, never widen to
	// everything.
	filters := feedFilters{params: params, query: query}

	for name := range query {
		if !slices.Contains(feedLaneParams[op.ID], name) {
			return feedUsageResult("%s is not yet passed to the event feed by this server; it is refused rather than dropped, "+
				"because a dropped filter would serve the lane unfiltered", name)
		}
	}

	// An entry point that is named and empty is refused for the same reason a
	// filter is. The SDK reads an empty since or position as no entry point,
	// and no entry point means the present: a consumer whose stored position
	// came back empty would silently skip every event it had not yet read.
	for _, name := range []string{"since", "position"} {
		if _, named := params[name]; named && query.Get(name) == "" {
			return feedUsageResult("%s was passed empty, which the feed reads as entering at the present and skipping history; "+
				"omit it to enter at the present on purpose", name)
		}
	}

	if since, position := query.Get("since"), query.Get("position"); since != "" && position != "" {
		return feedUsageResult("since and position are two ways to enter the lane; pass one, not both")
	}

	service := d.api.EventFeed()
	var result any
	var callErr error
	switch op.ID {
	case opPollEvents:
		options := &basecamp.PollEventsOptions{
			Since:    query.Get("since"),
			Position: query.Get("position"),
		}
		if err := filters.all(
			filters.strings("types", &options.Types),
			filters.ids("buckets", &options.Buckets),
			filters.ids("creators", &options.Creators),
			filters.strings("performers", &options.Performers),
			filters.strings("exclude_performers", &options.ExcludePerformers),
			filters.strings("actor_types", &options.ActorTypes),
		); err != nil {
			return feedUsageResult("%v", err)
		}
		result, callErr = service.PollEvents(ctx, options)
	case opPollInbox:
		options := &basecamp.PollInboxOptions{
			Since:    query.Get("since"),
			Position: query.Get("position"),
		}
		if err := filters.all(
			filters.strings("reasons", &options.Reasons),
			filters.strings("types", &options.Types),
			filters.ids("buckets", &options.Buckets),
		); err != nil {
			return feedUsageResult("%v", err)
		}
		result, callErr = service.PollInbox(ctx, options)
	default:
		return gateway.ErrorResult("internal error: %q is not an event feed operation", op.ID)
	}
	if callErr != nil {
		return feedErrorResult(op.ID, callErr)
	}

	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return gateway.ErrorResult("internal error: encode result")
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}}
}

// feedQuery reads back the query buildRequest assembled, so the filters go out
// exactly as the model says to spell them.
func feedQuery(path string) (url.Values, error) {
	parsed, err := url.Parse(path)
	if err != nil {
		return nil, fmt.Errorf("built an unparsable feed path: %w", err)
	}
	return parsed.Query(), nil
}

// feedFilters reads the filter dimensions off one request.
//
// It exists because the obvious reading is a fail-open. A filter the caller
// named but which arrives as nothing usable — "," or ",," or an empty string —
// would, if its components were simply dropped, leave the dimension unset, and
// an unset dimension on the wire means unfiltered. The caller asked to narrow
// and would be served the whole account feed, with nothing in the answer to
// say the filter had been thrown away. So a filter that is present and
// normalizes to nothing is refused, and one that is merely partly malformed
// keeps its components verbatim for BC3 to refuse by name.
type feedFilters struct {
	params map[string]any
	query  url.Values
}

// all returns the first error among the readers, so one malformed filter is
// reported rather than the last one.
func (f feedFilters) all(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// strings reads a list-of-strings filter into dst.
func (f feedFilters) strings(name string, dst *[]string) error {
	values, err := f.values(name)
	if err != nil {
		return err
	}
	*dst = values
	return nil
}

// ids reads a list-of-ids filter into dst. Every component must be a decimal
// id, empty ones included: the SDK's options are typed, so a component that is
// not a number has nowhere to go, and dropping it would narrow the filter
// silently — the mirror of the widening this type exists to prevent.
func (f feedFilters) ids(name string, dst *[]int64) error {
	values, err := f.values(name)
	if err != nil {
		return err
	}
	if values == nil {
		return nil
	}
	ids := make([]int64, 0, len(values))
	for _, value := range values {
		id, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil {
			return fmt.Errorf("%s takes decimal ids; got %q", name, value)
		}
		ids = append(ids, id)
	}
	*dst = ids
	return nil
}

// values splits one filter, or reports that the caller named it and gave it
// nothing to narrow by. A dimension the caller did not name at all is nil,
// which is the only way this returns an unset filter.
func (f feedFilters) values(name string) ([]string, error) {
	if _, named := f.params[name]; !named {
		return nil, nil
	}
	parts := feedSplit(f.query.Get(name))
	usable := 0
	for _, part := range parts {
		if part != "" {
			usable++
		}
	}
	if usable == 0 {
		return nil, fmt.Errorf("%s had no usable values; omit it rather than passing an empty filter, which means unfiltered", name)
	}
	return parts, nil
}

// feedSplit undoes the comma joining the wire uses, since the SDK's options
// take the values apart and rejoin them itself. Surrounding whitespace goes;
// an empty component stays, because dropping it is how "," turns into no
// filter at all.
func feedSplit(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		out = append(out, strings.TrimSpace(part))
	}
	return out
}

// feedErrorResult turns a feed failure into the typed in-band error.
//
// A type is claimed only on evidence — both digests on a 409, a resume (and,
// on the feed, an epoch) on a 410, a reason or a remedy on a 400 — because the
// type is a promise about how to recover, and a refusal that offers no
// recovery must not be dressed as one. What cannot be recognized keeps its
// status and says no more than that.
func feedErrorResult(operation string, err error) *mcp.CallToolResult {
	// The SDK types a 409 on either digest; the claim needs both. The two
	// digests are a comparison, and half of one does not say which side moved
	// — while a digest written as "" is a value a consumer compares against
	// and finds unequal. With one digest this is a refusal we cannot explain,
	// so it is served as one.
	var mismatch *basecamp.FeedFilterMismatchError
	if errors.As(err, &mismatch) && mismatch.PositionDigest != "" && mismatch.FiltersDigest != "" {
		return feedErrorPayloadResult(feedDetail(mismatch.Err, feedErrFilterMismatch, map[string]any{
			"position_digest": mismatch.PositionDigest,
			"filters_digest":  mismatch.FiltersDigest,
		}))
	}

	// The two 410s. The lanes fence on different things, so the SDK gives
	// each its own type and neither can stand in for the other: the feed
	// names the epoch it re-enters at, the inbox fences on a 30-day retention
	// window, has no epoch at all, and re-enters at the earliest item it still
	// holds. Both are checked against the lane that answered as well, so a
	// type arriving on the wrong lane is not believed.
	var feedGone *basecamp.FeedPositionGoneError
	if operation == opPollEvents && errors.As(err, &feedGone) {
		return feedErrorPayloadResult(feedDetail(feedGone.Err, feedErrStalePosition, map[string]any{
			"resume":         feedGone.Resume,
			"epoch_after_id": feedGone.EpochAfterID,
		}))
	}
	var inboxGone *basecamp.InboxPositionGoneError
	if operation == opPollInbox && errors.As(err, &inboxGone) {
		return feedErrorPayloadResult(feedDetail(inboxGone.Err, feedErrStalePosition, map[string]any{
			"resume": inboxGone.Resume,
		}))
	}

	// The 400, with its reason when the server gave one.
	var request *basecamp.FeedRequestError
	if errors.As(err, &request) {
		return feedErrorPayloadResult(feedDetail(request.Err, feedRequestErrorType(request), nil))
	}

	var apiErr *basecamp.Error
	if !errors.As(err, &apiErr) {
		// Transport, cancellation, a credential that could not be obtained:
		// not the feed answering, and nothing to recover from in band.
		return gateway.ErrorResult("%v", err)
	}

	detail := feedDetail(apiErr, feedErrorType(operation, apiErr), nil)
	if detail.Type == feedErrUnexpected {
		// Repeating a request the server refused on its merits cannot repair
		// it; a rate limit is worth another attempt after the wait it named.
		detail.Retryable = apiErr.Retryable
	}
	if detail.Type == feedErrAgentsOnly {
		// The message this replaces is the SDK's stand-in for a body that was
		// not there. "access denied" says nothing a caller can act on; the
		// guard this claim names does.
		detail.Message = "the event inbox is served to agent principals only"
	}
	return feedErrorPayloadResult(detail)
}

// feedDetail is the envelope every feed refusal shares: the canonical error's
// status, request id, wait and message, under the type claimed for it, with
// the recovery members it carries.
func feedDetail(base *basecamp.Error, errType string, data map[string]any) feedError {
	detail := feedError{
		Type:       errType,
		HTTPStatus: base.HTTPStatus,
		RequestID:  base.RequestID,
		RetryAfter: base.RetryAfter,
		Message:    base.Message,
	}
	if data != nil {
		detail.Data = feedData(data)
	}
	return detail
}

// feedRequestErrorType names a 400 from its reason, or from the remedy
// sentence when the deploy predates the reason, or declines to.
//
// An unrecognized reason is not guessed at through the sentence: the server
// answered, in a vocabulary this code does not yet know, and reading the prose
// over the top of that would be claiming to understand it better.
func feedRequestErrorType(request *basecamp.FeedRequestError) string {
	switch request.Reason {
	case basecamp.FeedReasonInvalidPosition:
		return feedErrInvalidPosition
	case basecamp.FeedReasonInvalidFilter:
		return feedErrInvalidFilter
	case "":
		return feedRemedyErrorType(request.Err.Message)
	}
	return feedErrBadRequest
}

// feedRemedyErrorType is the pre-reason reading of a 400: the one sentence
// that tells its two cases apart.
func feedRemedyErrorType(message string) string {
	switch {
	case strings.Contains(message, feedFilterRemedy):
		return feedErrInvalidFilter
	case strings.Contains(message, feedPositionRemedy):
		return feedErrInvalidPosition
	}
	return feedErrBadRequest
}

// feedErrorType names a refusal the SDK did not type, or declines to.
func feedErrorType(operation string, err *basecamp.Error) string {
	switch err.HTTPStatus {
	case 400:
		// A 400 whose body the SDK could not read as the feed's own: no error
		// member at all. The message here is the SDK's placeholder or some
		// other member it fell back to, not BC3's feed refusal, so no remedy
		// is read out of it — that would be claiming a recovery from text the
		// feed did not write.
		return feedErrBadRequest
	case 403:
		// The inbox is agents-only for now and refuses everyone else with a
		// bodyless 403. Naming it keeps a caller from retrying a principal
		// that will never be admitted — but only the bodyless one may be
		// claimed: a 403 BC3 gave a reason for came from scope or access, and
		// those are fixable, so typing them agents_only would tell a caller
		// to stop trying about a condition it could repair.
		//
		// The evidence available here is thinner than the companion server's.
		// That one reads the wire and tests the body for emptiness directly;
		// by the time a refusal reaches this file the SDK has already
		// substituted its own text for an absent message, so a bodyless 403
		// and one whose body said exactly "access denied" are identical. This
		// claims the narrower thing it can actually see, and BC3 sending that
		// precise phrase is the one case it would still get wrong.
		if operation == opPollInbox && err.Message == feedBodylessForbidden {
			return feedErrAgentsOnly
		}
	}
	return feedErrUnexpected
}

// feedUsageResult is a refusal this server made on the caller's arguments,
// served in the same typed shape as the ones BC3 makes.
//
// These are the refusals a caller hits most — a filter that narrows nothing,
// both entry points at once — and serving them as bare prose while every wire
// refusal carries a type would put the untyped answer on the common path and
// make "one contract, two servers" true only of the rare cases. The companion
// server answers the identical conditions as invalid_arguments; so does this.
func feedUsageResult(format string, args ...any) *mcp.CallToolResult {
	return feedErrorPayloadResult(feedError{
		Type:    feedErrInvalidArguments,
		Message: fmt.Sprintf(format, args...),
	})
}

func feedData(members map[string]any) json.RawMessage {
	encoded, err := json.Marshal(members)
	if err != nil {
		return nil
	}
	return encoded
}

func feedErrorPayloadResult(detail feedError) *mcp.CallToolResult {
	payload := feedErrorPayload{Error: detail}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return gateway.ErrorResult("internal error: encode event feed error")
	}
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
	}
}
