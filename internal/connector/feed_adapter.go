package connector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

// This file is the Layer-1 adapter the eventfeed package leaves open: the
// TicketMinter and PollSource seams, backed by the SDK's generated
// EventFeedService. It is the package's own documented path — "a host that
// wants the live feed supplies its own over the generated operations" — not a
// stand-in for one.
//
// SWAP POINT. basecamp-sdk PR 899 lands these adapters in the SDK, and PR 897
// an eventfeed.NewLive that wires them. When both are in, NewFeedAdapter's two
// seam methods are replaced by that constructor and everything below the
// translation line goes; the translation of the SDK's error shapes into this
// package's two 410 types stays, because it is what keeps the two recoveries
// apart, and nothing upstream owns that.

// FeedEpochGoneError is the ACCOUNT feed's 410: the held position fell below
// the feed's epoch. EpochAfterID names where servable history begins, and the
// served resume URL re-enters above that fence.
//
// Recovery: accept, follow the resume exactly as served, record the gap with
// its epoch, and classify the entry by the cursor the server chose.
type FeedEpochGoneError struct {
	EpochAfterID int64
	Resume       string
	Err          error
}

func (e *FeedEpochGoneError) Error() string {
	return fmt.Sprintf("event feed position is below the feed epoch %d: %v", e.EpochAfterID, e.Err)
}
func (e *FeedEpochGoneError) Unwrap() error { return e.Err }

// InboxRetentionGoneError is the INBOX lane's 410: the held position fell out
// of the 30-day retention window. There is no epoch — not a zero one — and the
// resume re-enters at since=0, the earliest retained item.
//
// Recovery: it is a different loss with a different shape, and the account
// lane must never produce one. Intake surfaces it rather than resuming, so
// that a contract change announces itself instead of being absorbed as "the
// epoch is zero".
type InboxRetentionGoneError struct {
	Resume string
	Err    error
}

func (e *InboxRetentionGoneError) Error() string {
	return fmt.Sprintf("inbox position is outside the retention window: %v", e.Err)
}
func (e *InboxRetentionGoneError) Unwrap() error { return e.Err }

// UndifferentiatedRequestError is a feed 400 the server gave no reason for.
//
// The two reasons need opposite recoveries — an invalid position re-enters
// with since=<last poll-served id>, an invalid filter is terminal — so a 400
// that names neither is surfaced rather than guessed. Guessing the position
// turns a bad filter into an endless re-entry loop; guessing the filter kills
// a feed a re-entry would have fixed.
type UndifferentiatedRequestError struct {
	Err error
}

func (e *UndifferentiatedRequestError) Error() string {
	return fmt.Sprintf("event feed refused the request without naming a reason: %v", e.Err)
}
func (e *UndifferentiatedRequestError) Unwrap() error { return e.Err }

// FeedClient is the slice of the SDK's EventFeedService this adapter needs.
// An interface so the adapter's error translation can be tested without a
// network, which is the only part of it that carries judgement.
type FeedClient interface {
	PollEvents(ctx context.Context, opts *basecamp.PollEventsOptions) (*basecamp.EventFeedPage, error)
	CreateStreamTicket(ctx context.Context) (*basecamp.StreamTicket, error)
}

// NewLiveFeedAdapter builds the adapter over its own SDK client, whose
// transport refuses to follow redirects.
//
// It owns its client for that reason alone. The SDK's default client follows
// a 3xx — to a foreign host too, with the Authorization header stripped but
// the request still sent — and the seam's zero-egress obligation is broken
// before any continuation check here could run. Client options cannot fix
// that from outside (a custom *http.Client is replaced at construction), but
// the transport is honored, and a transport sees each redirect hop before it
// leaves the machine.
func NewLiveFeedAdapter(cfg *basecamp.Config, tokens basecamp.TokenProvider, accountID string, inner http.RoundTripper, opts ...basecamp.ClientOption) (*FeedAdapter, error) {
	if cfg == nil || tokens == nil || accountID == "" {
		return nil, errors.New("connector: the live feed adapter needs a config, a token provider and an account id")
	}
	if inner == nil {
		inner = http.DefaultTransport
	}
	opts = append(opts, basecamp.WithTransport(RefuseRedirects(inner)))
	client := basecamp.NewClient(cfg, tokens, opts...)
	return NewFeedAdapter(client.ForAccount(accountID).EventFeed(), cfg.BaseURL)
}

// ErrRedirectRefused is a redirect hop the feed's transport would not send.
var ErrRedirectRefused = errors.New("connector: the event feed does not follow redirects")

// RefuseRedirects wraps a transport so that no redirect hop is ever sent. The
// client then reports the refusal as the request's failure.
func RefuseRedirects(inner http.RoundTripper) http.RoundTripper {
	return refuseRedirects{inner: inner}
}

type refuseRedirects struct{ inner http.RoundTripper }

func (t refuseRedirects) RoundTrip(req *http.Request) (*http.Response, error) {
	// Response is set exactly when the client created this request to follow
	// a redirect.
	if req.Response != nil {
		return nil, ErrRedirectRefused
	}
	return t.inner.RoundTrip(req)
}

// FeedAdapter backs the TicketMinter and PollSource seams.
type FeedAdapter struct {
	client FeedClient
	origin *url.URL
}

var (
	_ eventfeed.TicketMinter = (*FeedAdapter)(nil)
	_ eventfeed.PollSource   = (*FeedAdapter)(nil)
)

// NewFeedAdapter builds the adapter. origin is the API base the continuation
// and resume URLs are validated against.
func NewFeedAdapter(client FeedClient, origin string) (*FeedAdapter, error) {
	if client == nil {
		return nil, errors.New("connector: feed adapter needs a client")
	}
	canonical, err := eventfeed.CanonicalOrigin(origin)
	if err != nil {
		return nil, fmt.Errorf("connector: feed adapter origin: %w", err)
	}
	parsed, err := url.Parse(canonical)
	if err != nil {
		return nil, fmt.Errorf("connector: feed adapter origin: %w", err)
	}
	return &FeedAdapter{client: client, origin: parsed}, nil
}

// MintStreamTicket mints one ticket. Neither the ticket nor the URL it rides
// in is ever rendered into an error here: the URL's query string carries the
// bearer.
func (a *FeedAdapter) MintStreamTicket(ctx context.Context) (eventfeed.StreamTicket, error) {
	ticket, err := a.client.CreateStreamTicket(ctx)
	if err != nil {
		return eventfeed.StreamTicket{}, mintError(ctx, err)
	}
	return eventfeed.StreamTicket{
		Ticket:    ticket.Ticket,
		ExpiresIn: ticket.ExpiresIn,
		URL:       ticket.URL,
	}, nil
}

// Poll fetches one page at cursor under filters.
func (a *FeedAdapter) Poll(ctx context.Context, cursor eventfeed.Cursor, filters eventfeed.Filters) (eventfeed.PollPage, error) {
	opts, err := a.optionsFor(cursor, filters)
	if err != nil {
		return eventfeed.PollPage{}, err
	}

	page, err := a.client.PollEvents(ctx, opts)
	if err != nil {
		return eventfeed.PollPage{}, pollError(ctx, err)
	}

	events := make([]eventfeed.Event, 0, len(page.Events))
	for _, e := range page.Events {
		events = append(events, eventfeed.Event{
			ID:            e.ID,
			Kind:          e.Kind,
			EventType:     e.EventType,
			Action:        e.Action,
			CreatedAt:     e.CreatedAt,
			BucketID:      e.BucketID,
			CreatorID:     e.CreatorID,
			PerformedByID: e.PerformedByID,
			RecordingID:   e.RecordingID,
			Details:       e.Details,
		})
	}
	// An empty Events with a Next is ordinary, not an end: a request crosses
	// up to a thousand ledger rows and serves at most a hundred matches, and
	// the rows the filters excluded still moved the cursor. The run loop
	// follows Next; nothing here shortcuts on len(events) == 0.
	return eventfeed.PollPage{Events: events, Position: page.Position, Next: page.Next}, nil
}

// optionsFor turns one cursor into the generated operation's options. Exactly
// one of the cursor's three fields is set; the zero cursor is the bare present
// entry.
func (a *FeedAdapter) optionsFor(cursor eventfeed.Cursor, filters eventfeed.Filters) (*basecamp.PollEventsOptions, error) {
	if cursor.PageURL != "" {
		// A continuation or a 410 resume. It is server-supplied, so it is
		// validated against the configured origin before it is used for
		// anything: the SPEC's zero-egress-to-a-foreign-target obligation
		// rides on this adapter, not on the package.
		if err := a.checkContinuation(cursor.PageURL); err != nil {
			return nil, err
		}
		opts, err := basecamp.PollEventsOptionsFromURL(cursor.PageURL)
		if err != nil {
			return nil, &eventfeed.PollError{Kind: eventfeed.PollUnrecoverable, Err: err}
		}
		// The URL carries the server's own canonical filter set. It is used
		// as served: re-imposing the local filters on a resume is how a
		// resume stops being the server's.
		return opts, nil
	}

	opts := &basecamp.PollEventsOptions{
		Since:             cursor.Since,
		Position:          cursor.Position,
		Types:             filters.Types,
		Buckets:           filters.Buckets,
		Creators:          filters.Creators,
		Performers:        idStrings(filters.Performers),
		ExcludePerformers: idStrings(filters.ExcludePerformers),
		ActorTypes:        filters.ActorTypes,
	}
	return opts, nil
}

// checkContinuation enforces same-origin and no-downgrade on a server-supplied
// URL before it is followed.
func (a *FeedAdapter) checkContinuation(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return &eventfeed.PollError{Kind: eventfeed.PollRedirectRefused, Err: fmt.Errorf("connector: unparseable continuation URL")}
	}
	if !parsed.IsAbs() {
		return &eventfeed.PollError{Kind: eventfeed.PollRedirectRefused, Err: errors.New("connector: continuation URL is not absolute")}
	}
	if !strings.EqualFold(parsed.Scheme, a.origin.Scheme) || !strings.EqualFold(parsed.Host, a.origin.Host) {
		// LocationOrigin is DATA and is deliberately not rendered: a hostile
		// target can reflect the bearer into a host label.
		return &eventfeed.PollError{
			Kind:           eventfeed.PollRedirectRefused,
			LocationOrigin: parsed.Scheme + "://" + parsed.Host,
			Err:            errors.New("connector: continuation URL leaves the configured API origin"),
		}
	}
	return nil
}

func idStrings(ids []int64) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, strconv.FormatInt(id, 10))
	}
	return out
}

// ---------------------------------------------------------------------------
// The translation line. Everything below maps the SDK's error shapes onto the
// seam's taxonomy, and it is the one place the two 410s are told apart.
// ---------------------------------------------------------------------------

// callerCanceled reports a failure that is the caller's own cancellation. The
// seam requires it to pass through unchanged: classified as transient, a
// shutdown or a reconnect would enter transport-retry handling. A deadline the
// client imposed on itself, with the caller's context still live, is not this
// and stays transient.
func callerCanceled(ctx context.Context, err error) bool {
	return ctx.Err() != nil && errors.Is(err, ctx.Err())
}

// pollError classifies a failed PollEvents call.
func pollError(ctx context.Context, err error) error {
	if err == nil || callerCanceled(ctx, err) {
		return err
	}

	// The inbox's 410 first, so it can never fall through to the feed's arm.
	//
	// On sdk main today both lanes answer one *basecamp.FeedPositionGoneError
	// with EpochAfterID *int64, nil on the inbox. basecamp-sdk PR 912 splits
	// them into two types with the epoch required on the feed's. Either way
	// the discrimination happens here and once: a nil epoch flattened into
	// eventfeed.PollError's plain int64 EpochAfterID would present a
	// retention loss as "the feed's epoch is 0" and send it down the epoch's
	// recovery path, which is the failure this arm exists to prevent.
	if gone := asFeedGone(err); gone != nil {
		if gone.EpochAfterID == nil {
			return &eventfeed.PollError{
				Kind: eventfeed.PollUnrecoverable,
				Err:  &InboxRetentionGoneError{Resume: gone.Resume, Err: err},
			}
		}
		return &eventfeed.PollError{
			Kind:         eventfeed.PollGone,
			EpochAfterID: *gone.EpochAfterID,
			ResumeURL:    gone.Resume,
			Err:          &FeedEpochGoneError{EpochAfterID: *gone.EpochAfterID, Resume: gone.Resume, Err: err},
		}
	}

	if errors.Is(err, ErrRedirectRefused) {
		return &eventfeed.PollError{Kind: eventfeed.PollRedirectRefused, Err: err}
	}

	var mismatch *basecamp.FeedFilterMismatchError
	if errors.As(err, &mismatch) {
		return &eventfeed.PollError{
			Kind:           eventfeed.PollFilterChanged,
			PositionDigest: mismatch.PositionDigest,
			FiltersDigest:  mismatch.FiltersDigest,
			Err:            err,
		}
	}

	var apiErr *basecamp.Error
	if !errors.As(err, &apiErr) {
		return &eventfeed.PollError{Kind: eventfeed.PollTransient, Err: err}
	}

	switch {
	case apiErr.HTTPStatus == 400:
		// SWAP POINT for basecamp-sdk PR 912's *FeedRequestError: when it
		// carries reason=invalid_position this becomes PollPositionInvalid,
		// and reason=invalid_filter becomes PollFilterInvalid. Until the
		// server names the reason, a 400 is undifferentiated and is surfaced
		// rather than guessed — see UndifferentiatedRequestError.
		return &eventfeed.PollError{
			Kind: eventfeed.PollUnrecoverable,
			Msg:  apiErr.Message,
			Err:  &UndifferentiatedRequestError{Err: err},
		}
	case apiErr.HTTPStatus == 401 || apiErr.HTTPStatus == 403:
		return &eventfeed.PollError{Kind: eventfeed.PollUnauthorized, Err: err}
	case apiErr.RetryAfter > 0:
		return &eventfeed.PollError{Kind: eventfeed.PollThrottled, RetryAfter: retryAfter(apiErr), Err: err}
	case apiErr.Retryable:
		return &eventfeed.PollError{Kind: eventfeed.PollTransient, Err: err}
	}
	return &eventfeed.PollError{Kind: eventfeed.PollUnrecoverable, Msg: apiErr.Message, Err: err}
}

// mintError classifies a failed CreateStreamTicket call.
func mintError(ctx context.Context, err error) error {
	if err == nil || callerCanceled(ctx, err) {
		return err
	}
	if errors.Is(err, ErrRedirectRefused) {
		return &eventfeed.MintError{Kind: eventfeed.MintUnrecoverable, Err: err}
	}
	var apiErr *basecamp.Error
	if !errors.As(err, &apiErr) {
		return &eventfeed.MintError{Kind: eventfeed.MintTransient, Err: err}
	}
	switch {
	case apiErr.HTTPStatus == 401 || apiErr.HTTPStatus == 403:
		return &eventfeed.MintError{Kind: eventfeed.MintUnauthorized, Err: err}
	case apiErr.RetryAfter > 0:
		return &eventfeed.MintError{Kind: eventfeed.MintThrottled, RetryAfter: retryAfter(apiErr), Err: err}
	case apiErr.Retryable:
		return &eventfeed.MintError{Kind: eventfeed.MintTransient, Err: err}
	}
	return &eventfeed.MintError{Kind: eventfeed.MintUnrecoverable, Err: err}
}

func retryAfter(apiErr *basecamp.Error) time.Duration {
	return time.Duration(apiErr.RetryAfter) * time.Second
}

// asFeedGone reads the SDK's feed 410 into a shape this package owns, so the
// rest of the file does not move when the SDK's does.
func asFeedGone(err error) *feedGone {
	var gone *basecamp.FeedPositionGoneError
	if !errors.As(err, &gone) {
		return nil
	}
	return &feedGone{EpochAfterID: gone.EpochAfterID, Resume: gone.Resume}
}

type feedGone struct {
	EpochAfterID *int64
	Resume       string
}
