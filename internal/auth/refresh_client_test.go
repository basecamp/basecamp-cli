package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// A credential of a type this version has never heard of was written by a
// newer CLI. It is refused before anything is sent — not refreshed as
// Launchpad, which sent Launchpad's client to the newer login's token
// endpoint and drew invalid_client on every command — and the refusal says
// to upgrade.
func TestRefresh_UnknownOAuthTypeRefusesWithoutSending(t *testing.T) {
	for name, endpoint := range map[string]string{
		"with its token endpoint": "https://3.basecampapi.com/oauth/tokens",
		// Launchpad's migration would have filled one in.
		"without one": "",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BASECAMP_OAUTH_CLIENT_ID", "")
			t.Setenv("BASECAMP_OAUTH_CLIENT_SECRET", "")
			transport := &recordingTransport{}
			cfg := config.Default()
			cfg.ActiveProfile = "work"
			m := &Manager{cfg: cfg, httpClient: &http.Client{Transport: transport}, store: newTestStore(t, t.TempDir())}
			creds := &Credentials{
				AccessToken:   "old-tok",
				RefreshToken:  "old-ref",
				OAuthType:     "bc6\x1b[31m",
				TokenEndpoint: endpoint,
				ExpiresAt:     time.Now().Add(-time.Hour).Unix(),
			}
			key := m.credentialKey()
			require.NoError(t, m.store.Save(key, creds))

			err := m.Refresh(context.Background())
			var cliErr *output.Error
			require.ErrorAs(t, err, &cliErr)
			assert.Equal(t, output.CodeAuth, cliErr.Code)
			assert.Contains(t, cliErr.Message, "newer basecamp CLI")
			assert.Contains(t, cliErr.Message, `"bc6\x1b[31m"`, "the type is quoted, never written raw")
			assert.NotContains(t, cliErr.Message, "\x1b")
			assert.Equal(t, "Run: basecamp upgrade (or, to keep this version, log in again with it: basecamp auth login -P work)", cliErr.Hint)
			assert.False(t, transport.attempted.Load(), "an unknown type must be refused without any network request")

			stored, loadErr := m.store.Load(key)
			require.NoError(t, loadErr, "the newer CLI's credential is left for the newer CLI")
			assert.Equal(t, "bc6\x1b[31m", stored.OAuthType)
			assert.Equal(t, endpoint, stored.TokenEndpoint, "nothing is migrated onto it")

			refusal := m.RefreshRefusal(stored)
			require.Error(t, refusal)
			assert.Contains(t, output.AsError(refusal).Message, "newer basecamp CLI", "the report says what the next command will do")
		})
	}
}

// invalidClientRefresh is a token endpoint that answers every refresh with
// status and an invalid_client body, counting what reaches it, and a Manager
// whose active profile's credential of oauthType refreshes against it.
func invalidClientRefresh(t *testing.T, status int, oauthType string) (*Manager, string, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, `{"error":"invalid_client","error_description":"Client authentication failed"}`)
	}))
	t.Cleanup(srv.Close)

	cfg := config.Default()
	cfg.ActiveProfile = "work"
	cfg.BaseURL = srv.URL
	m := &Manager{cfg: cfg, httpClient: srv.Client(), store: newTestStore(t, t.TempDir())}
	key := m.credentialKey()
	require.NoError(t, m.store.Save(key, &Credentials{
		AccessToken:   "old-tok",
		RefreshToken:  "old-ref",
		OAuthType:     oauthType,
		TokenEndpoint: srv.URL + "/oauth/tokens",
		ExpiresAt:     time.Now().Add(-time.Hour).Unix(),
	}))
	return m, key, &hits
}

// invalid_client on a refresh is terminal: the token endpoint does not know
// the client, and asking again as the same client cannot change that. The
// first refusal tells the person to log in again; every later command, in
// this process or another, answers from the store and sends nothing — each
// resend is charged to the abuse tracker that blocks the address.
func TestRefresh_InvalidClientIsTerminal(t *testing.T) {
	for _, oauthType := range []string{oauthTypeBC5, oauthTypeLaunchpad} {
		t.Run(oauthType, func(t *testing.T) {
			t.Setenv("BASECAMP_OAUTH_CLIENT_ID", "")
			t.Setenv("BASECAMP_OAUTH_CLIENT_SECRET", "")
			t.Setenv("BASECAMP_TOKEN", "")
			m, key, hits := invalidClientRefresh(t, http.StatusUnauthorized, oauthType)

			err := m.Refresh(context.Background())
			var cliErr *output.Error
			require.ErrorAs(t, err, &cliErr)
			assert.Equal(t, output.CodeAuth, cliErr.Code)
			assert.Contains(t, cliErr.Message, "invalid_client")
			assert.Contains(t, cliErr.Message, "will not be sent again")
			assert.Equal(t, "Run: basecamp auth login -P work", cliErr.Hint)
			var refusal *basecamp.Error
			require.ErrorAs(t, err, &refusal, "the SDK's refusal stays in the chain")
			assert.Equal(t, int32(1), hits.Load())

			stored, loadErr := m.store.Load(key)
			require.NoError(t, loadErr, "the credential is kept: a hold is undone by a login, a delete is not undone")
			require.NotNil(t, stored.RenewalHold)
			assert.Equal(t, renewalHoldRefused, stored.RenewalHold.Kind)
			assert.Zero(t, stored.RenewalHold.Until, "invalid_client is never rechecked")

			for range 3 {
				_, err = m.AccessToken(context.Background())
				require.ErrorAs(t, err, &cliErr)
				assert.Equal(t, output.CodeAuth, cliErr.Code)
				assert.Contains(t, cliErr.Message, "remembered")
				assert.Equal(t, "Run: basecamp auth login -P work", cliErr.Hint)
			}
			fresh := &Manager{cfg: m.cfg, httpClient: m.httpClient, store: m.store}
			require.Error(t, fresh.Refresh(context.Background()))
			assert.Equal(t, int32(1), hits.Load(), "a refused client was sent again")

			status := m.RenewalHoldStatus(stored, m.RefreshRefusal(stored))
			require.NotNil(t, status, "the report shows the hold")
			assert.True(t, status.Permanent())

			// A login writes a new refresh token, which the hold is not about.
			stored.RefreshToken = "new-ref"
			require.NoError(t, m.store.Save(key, stored))
			require.Error(t, m.Refresh(context.Background()))
			assert.Equal(t, int32(2), hits.Load(), "a new login was held to the old one's refusal")
		})
	}
}

// A Launchpad refresh is sent as whatever client the environment names, so
// a refusal is held for that client only: fixing BASECAMP_OAUTH_CLIENT_ID
// sends the refresh again, without a login.
func TestRefresh_InvalidClientHoldIsForTheClientItRefused(t *testing.T) {
	t.Setenv("BASECAMP_OAUTH_CLIENT_ID", "wrong-client")
	t.Setenv("BASECAMP_OAUTH_CLIENT_SECRET", "wrong-secret")
	m, _, hits := invalidClientRefresh(t, http.StatusUnauthorized, oauthTypeLaunchpad)

	err := m.Refresh(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BASECAMP_OAUTH_CLIENT_ID/SECRET")
	require.Error(t, m.Refresh(context.Background()))
	assert.Equal(t, int32(1), hits.Load())

	t.Setenv("BASECAMP_OAUTH_CLIENT_ID", "")
	t.Setenv("BASECAMP_OAUTH_CLIENT_SECRET", "")
	require.Error(t, m.Refresh(context.Background()))
	assert.Equal(t, int32(2), hits.Load(), "a refresh as another client was held to this one's refusal")
}

// The status decides, as it does for an agent's mint: a server fault or a
// rate limit that happens to name invalid_client is no verdict on the
// client, and holds nothing of the kind.
func TestRefresh_InvalidClientFromAServerFaultIsNotTerminal(t *testing.T) {
	m, key, hits := invalidClientRefresh(t, http.StatusServiceUnavailable, oauthTypeBC5)

	err := m.Refresh(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "will not be sent again")
	stored, loadErr := m.store.Load(key)
	require.NoError(t, loadErr)
	assert.Nil(t, stored.RenewalHold)

	_ = m.Refresh(context.Background())
	assert.Equal(t, int32(2), hits.Load(), "a server fault is tried again")
}

// invalidClient is read off the typed refusal and its status, never the text.
func TestInvalidClient_ReadsTheTypedCodeAndStatus(t *testing.T) {
	assert.True(t, invalidClient(fmt.Errorf("wrapped: %w", &basecamp.Error{HTTPStatus: 401, OAuthError: "invalid_client"})))
	assert.True(t, invalidClient(&basecamp.Error{HTTPStatus: 400, OAuthError: "invalid_client"}))
	assert.False(t, invalidClient(&basecamp.Error{HTTPStatus: 429, OAuthError: "invalid_client"}))
	assert.False(t, invalidClient(&basecamp.Error{HTTPStatus: 500, OAuthError: "invalid_client"}))
	assert.False(t, invalidClient(&basecamp.Error{HTTPStatus: 401, OAuthError: "invalid_grant", Message: "invalid_client"}))
	assert.False(t, invalidClient(fmt.Errorf("token error: invalid_client")))
}
