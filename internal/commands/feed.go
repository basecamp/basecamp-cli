package commands

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// The account event feed: the catch-up poll lane (`events poll`), the agent
// inbox (`inbox`), and stream-ticket minting for the live lane
// (`events ticket`).
//
// Pagination here is the response body, not the Link-header page walk every
// other listing in this CLI uses: a page carries a durable `position` to
// resume from later and a `next` continuation URL while the current walk has
// more to serve. One call is one page; --all walks `next` to the end of the
// current walk, which is not the same as following the feed forever.
//
// The feed is a notification lane, not an audit log. Events are thin
// pointers: deduplicate by event id (inbox items by addressing_id, since one
// event addresses a principal once per reason) and refetch the referenced
// recording before acting on it.

// feedDefaultMaxPages bounds an --all walk. The server serves up to 100
// events per page, so the default admits 5,000 events before the walk reports
// itself capped rather than complete. It is a runaway backstop for a
// since=0 replay, not a tuning knob for ordinary polling: an ordinary poll
// reaches the end of its walk in a handful of pages.
const feedDefaultMaxPages = 50

// feedLane is the per-lane text the shared machinery cannot derive: which
// command to re-run, and how that lane re-enters after its position stops
// being servable. The feed re-enters at the epoch the 410 body names; the
// inbox re-enters at since=0, the earliest item still retained.
type feedLane struct {
	name      string
	pollCmd   string
	sinceHint string
	// continuationFilters reads the filter flags a continuation or resume URL
	// carries, through the lane's own parser: the two lanes have different
	// filter dimensions and their URLs are never interchangeable.
	continuationFilters func(string) ([]flagValues, error)
	// positionGone recognizes this lane's 410 and answers with the --since
	// that re-enters it and the server's resume URL. The SDK gives the two
	// lanes distinct types precisely so one arm cannot silently handle the
	// other's: the feed re-enters at the epoch, the inbox at the earliest
	// retained item, and those recoveries are not interchangeable.
	positionGone  func(error) (since, resume string, ok bool)
	forbiddenMsg  string
	forbiddenHint string
}

var (
	eventsLane = feedLane{
		name:                "event",
		pollCmd:             "basecamp events poll",
		sinceHint:           "Pass an event id to start after, 'now' to enter at the present, or 0 to replay served history",
		continuationFilters: eventsContinuationFilters,
		positionGone:        eventsPositionGone,
	}
	inboxLane = feedLane{
		name:                "inbox item",
		pollCmd:             "basecamp inbox",
		sinceHint:           "Pass an inbox item id to start after, 'now' to enter at the present, or 0 for the earliest retained items",
		continuationFilters: inboxContinuationFilters,
		positionGone:        inboxPositionGone,
		forbiddenMsg:        "The inbox is served to agent principals only",
		forbiddenHint:       "Authenticate as an agent, or use 'basecamp events poll' for the account-wide feed",
	}
)

// eventsPositionGone reads the feed's 410, which names the epoch the feed can
// still serve from.
func eventsPositionGone(err error) (string, string, bool) {
	var gone *basecamp.FeedPositionGoneError
	if !errors.As(err, &gone) {
		return "", "", false
	}
	return strconv.FormatInt(gone.EpochAfterID, 10), gone.Resume, true
}

// inboxPositionGone reads the inbox's 410, which has no epoch: the position
// fell behind the retention window, and since=0 is the earliest retained item.
func inboxPositionGone(err error) (string, string, bool) {
	var gone *basecamp.InboxPositionGoneError
	if !errors.As(err, &gone) {
		return "", "", false
	}
	return basecamp.SinceEpoch, gone.Resume, true
}

func eventsContinuationFilters(raw string) ([]flagValues, error) {
	opts, err := basecamp.PollEventsOptionsFromURL(raw)
	if err != nil {
		return nil, err
	}
	return eventsFilterFlags(opts), nil
}

func inboxContinuationFilters(raw string) ([]flagValues, error) {
	opts, err := basecamp.PollInboxOptionsFromURL(raw)
	if err != nil {
		return nil, err
	}
	return inboxFilterFlags(opts), nil
}

// feedEntry is the entry point a poll presents: an explicit since, a held
// position, or neither (which enters at the present).
type feedEntry struct {
	since    string
	position string
}

// validate rejects the two entry points the server would reject anyway, with
// a message that names the fix. The remedy comes from the lane, because the
// two lanes number different things: --since takes an event id on the feed
// and an addressed-item id on the inbox. The shape of --since is checked,
// never its value: an id the lane has never served is the server's verdict to
// give, not ours.
//
// A flag given an empty value is refused before any of that. The documented
// way to resume is --position "$POSITION", so an unset variable arrives as an
// empty string — indistinguishable here from not passing the flag, which
// enters at the present. Silently skipping the backlog the caller meant to
// resume is the worst available reading of that, so it is not offered.
func (e feedEntry) validate(cmd *cobra.Command, lane feedLane) error {
	for _, given := range []struct{ name, value string }{
		{"since", e.since},
		{"position", e.position},
	} {
		if cmd.Flags().Changed(given.name) && strings.TrimSpace(given.value) == "" {
			return output.ErrUsageHint(
				fmt.Sprintf("--%s was given an empty value", given.name),
				"An empty value reads the same as omitting the flag, which enters at the present and skips what came before; pass a value or drop the flag")
		}
	}

	switch {
	case e.since != "" && e.position != "":
		return output.ErrUsage("--since and --position are mutually exclusive")
	case e.since == "", e.since == basecamp.SinceNow, e.since == basecamp.SinceEpoch:
		return nil
	}
	if _, err := strconv.ParseInt(e.since, 10, 64); err != nil {
		return output.ErrUsageHint(fmt.Sprintf("Invalid --since: %s", e.since), lane.sinceHint)
	}
	return nil
}

// feedIDs parses a comma-separated id filter. Only the shape is enforced:
// the server owns how many ids a filter may carry, and a cap written here
// would go stale as a rejection of a request the server would have served.
func feedIDs(values []string, flag string) ([]int64, error) {
	if len(values) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(values))
	for _, value := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return nil, output.ErrUsage(fmt.Sprintf("Invalid %s id: %s", flag, value))
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// feedPerformers parses a performer filter, which takes ids or the literal
// "self" — the request's own effective actor, resolved server-side.
// --exclude-performers self is the loop guard for an agent that acts on what
// it hears.
func feedPerformers(values []string, flag string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	performers := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "self" {
			if _, err := strconv.ParseInt(value, 10, 64); err != nil {
				return nil, output.ErrUsage(fmt.Sprintf("Invalid %s: %s (expected a person id or 'self')", flag, value))
			}
		}
		performers = append(performers, value)
	}
	return performers, nil
}

// feedActorTypes parses the actor-kind filter. Unlike types and reasons,
// whose catalogs grow, this dimension is the closed pair the contract names.
func feedActorTypes(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	actorTypes := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "agent" && value != "person" {
			return nil, output.ErrUsage(fmt.Sprintf("Invalid --actor-types: %s (expected 'agent' or 'person')", value))
		}
		actorTypes = append(actorTypes, value)
	}
	return actorTypes, nil
}

// trimFilter drops empty entries a trailing comma leaves behind, so
// --types "message.created," is the one-type filter it reads as rather than a
// filter carrying an empty type the server rejects.
func trimFilter(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	trimmed := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			trimmed = append(trimmed, value)
		}
	}
	if len(trimmed) == 0 {
		return nil
	}
	return trimmed
}

// feedRequest is what a poll knows about itself when it has to tell a caller
// how to come back: which lane it is, the configured API base a
// server-supplied URL has to match, and the filter flags it was invoked with.
//
// Every re-entry carries the filters, because a position is bound to its
// filter set: a resume command that dropped them would 409 on the spot, and
// dropping --exclude-performers self turns an agent's recovery into a run on
// its own activity.
type feedRequest struct {
	lane    feedLane
	baseURL string
	filters string
}

// resumeCommand renders the invocation that re-enters this lane after a 410.
//
// The server's resume URL preserves the request's canonical filters, so they
// come back out of it when it is one this client would follow. A resume URL
// that fails the origin check, that the SDK cannot parse, or that carries a
// filter value outside the contract's alphabet falls back to the filters this
// request was invoked with: a hint is meant to be pasted, and half-rendered
// server text is not.
func (r feedRequest) resumeCommand(since, resume string) string {
	if since == "" {
		since = basecamp.SinceNow
	}
	command := fmt.Sprintf("%s --since %s", r.lane.pollCmd, since)

	if filters, ok := r.resumeFilters(resume); ok {
		return command + filters
	}
	return command + r.filters
}

// resumeFilters renders the filter flags a resume URL carries, or reports
// that it carries none this client would act on.
func (r feedRequest) resumeFilters(resume string) (string, bool) {
	if resume == "" || !r.followable(resume) {
		return "", false
	}
	flags, err := r.lane.continuationFilters(resume)
	if err != nil {
		return "", false
	}
	return renderFilterFlags(flags)
}

// followable reports whether a server-supplied URL passes the same origin
// check the walk applies to a continuation. A 410's resume URL is displayed
// as authoritative and read for filters, which is acting on it.
func (r feedRequest) followable(raw string) bool {
	return checkContinuation(r.baseURL, raw) == nil
}

// renderFilterFlags renders flags as a pasteable suffix, reporting false when
// nothing was present or a value fell outside the alphabet below.
func renderFilterFlags(flags []flagValues) (string, bool) {
	rendered := ""
	for _, flag := range flags {
		if len(flag.values) == 0 {
			continue
		}
		for _, value := range flag.values {
			if !renderableFilterValue(value) {
				return "", false
			}
		}
		rendered += fmt.Sprintf(" %s %s", flag.name, strings.Join(flag.values, ","))
	}
	return rendered, rendered != ""
}

// requestFilters renders the filter flags an invocation carried, for the
// resume breadcrumb and the re-entry hints. Unrenderable values yield an
// empty suffix; the surrounding description says to keep the same filters in
// words, which is true whether or not they can be pasted.
func requestFilters(flags []flagValues) string {
	rendered, _ := renderFilterFlags(flags)
	return rendered
}

func eventsFilterFlags(opts *basecamp.PollEventsOptions) []flagValues {
	return []flagValues{
		{"--types", opts.Types},
		{"--buckets", int64Strings(opts.Buckets)},
		{"--creators", int64Strings(opts.Creators)},
		{"--performers", opts.Performers},
		{"--exclude-performers", opts.ExcludePerformers},
		{"--actor-types", opts.ActorTypes},
	}
}

func inboxFilterFlags(opts *basecamp.PollInboxOptions) []flagValues {
	return []flagValues{
		{"--reasons", opts.Reasons},
		{"--types", opts.Types},
		{"--buckets", int64Strings(opts.Buckets)},
	}
}

// flagValues pairs a flag with the comma-joined list it carries.
type flagValues struct {
	name   string
	values []string
}

func int64Strings(values []int64) []string {
	if len(values) == 0 {
		return nil
	}
	rendered := make([]string, len(values))
	for i, value := range values {
		rendered[i] = strconv.FormatInt(value, 10)
	}
	return rendered
}

// renderableFilterValue reports whether a filter value from a server response
// is safe to paste into a shell command unquoted. srv2's domain is cataloged
// type strings, actor types, addressing reasons and integer ids, so anything
// outside that alphabet means the URL is not what the contract describes and
// the whole hint falls back rather than quoting around the surprise.
func renderableFilterValue(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// feedError maps the poll lanes' documented failures onto the CLI's error
// taxonomy. Every branch is an errors.As on a typed SDK error, so the split
// the SDK is adding — a per-lane 410 and a reason-carrying 400 — lands as two
// more arms here and nowhere else.
//
// The mapping:
//
//	409 → usage. The held position was minted for different filters; the fix
//	      is the caller's, and both digests name which side is which.
//	410 → not found. The position is no longer servable. Not the caller's
//	      mistake, so the message carries the lane's own re-entry point, read
//	      through the lane's own 410 type.
//	403 → forbidden, with the lane's documented reason (the inbox is
//	      agents-only for now).
//	400 → the server's message, plus the remedy its reason names: re-enter
//	      for a malformed position, fix the filters for a malformed filter.
//	      A 400 carrying no reason gets no hint — the two remedies are
//	      opposite, and guessing sends the caller to the wrong one.
func feedError(req feedRequest, err error) error {
	var mismatch *basecamp.FeedFilterMismatchError
	if errors.As(err, &mismatch) {
		return &output.Error{
			Code: output.CodeUsage,
			Message: fmt.Sprintf(
				"Position was minted for a different filter set (position digest %s, this request's filters digest %s)",
				mismatch.PositionDigest, mismatch.FiltersDigest),
			Hint: fmt.Sprintf("Re-enter with --since to acknowledge the filter change, keeping these filters: %s --since now%s",
				req.lane.pollCmd, req.filters),
			HTTPStatus: http.StatusConflict,
			Cause:      err,
		}
	}

	if since, resume, ok := req.lane.positionGone(err); ok {
		message := fmt.Sprintf("Position is no longer servable for the %s feed", req.lane.name)
		if resume != "" && req.followable(resume) {
			message = fmt.Sprintf("%s. Resume URL: %s", message, resume)
		}
		return &output.Error{
			Code:       output.CodeNotFound,
			Message:    message,
			Hint:       "Re-enter with: " + req.resumeCommand(since, resume),
			HTTPStatus: http.StatusGone,
			Cause:      err,
		}
	}

	var request *basecamp.FeedRequestError
	if errors.As(err, &request) {
		switch request.Reason {
		case basecamp.FeedReasonInvalidPosition:
			return feedRequestError(request, err,
				fmt.Sprintf("Re-enter with: %s --since now%s", req.lane.pollCmd, req.filters))
		case basecamp.FeedReasonInvalidFilter:
			return feedRequestError(request, err,
				"Fix the filters and re-run; a position reset will not help")
		}
		// No reason: the server did not say which case this is, and the two
		// have opposite remedies. Surface its message with no hint rather
		// than send the caller to the wrong one.
	}

	var sdkErr *basecamp.Error
	if errors.As(err, &sdkErr) && sdkErr.HTTPStatus == http.StatusForbidden && req.lane.forbiddenMsg != "" {
		return &output.Error{
			Code:       output.CodeForbidden,
			Message:    req.lane.forbiddenMsg,
			Hint:       req.lane.forbiddenHint,
			HTTPStatus: http.StatusForbidden,
			Cause:      err,
		}
	}

	return convertSDKError(err)
}

// feedRequestError renders a 400 with the remedy its reason names, keeping
// the server's own message and the SDK's classification.
func feedRequestError(request *basecamp.FeedRequestError, cause error, hint string) error {
	return &output.Error{
		Code:       request.Err.Code,
		Message:    request.Err.Message,
		Hint:       hint,
		HTTPStatus: request.Err.HTTPStatus,
		Retryable:  request.Err.Retryable,
		Cause:      cause,
	}
}

// checkContinuation is the caller-side obligation the SDK's
// Poll*OptionsFromURL documents: a continuation URL arrives in a server
// response, so its origin is checked against the configured API base before
// the walk follows it. Only the query is ever re-issued — the SDK builds the
// next request against the configured base URL, so a foreign origin cannot
// receive the bearer — but a server that could redirect a walk's filters
// still has no business doing so.
//
// An unconfigured base URL (the SDK's own default) is the default host.
func checkContinuation(baseURL, raw string) error {
	base, err := url.Parse(baseURL)
	if err != nil || base.Host == "" {
		base = &url.URL{Scheme: "https", Host: "3.basecampapi.com"}
	}
	next, err := url.Parse(raw)
	if err != nil {
		return &output.Error{
			Code:    output.CodeAPI,
			Message: "Continuation URL is not a URL",
		}
	}
	if !strings.EqualFold(next.Scheme, base.Scheme) || !sameHostPort(next, base) {
		return &output.Error{
			Code:    output.CodeAPI,
			Message: "Continuation URL points outside the configured Basecamp API host",
			Hint:    "Re-enter with --since rather than following this walk",
		}
	}
	return nil
}

// resumeNotice is where a person gets their position back.
//
// The envelope carries it for machines, but the rendered output is the rows —
// and the generic renderer drops a field named "position" from objects
// anyway, since on a to-do or a card that name means an ordering integer. So
// the notice carries the whole resume command. A walk that stopped at its
// page cap says so in the same sentence, because "more remains" and "here is
// where to continue" are one thought.
//
// Every format reaches it: the envelope formats render it, and --ids-only
// and --count — whose stdout is rows alone — get it on stderr. Without that
// second channel a capped walk prints an id list or a count that reads as
// the complete answer.
func (r feedRequest) resumeNotice(position string, capped bool, maxPages int) string {
	notice := ""
	if capped {
		notice = fmt.Sprintf("Stopped at --max-pages %d; more remains. ", maxPages)
	}
	if position == "" {
		return strings.TrimSpace(notice)
	}
	return notice + "Resume from here with: " + r.positionCommand(position)
}

// positionCommand is the invocation that resumes from a position. The filters
// ride along because a position is bound to the set it was minted for: the
// same command without them is a different filter set, and the server answers
// 409. The position itself is shell-quoted — it is opaque server text going
// into a command somebody pastes, and nothing about it promises to be a bare
// word.
func (r feedRequest) positionCommand(position string) string {
	return fmt.Sprintf("%s --position %s%s", r.lane.pollCmd, shellQuote(position), r.filters)
}

// newEventsPollCmd builds `basecamp events poll`.
func newEventsPollCmd() *cobra.Command {
	var (
		entry             feedEntry
		types             []string
		buckets           []string
		creators          []string
		performers        []string
		excludePerformers []string
		actorTypes        []string
		all               bool
		maxPages          int
	)

	cmd := &cobra.Command{
		Use:   "poll",
		Short: "Poll the account event feed",
		Long: `Poll the account-wide event feed.

One call is one page. Each page carries a durable 'position' to resume from
and, while the current walk has more to serve, a 'next' continuation URL.
With no --since or --position the feed begins at the present.

  basecamp events poll --since now
  basecamp events poll --position "$POSITION"
  basecamp events poll --since 0 --types message.created,comment.created --all
  basecamp events poll --exclude-performers self

The feed is a notification lane, not an audit log: events are thin pointers,
delivery is best effort, and the same event can arrive twice. Deduplicate by
event id and refetch the referenced recording before acting on it.`,
		Annotations: map[string]string{
			"agent_notes": "Account-wide — no --in <project> needed.\n" +
				"Returns {events, position, next}. Persist 'position' only after processing the page's events, then resume with --position.\n" +
				"--since takes an event id (int64), 'now' to enter at the present, or 0 to replay served history back to the feed's epoch.\n" +
				"Deduplicate by event id: polls repeat events the live stream already delivered, and events can appear up to ~30s after they happen.\n" +
				"Events are thin pointers — refetch the referenced recording with 'basecamp show <recording_id>' before acting on it.\n" +
				"An agent that acts on what it hears should pass --exclude-performers self to avoid reacting to its own activity.\n" +
				"Changing filters invalidates a held position (exit 1, HTTP 409): re-enter with --since.\n" +
				"A position that predates the feed's epoch exits 2 (HTTP 410) and the hint names the --since to re-enter with.\n" +
				"--all walks 'next' to the end of the current walk; it is not a live tail.",
		},
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := entry.validate(cmd, eventsLane); err != nil {
				return err
			}
			if maxPages < 1 {
				return output.ErrUsage("--max-pages must be at least 1")
			}

			bucketIDs, err := feedIDs(trimFilter(buckets), "--buckets")
			if err != nil {
				return err
			}
			creatorIDs, err := feedIDs(trimFilter(creators), "--creators")
			if err != nil {
				return err
			}
			performerIDs, err := feedPerformers(trimFilter(performers), "--performers")
			if err != nil {
				return err
			}
			excludeIDs, err := feedPerformers(trimFilter(excludePerformers), "--exclude-performers")
			if err != nil {
				return err
			}
			kinds, err := feedActorTypes(trimFilter(actorTypes))
			if err != nil {
				return err
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			opts := &basecamp.PollEventsOptions{
				Since:             entry.since,
				Position:          entry.position,
				Types:             trimFilter(types),
				Buckets:           bucketIDs,
				Creators:          creatorIDs,
				Performers:        performerIDs,
				ExcludePerformers: excludeIDs,
				ActorTypes:        kinds,
			}

			request := feedRequest{
				lane:    eventsLane,
				baseURL: app.Config.BaseURL,
				filters: requestFilters(eventsFilterFlags(opts)),
			}

			events := make([]basecamp.FeedEvent, 0)
			var position, next string
			pages, capped := 0, false
			service := app.Account().EventFeed()

			for {
				page, err := service.PollEvents(cmd.Context(), opts)
				if err != nil {
					return feedError(request, err)
				}
				pages++
				events = append(events, page.Events...)
				position, next = page.Position, page.Next

				if !all || next == "" {
					break
				}
				if pages >= maxPages {
					capped = true
					break
				}
				if err := checkContinuation(app.Config.BaseURL, next); err != nil {
					return err
				}
				if opts, err = basecamp.PollEventsOptionsFromURL(next); err != nil {
					return convertSDKError(err)
				}
				// A position is bound to the filter set it was minted for, and
				// the continuation carries its own. Re-entering with the first
				// call's flags would be a different set, and a 409.
				request.filters = requestFilters(eventsFilterFlags(opts))
			}

			summary := fmt.Sprintf("%d event(s)", len(events))
			if pages > 1 {
				summary = fmt.Sprintf("%s over %d pages", summary, pages)
			}
			if next != "" && !capped {
				summary += "; more to serve"
			}

			respOpts := []output.ResponseOption{
				output.WithSummary(summary),
				output.WithNotice(request.resumeNotice(position, capped, maxPages)),
				// The envelope is one object: rendered for a person it is a
				// Go dump, --count answers 1 for any page, and --ids-only
				// finds no id to print. The rows are what all three want.
				// The position a person needs to resume comes back in the
				// notice below, since it lives in the envelope, not the rows.
				output.WithDisplayData(events),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "resume",
						Cmd:         request.positionCommand(position),
						Description: "Poll again from this page's position, with the same filters",
					},
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp show <recording_id>",
						Description: "Fetch a referenced recording",
					},
				),
			}
			return app.OK(map[string]any{
				"events":   events,
				"position": position,
				"next":     next,
			}, respOpts...)
		},
	}

	cmd.Flags().StringVar(&entry.since, "since", "", "Enter the feed: an event id to start after, 'now', or 0 to replay served history")
	cmd.Flags().StringVar(&entry.position, "position", "", "Resume from a position token a previous page issued")
	cmd.Flags().StringSliceVar(&types, "types", nil, "Filter by event type (comma-separated, e.g. message.created)")
	cmd.Flags().StringSliceVar(&buckets, "buckets", nil, "Filter by bucket (project) id (comma-separated)")
	cmd.Flags().StringSliceVar(&creators, "creators", nil, "Filter by creator person id (comma-separated)")
	cmd.Flags().StringSliceVar(&performers, "performers", nil, "Filter by effective performer: person ids or 'self' (comma-separated)")
	cmd.Flags().StringSliceVar(&excludePerformers, "exclude-performers", nil, "Exclude effective performers: person ids or 'self' (comma-separated)")
	cmd.Flags().StringSliceVar(&actorTypes, "actor-types", nil, "Filter by actor kind: agent, person (comma-separated)")
	cmd.Flags().BoolVar(&all, "all", false, "Walk 'next' to the end of the current walk (not a live tail)")
	cmd.Flags().IntVar(&maxPages, "max-pages", feedDefaultMaxPages, "Maximum pages to fetch with --all")

	return cmd
}

// newEventsTicketCmd builds `basecamp events ticket`.
func newEventsTicketCmd() *cobra.Command {
	var showSecret bool

	cmd := &cobra.Command{
		Use:   "ticket",
		Short: "Mint a live event stream ticket",
		Long: `Mint a short-lived ticket for the live event stream over WebSocket.

The response carries the exact URL to connect to — connect to it verbatim
rather than assembling cable topology yourself. The ticket is a replayable
bearer for its ~2 minute window and the URL embeds it, so both are redacted
unless --show-secret is passed. Mint a fresh ticket for every connection
attempt; an open socket does not refresh one.

  basecamp events ticket
  basecamp events ticket --show-secret`,
		Annotations: map[string]string{
			"agent_notes": "Account-wide — no --in <project> needed.\n" +
				"Returns {ticket, expires_in, url}. Both ticket and url are redacted unless --show-secret is passed; the url embeds the ticket.\n" +
				"Never log either value. Mint one ticket per connection attempt — tickets expire in about 2 minutes and are not refreshed by an open socket.\n" +
				"Live frames never advance a durable position; only polls do. Keep polling from your position to repair what push missed.",
		},
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			ticket, err := app.Account().EventFeed().CreateStreamTicket(cmd.Context())
			if err != nil {
				return convertSDKError(err)
			}

			data := map[string]any{
				"ticket":     ticket.Ticket,
				"expires_in": ticket.ExpiresIn,
				"url":        ticket.URL,
			}
			if !showSecret {
				data["ticket"] = redactedTicket
				data["url"] = redactTicketURL(ticket.URL, ticket.Ticket)
			}

			respOpts := []output.ResponseOption{
				output.WithSummary(fmt.Sprintf("Stream ticket expires in %ds", ticket.ExpiresIn)),
			}
			if !showSecret {
				respOpts = append(respOpts, output.WithNotice("Ticket and URL redacted. Pass --show-secret to reveal them; never log either."))
			}

			return app.OK(data, respOpts...)
		},
	}

	cmd.Flags().BoolVar(&showSecret, "show-secret", false, "Print the ticket and its URL verbatim instead of redacting them")

	return cmd
}

// redactedTicket stands in for a ticket the caller did not ask to see. It is
// deliberately not ticket-shaped: a placeholder that still looked like a
// credential invites a consumer to present it.
const redactedTicket = "[REDACTED]"

// redactTicketURL renders a cable URL without its credential: enough of the
// endpoint to see where a stream would open, and none of the response's own
// text.
//
// The result is a diagnostic, never a paste target — it carries
// ticket=[REDACTED], so it cannot open a stream whatever else it keeps. The
// connectable URL is the one --show-secret returns, which is the server's
// verbatim and passes through this function not at all. That is what makes
// dropping the server's other parameters here free: no artifact anyone
// connects with loses them, including a protocol or region parameter the
// mint might start sending.
//
// The ticket is opaque and the body is not ours to trust, so the display URL
// is built rather than edited. Only scheme, host and path survive; the query
// is rebuilt from a constant, which is what makes this answerable at all —
// carrying the server's other parameters through means asking whether a copy
// of the ticket is hiding in one, and the answer depends on the encoding the
// rendering happens to choose (a ticket "abc/def" echoed as "abc%2Fdef"
// reads as neither). Userinfo and the fragment are dropped the same way.
//
// What survives is then vetted at the exit point, against two axes that each
// cost a round of this to find.
//
// Which string is secret: every ticket the response presents, not just the
// body's field. If the URL's own ticket parameter disagrees with the body,
// the URL's is the one that would open the stream.
//
// Which spelling to look for: the rendering and its percent-decoding, not the
// components one at a time. url.URL.String escapes per component and picks
// the encoding itself — an escaped path, a non-ASCII host — so a search for
// the raw secret against a list of components misses whichever one it
// happened to re-spell, which is how the scheme and then the host got
// through. Decoding the finished string normalizes that away for every
// component at once, including the ones nobody has named.
//
// Anything still holding any ticket is withheld whole, as is a URL that will
// not parse, will not decode, or carries no ticket parameter — that is not
// the shape this was promised.
func redactTicketURL(raw, ticket string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return redactedTicket
	}
	query := parsed.Query()
	if !query.Has("ticket") {
		return redactedTicket
	}

	display := url.URL{
		Scheme:   parsed.Scheme,
		Host:     parsed.Host,
		Path:     parsed.Path,
		RawQuery: url.Values{"ticket": {redactedTicket}}.Encode(),
	}
	rendered := display.String()
	decoded, err := url.PathUnescape(rendered)
	if err != nil {
		return redactedTicket
	}

	for _, secret := range append([]string{ticket}, query["ticket"]...) {
		if secret != "" && (strings.Contains(rendered, secret) || strings.Contains(decoded, secret)) {
			return redactedTicket
		}
	}
	return rendered
}
