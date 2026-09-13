package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// revokeRequestTimeout bounds each revocation round trip — the metadata
// fetch and each token POST. A logout must not hang on a slow server: the
// local delete happens regardless, and an unrevoked access token expires on
// its own within the hour. Replaceable in tests.
var revokeRequestTimeout = 10 * time.Second

// maxRevocationBodyBytes bounds what is read from the authorization server
// during revocation. RFC 8414 metadata is a few hundred bytes and an RFC
// 7009 response body is empty or a one-line error.
const maxRevocationBodyBytes = 64 * 1024

// wellKnownAuthorizationServer is the RFC 8414 metadata path under an
// issuer origin.
const wellKnownAuthorizationServer = "/.well-known/oauth-authorization-server"

// Reasons a credential is removed locally without a revocation attempt.
const (
	// RevokeSkippedLaunchpad: the legacy provider has no revocation endpoint.
	// Credentials from the removed "bc3" development flow land here too —
	// the server that minted them is gone.
	RevokeSkippedLaunchpad = "launchpad"
	// RevokeSkippedImported: an imported personal access token is not the
	// CLI's to revoke — the same token lives in the operator's secret store
	// and stays valid until revoked in Basecamp.
	RevokeSkippedImported = "imported_token"
)

// What a failed revocation left usable.
const (
	// RemainingRefresh: the refresh token was not revoked, so the whole
	// family — its access token included — stays valid until it is.
	RemainingRefresh = "refresh_token"
	// RemainingAccess: only the access token remains (the refresh token was
	// revoked, or there never was one); it expires on its own.
	RemainingAccess = "access_token"
)

// LogoutResult reports what became of a credential server-side. The local
// copy is gone either way.
type LogoutResult struct {
	// Revoked reports that the authorization server accepted the revocation.
	Revoked bool
	// Skipped names why no revocation was attempted (one of the
	// RevokeSkipped* reasons); empty when one was.
	Skipped string
	// Err is why an attempted revocation did not go through; nil otherwise.
	Err error
	// Remaining names what Err left usable (one of the Remaining* values);
	// empty when Err is nil.
	Remaining string
}

// Outstanding describes, for a human, what a failed revocation left usable;
// empty when nothing did.
func (r *LogoutResult) Outstanding() string {
	switch r.Remaining {
	case RemainingRefresh:
		return "the refresh token stays valid until it is revoked"
	case RemainingAccess:
		return "only the access token remains and it expires within the hour"
	default:
		return ""
	}
}

// Logout revokes the current credential with its authorization server when
// it can be, then removes it from local storage regardless. ErrNoCredential
// means there was nothing to remove.
func (m *Manager) Logout(ctx context.Context) (*LogoutResult, error) {
	return m.LogoutCredential(ctx, m.credentialKey(), "")
}

// LogoutCredential is Logout for the credential stored under credKey — the
// path profile deletion takes for "profile:<name>". baseURL anchors the
// egress policy of the revocation request: the deleted profile's own base
// URL, which need not be the active configuration's — a loopback
// development profile deleted while production is active must still reach
// its loopback issuer. Empty means the active configuration.
func (m *Manager) LogoutCredential(ctx context.Context, credKey, baseURL string) (*LogoutResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	creds, err := m.store.Load(credKey)
	switch {
	case errors.Is(err, ErrNoCredential):
		return nil, ErrNoCredential
	case errors.Is(err, ErrInvalidCredentials):
		// A blob no command can read is still stored; clearing it is the
		// one thing a logout can do for it, and failing to is reported.
		if err := m.store.Delete(credKey); err != nil {
			return nil, err
		}
		return nil, ErrNoCredential
	case err != nil:
		// A store that could not be read (a locked keyring, an unreadable
		// file) is not "not logged in": the credential may well be there,
		// live on both sides, and a logout that says otherwise is a lie.
		return nil, err
	}
	result := m.revokeForDiscard(ctx, creds, baseURL)
	if err := m.store.Delete(credKey); err != nil {
		return nil, err
	}
	return result, nil
}

// revokeForDiscard revokes creds when they are the CLI's to revoke and
// classifies the outcome for the caller's copy. Best effort by design: the
// caller discards the credential locally whatever happened here.
func (m *Manager) revokeForDiscard(ctx context.Context, creds *Credentials, baseURL string) *LogoutResult {
	if skipped := revokeSkipReason(creds); skipped != "" {
		return &LogoutResult{Skipped: skipped}
	}
	if remaining, err := m.revoke(ctx, creds, baseURL); err != nil {
		return &LogoutResult{Err: err, Remaining: remaining}
	}
	return &LogoutResult{Revoked: true}
}

// RevokeStored revokes the current credential with its authorization server
// and, that done, removes the local copy — a revoked refresh family leaves
// nothing usable behind. Unlike Logout it is not best effort: a credential
// that could not be revoked stays stored so the operator can try again,
// and one that is not the CLI's to revoke (Launchpad, an imported token)
// is refused with the reason. ErrNoCredential means nothing is stored.
func (m *Manager) RevokeStored(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	credKey := m.credentialKey()
	creds, err := m.store.Load(credKey)
	if errors.Is(err, ErrNoCredential) {
		return ErrNoCredential
	}
	if err != nil {
		return err
	}
	switch revokeSkipReason(creds) {
	case RevokeSkippedLaunchpad:
		return output.ErrUsageHint("Launchpad tokens cannot be revoked from the CLI",
			"Forget the credential locally instead: basecamp auth logout")
	case RevokeSkippedImported:
		return output.ErrUsageHint("An imported personal access token is not the CLI's to revoke; revoke it in Basecamp",
			"Forget the credential locally instead: basecamp auth logout")
	}
	if err := m.Revoke(ctx, creds); err != nil {
		// Keep the failure's taxonomy — a transport failure or a 5xx stays
		// retryable, a refusal does not — under the revocation's own message
		// and remedy.
		e := *output.AsError(err)
		e.Message = "could not revoke the token server-side: " + e.Message + "; the credential is kept so you can retry"
		e.Hint = "Retry: basecamp auth revoke — or forget it locally: basecamp auth logout"
		e.Cause = err
		return &e
	}
	return m.store.Delete(credKey)
}

// transportFailure is a revocation request that got no answer: retryable,
// under a message that names the step rather than the SDK's generic one.
func transportFailure(msg string, cause error) error {
	e := output.ErrNetwork(cause)
	e.Message = msg + ": " + cause.Error()
	e.Hint = ""
	return e
}

// statusFailure is a revocation request the server answered with something
// other than 200, classified the way the SDK classifies any other response:
// 429 is a rate limit (retryable, with its Retry-After), 507 an account
// limit (a verdict, not retryable), any other 5xx retryable, the rest final.
func statusFailure(msg string, resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		retryAfter, _ := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After")))
		e := output.ErrRateLimit(retryAfter)
		e.Message = msg
		e.HTTPStatus = resp.StatusCode
		return e
	case http.StatusInsufficientStorage:
		e := output.ErrAPI(resp.StatusCode, msg)
		e.Code = output.CodeLimitExceeded
		return e
	default:
		e := output.ErrAPI(resp.StatusCode, msg)
		e.Retryable = resp.StatusCode >= 500 && resp.StatusCode < 600
		return e
	}
}

// revokeSkipReason says why creds are not the CLI's to revoke, or "" when
// they are: a BC5 credential minted by an OAuth login.
func revokeSkipReason(creds *Credentials) string {
	switch {
	case creds.Source == CredentialSourceToken:
		return RevokeSkippedImported
	case creds.OAuthType != oauthTypeBC5:
		return RevokeSkippedLaunchpad
	default:
		return ""
	}
}

// Revoke asks the credential's authorization server to revoke it (RFC
// 7009) as the public basecamp-cli client: the refresh token first, which
// revokes its whole family, then the access token — the type hint is
// advisory and the second request is cheap insurance. Both ride the BC5
// lane client, so the metadata and revocation endpoints are judged by the
// same address policy as every other BC5 request. An error means the server
// could not be reached, did not describe a revocation endpoint, or did not
// answer 200; the tokens never appear in it.
func (m *Manager) Revoke(ctx context.Context, creds *Credentials) error {
	_, err := m.revoke(ctx, creds, "")
	return err
}

// revoke reports, alongside a failure, which token it left usable: the
// refresh token until its own POST is accepted, only the access token after.
func (m *Manager) revoke(ctx context.Context, creds *Credentials, baseURL string) (remaining string, err error) {
	remaining = RemainingAccess
	if creds.RefreshToken != "" {
		remaining = RemainingRefresh
	}
	if creds.RefreshToken == "" && creds.AccessToken == "" {
		return remaining, errors.New("stored credential carries no token")
	}
	issuer, err := credentialIssuer(creds)
	if err != nil {
		return remaining, err
	}
	client, err := m.revocationClient(baseURL)
	if err != nil {
		return remaining, err
	}
	endpoint, err := m.revocationEndpoint(ctx, client, issuer)
	if err != nil {
		return remaining, err
	}
	if creds.RefreshToken != "" {
		if err := revokeToken(ctx, client, endpoint, creds.RefreshToken, RemainingRefresh); err != nil {
			return remaining, err
		}
		remaining = RemainingAccess
	}
	if creds.AccessToken != "" {
		if err := revokeToken(ctx, client, endpoint, creds.AccessToken, RemainingAccess); err != nil {
			return remaining, err
		}
	}
	return "", nil
}

// revocationClient is the egress lane a revocation rides: the BC5 lane of
// the active configuration, or one built on baseURL when the credential
// belongs to a profile anchored elsewhere. A caller-owned httpClient
// carries everything, as it does for every other OAuth request.
func (m *Manager) revocationClient(baseURL string) (*http.Client, error) {
	if m.httpClient != nil || baseURL == "" || baseURL == m.cfg.BaseURL {
		return m.bc5Client()
	}
	return m.buildLaneClient(baseURL, "profile base URL")
}

// credentialIssuer names the authorization server that minted creds. Logins
// record it; credentials stored before Issuer existed carry only the token
// endpoint, and Basecamp mounts that under the issuer origin
// (https://3.basecamp.com/oauth/tokens), so the endpoint's origin is the
// issuer.
func credentialIssuer(creds *Credentials) (string, error) {
	if creds.Issuer != "" {
		return strings.TrimRight(creds.Issuer, "/"), nil
	}
	u, err := url.Parse(creds.TokenEndpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("stored credential names no authorization server")
	}
	return u.Scheme + "://" + u.Host, nil
}

// revocationEndpoint reads the issuer's RFC 8414 metadata for its
// revocation_endpoint. The SDK's discovery drops that field, so the fetch
// is local: one GET, one decoded field, checked like every other
// server-named OAuth endpoint before it receives a token.
func (m *Manager) revocationEndpoint(ctx context.Context, client *http.Client, issuer string) (string, error) {
	metadataURL := issuer + wellKnownAuthorizationServer
	if err := requireSecureOAuthEndpoint("issuer metadata URL", metadataURL); err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, revokeRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", transportFailure("fetching authorization server metadata", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", statusFailure(fmt.Sprintf("authorization server metadata returned HTTP %d", resp.StatusCode), resp)
	}

	var doc struct {
		RevocationEndpoint string `json:"revocation_endpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRevocationBodyBytes)).Decode(&doc); err != nil {
		return "", fmt.Errorf("parsing authorization server metadata: %w", err)
	}
	if doc.RevocationEndpoint == "" {
		return "", errors.New("authorization server advertises no revocation endpoint")
	}
	if err := requireSecureOAuthEndpoint("revocation endpoint", doc.RevocationEndpoint); err != nil {
		return "", err
	}
	return doc.RevocationEndpoint, nil
}

// revokeToken POSTs one RFC 7009 revocation as the public client. The
// server answers 200 whatever the token's state, so any other status is a
// refusal worth reporting; the response body is drained and never read.
func revokeToken(ctx context.Context, client *http.Client, endpoint, token, hint string) error {
	what := strings.ReplaceAll(hint, "_", " ")
	ctx, cancel := context.WithTimeout(ctx, revokeRequestTimeout)
	defer cancel()
	form := url.Values{
		"token":           {token},
		"token_type_hint": {hint},
		"client_id":       {bc5ClientID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return transportFailure("revoking the "+what, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxRevocationBodyBytes))
	if resp.StatusCode != http.StatusOK {
		return statusFailure(fmt.Sprintf("revoking the %s: the server answered HTTP %d", what, resp.StatusCode), resp)
	}
	return nil
}
