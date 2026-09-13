package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/oauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// bc5Credentials is a device-flow credential minted by as.
func bc5Credentials(as *deviceAS) *Credentials {
	return &Credentials{
		AccessToken:   "dev-tok",
		RefreshToken:  "dev-ref",
		OAuthType:     "bc5",
		TokenEndpoint: as.srv.URL + "/oauth/token",
		Issuer:        as.srv.URL,
		Scope:         "full",
		ExpiresAt:     time.Now().Unix() + 3600,
	}
}

func assertRevoked(t *testing.T, calls []url.Values, tokens ...string) {
	t.Helper()
	require.Len(t, calls, len(tokens))
	for i, token := range tokens {
		hint := "access_token"
		if token == "dev-ref" {
			hint = "refresh_token"
		}
		assert.Equal(t, token, calls[i].Get("token"))
		assert.Equal(t, hint, calls[i].Get("token_type_hint"))
		assert.Equal(t, bc5ClientID, calls[i].Get("client_id"))
		assert.Empty(t, calls[i].Get("client_secret"), "the public client has no secret")
	}
}

func TestRevoke_PostsRefreshThenAccessAsThePublicClient(t *testing.T) {
	as := startDeviceAS(t)
	m := newDeviceTestManager(t, as.srv.URL)

	require.NoError(t, m.Revoke(context.Background(), bc5Credentials(as)))
	assertRevoked(t, as.revokeCalls(), "dev-ref", "dev-tok")
}

func TestRevoke_DerivesTheIssuerFromTheTokenEndpoint(t *testing.T) {
	as := startDeviceAS(t)
	m := newDeviceTestManager(t, as.srv.URL)
	creds := bc5Credentials(as)
	creds.Issuer = ""

	require.NoError(t, m.Revoke(context.Background(), creds))
	assertRevoked(t, as.revokeCalls(), "dev-ref", "dev-tok")
}

func TestRevoke_SendsOnlyTheTokensItHolds(t *testing.T) {
	as := startDeviceAS(t)
	m := newDeviceTestManager(t, as.srv.URL)
	creds := bc5Credentials(as)
	creds.RefreshToken = ""

	require.NoError(t, m.Revoke(context.Background(), creds))
	assertRevoked(t, as.revokeCalls(), "dev-tok")

	creds.AccessToken = ""
	err := m.Revoke(context.Background(), creds)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no token")
}

func TestRevoke_FailsWhenTheServerAdvertisesNoEndpoint(t *testing.T) {
	as := startDeviceAS(t)
	as.metadata = func() string {
		return fmt.Sprintf(`{"issuer": %q, "token_endpoint": %q}`, as.srv.URL, as.srv.URL+"/oauth/token")
	}
	m := newDeviceTestManager(t, as.srv.URL)

	err := m.Revoke(context.Background(), bc5Credentials(as))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no revocation endpoint")
	assert.Empty(t, as.revokeCalls())
}

func TestRevoke_FailsWhenMetadataIsUnavailable(t *testing.T) {
	as := startDeviceAS(t)
	as.metadata = func() string { return `not json` }
	m := newDeviceTestManager(t, as.srv.URL)

	err := m.Revoke(context.Background(), bc5Credentials(as))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing authorization server metadata")
	assert.Empty(t, as.revokeCalls())

	gone, _ := countingServer(t)
	creds := bc5Credentials(as)
	creds.Issuer = gone.URL
	err = m.Revoke(context.Background(), creds)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 404")
	assert.Empty(t, as.revokeCalls())
}

// A revocation endpoint is server-named data and receives the tokens, so it
// passes the same check as every other OAuth endpoint before any POST.
func TestRevoke_RefusesAnInsecureRevocationEndpoint(t *testing.T) {
	as := startDeviceAS(t)
	as.metadata = func() string {
		return fmt.Sprintf(`{"issuer": %q, "token_endpoint": %q, "revocation_endpoint": "http://revocations.example/oauth/revocations"}`,
			as.srv.URL, as.srv.URL+"/oauth/token")
	}
	m := newDeviceTestManager(t, as.srv.URL)

	err := m.Revoke(context.Background(), bc5Credentials(as))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid revocation endpoint")
	assert.Empty(t, as.revokeCalls())
}

func TestRevoke_ReportsARefusalWithoutTheToken(t *testing.T) {
	as := startDeviceAS(t)
	as.revoke = func(int) (int, string) { return http.StatusServiceUnavailable, `{"error":"temporarily_unavailable"}` }
	m := newDeviceTestManager(t, as.srv.URL)

	err := m.Revoke(context.Background(), bc5Credentials(as))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "revoking the refresh token")
	assert.Contains(t, err.Error(), "HTTP 503")
	assert.NotContains(t, err.Error(), "dev-ref")
	assert.NotContains(t, err.Error(), "dev-tok")
	assert.Len(t, as.revokeCalls(), 1, "a refused refresh revocation stops the sequence")
}

func TestRevoke_BoundsEachRequest(t *testing.T) {
	as := startDeviceAS(t)
	as.revoke = func(int) (int, string) {
		time.Sleep(300 * time.Millisecond)
		return http.StatusOK, `{}`
	}
	m := newDeviceTestManager(t, as.srv.URL)
	prev := revokeRequestTimeout
	revokeRequestTimeout = 50 * time.Millisecond
	t.Cleanup(func() { revokeRequestTimeout = prev })

	start := time.Now()
	err := m.Revoke(context.Background(), bc5Credentials(as))
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 250*time.Millisecond)
}

func TestLogout_RevokesThenDeletes(t *testing.T) {
	as := startDeviceAS(t)
	m := newDeviceTestManager(t, as.srv.URL)
	credKey := config.NormalizeBaseURL(as.srv.URL)
	require.NoError(t, m.store.Save(credKey, bc5Credentials(as)))

	result, err := m.Logout(context.Background())
	require.NoError(t, err)
	assert.Equal(t, &LogoutResult{Revoked: true}, result)
	assertRevoked(t, as.revokeCalls(), "dev-ref", "dev-tok")
	_, err = m.store.Load(credKey)
	assert.Error(t, err, "the local copy is gone")
}

func TestLogout_DeletesEvenWhenRevocationFails(t *testing.T) {
	as := startDeviceAS(t)
	as.revoke = func(int) (int, string) { return http.StatusInternalServerError, `{}` }
	m := newDeviceTestManager(t, as.srv.URL)
	credKey := config.NormalizeBaseURL(as.srv.URL)
	require.NoError(t, m.store.Save(credKey, bc5Credentials(as)))

	result, err := m.Logout(context.Background())
	require.NoError(t, err)
	assert.False(t, result.Revoked)
	assert.Empty(t, result.Skipped)
	require.Error(t, result.Err)
	assert.Contains(t, result.Err.Error(), "HTTP 500")
	_, err = m.store.Load(credKey)
	assert.Error(t, err, "the local copy is gone regardless")
}

func TestLogout_SkipsWhatIsNotItsToRevoke(t *testing.T) {
	as := startDeviceAS(t)
	m := newDeviceTestManager(t, as.srv.URL)
	credKey := config.NormalizeBaseURL(as.srv.URL)

	launchpad := &Credentials{AccessToken: "lp-tok", RefreshToken: "lp-ref", OAuthType: "launchpad", TokenEndpoint: "https://launchpad.37signals.com/authorization/token"}
	require.NoError(t, m.store.Save(credKey, launchpad))
	result, err := m.Logout(context.Background())
	require.NoError(t, err)
	assert.Equal(t, &LogoutResult{Skipped: RevokeSkippedLaunchpad}, result)

	imported := bc5Credentials(as)
	imported.RefreshToken = ""
	imported.Source = CredentialSourceToken
	require.NoError(t, m.store.Save(credKey, imported))
	result, err = m.Logout(context.Background())
	require.NoError(t, err)
	assert.Equal(t, &LogoutResult{Skipped: RevokeSkippedImported}, result)

	assert.Empty(t, as.revokeCalls())
	_, err = m.store.Load(credKey)
	assert.Error(t, err)
}

func TestLogoutCredential_ClearsTheNamedKey(t *testing.T) {
	as := startDeviceAS(t)
	m := newDeviceTestManager(t, as.srv.URL)
	require.NoError(t, m.store.Save("profile:bot", bc5Credentials(as)))
	require.NoError(t, m.store.Save(config.NormalizeBaseURL(as.srv.URL), bc5Credentials(as)))

	result, err := m.LogoutCredential(context.Background(), "profile:bot")
	require.NoError(t, err)
	assert.True(t, result.Revoked)
	assertRevoked(t, as.revokeCalls(), "dev-ref", "dev-tok")
	_, err = m.store.Load("profile:bot")
	assert.Error(t, err)
	assert.True(t, m.IsAuthenticated(), "the other credential is untouched")

	_, err = m.LogoutCredential(context.Background(), "profile:bot")
	assert.ErrorIs(t, err, ErrNoCredential)
}

// A stored login records where it can be revoked.
func TestLoginDevice_RecordsTheIssuer(t *testing.T) {
	as := startDeviceAS(t)
	resource := startResourceServer(t, as.srv.URL)
	m := newDeviceTestManager(t, resource.URL)

	_, err := m.Login(context.Background(), LoginOptions{
		Remote:        true,
		Logger:        func(string) {},
		deviceOptions: []oauth.DeviceOption{instantSleep()},
	})
	require.NoError(t, err)
	creds, err := m.store.Load(config.NormalizeBaseURL(resource.URL))
	require.NoError(t, err)
	assert.Equal(t, as.srv.URL, creds.Issuer)
	assert.Empty(t, as.revokeCalls(), "an accepted login revokes nothing")
}

func TestRevokeStored_RevokesThenDeletes(t *testing.T) {
	as := startDeviceAS(t)
	m := newDeviceTestManager(t, as.srv.URL)
	credKey := config.NormalizeBaseURL(as.srv.URL)
	require.NoError(t, m.store.Save(credKey, bc5Credentials(as)))

	require.NoError(t, m.RevokeStored(context.Background()))
	assertRevoked(t, as.revokeCalls(), "dev-ref", "dev-tok")
	_, err := m.store.Load(credKey)
	assert.Error(t, err, "a revoked credential is useless and goes")

	assert.ErrorIs(t, m.RevokeStored(context.Background()), ErrNoCredential)
}

// Unlike a logout, a revoke that did not happen keeps the credential: the
// operator asked for the token to be dead, and forgetting it locally would
// make that impossible from here.
func TestRevokeStored_KeepsTheCredentialWhenTheServerRefuses(t *testing.T) {
	as := startDeviceAS(t)
	as.revoke = func(int) (int, string) { return http.StatusServiceUnavailable, `{}` }
	m := newDeviceTestManager(t, as.srv.URL)
	credKey := config.NormalizeBaseURL(as.srv.URL)
	require.NoError(t, m.store.Save(credKey, bc5Credentials(as)))

	err := m.RevokeStored(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not revoke the token server-side")
	assert.Contains(t, err.Error(), "HTTP 503")
	assert.Contains(t, err.Error(), "kept so you can retry")
	assert.NotContains(t, err.Error(), "dev-ref")
	assert.Contains(t, output.AsError(err).Hint, "basecamp auth revoke")
	assert.True(t, m.IsAuthenticated(), "the credential stays for a retry")
}

func TestRevokeStored_RefusesWhatIsNotItsToRevoke(t *testing.T) {
	as := startDeviceAS(t)
	m := newDeviceTestManager(t, as.srv.URL)
	credKey := config.NormalizeBaseURL(as.srv.URL)

	require.NoError(t, m.store.Save(credKey, &Credentials{AccessToken: "lp-tok", OAuthType: "launchpad"}))
	err := m.RevokeStored(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Launchpad tokens cannot be revoked from the CLI")
	assert.Contains(t, output.AsError(err).Hint, "basecamp auth logout")

	imported := bc5Credentials(as)
	imported.Source = CredentialSourceToken
	require.NoError(t, m.store.Save(credKey, imported))
	err = m.RevokeStored(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "revoke it in Basecamp")

	assert.Empty(t, as.revokeCalls())
	assert.True(t, m.IsAuthenticated(), "a refused revoke changes nothing")
}
