package connector

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

type fakeFeedClient struct {
	page    *basecamp.EventFeedPage
	ticket  *basecamp.StreamTicket
	err     error
	lastOpt *basecamp.PollEventsOptions
	calls   int
}

func (f *fakeFeedClient) PollEvents(_ context.Context, opts *basecamp.PollEventsOptions) (*basecamp.EventFeedPage, error) {
	f.calls++
	f.lastOpt = opts
	if f.err != nil {
		return nil, f.err
	}
	return f.page, nil
}

func (f *fakeFeedClient) CreateStreamTicket(context.Context) (*basecamp.StreamTicket, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.ticket, nil
}

func newTestAdapter(t *testing.T, client FeedClient) *FeedAdapter {
	t.Helper()
	adapter, err := NewFeedAdapter(client, "https://3.basecampapi.com")
	require.NoError(t, err)
	return adapter
}

// The card's hardest constraint: the feed's two 410s mean different things and
// must not share one recovery path. PR 898 models both with one
// FeedPositionGoneError discriminated by a nil EpochAfterID, while the
// connector seam's PollError.EpochAfterID is a plain int64 — so the naive
// conversion silently turns "the inbox's retention window closed" into "the
// feed's epoch is event 0".
func TestFeedEpoch410BecomesTheGapSignal(t *testing.T) {
	epoch := int64(17099838487)
	client := &fakeFeedClient{err: &basecamp.FeedPositionGoneError{
		Err:          &basecamp.Error{Code: basecamp.CodeAPI, HTTPStatus: 410, Message: "position below epoch"},
		EpochAfterID: &epoch,
		Resume:       "https://3.basecampapi.com/2914079/events.json?since=17099838487",
	}}

	_, err := newTestAdapter(t, client).Poll(context.Background(), eventfeed.Cursor{}, eventfeed.Filters{})

	var pollErr *eventfeed.PollError
	require.ErrorAs(t, err, &pollErr)
	assert.Equal(t, eventfeed.PollGone, pollErr.Kind)
	assert.Equal(t, epoch, pollErr.EpochAfterID)
	assert.Equal(t, "https://3.basecampapi.com/2914079/events.json?since=17099838487", pollErr.ResumeURL)

	var epochGone *FeedEpochGoneError
	assert.ErrorAs(t, err, &epochGone)
}

func TestRetention410NeverBecomesTheGapSignal(t *testing.T) {
	client := &fakeFeedClient{err: &basecamp.FeedPositionGoneError{
		Err:          &basecamp.Error{Code: basecamp.CodeAPI, HTTPStatus: 410, Message: "position outside the retention window"},
		EpochAfterID: nil, // the inbox lane's shape: no epoch, not a zero one
		Resume:       "https://3.basecampapi.com/2914079/my/inbox.json?since=0",
	}}

	_, err := newTestAdapter(t, client).Poll(context.Background(), eventfeed.Cursor{}, eventfeed.Filters{})

	var pollErr *eventfeed.PollError
	require.ErrorAs(t, err, &pollErr)
	assert.NotEqual(t, eventfeed.PollGone, pollErr.Kind,
		"a retention 410 dispatched as FeedGap resumes the feed down the epoch's path")
	assert.Zero(t, pollErr.EpochAfterID,
		"no epoch may be invented for a 410 that carried none")

	var retention *InboxRetentionGoneError
	require.ErrorAs(t, err, &retention)
	assert.Equal(t, "https://3.basecampapi.com/2914079/my/inbox.json?since=0", retention.Resume)

	var epochGone *FeedEpochGoneError
	assert.False(t, errors.As(err, &epochGone),
		"the two 410s must not both satisfy the epoch arm")
}

func TestFilterMismatchCarriesBothDigests(t *testing.T) {
	client := &fakeFeedClient{err: &basecamp.FeedFilterMismatchError{
		Err:            &basecamp.Error{Code: basecamp.CodeAPI, HTTPStatus: 409, Message: "filters changed"},
		PositionDigest: "9f2ab04e5c11d3a7",
		FiltersDigest:  "0011223344556677",
	}}

	_, err := newTestAdapter(t, client).Poll(context.Background(), eventfeed.Cursor{}, eventfeed.Filters{})

	var pollErr *eventfeed.PollError
	require.ErrorAs(t, err, &pollErr)
	assert.Equal(t, eventfeed.PollFilterChanged, pollErr.Kind)
	assert.Equal(t, "9f2ab04e5c11d3a7", pollErr.PositionDigest)
	assert.Equal(t, "0011223344556677", pollErr.FiltersDigest)
}

// The two reasons behind a feed 400 need opposite recoveries, so a 400 that
// names neither is surfaced rather than guessed.
func TestUnreasonedBadRequestIsSurfacedNotGuessed(t *testing.T) {
	client := &fakeFeedClient{err: &basecamp.Error{
		Code: basecamp.CodeValidation, HTTPStatus: 400, Message: "bad request",
	}}

	_, err := newTestAdapter(t, client).Poll(context.Background(), eventfeed.Cursor{}, eventfeed.Filters{})

	var pollErr *eventfeed.PollError
	require.ErrorAs(t, err, &pollErr)
	assert.NotEqual(t, eventfeed.PollPositionInvalid, pollErr.Kind,
		"guessing the position turns a bad filter into an endless re-entry loop")
	assert.NotEqual(t, eventfeed.PollFilterInvalid, pollErr.Kind,
		"guessing the filter kills a feed a re-entry would have fixed")

	var undifferentiated *UndifferentiatedRequestError
	assert.ErrorAs(t, err, &undifferentiated)
}

func TestThrottleAndTransientAreClassifiedApart(t *testing.T) {
	throttled := &fakeFeedClient{err: &basecamp.Error{
		Code: basecamp.CodeRateLimit, HTTPStatus: 429, Retryable: true, RetryAfter: 7,
	}}
	_, err := newTestAdapter(t, throttled).Poll(context.Background(), eventfeed.Cursor{}, eventfeed.Filters{})
	var pollErr *eventfeed.PollError
	require.ErrorAs(t, err, &pollErr)
	assert.Equal(t, eventfeed.PollThrottled, pollErr.Kind)
	assert.Equal(t, 7*time.Second, pollErr.RetryAfter)

	transient := &fakeFeedClient{err: &basecamp.Error{
		Code: basecamp.CodeAPI, HTTPStatus: 503, Retryable: true,
	}}
	_, err = newTestAdapter(t, transient).Poll(context.Background(), eventfeed.Cursor{}, eventfeed.Filters{})
	require.ErrorAs(t, err, &pollErr)
	assert.Equal(t, eventfeed.PollTransient, pollErr.Kind)
}

func TestForeignContinuationIsRefusedBeforeAnyRequest(t *testing.T) {
	client := &fakeFeedClient{page: &basecamp.EventFeedPage{}}

	_, err := newTestAdapter(t, client).Poll(context.Background(),
		eventfeed.Cursor{PageURL: "https://evil.example.com/2914079/events.json?position=x"},
		eventfeed.Filters{})

	var pollErr *eventfeed.PollError
	require.ErrorAs(t, err, &pollErr)
	assert.Equal(t, eventfeed.PollRedirectRefused, pollErr.Kind)
	assert.Equal(t, "https://evil.example.com", pollErr.LocationOrigin)
	assert.NotContains(t, pollErr.Error(), "evil.example.com",
		"a hostile target can reflect the bearer into a host label, so the origin is data and never a rendering")
	assert.Zero(t, client.calls, "zero egress to a foreign target")
}

func TestSchemeDowngradeIsRefused(t *testing.T) {
	client := &fakeFeedClient{page: &basecamp.EventFeedPage{}}

	_, err := newTestAdapter(t, client).Poll(context.Background(),
		eventfeed.Cursor{PageURL: "http://3.basecampapi.com/2914079/events.json?position=x"},
		eventfeed.Filters{})

	var pollErr *eventfeed.PollError
	require.ErrorAs(t, err, &pollErr)
	assert.Equal(t, eventfeed.PollRedirectRefused, pollErr.Kind)
	assert.Zero(t, client.calls)
}

func TestEmptyPageWithANextIsNotAnEnd(t *testing.T) {
	client := &fakeFeedClient{page: &basecamp.EventFeedPage{
		Events:   nil,
		Position: "opaque-position-2",
		Next:     "https://3.basecampapi.com/2914079/events.json?position=opaque-position-2",
	}}

	page, err := newTestAdapter(t, client).Poll(context.Background(), eventfeed.Cursor{}, eventfeed.Filters{})
	require.NoError(t, err)
	assert.Empty(t, page.Events)
	assert.NotEmpty(t, page.Next, "a page that served no match still advanced the cursor")
	assert.Equal(t, "opaque-position-2", page.Position)
}

func TestPollPassesTheFilterSetAndCursor(t *testing.T) {
	client := &fakeFeedClient{page: &basecamp.EventFeedPage{}}
	adapter := newTestAdapter(t, client)

	_, err := adapter.Poll(context.Background(),
		eventfeed.Cursor{Since: "17099838487"},
		eventfeed.Filters{
			Types:             []string{"comment.created"},
			Buckets:           []int64{48699913},
			ExcludePerformers: []int64{52007412},
			ActorTypes:        []string{"person"},
		})
	require.NoError(t, err)

	require.NotNil(t, client.lastOpt)
	assert.Equal(t, "17099838487", client.lastOpt.Since)
	assert.Equal(t, []string{"comment.created"}, client.lastOpt.Types)
	assert.Equal(t, []int64{48699913}, client.lastOpt.Buckets)
	assert.Equal(t, []string{"52007412"}, client.lastOpt.ExcludePerformers,
		"the loop guard is the agent's own resolved id")
	assert.Equal(t, []string{"person"}, client.lastOpt.ActorTypes)
}

func TestResumeURLIsUsedAsServed(t *testing.T) {
	client := &fakeFeedClient{page: &basecamp.EventFeedPage{}}
	adapter := newTestAdapter(t, client)

	_, err := adapter.Poll(context.Background(),
		eventfeed.Cursor{PageURL: "https://3.basecampapi.com/2914079/events.json?since=17099838487&types=comment.created"},
		eventfeed.Filters{Types: []string{"card.created"}})
	require.NoError(t, err)

	require.NotNil(t, client.lastOpt)
	assert.Equal(t, "17099838487", client.lastOpt.Since)
	assert.Equal(t, []string{"comment.created"}, client.lastOpt.Types,
		"re-imposing the local filters on a resume is how a resume stops being the server's")
}

func TestMintClassifiesUnauthorized(t *testing.T) {
	client := &fakeFeedClient{err: &basecamp.Error{Code: basecamp.CodeAuth, HTTPStatus: 401}}

	_, err := newTestAdapter(t, client).MintStreamTicket(context.Background())

	var mintErr *eventfeed.MintError
	require.ErrorAs(t, err, &mintErr)
	assert.Equal(t, eventfeed.MintUnauthorized, mintErr.Kind)
}

func TestMintNeverRendersTheTicket(t *testing.T) {
	client := &fakeFeedClient{ticket: &basecamp.StreamTicket{
		Ticket:    "s3cr3t-bearer",
		ExpiresIn: 120,
		URL:       "wss://cable.basecamp.com/cable?ticket=s3cr3t-bearer",
	}}

	ticket, err := newTestAdapter(t, client).MintStreamTicket(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "s3cr3t-bearer", ticket.Ticket)
	assert.Equal(t, "wss://cable.basecamp.com/cable?ticket=s3cr3t-bearer", ticket.URL,
		"the URL is connected to verbatim; the connector never assembles cable topology")
}
