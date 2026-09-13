package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/oauth"
	"github.com/basecamp/cli/credstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"

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

// Metadata is bound to the issuer it was fetched for: a redirect to another
// server's valid document must not decide where the tokens are POSTed.
func TestRevoke_RefusesMetadataForAnotherIssuer(t *testing.T) {
	as := startDeviceAS(t)
	other := startDeviceAS(t)
	as.metadata = func() string { return other.metadata() }
	m := newDeviceTestManager(t, as.srv.URL)

	err := m.Revoke(context.Background(), bc5Credentials(as))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "different issuer")
	assert.Empty(t, as.revokeCalls())
	assert.Empty(t, other.revokeCalls())
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
	assert.Equal(t, RemainingRefresh, result.Remaining, "a refused refresh revocation leaves the whole family live")
	assert.Contains(t, result.Outstanding(), "refresh token stays valid")
	_, err = m.store.Load(credKey)
	assert.Error(t, err, "the local copy is gone regardless")
}

// What a failure left usable depends on where it struck: before the refresh
// token was accepted the family is live; after, only the access token.
func TestLogout_ReportsWhatAFailureLeftUsable(t *testing.T) {
	as := startDeviceAS(t)
	as.revoke = func(call int) (int, string) {
		if call == 0 {
			return http.StatusOK, `{}`
		}
		return http.StatusInternalServerError, `{}`
	}
	m := newDeviceTestManager(t, as.srv.URL)
	credKey := config.NormalizeBaseURL(as.srv.URL)
	require.NoError(t, m.store.Save(credKey, bc5Credentials(as)))

	result, err := m.Logout(context.Background())
	require.NoError(t, err)
	assert.Contains(t, result.Err.Error(), "revoking the access token")
	assert.Equal(t, RemainingAccess, result.Remaining)
	assert.Contains(t, result.Outstanding(), "only the access token remains and it expires in 1h0m0s", "the lifetime is the one the server reported")
	assertRevoked(t, as.revokeCalls(), "dev-ref", "dev-tok")

	assert.Equal(t, "only the access token remains and it reports no expiry", (&LogoutResult{Remaining: RemainingAccess}).Outstanding())
	assert.Equal(t, "only the access token remains and it has already expired", (&LogoutResult{Remaining: RemainingAccess, ExpiresAt: 1}).Outstanding())

	as.metadata = func() string { return `{}` }
	noRefresh := bc5Credentials(as)
	noRefresh.RefreshToken = ""
	require.NoError(t, m.store.Save(credKey, noRefresh))
	result, err = m.Logout(context.Background())
	require.NoError(t, err)
	require.Error(t, result.Err)
	assert.Equal(t, RemainingAccess, result.Remaining, "with no refresh token only the access token was ever at stake")
}

// lockedCredStore is a credential store that cannot be reached at all — a
// locked keyring, an unreadable file — as opposed to one with nothing in it.
type lockedCredStore struct{ err error }

func (s lockedCredStore) Load(string) ([]byte, error) { return nil, s.err }
func (s lockedCredStore) Save(string, []byte) error   { return s.err }
func (s lockedCredStore) Delete(string) error         { return s.err }
func (lockedCredStore) MigrateToKeyring() error       { return nil }
func (lockedCredStore) UsingKeyring() bool            { return false }
func (lockedCredStore) FallbackWarning() string       { return "" }

// A store that cannot be read is not "not logged in": the credential may be
// there, live on both sides, so the failure is the answer.
func TestLogout_PropagatesAnUnreachableStore(t *testing.T) {
	as := startDeviceAS(t)
	m := newDeviceTestManager(t, as.srv.URL)
	swapNewCredStore(t, func(credstore.StoreOptions) credStore { return lockedCredStore{err: errors.New("keyring locked")} })
	m.store = NewStore(t.TempDir())

	_, err := m.Logout(context.Background())
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoCredential)
	assert.ErrorContains(t, err, "keyring locked")

	_, err = m.LogoutCredential(context.Background(), "profile:bot", "")
	assert.ErrorContains(t, err, "keyring locked")

	err = m.RevokeStored(context.Background())
	assert.NotErrorIs(t, err, ErrNoCredential)
	assert.ErrorContains(t, err, "keyring locked")
	assert.Empty(t, as.revokeCalls())
}

// The file backend's miss is the one error that means "nothing stored".
func TestStoreLoad_TellsAMissFromAFailure(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	_, err := store.Load("profile:absent")
	assert.ErrorIs(t, err, ErrNoCredential)

	assert.True(t, isMissingCredential(fmt.Errorf("credentials not found: %w", keyring.ErrNotFound)))
	assert.False(t, isMissingCredential(errors.New("credentials not found: keychain locked")),
		"a keyring failure under credstore's not-found prefix is not a miss")
	assert.False(t, isMissingCredential(errors.New("open credentials.json: permission denied")))
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

	result, err := m.LogoutCredential(context.Background(), "profile:bot", "")
	require.NoError(t, err)
	assert.True(t, result.Revoked)
	assertRevoked(t, as.revokeCalls(), "dev-ref", "dev-tok")
	_, err = m.store.Load("profile:bot")
	assert.Error(t, err)
	assert.True(t, m.IsAuthenticated(), "the other credential is untouched")

	_, err = m.LogoutCredential(context.Background(), "profile:bot", "")
	assert.ErrorIs(t, err, ErrNoCredential)
}

// The revocation's egress policy follows the profile being deleted, not the
// active configuration: with production active, a loopback development
// profile's issuer is still reachable when the delete is anchored on that
// profile's own base URL.
func TestLogoutCredential_AnchorsTheLaneOnTheProfileBaseURL(t *testing.T) {
	as := startDeviceAS(t)
	m := newDeviceTestManager(t, "https://3.basecampapi.com")
	m.httpClient = nil // the real per-anchor lanes, address policy included
	m.Warnf = func(string, ...any) {}

	require.NoError(t, m.store.Save("profile:dev", bc5Credentials(as)))
	result, err := m.LogoutCredential(context.Background(), "profile:dev", "")
	require.NoError(t, err)
	assert.False(t, result.Revoked)
	require.Error(t, result.Err, "the production lane refuses a loopback issuer")
	assert.Empty(t, as.revokeCalls())

	require.NoError(t, m.store.Save("profile:dev", bc5Credentials(as)))
	result, err = m.LogoutCredential(context.Background(), "profile:dev", as.srv.URL)
	require.NoError(t, err)
	assert.Equal(t, &LogoutResult{Revoked: true}, result)
	assertRevoked(t, as.revokeCalls(), "dev-ref", "dev-tok")
}

// A stored blob that is not a credential is cleared, and only then is the
// key reported empty.
func TestLogout_ClearsAnUnreadableCredential(t *testing.T) {
	as := startDeviceAS(t)
	m := newDeviceTestManager(t, as.srv.URL)
	credKey := config.NormalizeBaseURL(as.srv.URL)
	require.NoError(t, m.store.ensure().Save(credKey, []byte(`["not a credential"]`)))
	_, err := m.store.Load(credKey)
	require.ErrorIs(t, err, ErrInvalidCredentials)

	_, err = m.Logout(context.Background())
	assert.ErrorIs(t, err, ErrNoCredential)
	_, err = m.store.ensure().Load(credKey)
	assert.Error(t, err, "the blob is gone")
	assert.Empty(t, as.revokeCalls())
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

// A login refused by Verify (an --expect-identity mismatch) stores nothing,
// and now also leaves nothing live: the grant it minted is revoked before
// the refusal is returned.
func TestLoginDevice_VerifyFailureRevokesTheGrant(t *testing.T) {
	as := startDeviceAS(t)
	resource := startResourceServer(t, as.srv.URL)
	m := newDeviceTestManager(t, resource.URL)
	credKey := config.NormalizeBaseURL(resource.URL)

	cl := &collectLogger{}
	_, err := m.Login(context.Background(), LoginOptions{
		Remote:        true,
		Logger:        cl.log,
		deviceOptions: []oauth.DeviceOption{instantSleep()},
		Verify:        func(context.Context, string, string) error { return output.ErrAuth("not you") },
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not you", "the refusal stays the error the caller sees")
	assertRevoked(t, as.revokeCalls(), "dev-ref", "dev-tok")
	assert.NotContains(t, cl.joined(), "warning")
	_, loadErr := m.store.Load(credKey)
	assert.Error(t, loadErr, "a rejected token is never stored")
}

func TestLoginDevice_VerifyFailureWarnsWhenTheGrantOutlivesIt(t *testing.T) {
	as := startDeviceAS(t)
	as.revoke = func(int) (int, string) { return http.StatusInternalServerError, `{}` }
	resource := startResourceServer(t, as.srv.URL)
	m := newDeviceTestManager(t, resource.URL)

	cl := &collectLogger{}
	_, err := m.Login(context.Background(), LoginOptions{
		Remote:        true,
		Logger:        cl.log,
		deviceOptions: []oauth.DeviceOption{instantSleep()},
		Verify:        func(context.Context, string, string) error { return output.ErrAuth("not you") },
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not you")
	assert.Contains(t, cl.joined(), "warning: could not revoke the refused credential server-side")
	assert.Contains(t, cl.joined(), "refresh token stays valid until it is revoked")
	assert.NotContains(t, cl.joined(), "dev-ref")
	assert.NotContains(t, cl.joined(), "dev-tok")
}

// A refusal after the login context is gone — Verify timed out or was
// canceled — must still revoke: that is exactly when the grant would
// otherwise be orphaned.
func TestLoginDevice_VerifyFailureRevokesAfterCancellation(t *testing.T) {
	as := startDeviceAS(t)
	resource := startResourceServer(t, as.srv.URL)
	m := newDeviceTestManager(t, resource.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cl := &collectLogger{}
	_, err := m.Login(ctx, LoginOptions{
		Remote:        true,
		Logger:        cl.log,
		deviceOptions: []oauth.DeviceOption{instantSleep()},
		Verify: func(context.Context, string, string) error {
			cancel()
			return output.ErrAuth("not you")
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not you")
	assertRevoked(t, as.revokeCalls(), "dev-ref", "dev-tok")
	assert.NotContains(t, cl.joined(), "warning")
}

// A refused Launchpad login stores nothing and revokes nothing: Launchpad
// has no revocation endpoint, so no request leaves and no warning prints.
func TestLoginLaunchpad_VerifyFailureRevokesNothing(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/authorization/token":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"remote-tok","token_type":"bearer","refresh_token":"remote-refresh"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("BASECAMP_LAUNCHPAD_URL", srv.URL)
	t.Setenv("BASECAMP_OAUTH_ISSUER", "")
	cfg := &config.Config{BaseURL: srv.URL}
	m := NewManager(cfg, srv.Client())
	m.store = newTestStore(t, tmpDir)
	credKey := config.NormalizeBaseURL(srv.URL)

	sl := newSyncLogger()
	pr, pw := io.Pipe()
	defer pr.Close()
	errCh := make(chan error, 1)
	go func() {
		_, err := m.Login(context.Background(), LoginOptions{
			Remote:      true,
			Logger:      sl.log,
			InputReader: pr,
			Verify:      func(context.Context, string, string) error { return output.ErrAuth("not you") },
		})
		errCh <- err
	}()

	var authURL string
	select {
	case authURL = <-sl.authReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for auth URL to be logged")
	}
	u, err := url.Parse(authURL)
	require.NoError(t, err)
	_, err = fmt.Fprintf(pw, "http://127.0.0.1:8976/callback?code=test-code&state=%s\n", u.Query().Get("state"))
	require.NoError(t, err)
	pw.Close()

	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not you")
	case <-time.After(5 * time.Second):
		t.Fatal("Login timed out")
	}

	_, loadErr := m.store.Load(credKey)
	assert.Error(t, loadErr, "a rejected token is never stored")
	assert.NotContains(t, strings.Join(sl.snapshot(), "\n"), "could not revoke")
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, paths, "/authorization/token")
	assert.NotContains(t, paths, "/.well-known/oauth-authorization-server", "no revocation metadata is fetched for a Launchpad grant")
	assert.NotContains(t, paths, "/oauth/revocations")
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
	assert.Contains(t, err.Error(), "the credential is kept")
	assert.NotContains(t, err.Error(), "dev-ref")
	assert.Contains(t, output.AsError(err).Hint, "basecamp auth revoke")
	assert.True(t, output.AsError(err).Retryable, "a 503 is the server saying come back")
	assert.Equal(t, http.StatusServiceUnavailable, output.AsError(err).HTTPStatus)
	assert.True(t, m.IsAuthenticated(), "the credential stays for a retry")

	as.revoke = func(int) (int, string) { return http.StatusBadRequest, `{"error":"unsupported_token_type"}` }
	err = m.RevokeStored(context.Background())
	require.Error(t, err)
	assert.False(t, output.AsError(err).Retryable, "a 400 will not change on retry")
	assert.Equal(t, output.CodeAPI, output.AsError(err).Code)
	assert.NotContains(t, output.AsError(err).Hint, "auth revoke", "no retry is advertised for a final refusal")
	assert.Contains(t, output.AsError(err).Hint, "basecamp auth logout")

	as.revoke = func(int) (int, string) { return http.StatusTooManyRequests, `{"error":"rate_limited"}` }
	err = m.RevokeStored(context.Background())
	require.Error(t, err)
	assert.Equal(t, output.CodeRateLimit, output.AsError(err).Code, "a 429 is a rate limit, as everywhere else in the CLI")
	assert.True(t, output.AsError(err).Retryable)

	as.revoke = func(int) (int, string) { return http.StatusInsufficientStorage, `{}` }
	err = m.RevokeStored(context.Background())
	require.Error(t, err)
	assert.Equal(t, output.CodeLimitExceeded, output.AsError(err).Code)
	assert.False(t, output.AsError(err).Retryable, "a 507 is a verdict, not a 5xx to retry")

	as.srv.Close()
	err = m.RevokeStored(context.Background())
	require.Error(t, err)
	assert.True(t, output.AsError(err).Retryable, "an unreachable server may come back")
	assert.Contains(t, err.Error(), "fetching authorization server metadata")
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
