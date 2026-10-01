package commands

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// tokenEndpointFeed is the connector's live feed over an Agent whose token
// has expired, against a server whose token endpoint responds through the
// answer callback.
// Nothing but the token endpoint is ever reached: every feed request needs
// a token first. It reports how many mints the endpoint saw.
func tokenEndpointFeed(t *testing.T, answer func(http.ResponseWriter)) (*eventfeed.Live, func() int32) {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("BASECAMP_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var mints atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/tokens" {
			t.Errorf("the feed reached %s without a token", r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
			return
		}
		mints.Add(1)
		answer(w)
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{AccountID: "555", BaseURL: srv.URL, ActiveProfile: "agent", Sources: map[string]string{}}
	mgr := auth.NewManager(cfg, srv.Client())
	store := auth.NewStore(config.GlobalConfigDir())
	mgr.SetStore(store)
	require.NoError(t, store.Save("profile:agent", &auth.Credentials{
		AccessToken:   "bc_at_expired",
		OAuthType:     "agent",
		ClientID:      "agent-client",
		ClientSecret:  "agent-secret",
		TokenEndpoint: srv.URL + "/oauth/tokens",
		ExpiresAt:     time.Now().Add(-time.Minute).Unix(),
	}))

	live, err := eventfeed.NewLive(&basecamp.Config{BaseURL: srv.URL}, &feedTokens{managerTokens{mgr: mgr}}, "555", eventfeed.AccountLane, connectSDKOptions()...)
	require.NoError(t, err)
	return live, mints.Load
}

func answerStatus(status int, header map[string]string, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// A token endpoint that rate-limits the Agent is a wait, not the end of the
// connector: the feed is told to back off for the Retry-After the endpoint
// named, on the mint lane and the poll lane alike. The second is answered
// by the stored hold, which names what is left of the same wait — the
// deadline, rounded up to the second it is stored in.
func TestARateLimitedTokenRenewalThrottlesTheFeed(t *testing.T) {
	live, mints := tokenEndpointFeed(t, answerStatus(http.StatusTooManyRequests, map[string]string{"Retry-After": "42"}, `{"error":"slow_down"}`))

	_, err := live.Minter().MintStreamTicket(t.Context())
	var mintErr *eventfeed.MintError
	require.ErrorAs(t, err, &mintErr)
	assert.Equal(t, eventfeed.MintThrottled, mintErr.Kind, "%v", err)
	assert.InDelta(t, 42*time.Second, mintErr.RetryAfter, float64(time.Second))

	_, err = live.Polls().Poll(t.Context(), eventfeed.Cursor{Since: "now"}, eventfeed.Filters{})
	var pollErr *eventfeed.PollError
	require.ErrorAs(t, err, &pollErr)
	assert.Equal(t, eventfeed.PollThrottled, pollErr.Kind, "%v", err)
	assert.InDelta(t, 42*time.Second, pollErr.RetryAfter, float64(time.Second))

	assert.Equal(t, int32(1), mints(), "the poll's renewal was the stored hold's to answer")
}

// A wait named as an HTTP date is honored as surely as one named in seconds.
func TestARateLimitUntilADateThrottlesTheFeed(t *testing.T) {
	until := time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat)
	live, _ := tokenEndpointFeed(t, answerStatus(http.StatusTooManyRequests, map[string]string{"Retry-After": until}, ""))

	_, err := live.Minter().MintStreamTicket(t.Context())
	var mintErr *eventfeed.MintError
	require.ErrorAs(t, err, &mintErr)
	assert.Equal(t, eventfeed.MintThrottled, mintErr.Kind, "%v", err)
	assert.InDelta(t, 2*time.Minute, mintErr.RetryAfter, float64(2*time.Second))
}

// A rate limit that names no wait is held for the default, and the feed
// waits that out rather than backing off into the hold.
func TestARateLimitWithoutRetryAfterWaitsOutTheDefaultHold(t *testing.T) {
	live, _ := tokenEndpointFeed(t, answerStatus(http.StatusTooManyRequests, nil, ""))

	_, err := live.Minter().MintStreamTicket(t.Context())
	var mintErr *eventfeed.MintError
	require.ErrorAs(t, err, &mintErr)
	assert.Equal(t, eventfeed.MintThrottled, mintErr.Kind, "%v", err)
	assert.InDelta(t, time.Minute, mintErr.RetryAfter, float64(time.Second))
}

// The token endpoint's own trouble is retryable: the feed reconnects or
// re-polls after its backoff instead of ending the run.
func TestAServerFaultAtTheTokenEndpointIsTransient(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		live, _ := tokenEndpointFeed(t, answerStatus(status, nil, `{"error":"invalid_client"}`))

		_, err := live.Minter().MintStreamTicket(t.Context())
		var mintErr *eventfeed.MintError
		require.ErrorAs(t, err, &mintErr, status)
		assert.Equal(t, eventfeed.MintTransient, mintErr.Kind, "%d: %v", status, err)

		_, err = live.Polls().Poll(t.Context(), eventfeed.Cursor{Since: "now"}, eventfeed.Filters{})
		var pollErr *eventfeed.PollError
		require.ErrorAs(t, err, &pollErr, status)
		assert.Equal(t, eventfeed.PollTransient, pollErr.Kind, "%d: %v", status, err)
	}
}

// A token endpoint that does not answer at all is a transport failure the
// feed already rides out.
func TestAnUnreachableTokenEndpointIsTransient(t *testing.T) {
	live, _ := tokenEndpointFeed(t, func(w http.ResponseWriter) {
		// The handler runs off the test goroutine, where only assert is safe.
		hj, ok := w.(http.Hijacker)
		if !assert.True(t, ok) {
			return
		}
		conn, _, err := hj.Hijack()
		if !assert.NoError(t, err) {
			return
		}
		_ = conn.Close()
	})

	_, err := live.Minter().MintStreamTicket(t.Context())
	var mintErr *eventfeed.MintError
	require.ErrorAs(t, err, &mintErr)
	assert.Equal(t, eventfeed.MintTransient, mintErr.Kind, "%v", err)
}

// The CLI's own words still reach whoever renders the failure: the SDK
// error the feed classifies wraps them, it does not replace them.
func TestAFailedRenewalKeepsTheCLIsMessage(t *testing.T) {
	live, _ := tokenEndpointFeed(t, answerStatus(http.StatusServiceUnavailable, nil, ""))

	_, err := live.Minter().MintStreamTicket(t.Context())
	e := output.AsError(err)
	assert.Equal(t, "minting an agent token: the server answered HTTP 503", e.Message)
	assert.True(t, e.Retryable)
}

// A refusal of the Agent's credential stays the end of the run, and stays
// recognizable as the disconnect it is.
func TestARefusedTokenRenewalStillEndsTheFeed(t *testing.T) {
	for name, answer := range map[string]func(http.ResponseWriter){
		"invalid_client": answerStatus(http.StatusBadRequest, nil, `{"error":"invalid_client"}`),
		"bare 401":       answerStatus(http.StatusUnauthorized, nil, ""),
	} {
		live, _ := tokenEndpointFeed(t, answer)

		_, err := live.Minter().MintStreamTicket(t.Context())
		var mintErr *eventfeed.MintError
		require.ErrorAs(t, err, &mintErr, name)
		assert.Equal(t, eventfeed.MintUnrecoverable, mintErr.Kind, "%s: %v", name, err)
		assert.True(t, errors.Is(err, auth.ErrAgentCredentialRefused), name)

		_, err = live.Polls().Poll(t.Context(), eventfeed.Cursor{Since: "now"}, eventfeed.Filters{})
		var pollErr *eventfeed.PollError
		require.ErrorAs(t, err, &pollErr, name)
		assert.Equal(t, eventfeed.PollUnrecoverable, pollErr.Kind, "%s: %v", name, err)
		assert.True(t, errors.Is(err, auth.ErrAgentCredentialRefused), name)
	}
}

// A wait too long to represent is bounded, never turned negative.
func TestAnAbsurdRetryAfterIsBounded(t *testing.T) {
	live, _ := tokenEndpointFeed(t, answerStatus(http.StatusTooManyRequests, map[string]string{"Retry-After": "99999999999999"}, ""))

	_, err := live.Minter().MintStreamTicket(t.Context())
	var mintErr *eventfeed.MintError
	require.ErrorAs(t, err, &mintErr)
	assert.Equal(t, eventfeed.MintThrottled, mintErr.Kind, "%v", err)
	assert.Positive(t, mintErr.RetryAfter)
}

// Every failure reaches the feed already classified: a rate limit that
// carries no number — a hold the CLI keeps itself, say — and a request that
// got no response are retryable; anything else carries no status, so the
// feed ends on it rather than counting it as an authorization failure.
func TestTheFeedSeesEveryFailureClassified(t *testing.T) {
	assert.Nil(t, tokenFailureInSDKTerms(nil))

	held := tokenFailureInSDKTerms(output.ErrRateLimit(30))
	assert.True(t, held.Retryable)
	assert.Equal(t, http.StatusTooManyRequests, held.HTTPStatus)

	network := tokenFailureInSDKTerms(fmt.Errorf("minting an agent token: %w", &url.Error{Op: "Post", URL: "https://example.test/oauth/tokens", Err: io.EOF}))
	assert.True(t, network.Retryable)
	assert.Equal(t, basecamp.CodeNetwork, network.Code)
	assert.Zero(t, network.HTTPStatus)

	refused := output.ErrAuth("Minting an agent token was refused (HTTP 401)")
	refused.HTTPStatus = http.StatusUnauthorized
	refused.Cause = auth.ErrAgentCredentialRefused
	for _, err := range []error{
		refused,
		output.ErrAPI(http.StatusNotFound, "minting an agent token: the server answered HTTP 404"),
		errors.New("the store could not be read"),
	} {
		e := tokenFailureInSDKTerms(err)
		assert.False(t, e.Retryable, "%v", err)
		assert.Zero(t, e.HTTPStatus, "%v", err)
		assert.ErrorIs(t, e, err)
	}
}

// The feed goes by the same rule as the start: a truncated 401 ends it as a
// refusal, a truncated 503 is waited out.
func TestTheFeedClassifiesATruncatedResponseByItsStatus(t *testing.T) {
	live, _ := tokenEndpointFeed(t, truncatedAnswer(t, http.StatusUnauthorized))
	_, err := live.Minter().MintStreamTicket(t.Context())
	var mintErr *eventfeed.MintError
	require.ErrorAs(t, err, &mintErr)
	assert.Equal(t, eventfeed.MintUnrecoverable, mintErr.Kind, "%v", err)
	assert.ErrorIs(t, err, auth.ErrAgentCredentialRefused)

	live, _ = tokenEndpointFeed(t, truncatedAnswer(t, http.StatusServiceUnavailable))
	_, err = live.Minter().MintStreamTicket(t.Context())
	require.ErrorAs(t, err, &mintErr)
	assert.Equal(t, eventfeed.MintTransient, mintErr.Kind, "%v", err)
}
