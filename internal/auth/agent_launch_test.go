package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

const testSessionID = "0123456789abcdef0123456789abcdef"

// liveAgentCredential is an agent profile whose shared token is comfortably
// inside its lifetime, so anything that mints did so on purpose.
func liveAgentCredential(tokenEndpoint string) *Credentials {
	creds := agentCredential(tokenEndpoint, time.Now().Add(time.Hour))
	creds.AccessToken = "shared-cached"
	return creds
}

// TestSessionTokenMintsFreshAndLeavesTheSharedTokenAlone: a session asks for
// its own token every time, with its id and label on the mint, and the
// profile's cached token is neither served nor overwritten.
func TestSessionTokenMintsFreshAndLeavesTheSharedTokenAlone(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(call int) (int, string) {
		return http.StatusOK, `{"access_token":"session-` + string(rune('1'+call)) + `","token_type":"Bearer","expires_in":3600,"scope":"full",` +
			`"launch_id":"` + testSessionID + `","launch_label":"coworker@box"}`
	}

	m := newDeviceTestManager(t, as.srv.URL)
	key := storeAgent(t, m, liveAgentCredential(as.srv.URL+"/oauth/token"))
	before, err := m.store.Load(key)
	require.NoError(t, err)

	first, err := m.SessionAccessToken(context.Background(), testSessionID, "coworker@box")
	require.NoError(t, err)
	assert.Equal(t, "session-1", first)

	second, err := m.SessionAccessToken(context.Background(), testSessionID, "coworker@box")
	require.NoError(t, err)
	assert.Equal(t, "session-2", second, "a session token is minted fresh, never cached")

	calls := as.tokenCalls()
	require.Len(t, calls, 2)
	// Every mint carries the session, not just the first: the mock echoes
	// the id back whatever was sent, so only the request shows it.
	for i, call := range calls {
		assert.Equal(t, "client_credentials", call.Get("grant_type"), "mint %d", i+1)
		assert.Equal(t, testSessionID, call.Get("launch_id"), "mint %d", i+1)
		assert.Equal(t, "coworker@box", call.Get("launch_label"), "mint %d", i+1)
		assert.Equal(t, AgentResourceURNPrefix+"42", call.Get("resource"), "mint %d", i+1)
	}

	after, err := m.store.Load(key)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a session mint must not touch the stored credential")

	shared, err := m.StoredAccessToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "shared-cached", shared)
	assert.Len(t, as.tokenCalls(), 2, "the shared token was still live and needed no mint")
}

// TestSessionTokenWithoutLabelSendsNoLabel: an id alone is a session; the
// label is optional and is not sent empty.
func TestSessionTokenWithoutLabelSendsNoLabel(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(int) (int, string) {
		return http.StatusOK, `{"access_token":"session","token_type":"Bearer","expires_in":3600,"launch_id":"` + testSessionID + `"}`
	}
	m := newDeviceTestManager(t, as.srv.URL)
	storeAgent(t, m, liveAgentCredential(as.srv.URL+"/oauth/token"))

	token, err := m.SessionAccessToken(context.Background(), testSessionID, "")
	require.NoError(t, err)
	assert.Equal(t, "session", token)

	calls := as.tokenCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, testSessionID, calls[0].Get("launch_id"))
	_, sent := calls[0]["launch_label"]
	assert.False(t, sent, "an absent label must not be sent as an empty one")
}

// TestSessionTokenFromAServerThatIgnoresTheSessionFailsClosed: a Basecamp
// that predates session attribution answers with an ordinary token. That
// token is not bound to the session, so it is discarded, not printed. The
// message says which way the echo missed: a server that echoed the id
// does record sessions, so it is not told it predates them.
func TestSessionTokenFromAServerThatIgnoresTheSessionFailsClosed(t *testing.T) {
	for name, tc := range map[string]struct{ body, says, never string }{
		"no echo": {
			`{"access_token":"unbound","token_type":"Bearer","expires_in":3600}`,
			"predates agent session attribution", "different"},
		"other id": {
			`{"access_token":"unbound","token_type":"Bearer","expires_in":3600,"launch_id":"ffffffffffffffffffffffffffffffff","launch_label":"coworker@box"}`,
			"answered for a different session", "predates"},
		"other label": {
			`{"access_token":"unbound","token_type":"Bearer","expires_in":3600,"launch_id":"` + testSessionID + `","launch_label":"someone-else"}`,
			"recorded a different label", "predates"},
		"label not given": {
			`{"access_token":"unbound","token_type":"Bearer","expires_in":3600,"launch_id":"` + testSessionID + `"}`,
			"did not record the label", "predates"},
	} {
		body := tc.body
		t.Run(name, func(t *testing.T) {
			as := startDeviceAS(t)
			as.token = func(int) (int, string) { return http.StatusOK, body }
			m := newDeviceTestManager(t, as.srv.URL)
			key := storeAgent(t, m, liveAgentCredential(as.srv.URL+"/oauth/token"))

			token, err := m.SessionAccessToken(context.Background(), testSessionID, "coworker@box")
			require.Error(t, err)
			assert.Empty(t, token)
			assert.NotContains(t, err.Error(), "unbound", "the unbound token must not leak into the error")
			assert.Contains(t, err.Error(), testSessionID)
			assert.Contains(t, err.Error(), tc.says)
			assert.NotContains(t, err.Error(), tc.never)

			stored, loadErr := m.store.Load(key)
			require.NoError(t, loadErr)
			assert.Equal(t, "shared-cached", stored.AccessToken)
			assert.Nil(t, stored.RenewalHold, "an unattributing server is no verdict on the credential")
		})
	}
}

// TestSessionTokenRefusedForItsSessionNamesItAndHoldsNothing: a refusal of
// the session's own parameters is about this request, not the credential,
// so nothing is held against the profile.
func TestSessionTokenRefusedForItsSessionNamesItAndHoldsNothing(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(int) (int, string) {
		return http.StatusBadRequest, `{"error":"invalid_request","error_description":"launch_label is too long"}`
	}
	m := newDeviceTestManager(t, as.srv.URL)
	key := storeAgent(t, m, liveAgentCredential(as.srv.URL+"/oauth/token"))

	_, err := m.SessionAccessToken(context.Background(), testSessionID, "coworker@box")
	require.Error(t, err)
	assert.Contains(t, err.Error(), testSessionID)
	assert.Contains(t, err.Error(), "invalid_request")

	stored, loadErr := m.store.Load(key)
	require.NoError(t, loadErr)
	assert.Nil(t, stored.RenewalHold)
	assert.Equal(t, "shared-cached", stored.AccessToken)
}

// TestSessionTokenRefusalOfTheCredentialIsHeld: a session mint presents the
// same client secret the shared path does, so the token endpoint's verdict
// on that secret is remembered for both — a dozen sandbox launches must not
// each keep presenting a dead secret.
func TestSessionTokenRefusalOfTheCredentialIsHeld(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(int) (int, string) {
		return http.StatusUnauthorized, `{"error":"invalid_client"}`
	}
	m := newDeviceTestManager(t, as.srv.URL)
	key := storeAgent(t, m, liveAgentCredential(as.srv.URL+"/oauth/token"))

	_, err := m.SessionAccessToken(context.Background(), testSessionID, "coworker@box")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAgentCredentialRefused)

	stored, loadErr := m.store.Load(key)
	require.NoError(t, loadErr)
	require.NotNil(t, stored.RenewalHold)
	assert.Equal(t, renewalHoldRefused, stored.RenewalHold.Kind)

	_, err = m.SessionAccessToken(context.Background(), testSessionID, "coworker@box")
	require.Error(t, err)
	assert.ErrorIs(t, err, errRenewalHeld)
	assert.Len(t, as.tokenCalls(), 1, "a held credential was presented again")
}

// TestConcurrentVerdictsOnTheSecretKeepTheLongerHold: a session mint holds
// no lock across its round trip, so another mint's verdict on the same
// secret can land while it is in flight. Whichever is written last, the
// hold that stands is the one that holds longer, and the session is told
// that one: a dead secret is not sent again a minute later because a 429
// from the abuse tracker it tripped landed after its invalid_client.
func TestConcurrentVerdictsOnTheSecretKeepTheLongerHold(t *testing.T) {
	type answer struct {
		status int
		header http.Header
		body   string
	}
	invalidClient := answer{http.StatusUnauthorized, nil, `{"error":"invalid_client"}`}
	invalidGrant := answer{http.StatusBadRequest, nil, `{"error":"invalid_grant"}`}
	rateLimited := func(seconds string) answer {
		return answer{http.StatusTooManyRequests, http.Header{"Retry-After": {seconds}}, `{}`}
	}

	for name, tc := range map[string]struct {
		// early is the verdict that lands while the session's mint is in
		// flight, from another launch, or from the shared path when
		// shared; late is the session's own.
		early, late answer
		shared      bool

		kind  string
		until time.Duration // from now; zero is never
		// stood is whether the session is answered with the earlier
		// verdict rather than its own; retryAfter is the wait it is told.
		stood      bool
		retryAfter int
	}{
		"a 429 after invalid_client":               {early: invalidClient, late: rateLimited("60"), kind: renewalHoldRefused, stood: true},
		"a 429 after the shared path's refusal":    {early: invalidClient, late: rateLimited("60"), shared: true, kind: renewalHoldRefused, stood: true},
		"a 429 after invalid_grant":                {early: invalidGrant, late: rateLimited("60"), kind: renewalHoldRefused, until: time.Hour, stood: true},
		"a shorter 429 after a longer one":         {early: rateLimited("1800"), late: rateLimited("60"), kind: renewalHoldRateLimited, until: 30 * time.Minute, stood: true, retryAfter: 1800},
		"invalid_client after a 429":               {early: rateLimited("60"), late: invalidClient, kind: renewalHoldRefused},
		"a longer 429 after a shorter one":         {early: rateLimited("60"), late: rateLimited("1800"), kind: renewalHoldRateLimited, until: 30 * time.Minute, retryAfter: 1800},
		"invalid_client after the shared path 429": {early: rateLimited("60"), late: invalidClient, shared: true, kind: renewalHoldRefused},
	} {
		t.Run(name, func(t *testing.T) {
			e := startMintEndpoint(t)
			first, key, now := heldManager(t, e)
			second := newDeviceTestManager(t, e.srv.URL)
			second.store, second.clock = first.store, first.clock
			start := *now

			e.answer = func(call int) (int, http.Header, string) {
				a := tc.early
				if call == 0 {
					// The session's mint is in flight: the other verdict
					// lands, and is written, before it is answered.
					var err error
					if tc.shared {
						_, err = first.AccessToken(context.Background())
					} else {
						_, err = first.SessionAccessToken(context.Background(), "fedcba9876543210fedcba9876543210", "")
					}
					assert.Error(t, err)
					a = tc.late
				}
				return a.status, a.header, a.body
			}

			_, err := second.SessionAccessToken(context.Background(), testSessionID, "coworker@box")
			require.Error(t, err)
			require.EqualValues(t, 2, e.calls.Load())
			assert.Contains(t, err.Error(), testSessionID)
			assert.Equal(t, tc.stood, errors.Is(err, errRenewalHeld), "whether the session was answered by the hold that stood")
			assert.Equal(t, tc.retryAfter, RetryAfter(err), "the wait the session is told")
			if tc.kind == renewalHoldRefused {
				assert.ErrorIs(t, err, ErrAgentCredentialRefused)
			}

			stored, loadErr := second.store.Load(key)
			require.NoError(t, loadErr)
			require.NotNil(t, stored.RenewalHold)
			assert.Equal(t, tc.kind, stored.RenewalHold.Kind)
			var until int64
			if tc.until != 0 {
				until = start.Add(tc.until).Unix()
			}
			assert.Equal(t, until, stored.RenewalHold.Until)

			// Past the shorter hold, nothing goes out from either launch.
			*now = now.Add(2 * time.Minute)
			_, err = second.SessionAccessToken(context.Background(), testSessionID, "coworker@box")
			require.Error(t, err)
			assert.ErrorIs(t, err, errRenewalHeld)
			assert.EqualValues(t, 2, e.calls.Load(), "the shorter hold was written over the longer one")
		})
	}
}

// TestSessionTokenNeedsAnAgentProfile: only an agent credential mints for
// itself; a person's login has no session to attribute and is not handed
// out in place of one.
func TestSessionTokenNeedsAnAgentProfile(t *testing.T) {
	as := startDeviceAS(t)
	m := newDeviceTestManager(t, as.srv.URL)
	storeAgent(t, m, &Credentials{AccessToken: "person-token", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour).Unix()})

	token, err := m.SessionAccessToken(context.Background(), testSessionID, "coworker@box")
	require.Error(t, err)
	assert.Empty(t, token)
	assert.Equal(t, output.CodeUsage, output.AsError(err).Code)
	assert.Empty(t, as.tokenCalls())
}

// TestSessionTokenValidatesItsIdentityBeforeSending: the same rules the
// server holds a session to are checked before anything goes out.
func TestSessionTokenValidatesItsIdentityBeforeSending(t *testing.T) {
	for name, tc := range map[string]struct{ id, label string }{
		"empty id":            {"", "coworker@box"},
		"short id":            {"0123456789abcdef", ""},
		"uppercase id":        {strings.ToUpper(testSessionID), ""},
		"non-hex id":          {"0123456789abcdef0123456789abcdeg", ""},
		"label too long":      {testSessionID, strings.Repeat("x", 101)},
		"blank label":         {testSessionID, "   "},
		"control in label":    {testSessionID, "coworker\nbox"},
		"bidi override":       {testSessionID, "coworker\u202ebox"},
		"line separator":      {testSessionID, "coworker\u2028box"},
		"invalid utf-8 label": {testSessionID, "coworker\xffbox"},
	} {
		t.Run(name, func(t *testing.T) {
			as := startDeviceAS(t)
			m := newDeviceTestManager(t, as.srv.URL)
			storeAgent(t, m, liveAgentCredential(as.srv.URL+"/oauth/token"))

			_, err := m.SessionAccessToken(context.Background(), tc.id, tc.label)
			require.Error(t, err)
			assert.Equal(t, output.CodeUsage, output.AsError(err).Code)
			assert.Empty(t, as.tokenCalls())
		})
	}

	assert.NoError(t, ValidateSession(testSessionID, strings.Repeat("é", 100)), "100 characters, not bytes")
	assert.NoError(t, ValidateSession(testSessionID, "claude · coworker"))
	// A character newer than this build's Unicode tables is accepted, as bc3
	// accepts it: refusing it would cost the launch its whole token over a
	// table the two sides need not share.
	assert.NoError(t, ValidateSession(testSessionID, "coworker \U0001FAE9"))
	assert.NoError(t, ValidateSession(testSessionID, "coworker\U000E0080box"))
}
