package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/config"
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

// TestAgentRevokeIsRefusedAndKeepsTheCredential: `auth revoke` is the
// not-best-effort path — it refuses rather than discarding when it cannot
// revoke — and an agent self-token is one it will not try for, since the
// client that minted it can mint another. It must say so without
// contacting the authorization server and without losing the credential.
func TestAgentRevokeIsRefusedAndKeepsTheCredential(t *testing.T) {
	as := startDeviceAS(t)
	m := newDeviceTestManager(t, as.srv.URL)
	key := storeAgent(t, m, agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(time.Hour)))

	err := m.RevokeStored(context.Background())
	require.Error(t, err)
	assert.Equal(t, output.CodeUsage, output.AsError(err).Code)
	assert.Contains(t, err.Error(), "rotate the client secret")
	assert.Empty(t, as.revokeCalls(), "a revocation was sent for an agent self-token")

	creds, loadErr := m.store.Load(key)
	require.NoError(t, loadErr, "a refused revoke removed the credential")
	assert.Equal(t, "spent", creds.AccessToken)
	assert.Equal(t, "agent-secret", creds.ClientSecret)
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
// signs a PERSON in. An operator who followed it after an agent's
// credential failed would store their own under the agent's profile.
//
// The remedy is decided from the stored credential rather than at each
// error site, so it holds for failures that never went near agent.go — a
// poisoned token endpoint, a 401 the SDK classified — as well as a refused
// mint.
func TestAgentFailureNeverPointsAtTheInteractiveLogin(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(int) (int, string) {
		return http.StatusUnauthorized, `{"error":"invalid_client"}`
	}

	m := newDeviceTestManager(t, as.srv.URL)
	m.cfg.ActiveProfile = "clawdito"
	key := storeAgent(t, m, agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(-time.Minute)))

	wantAgentRemedy := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		hint := output.AsError(err).Hint
		assert.Contains(t, hint, "--with-client-credentials")
		assert.Contains(t, hint, "--client-id agent-client")
		assert.Contains(t, hint, "-P clawdito")
		assert.NotContains(t, hint, "Run: basecamp auth login -P")
	}

	_, err := m.AccessToken(context.Background())
	wantAgentRemedy(t, err)

	// A stored endpoint the CLI refuses to post to fails before agent.go
	// classifies anything, and must still not send the operator to the
	// interactive login.
	poisoned := agentCredential("http://evil.example/token", time.Now().Add(-time.Minute))
	require.NoError(t, m.store.Save(key, poisoned))
	_, err = m.AccessToken(context.Background())
	wantAgentRemedy(t, err)

	// And the hint an API 401 would be given, which is produced from the
	// credential alone.
	assert.Contains(t, m.LoginHint(), "--with-client-credentials")
	assert.Contains(t, m.LoginHint(), "Pipe the agent's client secret in:")
}

// TestAgentLoginCommandFillsInWhatIsMissing: a credential missing its
// client id is one of the things this command is suggested for, so the
// command must read as something to complete rather than something to
// paste and watch fail with the error that produced it.
func TestAgentLoginCommandFillsInWhatIsMissing(t *testing.T) {
	as := startDeviceAS(t)
	m := newDeviceTestManager(t, as.srv.URL)

	m.cfg.ActiveProfile = ""
	assert.Contains(t, m.agentLoginCommand("", ""), "--client-id <client-id>")
	assert.Contains(t, m.agentLoginCommand("", ""), "-P <profile>")

	m.cfg.ActiveProfile = "clawdito"
	assert.Contains(t, m.agentLoginCommand("a client", ""), "--client-id 'a client'")
	assert.Contains(t, m.agentLoginCommand("a client", ""), "-P clawdito")
}

// TestAgentMintRefusalRepeatsNothingTheServerWrote: the request carried a
// client secret, and a token endpoint is free to quote back what it
// rejected — in any escaping it likes. Nothing free-form from the response
// reaches the operator, so none of these spellings can matter.
func TestAgentMintRefusalRepeatsNothingTheServerWrote(t *testing.T) {
	type refusal struct{ body, want string }
	for name, c := range map[string]refusal{
		"in the description": {`{"error":"invalid_client","error_description":"Rejected client_secret=agent-secret for this client"}`, "invalid_client"},
		"percent-encoded":    {`{"error":"invalid_client","error_description":"got client_secret=agent%2Dsecret"}`, "invalid_client"},
		"html-escaped":       {`{"error":"invalid_client","error_description":"got agent&#45;secret"}`, "invalid_client"},
		// Not JSON, or not parseable as it: the status is all there is to
		// say.
		"in a raw body":               {`<html>bad request: client_secret=agent-secret</html>`, "HTTP 400"},
		"split by a control sequence": {"{\"error\":\"invalid_client\",\"error_description\":\"got agent-\u001b[31msecret\"}", "HTTP 400"},
		// An error code outside RFC 6749's vocabulary is server-chosen
		// text like any other, so it is not repeated either.
		"an invented error code": {`{"error":"you-are-holding-it-wrong <script>","error_description":"agent-secret"}`, "HTTP 400"},
	} {
		t.Run(name, func(t *testing.T) {
			as := startDeviceAS(t)
			as.token = func(int) (int, string) { return http.StatusBadRequest, c.body }

			m := newDeviceTestManager(t, as.srv.URL)
			storeAgent(t, m, agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(-time.Minute)))

			_, err := m.AccessToken(context.Background())
			require.Error(t, err)
			e := output.AsError(err)
			assert.NotContains(t, e.Message, "agent-secret")
			assert.NotContains(t, e.Message, "agent%2Dsecret")
			assert.NotContains(t, e.Message, "agent&#45;secret")
			assert.NotContains(t, e.Message, "Rejected")
			assert.NotContains(t, e.Message, "script")
			assert.Contains(t, e.Message, c.want)
		})
	}
}

// TestRefusedAgentLoginNeverReadsTheCredentialStore: a login's first mint
// happens before anything is stored, so rendering its refusal must not be
// the thing that reaches for the OS keyring — nor answer with the
// interactive login for an operation that is explicitly an agent's.
func TestRefusedAgentLoginNeverReadsTheCredentialStore(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(int) (int, string) {
		return http.StatusUnauthorized, `{"error":"invalid_client"}`
	}
	resource := startResourceServer(t, as.srv.URL)

	m := newDeviceTestManager(t, resource.URL)
	m.cfg.ActiveProfile = "clawdito"
	reads := &countingStore{}
	m.store.inner = reads
	m.store.initOnce.Do(func() {})

	_, err := m.LoginClientCredentials(context.Background(), ClientCredentialsOptions{
		ClientID:     "agent-client",
		ClientSecret: "wrong",
	})
	require.Error(t, err)
	assert.Zero(t, reads.loads, "a refused login read the credential store to render its error")

	hint := output.AsError(err).Hint
	assert.Contains(t, hint, "--with-client-credentials")
	assert.Contains(t, hint, "--client-id agent-client")
	assert.Contains(t, hint, "-P clawdito")
}

// countingStore is a credStore that holds nothing and counts what it was
// asked for.
type countingStore struct{ loads int }

func (c *countingStore) Load(string) ([]byte, error) {
	c.loads++
	return nil, errors.New("credentials not found for test")
}
func (c *countingStore) Save(string, []byte) error { return nil }
func (c *countingStore) Delete(string) error       { return nil }
func (c *countingStore) MigrateToKeyring() error   { return nil }
func (c *countingStore) UsingKeyring() bool        { return false }
func (c *countingStore) FallbackWarning() string   { return "" }

// TestMintedScopeMustBeOneTheCLICanStore: the response scope is stored
// with the credential, written into the profile entry, and printed. A
// server naming something else — or smuggling terminal controls through
// the field — is refused rather than persisted.
func TestMintedScopeMustBeOneTheCLICanStore(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(int) (int, string) {
		return http.StatusOK, `{"access_token":"minted","token_type":"bearer","expires_in":3600,"scope":"everything"}`
	}

	m := newDeviceTestManager(t, as.srv.URL)
	key := storeAgent(t, m, agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(-time.Minute)))

	_, err := m.AccessToken(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only those can be stored")

	stored, loadErr := m.store.Load(key)
	require.NoError(t, loadErr)
	assert.Equal(t, "spent", stored.AccessToken, "a token with an unstorable scope was applied anyway")
	assert.Equal(t, scopeFull, stored.Scope)
}

// TestCanceledTokenImportStoresNothing: --with-token waits for the
// credential's lock like everything else, and a person who stops it there
// must not get the token stored the moment the lock frees up.
func TestCanceledTokenImportStoresNothing(t *testing.T) {
	as := startDeviceAS(t)
	m := newDeviceTestManager(t, as.srv.URL)
	m.cfg.ActiveProfile = "bot"

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := m.ImportToken(ctx, "bc_at_secret", scopeFull, "1", "bot@example.com", time.Time{})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	_, loadErr := m.store.Load("profile:bot")
	assert.ErrorIs(t, loadErr, ErrNoCredential)
}

// TestUnstorableScopeIsNeverRepeated: the scope is another field the
// server controls on a request that carried the secret, so the value is
// refused without being quoted back.
func TestUnstorableScopeIsNeverRepeated(t *testing.T) {
	for name, scope := range map[string]string{
		"the secret verbatim":                    "agent-secret",
		"the secret split by a control sequence": `agent-\u001b[31msecret`,
	} {
		t.Run(name, func(t *testing.T) {
			as := startDeviceAS(t)
			as.token = func(int) (int, string) {
				return http.StatusOK, fmt.Sprintf(
					`{"access_token":"minted","token_type":"bearer","expires_in":3600,"scope":"%s"}`, scope)
			}

			m := newDeviceTestManager(t, as.srv.URL)
			storeAgent(t, m, agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(-time.Minute)))

			_, err := m.AccessToken(context.Background())
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "agent-secret")
			assert.Contains(t, err.Error(), "only those can be stored")
		})
	}
}

// TestMintedTokenMustBeABearerCredential: every request this CLI makes
// sends the token as a Bearer credential, so a response naming another
// scheme is not a successful login — it is one that would be stored and
// then fail every command.
func TestMintedTokenMustBeABearerCredential(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(int) (int, string) {
		return http.StatusOK, `{"access_token":"minted","token_type":"DPoP","expires_in":3600}`
	}

	m := newDeviceTestManager(t, as.srv.URL)
	key := storeAgent(t, m, agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(-time.Minute)))

	_, err := m.AccessToken(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only sends Bearer credentials")
	assert.NotContains(t, err.Error(), "DPoP", "the server's value was repeated back")

	stored, loadErr := m.store.Load(key)
	require.NoError(t, loadErr)
	assert.Equal(t, "spent", stored.AccessToken, "a token the CLI cannot send was stored")

	// An omitted type is Bearer, and a differently-cased one is too.
	for _, body := range []string{
		`{"access_token":"minted","expires_in":3600}`,
		`{"access_token":"minted","token_type":"bearer","expires_in":3600}`,
		`{"access_token":"minted","token_type":"Bearer","expires_in":3600}`,
	} {
		as.token = func(int) (int, string) { return http.StatusOK, body }
		require.NoError(t, m.store.Save(key, agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(-time.Minute))))
		token, tokenErr := m.AccessToken(context.Background())
		require.NoError(t, tokenErr, body)
		assert.Equal(t, "minted", token)
	}
}

// TestAgentRetryCommandCreatesTheProfileItNeeds: a refused first mint
// registers no profile, and the login that would fix it creates one — which
// needs the account it addresses. Handing over a command that fails with
// "profile does not exist" would be worse than saying nothing.
func TestAgentRetryCommandCreatesTheProfileItNeeds(t *testing.T) {
	as := startDeviceAS(t)
	m := newDeviceTestManager(t, as.srv.URL)
	m.cfg.ActiveProfile = "clawdito"

	// Only an account THIS invocation supplied is named: a profile with
	// none of its own inherits whatever the global or repo config set,
	// and the login refuses to bind that — so suggesting it would be
	// suggesting a command that fails, or one that succeeds wrongly.
	m.cfg.AccountID = "999"
	m.cfg.Sources = map[string]string{"account_id": string(config.SourceFlag)}
	assert.Contains(t, m.agentLoginCommand("c", ""), "--account 999")

	m.cfg.Sources = map[string]string{"account_id": string(config.SourceEnv)}
	assert.Contains(t, m.agentLoginCommand("c", ""), "--account 999")

	m.cfg.Sources = map[string]string{"account_id": string(config.SourceGlobal)}
	assert.Contains(t, m.agentLoginCommand("c", ""), "--account <account-id>")

	m.cfg.AccountID = ""
	m.cfg.Sources = map[string]string{}
	assert.Contains(t, m.agentLoginCommand("c", ""), "--account <account-id>")

	// An entry that exists WITHOUT an account is the same situation: the
	// login binds one and refuses without it.
	m.cfg.Profiles = map[string]*config.ProfileConfig{"clawdito": {BaseURL: as.srv.URL}}
	assert.Contains(t, m.agentLoginCommand("c", ""), "--account <account-id>")

	// A profile that exists and is already bound needs nothing added.
	m.cfg.Profiles = map[string]*config.ProfileConfig{"clawdito": {BaseURL: as.srv.URL, AccountID: "999"}}
	assert.NotContains(t, m.agentLoginCommand("c", ""), "--account")
}

// TestOnlyACredentialRefusalAsksForTheSecretAgain: telling an automated
// caller to fetch its client secret again is advice that only helps when
// the credentials are actually the problem. A proxy's bare 400, a 408, a
// 404 at a misconfigured endpoint — none of those are verdicts on the
// client, and none of them should send it looking for a new secret.
func TestOnlyACredentialRefusalAsksForTheSecretAgain(t *testing.T) {
	type refusal struct {
		status int
		body   string
		code   string
	}
	for name, c := range map[string]refusal{
		"a named client refusal":         {http.StatusBadRequest, `{"error":"invalid_client"}`, output.CodeAuth},
		"a named grant refusal":          {http.StatusBadRequest, `{"error":"invalid_grant"}`, output.CodeAuth},
		"a client barred from the grant": {http.StatusBadRequest, `{"error":"unauthorized_client"}`, output.CodeAPI},
		"a refused scope":                {http.StatusBadRequest, `{"error":"invalid_scope"}`, output.CodeAPI},
		"a bare 401":                     {http.StatusUnauthorized, `nothing useful`, output.CodeAuth},
		"a bare 403":                     {http.StatusForbidden, `nothing useful`, output.CodeAuth},
		// A named reason decides; the status does not get a second vote,
		// or the narrowing of clientRefusalCodes would mean nothing.
		"a 403 naming a policy refusal":     {http.StatusForbidden, `{"error":"access_denied"}`, output.CodeAPI},
		"a 401 barring the client":          {http.StatusUnauthorized, `{"error":"unauthorized_client"}`, output.CodeAPI},
		"a 401 naming a credential refusal": {http.StatusUnauthorized, `{"error":"invalid_client"}`, output.CodeAuth},
		"a malformed request":               {http.StatusBadRequest, `{"error":"invalid_request"}`, output.CodeAPI},
		"a grant the server lacks":          {http.StatusBadRequest, `{"error":"unsupported_grant_type"}`, output.CodeAPI},
		"a proxy's bare 400":                {http.StatusBadRequest, `<html>Bad Request</html>`, output.CodeAPI},
		"a timeout in front of it":          {http.StatusRequestTimeout, `<html>Request Timeout</html>`, output.CodeAPI},
		"a misconfigured endpoint":          {http.StatusNotFound, `<html>Not Found</html>`, output.CodeAPI},
		"a rate limit":                      {http.StatusTooManyRequests, `{"error":"slow_down"}`, output.CodeRateLimit},
		// The STATUS decides: a server saying invalid_client alongside a
		// 429 or a 503 is saying two things at once, and the status is the
		// one that says what to do next.
		"a rate limit naming the client":   {http.StatusTooManyRequests, `{"error":"invalid_client"}`, output.CodeRateLimit},
		"a server fault naming the client": {http.StatusServiceUnavailable, `{"error":"invalid_client"}`, output.CodeAPI},
		"a server fault":                   {http.StatusBadGateway, `<html>Bad Gateway</html>`, output.CodeAPI},
	} {
		t.Run(name, func(t *testing.T) {
			as := startDeviceAS(t)
			as.token = func(int) (int, string) { return c.status, c.body }

			m := newDeviceTestManager(t, as.srv.URL)
			storeAgent(t, m, agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(-time.Minute)))

			_, err := m.AccessToken(context.Background())
			require.Error(t, err)
			e := output.AsError(err)
			assert.Equal(t, c.code, e.Code)
			if c.code != output.CodeAuth {
				assert.NotContains(t, e.Hint, "--with-client-credentials",
					"a response that is not a verdict on the client asked for its secret again")
			}
		})
	}
}

// TestARateLimitedMintKeepsItsRetryAfter: a token endpoint that names a
// credential refusal while rate limiting must not cost the caller the
// delay it was told to wait.
func TestARateLimitedMintKeepsItsRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":"invalid_client"}`)
	}))
	defer srv.Close()

	m := newDeviceTestManager(t, srv.URL)
	storeAgent(t, m, agentCredential(srv.URL+"/oauth/tokens", time.Now().Add(-time.Minute)))

	_, err := m.AccessToken(context.Background())
	require.Error(t, err)
	e := output.AsError(err)
	assert.Equal(t, output.CodeRateLimit, e.Code)
	assert.True(t, e.Retryable)
	assert.Contains(t, e.Hint, "30 seconds")
	assert.NotContains(t, e.Hint, "--with-client-credentials")
}

// TestARecoveryCommandKeepsTheScope: a read-only agent told to
// re-authenticate without --scope would come back with full access, or be
// refused for asking for more than its client is allowed.
func TestARecoveryCommandKeepsTheScope(t *testing.T) {
	as := startDeviceAS(t)
	m := newDeviceTestManager(t, as.srv.URL)
	m.cfg.ActiveProfile = "clawdito"
	m.cfg.Profiles = map[string]*config.ProfileConfig{"clawdito": {BaseURL: as.srv.URL, AccountID: "999"}}

	assert.Contains(t, m.agentLoginCommand("c", scopeRead), "--scope read")
	// full is the default; naming it says nothing.
	assert.NotContains(t, m.agentLoginCommand("c", scopeFull), "--scope")

	// And through a real refusal, from the credential's stored scope.
	as.token = func(int) (int, string) {
		return http.StatusUnauthorized, `{"error":"invalid_client"}`
	}
	creds := agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(-time.Minute))
	creds.Scope = scopeRead
	storeAgent(t, m, creds)

	_, err := m.AccessToken(context.Background())
	require.Error(t, err)
	assert.Contains(t, output.AsError(err).Hint, "--scope read")
}

// TestATokenTheServerCallsExpiredIsNotGivenAnHour: an explicit
// "expires_in": 0 is the server saying the token it just issued is already
// spent, which is the opposite of an absent field. Assuming the documented
// hour for it would serve a retired token for an hour.
func TestATokenTheServerCallsExpiredIsNotGivenAnHour(t *testing.T) {
	for name, body := range map[string]string{
		"zero":     `{"access_token":"minted","token_type":"bearer","expires_in":0}`,
		"negative": `{"access_token":"minted","token_type":"bearer","expires_in":-30}`,
	} {
		t.Run(name, func(t *testing.T) {
			as := startDeviceAS(t)
			as.token = func(int) (int, string) { return http.StatusOK, body }

			m := newDeviceTestManager(t, as.srv.URL)
			key := storeAgent(t, m, agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(-time.Minute)))

			_, err := m.AccessToken(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "already expired")

			stored, loadErr := m.store.Load(key)
			require.NoError(t, loadErr)
			assert.Equal(t, "spent", stored.AccessToken, "an already-expired token was stored")
		})
	}
}

// TestAnAbsurdTokenLifetimeIsCapped: the conversion to a Duration
// overflows outright past a few hundred years, and nothing about a
// one-hour grant should be planned around a lifetime past a day.
func TestAnAbsurdTokenLifetimeIsCapped(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(int) (int, string) {
		return http.StatusOK, `{"access_token":"minted","token_type":"bearer","expires_in":999999999999}`
	}

	m := newDeviceTestManager(t, as.srv.URL)
	key := storeAgent(t, m, agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(-time.Minute)))

	_, err := m.AccessToken(context.Background())
	require.NoError(t, err)

	stored, loadErr := m.store.Load(key)
	require.NoError(t, loadErr)
	assert.InDelta(t, time.Now().Add(maxAgentTokenLifetime).Unix(), stored.ExpiresAt, 60)
}
