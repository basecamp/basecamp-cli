package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/oauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// invalid_grant is read off the SDK's typed refusal, not the text of its
// message: a refusal the SDK words differently is still the grant refused,
// and text that merely starts the same way is not.
func TestInvalidGrant_ReadsTheTypedCode(t *testing.T) {
	typed := &basecamp.Error{
		Code: basecamp.CodeAuth, HTTPStatus: 400, Message: "token refresh failed: invalid_grant - Token has been revoked",
		OAuthError: "invalid_grant", OAuthErrorDescription: "Token has been revoked",
	}
	desc, ok := invalidGrant(fmt.Errorf("wrapped: %w", typed))
	assert.True(t, ok, "a typed invalid_grant is the grant refused, whatever its message says")
	assert.Equal(t, "Token has been revoked", desc)

	_, ok = invalidGrant(errors.New("token error: invalid_grant - looks like one"))
	assert.False(t, ok, "untyped text is not a verdict")

	_, ok = invalidGrant(&basecamp.Error{Code: basecamp.CodeAuth, HTTPStatus: 400, Message: "token error: invalid_grant", OAuthError: "invalid_client"})
	assert.False(t, ok, "the code decides, not the message")
}

// rateLimitedRefresh is a token endpoint that answers every refresh 429 with
// retryAfter, counting the requests that reach it, and a Manager whose
// active profile's BC5 credential refreshes against it on a clock the test
// moves.
func rateLimitedRefresh(t *testing.T, retryAfter string) (*Manager, string, *atomic.Int32, *time.Time) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":"too_many_requests","error_description":"Temporarily blocked due to repeated failures. Try again later."}`)
	}))
	t.Cleanup(srv.Close)

	now := time.Unix(1_800_000_000, 0)
	cfg := config.Default()
	cfg.ActiveProfile = "work"
	m := &Manager{cfg: cfg, httpClient: srv.Client(), store: newTestStore(t, t.TempDir())}
	m.SetClock(func() time.Time { return now })
	key := m.credentialKey()
	require.NoError(t, m.store.Save(key, &Credentials{
		AccessToken:   "old-tok",
		RefreshToken:  "old-ref",
		OAuthType:     "bc5",
		TokenEndpoint: srv.URL + "/oauth/tokens",
		Scope:         "full",
		// Expired on the real clock, which is the one needsRenewal reads;
		// the hold reads the Manager's.
		ExpiresAt: time.Now().Add(-time.Hour).Unix(),
	}))
	return m, key, &hits, &now
}

// A refresh answered 429 is held for its Retry-After on the stored login, so
// the next command — another process, the next run of a scheduled job —
// answers it locally instead of sending the refresh again. A job retrying
// every two minutes into an abuse block is what this stops.
func TestRefresh_RateLimitIsHeldOnTheLogin(t *testing.T) {
	m, key, hits, now := rateLimitedRefresh(t, "600")

	err := m.Refresh(context.Background())
	var cliErr *output.Error
	require.ErrorAs(t, err, &cliErr)
	assert.Equal(t, output.CodeRateLimit, cliErr.Code)
	assert.Equal(t, 600, RetryAfter(err))
	assert.Contains(t, cliErr.Message, now.Add(600*time.Second).UTC().Format(time.RFC3339))
	assert.Equal(t, int32(1), hits.Load())
	var refusal *basecamp.Error
	require.ErrorAs(t, err, &refusal, "the SDK's refusal stays in the chain, request id and all")
	assert.Equal(t, "too_many_requests", refusal.OAuthError)

	creds, loadErr := m.store.Load(key)
	require.NoError(t, loadErr, "a rate limit says nothing about the login, which is kept")
	assert.Equal(t, "old-ref", creds.RefreshToken)
	require.NotNil(t, creds.RenewalHold)
	assert.Equal(t, renewalHoldRateLimited, creds.RenewalHold.Kind)

	// Held: nothing is sent, and the answer is the same rate limit, with the
	// wait that is left.
	*now = now.Add(100 * time.Second)
	t.Setenv("BASECAMP_TOKEN", "")
	_, err = m.AccessToken(context.Background())
	require.ErrorAs(t, err, &cliErr)
	assert.Equal(t, output.CodeRateLimit, cliErr.Code)
	assert.Equal(t, 500, RetryAfter(err))
	assert.ErrorIs(t, err, errRenewalHeld)
	assert.Equal(t, int32(1), hits.Load(), "a held refresh sends nothing")

	// Over: the refresh goes out again.
	*now = now.Add(501 * time.Second)
	_ = m.Refresh(context.Background())
	assert.Equal(t, int32(2), hits.Load())
}

// The status report says the same thing a command would: the refresh is
// held, until when, and the login is still refreshable once it ends.
func TestRefreshRefusal_ReportsTheHold(t *testing.T) {
	m, key, _, _ := rateLimitedRefresh(t, "600")
	_ = m.Refresh(context.Background())

	creds, err := m.store.Load(key)
	require.NoError(t, err)
	refusal := m.RefreshRefusal(creds)
	require.Error(t, refusal)
	status := m.RenewalHoldStatus(creds, refusal)
	require.NotNil(t, status)
	assert.Equal(t, "rate_limited", status.Kind)
	assert.False(t, status.Permanent())
}

// bc3's abuse block runs for hours; the hold is capped at MaxServerWait as an
// agent's is, so a block lifted early is noticed, and the error still says
// how long the server asked for. A request inside the block is answered 429
// without being counted against the address, so the cap costs one request,
// not an escalation.
func TestRefresh_RateLimitHoldIsCappedButSaysWhatTheServerAsked(t *testing.T) {
	m, key, _, now := rateLimitedRefresh(t, "14400")

	err := m.Refresh(context.Background())
	require.Error(t, err)
	assert.Equal(t, int(MaxServerWait.Seconds()), RetryAfter(err))
	assert.Equal(t, 14400, NamedWait(err))
	assert.Contains(t, err.Error(), now.Add(4*time.Hour).UTC().Format(time.RFC3339), "the message names the server's own deadline")

	creds, loadErr := m.store.Load(key)
	require.NoError(t, loadErr)
	require.NotNil(t, creds.RenewalHold)
	assert.Equal(t, ceilUnix(now.Add(MaxServerWait)), creds.RenewalHold.Until)
}

// A hold is about the refresh token it was given for. Another process that
// has since rotated the login, or a fresh login, is not held.
func TestRefresh_HoldIsForTheRefusedToken(t *testing.T) {
	m, key, hits, _ := rateLimitedRefresh(t, "600")
	_ = m.Refresh(context.Background())

	creds, err := m.store.Load(key)
	require.NoError(t, err)
	creds.RefreshToken = "rotated-ref"
	require.NoError(t, m.store.Save(key, creds))

	_ = m.Refresh(context.Background())
	assert.Equal(t, int32(2), hits.Load(), "a hold for another refresh token holds nothing")
}

// A refresh that succeeds clears whatever hold the login carried.
func TestRefresh_SuccessClearsTheHold(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"new-tok","refresh_token":"new-ref","expires_in":3600}`)
	}))
	defer srv.Close()

	m := &Manager{cfg: config.Default(), httpClient: srv.Client(), store: newTestStore(t, t.TempDir())}
	key := m.credentialKey()
	require.NoError(t, m.store.Save(key, &Credentials{
		AccessToken: "old-tok", RefreshToken: "old-ref", OAuthType: "bc5",
		TokenEndpoint: srv.URL + "/oauth/tokens", ExpiresAt: time.Now().Add(-time.Hour).Unix(),
		// Expired, so it no longer holds, but is still on the record.
		RenewalHold: &RenewalHold{Kind: renewalHoldRateLimited, Detail: "the server answered HTTP 429",
			Client: refreshTokenFingerprint("old-ref"), Until: time.Now().Add(-time.Minute).Unix()},
	}))

	require.NoError(t, m.Refresh(context.Background()))
	creds, err := m.store.Load(key)
	require.NoError(t, err)
	assert.Nil(t, creds.RenewalHold)
}

// A device login refused 429 — Basecamp's abuse block covers device
// authorization too — says what happened and until when, in the reader's
// own clock, instead of "status 429"; and it names the usual cause, since
// signing in again cannot help while something else keeps failing.
func TestLoginDevice_RateLimitSaysUntilWhen(t *testing.T) {
	as := startDeviceAS(t)
	as.deviceRetryAfter = "7200"
	as.deviceAuth = func() (int, string) {
		return http.StatusTooManyRequests, `{"error":"too_many_requests","error_description":"Temporarily blocked due to repeated failures. Try again later."}`
	}
	resource := startResourceServer(t, as.srv.URL)
	m := newDeviceTestManager(t, resource.URL)
	now := time.Unix(1_800_000_000, 0)
	m.SetClock(func() time.Time { return now })

	_, err := m.Login(context.Background(), LoginOptions{
		Remote:        true,
		Logger:        func(string) {},
		deviceOptions: []oauth.DeviceOption{instantSleep()},
	})
	var cliErr *output.Error
	require.ErrorAs(t, err, &cliErr)
	assert.Equal(t, output.CodeRateLimit, cliErr.Code)
	assert.Equal(t, 7200, RetryAfter(err))
	assert.Contains(t, cliErr.Message, "Basecamp is refusing sign-ins from this address")
	assert.Contains(t, cliErr.Message, now.Add(2*time.Hour).Local().Format(localClockLayout))
	assert.NotContains(t, cliErr.Message, "status 429")
	assert.True(t, strings.Contains(cliErr.Hint, "credentials.json"), cliErr.Hint)
	assert.Empty(t, as.tokenCalls(), "nothing is polled after a refused authorization")
}

// Without a Retry-After the device login still explains itself.
func TestLoginDevice_RateLimitWithoutRetryAfter(t *testing.T) {
	as := startDeviceAS(t)
	as.deviceAuth = func() (int, string) {
		return http.StatusTooManyRequests, `{"error":"too_many_requests"}`
	}
	resource := startResourceServer(t, as.srv.URL)
	m := newDeviceTestManager(t, resource.URL)

	_, err := m.Login(context.Background(), LoginOptions{
		Remote:        true,
		Logger:        func(string) {},
		deviceOptions: []oauth.DeviceOption{instantSleep()},
	})
	var cliErr *output.Error
	require.ErrorAs(t, err, &cliErr)
	assert.Equal(t, output.CodeRateLimit, cliErr.Code)
	assert.Contains(t, cliErr.Message, "Basecamp is refusing sign-ins from this address")
	assert.Contains(t, cliErr.Message, "try again later")
}

// A stored hold never masks a local failure: a login whose token endpoint is
// missing reports that, not a rate limit it could not have reached anyway.
func TestRefreshRefusal_LocalFailureIsNotMaskedByAHold(t *testing.T) {
	m, key, _, _ := rateLimitedRefresh(t, "600")
	_ = m.Refresh(context.Background())

	creds, err := m.store.Load(key)
	require.NoError(t, err)
	require.NotNil(t, creds.RenewalHold)
	creds.TokenEndpoint = ""

	refusal := m.RefreshRefusal(creds)
	require.Error(t, refusal)
	assert.Contains(t, refusal.Error(), "missing their token endpoint")
	assert.NotErrorIs(t, refusal, errRenewalHeld)
	assert.Nil(t, m.RenewalHoldStatus(creds, refusal))
}

// Nor does it mask a lane that cannot be built: a credential whose
// configured base URL is unusable reports that, not a rate limit.
func TestRefreshRefusal_BadLaneIsNotMaskedByAHold(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cfg := config.Default()
	cfg.BaseURL = "http://exa mple.com"
	m := &Manager{cfg: cfg, store: newTestStore(t, t.TempDir())}
	m.SetClock(func() time.Time { return now })
	creds := &Credentials{
		AccessToken: "old-tok", RefreshToken: "old-ref", OAuthType: "bc5",
		TokenEndpoint: "https://issuer.example/oauth/tokens", ExpiresAt: now.Add(-time.Hour).Unix(),
		RenewalHold: &RenewalHold{Kind: renewalHoldRateLimited, Detail: "the server answered HTTP 429",
			Client: refreshTokenFingerprint("old-ref"), Until: now.Add(10 * time.Minute).Unix()},
	}

	refusal := m.RefreshRefusal(creds)
	require.Error(t, refusal)
	assert.NotErrorIs(t, refusal, errRenewalHeld, "the lane's own failure is the answer: %v", refusal)
}
