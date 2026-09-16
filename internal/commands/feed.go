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
	name        string
	pollCmd     string
	sinceHint   string
	resumeSince string
	// continuationFilters reads the filter flags a continuation or resume URL
	// carries, through the lane's own parser: the two lanes have different
	// filter dimensions and their URLs are never interchangeable.
	continuationFilters func(string) ([]flagValues, error)
	forbiddenMsg        string
	forbiddenHint       string
}

var (
	eventsLane = feedLane{
		name:                "event",
		pollCmd:             "basecamp events poll",
		sinceHint:           "Pass an event id to start after, 'now' to enter at the present, or 0 to replay served history",
		continuationFilters: eventsContinuationFilters,
	}
	inboxLane = feedLane{
		name:                "inbox item",
		pollCmd:             "basecamp inbox",
		sinceHint:           "Pass an inbox item id to start after, 'now' to enter at the present, or 0 for the earliest retained items",
		resumeSince:         basecamp.SinceEpoch,
		continuationFilters: inboxContinuationFilters,
		forbiddenMsg:        "The inbox is served to agent principals only",
		forbiddenHint:       "Authenticate as an agent, or use 'basecamp events poll' for the account-wide feed",
	}
)

func eventsContinuationFilters(raw string) ([]flagValues, error) {
	opts, err := basecamp.PollEventsOptionsFromURL(raw)
	if err != nil {
		return nil, err
	}
	return []flagValues{
		{"--types", opts.Types},
		{"--buckets", int64Strings(opts.Buckets)},
		{"--creators", int64Strings(opts.Creators)},
		{"--performers", opts.Performers},
		{"--exclude-performers", opts.ExcludePerformers},
		{"--actor-types", opts.ActorTypes},
	}, nil
}

func inboxContinuationFilters(raw string) ([]flagValues, error) {
	opts, err := basecamp.PollInboxOptionsFromURL(raw)
	if err != nil {
		return nil, err
	}
	return []flagValues{
		{"--reasons", opts.Reasons},
		{"--types", opts.Types},
		{"--buckets", int64Strings(opts.Buckets)},
	}, nil
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
func (e feedEntry) validate(lane feedLane) error {
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

// resumeCommand renders the invocation that re-enters this lane after a 410.
//
// The server's resume URL preserves the request's canonical filters, and a
// bare --since would drop every one of them — including the
// --exclude-performers self loop guard, whose absence turns an agent's
// recovery into a run on its own activity. So the filters come back out of
// the resume URL rather than being left behind in it.
//
// A resume URL the SDK cannot parse, or one carrying a filter value outside
// the contract's alphabet, falls back to the lane's bare re-entry: a hint is
// meant to be pasted, and half-rendered server text is not.
func (l feedLane) resumeCommand(gone *basecamp.FeedPositionGoneError) string {
	since := l.resumeSince
	if gone.EpochAfterID != nil {
		since = strconv.FormatInt(*gone.EpochAfterID, 10)
	}
	if since == "" {
		since = basecamp.SinceNow
	}
	command := fmt.Sprintf("%s --since %s", l.pollCmd, since)

	filters, ok := l.resumeFilters(gone.Resume)
	if !ok {
		return command
	}
	return command + filters
}

// resumeFilters renders the filter flags a resume URL carries, or reports
// that it carries none it can render.
func (l feedLane) resumeFilters(resume string) (string, bool) {
	if resume == "" {
		return "", false
	}
	flags, err := l.continuationFilters(resume)
	if err != nil {
		return "", false
	}

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
//	      mistake, so the message carries the lane's own re-entry point.
//	403 → forbidden, with the lane's documented reason (the inbox is
//	      agents-only for now).
//	400 → whatever the server said. A malformed position and a malformed
//	      filter have different fixes and the body says which; guessing
//	      between them would send the caller to the wrong one.
func feedError(lane feedLane, err error) error {
	var mismatch *basecamp.FeedFilterMismatchError
	if errors.As(err, &mismatch) {
		return &output.Error{
			Code: output.CodeUsage,
			Message: fmt.Sprintf(
				"Position was minted for a different filter set (position digest %s, this request's filters digest %s)",
				mismatch.PositionDigest, mismatch.FiltersDigest),
			Hint:       fmt.Sprintf("Re-enter with --since to acknowledge the filter change: %s --since now", lane.pollCmd),
			HTTPStatus: http.StatusConflict,
			Cause:      err,
		}
	}

	var gone *basecamp.FeedPositionGoneError
	if errors.As(err, &gone) {
		message := fmt.Sprintf("Position is no longer servable for the %s feed", lane.name)
		if gone.Resume != "" {
			message = fmt.Sprintf("%s. Resume URL: %s", message, gone.Resume)
		}
		return &output.Error{
			Code:       output.CodeNotFound,
			Message:    message,
			Hint:       "Re-enter with: " + lane.resumeCommand(gone),
			HTTPStatus: http.StatusGone,
			Cause:      err,
		}
	}

	var sdkErr *basecamp.Error
	if errors.As(err, &sdkErr) && sdkErr.HTTPStatus == http.StatusForbidden && lane.forbiddenMsg != "" {
		return &output.Error{
			Code:       output.CodeForbidden,
			Message:    lane.forbiddenMsg,
			Hint:       lane.forbiddenHint,
			HTTPStatus: http.StatusForbidden,
			Cause:      err,
		}
	}

	return convertSDKError(err)
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

// feedWalkNotice reports a walk that stopped at its page cap rather than at
// the end of the walk, so a caller reads "more remains" instead of "done".
func feedWalkNotice(capped bool, maxPages int) string {
	if !capped {
		return ""
	}
	return fmt.Sprintf("Stopped at --max-pages %d; more remains. Poll again from the position above.", maxPages)
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

			if err := entry.validate(eventsLane); err != nil {
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

			events := make([]basecamp.FeedEvent, 0)
			var position, next string
			pages, capped := 0, false
			service := app.Account().EventFeed()

			for {
				page, err := service.PollEvents(cmd.Context(), opts)
				if err != nil {
					return feedError(eventsLane, err)
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
				// The envelope is the machine contract, but it is one object,
				// so --count would answer 1 for any page and --ids-only would
				// find no id to print. The rows are what those modes are
				// about; --json keeps the resumable envelope.
				output.WithDisplayData(events),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "resume",
						Cmd:         "basecamp events poll --position <position>",
						Description: "Poll again from this page's position",
					},
					output.Breadcrumb{
						Action:      "show",
						Cmd:         "basecamp show <recording_id>",
						Description: "Fetch a referenced recording",
					},
				),
			}
			if notice := feedWalkNotice(capped, maxPages); notice != "" {
				respOpts = append(respOpts, output.WithNotice(notice))
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
				data["url"] = redactTicketURL(ticket.URL)
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

// redactTicketURL renders a cable URL without its credential. A URL that
// cannot be parsed is withheld whole rather than echoed: the ticket is
// opaque, so any component of an unparseable URL can be carrying it.
func redactTicketURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return redactedTicket
	}
	query := parsed.Query()
	if !query.Has("ticket") {
		return redactedTicket
	}
	query.Set("ticket", redactedTicket)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
