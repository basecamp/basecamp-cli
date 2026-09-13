package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// ErrNoCredential reports that nothing is stored under the credential key a
// logout was asked to clear.
var ErrNoCredential = errors.New("no stored credential")

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
}

// Logout revokes the current credential with its authorization server when
// it can be, then removes it from local storage regardless. ErrNoCredential
// means there was nothing to remove.
func (m *Manager) Logout(ctx context.Context) (*LogoutResult, error) {
	return m.LogoutCredential(ctx, m.credentialKey())
}

// LogoutCredential is Logout for the credential stored under credKey — the
// path profile deletion takes for "profile:<name>".
func (m *Manager) LogoutCredential(ctx context.Context, credKey string) (*LogoutResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	creds, err := m.store.Load(credKey)
	if err != nil {
		// Nothing usable under the key: absent, or a blob no command can
		// read. Either way the key ends up clear.
		_ = m.store.Delete(credKey)
		return nil, ErrNoCredential
	}
	result := m.revokeForDiscard(ctx, creds)
	if err := m.store.Delete(credKey); err != nil {
		return nil, err
	}
	return result, nil
}

// revokeForDiscard revokes creds when they are the CLI's to revoke and
// classifies the outcome for the caller's copy. Best effort by design: the
// caller discards the credential locally whatever happened here.
func (m *Manager) revokeForDiscard(ctx context.Context, creds *Credentials) *LogoutResult {
	if skipped := revokeSkipReason(creds); skipped != "" {
		return &LogoutResult{Skipped: skipped}
	}
	if err := m.Revoke(ctx, creds); err != nil {
		return &LogoutResult{Err: err}
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
	if err != nil {
		return ErrNoCredential
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
		e := output.ErrAPI(0, "could not revoke the token server-side: "+err.Error()+"; the credential is kept so you can retry")
		e.Hint = "Retry: basecamp auth revoke — or forget it locally: basecamp auth logout"
		e.Cause = err
		return e
	}
	return m.store.Delete(credKey)
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
	if creds.RefreshToken == "" && creds.AccessToken == "" {
		return errors.New("stored credential carries no token")
	}
	issuer, err := credentialIssuer(creds)
	if err != nil {
		return err
	}
	client, err := m.bc5Client()
	if err != nil {
		return err
	}
	endpoint, err := m.revocationEndpoint(ctx, client, issuer)
	if err != nil {
		return err
	}
	for _, tok := range []struct{ value, hint string }{
		{creds.RefreshToken, "refresh_token"},
		{creds.AccessToken, "access_token"},
	} {
		if tok.value == "" {
			continue
		}
		if err := revokeToken(ctx, client, endpoint, tok.value, tok.hint); err != nil {
			return err
		}
	}
	return nil
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
		return "", fmt.Errorf("fetching authorization server metadata: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("authorization server metadata returned HTTP %d", resp.StatusCode)
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
		return fmt.Errorf("revoking the %s: %w", what, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxRevocationBodyBytes))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("revoking the %s: the server answered HTTP %d", what, resp.StatusCode)
	}
	return nil
}
