package connector

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

// These pin, at the real seam intake is built on (eventfeed.NewLive over the
// generated operations), the contract its recovery paths depend on. The
// handling lives in basecamp-sdk now; if the SDK ever changes it, intake's
// assumptions break here rather than silently in production.

type feedServer struct {
	*httptest.Server
	foreignURL  string
	foreignHits atomic.Int32
}

func newFeedServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, s *feedServer)) *feedServer {
	t.Helper()
	s := &feedServer{}
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.foreignHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(foreign.Close)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler(w, r, s)
	}))
	t.Cleanup(s.Close)
	s.foreignURL = foreign.URL
	return s
}

func (s *feedServer) json(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(strings.ReplaceAll(body, "ORIGIN", s.URL)))
}

func livePolls(t *testing.T, s *feedServer) (eventfeed.PollSource, eventfeed.TicketMinter) {
	t.Helper()
	live, err := eventfeed.NewLive(&basecamp.Config{BaseURL: s.URL}, &basecamp.StaticTokenProvider{Token: "token"},
		"2914079", eventfeed.AccountLane, basecamp.WithMaxRetries(0))
	require.NoError(t, err)
	opts := LiveOptions(live)
	return opts.PollsFor(), opts.Minter
}

func pollOnce(t *testing.T, status int, body string) *eventfeed.PollError {
	t.Helper()
	s := newFeedServer(t, func(w http.ResponseWriter, _ *http.Request, s *feedServer) { s.json(w, status, body) })
	polls, _ := livePolls(t, s)
	_, err := polls.Poll(context.Background(), eventfeed.Cursor{Position: "p"}, eventfeed.Filters{})
	var pollErr *eventfeed.PollError
	require.ErrorAs(t, err, &pollErr)
	return pollErr
}

// D1: the feed's 410 names its epoch and becomes the gap signal.
func TestSeamTheFeedsEpoch410IsTheGapSignal(t *testing.T) {
	pollErr := pollOnce(t, 410, `{"error":"gone","epoch_after_id":17099838487,"resume":"ORIGIN/2914079/events.json?since=17099838487"}`)
	assert.Equal(t, eventfeed.PollGone, pollErr.Kind)
	assert.Equal(t, int64(17099838487), pollErr.EpochAfterID)
	assert.True(t, strings.HasSuffix(pollErr.ResumeURL, "since=17099838487"))
}

// D1: the inbox's 410 shape — no epoch, a since=0 resume — arriving on the
// account lane never becomes the gap signal, and no epoch is invented for it.
func TestSeamAnInboxShaped410OnTheAccountLaneIsNeverTheGapSignal(t *testing.T) {
	pollErr := pollOnce(t, 410, `{"error":"gone","resume":"ORIGIN/2914079/events.json?since=0"}`)
	assert.NotEqual(t, eventfeed.PollGone, pollErr.Kind, "a retention loss must not take the epoch's recovery path")
	assert.Zero(t, pollErr.EpochAfterID)
}

// D1: a 410 whose resume does not re-enter at the fence it declares is refused
// rather than followed into a skip.
func TestSeamA410WhoseResumeSkipsTheFenceIsRefused(t *testing.T) {
	pollErr := pollOnce(t, 410, `{"error":"gone","epoch_after_id":7,"resume":"ORIGIN/2914079/events.json?since=now"}`)
	assert.NotEqual(t, eventfeed.PollGone, pollErr.Kind)
}

// D3: the 400's reason keys recover-versus-stop.
func TestSeamThe400sReasonDecidesRecoverOrStop(t *testing.T) {
	assert.Equal(t, eventfeed.PollPositionInvalid,
		pollOnce(t, 400, `{"error":"x","reason":"invalid_position"}`).Kind)
	assert.Equal(t, eventfeed.PollFilterInvalid,
		pollOnce(t, 400, `{"error":"x","reason":"invalid_filter"}`).Kind)
	unknown := pollOnce(t, 400, `{"error":"Unrecognized position","reason":"invalid_something"}`).Kind
	assert.NotEqual(t, eventfeed.PollPositionInvalid, unknown, "an unnamed reason is surfaced, never guessed")
	assert.NotEqual(t, eventfeed.PollFilterInvalid, unknown)
}

// D2: a followed URL carries exactly one cursor; with neither it would enter at
// the present.
func TestSeamACursorlessContinuationIsRefusedBeforeAnyRequest(t *testing.T) {
	var hits atomic.Int32
	s := newFeedServer(t, func(w http.ResponseWriter, _ *http.Request, s *feedServer) {
		hits.Add(1)
		s.json(w, 200, `{"events":[],"position":"p"}`)
	})
	polls, _ := livePolls(t, s)
	_, err := polls.Poll(context.Background(), eventfeed.Cursor{PageURL: s.URL + "/2914079/events.json"}, eventfeed.Filters{})
	var pollErr *eventfeed.PollError
	require.ErrorAs(t, err, &pollErr)
	assert.Zero(t, hits.Load())
}

// D4 / H1: a 3xx is never followed, same origin or not, and the target is not
// rendered.
func TestSeamRedirectsAreRefusedWithZeroEgress(t *testing.T) {
	s := newFeedServer(t, func(w http.ResponseWriter, r *http.Request, s *feedServer) {
		http.Redirect(w, r, s.foreignURL+"/steal?leak=SECRET-TARGET", http.StatusFound)
	})
	polls, minter := livePolls(t, s)

	_, err := polls.Poll(context.Background(), eventfeed.Cursor{Position: "p"}, eventfeed.Filters{})
	var pollErr *eventfeed.PollError
	require.ErrorAs(t, err, &pollErr)
	assert.Equal(t, eventfeed.PollRedirectRefused, pollErr.Kind)
	assert.NotContains(t, err.Error(), "SECRET-TARGET")

	_, err = minter.MintStreamTicket(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "SECRET-TARGET")

	assert.Zero(t, s.foreignHits.Load(), "no request may reach the redirect target")
}

// E4: the caller's own cancellation passes through unclassified.
func TestSeamCallerCancellationIsNotATransportFailure(t *testing.T) {
	s := newFeedServer(t, func(w http.ResponseWriter, _ *http.Request, s *feedServer) {
		s.json(w, 200, `{"events":[],"position":"p"}`)
	})
	polls, _ := livePolls(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := polls.Poll(ctx, eventfeed.Cursor{Position: "p"}, eventfeed.Filters{})
	var pollErr *eventfeed.PollError
	assert.False(t, errors.As(err, &pollErr))
}
