package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// agentCredential is a stored agent profile: a client id and secret, a
// spent access token, and no refresh token — which is the whole point.
func agentCredential(tokenEndpoint string, expiresAt time.Time) *Credentials {
	return &Credentials{
		AccessToken:   "spent",
		OAuthType:     oauthTypeAgent,
		ClientID:      "agent-client",
		ClientSecret:  "agent-secret",
		TokenEndpoint: tokenEndpoint,
		Scope:         scopeFull,
		Resource:      AgentResourceURNPrefix + "42",
		ExpiresAt:     expiresAt.Unix(),
	}
}

// storeAgent puts an agent credential under the manager's key and returns
// the key.
func storeAgent(t *testing.T, m *Manager, creds *Credentials) string {
	t.Helper()
	key := m.credentialKey()
	require.NoError(t, m.store.Save(key, creds))
	return key
}

// TestAgentCredentialMintsInsteadOfRefreshing: an agent has no refresh
// token, so an expired credential must produce a new one from its client
// credentials rather than be refused with "No refresh token available".
func TestAgentCredentialMintsInsteadOfRefreshing(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(int) (int, string) {
		return http.StatusOK, `{"access_token":"minted-1","token_type":"bearer","expires_in":3600,"resource":"urn:bc:agent:42","scope":"full"}`
	}

	m := newDeviceTestManager(t, as.srv.URL)
	key := storeAgent(t, m, agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(-time.Minute)))

	token, err := m.AccessToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "minted-1", token)

	calls := as.tokenCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "client_credentials", calls[0].Get("grant_type"))
	assert.Equal(t, "agent-client", calls[0].Get("client_id"))
	assert.Equal(t, "agent-secret", calls[0].Get("client_secret"))
	assert.Equal(t, scopeFull, calls[0].Get("scope"))
	// The binding is echoed, as a refresh echoes it: re-minting for one
	// agent must not silently widen to another.
	assert.Equal(t, AgentResourceURNPrefix+"42", calls[0].Get("resource"))
	assert.Empty(t, calls[0].Get("refresh_token"))

	stored, err := m.store.Load(key)
	require.NoError(t, err)
	assert.Equal(t, "minted-1", stored.AccessToken)
	assert.Empty(t, stored.RefreshToken, "a self-token has no refresh token to store")
	// The client that minted it is kept — it is the durable credential.
	assert.Equal(t, "agent-client", stored.ClientID)
	assert.Equal(t, "agent-secret", stored.ClientSecret)
	assert.InDelta(t, time.Now().Add(time.Hour).Unix(), stored.ExpiresAt, 60)
}

// TestAgentCredentialMintsOnceUntilItExpires: a minted token is served
// as-is until it enters the refresh window, then re-minted.
func TestAgentCredentialMintsOnceUntilItExpires(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(call int) (int, string) {
		return http.StatusOK, fmt.Sprintf(
			`{"access_token":"minted-%d","token_type":"bearer","expires_in":3600}`, call+1)
	}

	m := newDeviceTestManager(t, as.srv.URL)
	key := storeAgent(t, m, agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(-time.Minute)))

	first, err := m.AccessToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "minted-1", first)

	second, err := m.AccessToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "minted-1", second)
	assert.Len(t, as.tokenCalls(), 1, "a live self-token was re-minted")

	// Age the credential — an hour on, both the expiry it was minted with
	// and the renewal moment ahead of it have gone by — and it mints again.
	stored, err := m.store.Load(key)
	require.NoError(t, err)
	stored.ExpiresAt = time.Now().Add(RefreshWindow / 2).Unix()
	stored.RenewAfter = time.Now().Add(-time.Minute).Unix()
	require.NoError(t, m.store.Save(key, stored))

	third, err := m.AccessToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "minted-2", third)
	assert.Len(t, as.tokenCalls(), 2)
}

// TestAgentTokenWithoutExpiryTakesTheAssumedLifetime: a token response with
// no expires_in must not be stored as non-expiring. Zero is how every other
// path spells "never renew", and a self-token the server retires within the
// hour would then fail every request with nothing arranging a fix.
func TestAgentTokenWithoutExpiryTakesTheAssumedLifetime(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(int) (int, string) {
		return http.StatusOK, `{"access_token":"minted","token_type":"bearer"}`
	}

	m := newDeviceTestManager(t, as.srv.URL)
	key := storeAgent(t, m, agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(-time.Minute)))

	_, err := m.AccessToken(context.Background())
	require.NoError(t, err)

	stored, err := m.store.Load(key)
	require.NoError(t, err)
	assert.NotZero(t, stored.ExpiresAt, "a self-token was stored as non-expiring")
	assert.InDelta(t, time.Now().Add(defaultAgentTokenLifetime).Unix(), stored.ExpiresAt, 60)
}

// TestFailedMintKeepsTheStoredCredential: nothing is written unless a mint
// succeeded, and nothing is deleted. A server having a bad minute must
// leave the client credentials in place for the next command to try again
// with — not empty the store and send the operator to a login.
func TestFailedMintKeepsTheStoredCredential(t *testing.T) {
	for name, response := range map[string]func(int) (int, string){
		"server error": func(int) (int, string) {
			return http.StatusInternalServerError, `{"error":"server_error"}`
		},
		"refused client": func(int) (int, string) {
			return http.StatusUnauthorized, `{"error":"invalid_client","error_description":"unknown client"}`
		},
		"no access token": func(int) (int, string) {
			return http.StatusOK, `{"token_type":"bearer","expires_in":3600}`
		},
	} {
		t.Run(name, func(t *testing.T) {
			as := startDeviceAS(t)
			as.token = response

			m := newDeviceTestManager(t, as.srv.URL)
			original := agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(-time.Minute))
			key := storeAgent(t, m, original)

			_, err := m.AccessToken(context.Background())
			require.Error(t, err)

			stored, loadErr := m.store.Load(key)
			require.NoError(t, loadErr, "a failed mint removed the credential")
			assert.Equal(t, original.AccessToken, stored.AccessToken)
			assert.Equal(t, original.ClientID, stored.ClientID)
			assert.Equal(t, original.ClientSecret, stored.ClientSecret)
			assert.Equal(t, original.ExpiresAt, stored.ExpiresAt)
		})
	}
}

// TestRefusedMintIsAnAuthErrorAndAServerFaultIsNot: the operator acts
// differently on the two, so they must not arrive as the same class.
func TestRefusedMintIsAnAuthErrorAndAServerFaultIsNot(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(int) (int, string) {
		return http.StatusUnauthorized, `{"error":"invalid_client","error_description":"unknown client"}`
	}

	m := newDeviceTestManager(t, as.srv.URL)
	storeAgent(t, m, agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(-time.Minute)))

	_, err := m.AccessToken(context.Background())
	require.Error(t, err)
	assert.Equal(t, output.CodeAuth, output.AsError(err).Code)
	assert.Contains(t, err.Error(), "invalid_client")
	assert.Contains(t, err.Error(), "unknown client")

	as.token = func(int) (int, string) { return http.StatusBadGateway, `no json here` }
	_, err = m.AccessToken(context.Background())
	require.Error(t, err)
	assert.Equal(t, output.CodeAPI, output.AsError(err).Code)
}

// TestAgentMintRefusesAnUnsafeTokenEndpoint: the endpoint is a persisted
// value and this request carries a client secret, so a poisoned store must
// not be able to post it anywhere.
func TestAgentMintRefusesAnUnsafeTokenEndpoint(t *testing.T) {
	for name, endpoint := range map[string]string{
		"plaintext":   "http://evil.example/token",
		"userinfo":    "https://someone@evil.example/token",
		"no hostname": "https://:3000/token",
	} {
		t.Run(name, func(t *testing.T) {
			as := startDeviceAS(t)
			m := newDeviceTestManager(t, as.srv.URL)
			storeAgent(t, m, agentCredential(endpoint, time.Now().Add(-time.Minute)))

			_, err := m.AccessToken(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "token endpoint")
			assert.Empty(t, as.tokenCalls())
		})
	}
}

// TestAgentMintNeedsItsClient: a credential missing the client that mints
// its tokens is refused before anything is sent, and the report says so
// ahead of time rather than discovering it mid-command.
func TestAgentMintNeedsItsClient(t *testing.T) {
	as := startDeviceAS(t)
	m := newDeviceTestManager(t, as.srv.URL)

	for name, mangle := range map[string]func(*Credentials){
		"no client id":     func(c *Credentials) { c.ClientID = "" },
		"no client secret": func(c *Credentials) { c.ClientSecret = "" },
		"no token endpoint": func(c *Credentials) {
			c.TokenEndpoint = ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			creds := agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(-time.Minute))
			mangle(creds)

			refusal := m.RefreshRefusal(creds)
			require.Error(t, refusal)
			assert.Equal(t, output.CodeAuth, output.AsError(refusal).Code)

			storeAgent(t, m, creds)
			_, err := m.AccessToken(context.Background())
			require.Error(t, err)
			assert.Empty(t, as.tokenCalls())
		})
	}
}

// TestAgentCredentialIsRenewableInTheStatusReport: an agent credential has
// no refresh token, and the report must not read that as "cannot be
// renewed" — it renews by minting.
func TestAgentCredentialIsRenewableInTheStatusReport(t *testing.T) {
	as := startDeviceAS(t)
	m := newDeviceTestManager(t, as.srv.URL)
	creds := agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(time.Hour))
	assert.Empty(t, creds.RefreshToken)
	assert.NoError(t, m.RefreshRefusal(creds))
}

// TestLoginClientCredentialsStoresTheClientWithTheToken: the login proves
// the client credentials by minting once, and stores them alongside, since
// they are what every later renewal needs.
func TestLoginClientCredentialsStoresTheClientWithTheToken(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(int) (int, string) {
		return http.StatusOK, `{"access_token":"minted","token_type":"bearer","expires_in":3600,"resource":"urn:bc:agent:42","scope":"full"}`
	}
	resource := startResourceServer(t, as.srv.URL)

	m := newDeviceTestManager(t, resource.URL)
	m.cfg.ActiveProfile = "agent"

	result, err := m.LoginClientCredentials(context.Background(), ClientCredentialsOptions{
		ClientID:     "agent-client",
		ClientSecret: "agent-secret",
	})
	require.NoError(t, err)
	assert.Equal(t, oauthTypeAgent, result.OAuthType)
	assert.Equal(t, scopeFull, result.Scope)

	calls := as.tokenCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "client_credentials", calls[0].Get("grant_type"))
	// No stored binding yet, so none is asserted: the server chooses it.
	assert.Empty(t, calls[0].Get("resource"))

	stored, err := m.store.Load("profile:agent")
	require.NoError(t, err)
	assert.Equal(t, oauthTypeAgent, stored.OAuthType)
	assert.Equal(t, "minted", stored.AccessToken)
	assert.Equal(t, "agent-client", stored.ClientID)
	assert.Equal(t, "agent-secret", stored.ClientSecret)
	assert.Equal(t, AgentResourceURNPrefix+"42", stored.Resource)
	assert.Equal(t, as.srv.URL+"/oauth/token", stored.TokenEndpoint)
	assert.Equal(t, as.srv.URL, stored.Issuer)
	assert.Empty(t, stored.RefreshToken)
}

// TestLoginClientCredentialsStoresNothingWhenTheMintIsRefused: the mint is
// what proves the client, so a refused one must leave no credential behind.
func TestLoginClientCredentialsStoresNothingWhenTheMintIsRefused(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(int) (int, string) {
		return http.StatusUnauthorized, `{"error":"invalid_client"}`
	}
	resource := startResourceServer(t, as.srv.URL)

	m := newDeviceTestManager(t, resource.URL)
	m.cfg.ActiveProfile = "agent"

	_, err := m.LoginClientCredentials(context.Background(), ClientCredentialsOptions{
		ClientID:     "agent-client",
		ClientSecret: "wrong",
	})
	require.Error(t, err)
	_, loadErr := m.store.Load("profile:agent")
	assert.ErrorIs(t, loadErr, ErrNoCredential)
}

// TestLoginClientCredentialsRefusesTheLaunchpadFallback: a discovery that
// fell back to Launchpad is not a server with agent principals, and the
// request would put a client secret on the wire on the strength of a guess.
func TestLoginClientCredentialsRefusesTheLaunchpadFallback(t *testing.T) {
	// No protected-resource metadata => the soft Launchpad fallback.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	lp, lpCalls := countingServer(t)
	t.Setenv("BASECAMP_LAUNCHPAD_URL", lp.URL)

	m := newDeviceTestManager(t, srv.URL)
	m.cfg.ActiveProfile = "agent"

	_, err := m.LoginClientCredentials(context.Background(), ClientCredentialsOptions{
		ClientID:     "agent-client",
		ClientSecret: "agent-secret",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no agent grant")
	assert.Zero(t, *lpCalls, "a client secret was sent to the Launchpad fallback")
}

// TestLoginClientCredentialsNeedsBoth: half a client is a usage error, not
// a request.
func TestLoginClientCredentialsNeedsBoth(t *testing.T) {
	as := startDeviceAS(t)
	m := newDeviceTestManager(t, as.srv.URL)

	for name, opts := range map[string]ClientCredentialsOptions{
		"no id":     {ClientSecret: "agent-secret"},
		"no secret": {ClientID: "agent-client"},
		"bad scope": {ClientID: "a", ClientSecret: "b", Scope: "everything"},
		"nothing":   {},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := m.LoginClientCredentials(context.Background(), opts)
			require.Error(t, err)
			assert.Equal(t, output.CodeUsage, output.AsError(err).Code)
			assert.Empty(t, as.tokenCalls())
		})
	}
}

// TestAgentMintRefusesARedirect: the token request carries a client secret,
// so a 3xx from the token endpoint must fail rather than be replayed to
// wherever it points.
func TestAgentMintRefusesARedirect(t *testing.T) {
	var target string
	sink, sinkCalls := countingServer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	target = sink.URL + "/oauth/token"

	m := newDeviceTestManager(t, srv.URL)
	// The lane client is what refuses the redirect, so this manager must
	// build one rather than take the test's injected client.
	m.httpClient = nil
	storeAgent(t, m, agentCredential(srv.URL+"/oauth/token", time.Now().Add(-time.Minute)))

	_, err := m.AccessToken(context.Background())
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "redirect")
	assert.Zero(t, *sinkCalls, "the client secret followed a redirect")
}

// TestAgentLogoutSaysRotatingTheSecretIsTheRemedy: revoking one self-token
// accomplishes nothing while the client that minted it can mint another.
func TestAgentLogoutSaysRotatingTheSecretIsTheRemedy(t *testing.T) {
	as := startDeviceAS(t)
	m := newDeviceTestManager(t, as.srv.URL)
	key := storeAgent(t, m, agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(time.Hour)))

	result, err := m.LogoutCredential(context.Background(), key, "")
	require.NoError(t, err)
	assert.Equal(t, RevokeSkippedAgent, result.Skipped)
	assert.False(t, result.Revoked)

	_, loadErr := m.store.Load(key)
	assert.ErrorIs(t, loadErr, ErrNoCredential)
}

// TestShortLivedAgentTokenIsNotRenewedOnEveryCommand: a token whose whole
// lifetime is inside the five-minute refresh window would, on the default
// margin, be back inside the renewal window the instant it was minted —
// every command would mint again, and twenty concurrent ones would take
// twenty grants in turn instead of sharing one. The margin scales down with
// the lifetime instead.
func TestShortLivedAgentTokenIsNotRenewedOnEveryCommand(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(call int) (int, string) {
		return http.StatusOK, fmt.Sprintf(
			`{"access_token":"minted-%d","token_type":"bearer","expires_in":120}`, call+1)
	}

	m := newDeviceTestManager(t, as.srv.URL)
	key := storeAgent(t, m, agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(-time.Minute)))

	first, err := m.AccessToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "minted-1", first)

	stored, err := m.store.Load(key)
	require.NoError(t, err)
	assert.False(t, needsRenewal(stored), "a freshly minted two-minute token was already due for renewal")
	// Renewed with 30 seconds left — a quarter of the lifetime — not five
	// minutes before an expiry only two minutes away.
	assert.InDelta(t, stored.ExpiresAt-30, stored.RenewAfter, 5)

	for range 3 {
		token, tokenErr := m.AccessToken(context.Background())
		require.NoError(t, tokenErr)
		assert.Equal(t, "minted-1", token)
	}
	assert.Len(t, as.tokenCalls(), 1, "a live short-lived token was re-minted on every command")
}

// TestAgentRenewAfterNeverOutlivesTheToken: the margin shrinks with the
// lifetime but never inverts, and a zero or negative lifetime still asks to
// be renewed rather than pinning a dead token.
func TestAgentRenewAfterNeverOutlivesTheToken(t *testing.T) {
	now := time.Now()
	for name, lifetime := range map[string]time.Duration{
		"an hour":      time.Hour,
		"ten minutes":  10 * time.Minute,
		"two minutes":  2 * time.Minute,
		"ten seconds":  10 * time.Second,
		"already gone": -time.Minute,
	} {
		t.Run(name, func(t *testing.T) {
			expiry := now.Add(lifetime)
			renewAfter := agentRenewAfter(now, expiry)
			assert.False(t, renewAfter.After(expiry), "the renewal moment is past the expiry")
			if lifetime > 0 {
				assert.False(t, renewAfter.Before(now), "a token was due for renewal before it was minted")
			}
		})
	}
}

// TestRateLimitedMintStaysRetryable: a 429 from the token endpoint says the
// caller is early, not that the client is wrong. Reporting it as
// "authenticate again" would send an automated caller into a re-login loop
// against a server already asking it to slow down.
func TestRateLimitedMintStaysRetryable(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(int) (int, string) {
		return http.StatusTooManyRequests, `{"error":"slow_down"}`
	}

	m := newDeviceTestManager(t, as.srv.URL)
	storeAgent(t, m, agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(-time.Minute)))

	_, err := m.AccessToken(context.Background())
	require.Error(t, err)
	e := output.AsError(err)
	assert.Equal(t, output.CodeRateLimit, e.Code)
	assert.True(t, e.Retryable)
}

// TestAgentFailureNeverPointsAtTheInteractiveLogin: the default remedy for
// an auth_required error is `basecamp auth login -P <profile>`, which
// signs a PERSON in. An operator who followed it after a refused mint
// would replace the agent's credential with their own.
func TestAgentFailureNeverPointsAtTheInteractiveLogin(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(int) (int, string) {
		return http.StatusUnauthorized, `{"error":"invalid_client"}`
	}

	m := newDeviceTestManager(t, as.srv.URL)
	m.cfg.ActiveProfile = "clawdito"
	storeAgent(t, m, agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(-time.Minute)))

	_, err := m.AccessToken(context.Background())
	require.Error(t, err)
	hint := output.AsError(err).Hint
	assert.Contains(t, hint, "--with-client-credentials")
	assert.Contains(t, hint, "--client-id agent-client")
	assert.Contains(t, hint, "-P clawdito")
	assert.NotEqual(t, m.LoginCommand(), hint, "the remedy is the interactive login")

	// The same for a credential that cannot mint at all.
	broken := agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(-time.Minute))
	broken.ClientSecret = ""
	hint = output.AsError(m.RefreshRefusal(broken)).Hint
	assert.Contains(t, hint, "--with-client-credentials")
}
