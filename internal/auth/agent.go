package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/oauth"

	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// Agent self-tokens: the RFC 6749 §4.4 client_credentials grant.
//
// A Basecamp "agent" is a principal with no person behind it. It has no
// browser to send anywhere, so neither the device flow nor the
// authorization-code flow applies, and — this is the part the rest of this
// package has to be told about — the tokens it is issued carry NO REFRESH
// TOKEN. The grant itself is the durable credential: a client id and a
// client secret, which mint a fresh one-hour self-token whenever the last
// one is spent. Renewal is therefore another mint, not a refresh, and the
// client credentials live in the credential store beside the token they
// produce, in the OS keyring wherever one is available — the same place
// every other profile's refresh token lives.
//
// The tokens come back bound to the agent with an RFC 8707 resource
// indicator of the form urn:bc:agent:<id>. Nothing here parses or requires
// that shape: whatever the server binds the token to is stored and echoed
// back on the next mint.
//
// The grant is minted by the connection ceremony bc3 adds in PR 12314,
// which is not deployed yet. Everything below is built to the documented
// shape and covered by tests against a local token endpoint; it has not
// been exercised against production, and the bot-user device-flow profile
// remains the fallback until it has.

// oauthTypeAgent marks a credential that mints its own tokens from a
// confidential client rather than refreshing one it was granted.
const oauthTypeAgent = "agent"

// AgentResourceURNPrefix is the RFC 8707 resource indicator Basecamp binds
// an agent self-token to. The trailing segment is the agent's id.
const AgentResourceURNPrefix = "urn:bc:agent:"

// defaultAgentTokenLifetime is how long a minted self-token is assumed to
// last when the token response reports no expires_in. The grant issues
// one-hour tokens; the alternative — leaving ExpiresAt at zero, which every
// other path in this package reads as "non-expiring" — would pin a token
// the server retires within the hour and never renew it, so every request
// after that would fail with nothing arranging a fix. Minting one token
// more often than strictly necessary is much the cheaper mistake.
const defaultAgentTokenLifetime = time.Hour

// agentMintTimeout bounds one client_credentials round trip. It matches the
// OAuth lane client's own timeout, expressed as a context deadline so it
// binds a caller-injected client (the test seam) just the same.
const agentMintTimeout = 30 * time.Second

// maxAgentTokenBytes bounds the token response read. A token response is a
// few hundred bytes; this is the SDK exchanger's own ceiling.
const maxAgentTokenBytes int64 = 1 << 20

// maxAgentErrorBytes bounds how much of a server's error text is repeated
// back, matching the SDK exchanger.
const maxAgentErrorBytes = 500

// agentMint is everything one client_credentials request needs, resolved
// and checked without sending anything.
type agentMint struct {
	tokenEndpoint string
	clientID      string
	clientSecret  string
	scope         string
	resource      string
	client        *http.Client
}

// prepareAgentMint is the half of a mint that sends nothing: it checks what
// the credential holds and resolves the egress lane the request would go
// out through. It mutates nothing, so a report can ask what the next
// command would do without doing it.
func (m *Manager) prepareAgentMint(creds *Credentials) (*agentMint, error) {
	switch {
	case creds.ClientID == "":
		return nil, m.errAgentAuth("Agent credentials are missing their OAuth client id and cannot mint a token", creds.ClientID)
	case creds.ClientSecret == "":
		return nil, m.errAgentAuth("Agent credentials are missing their OAuth client secret and cannot mint a token", creds.ClientID)
	case creds.TokenEndpoint == "":
		return nil, m.errAgentAuth("Agent credentials are missing their token endpoint and cannot mint a token", creds.ClientID)
	}

	// The token endpoint is a persisted value and receives the client
	// secret, so it passes the same strict check every other stored OAuth
	// endpoint does before a byte goes out.
	if err := requireSecureOAuthEndpoint("token endpoint", creds.TokenEndpoint); err != nil {
		return nil, err
	}

	// Agent tokens are Basecamp's own, so the mint rides the BC5 lane:
	// its address policy derives from cfg.BaseURL, exactly as the device
	// flow's and a bc5 refresh's do.
	client, err := m.bc5Client()
	if err != nil {
		return nil, err
	}

	return &agentMint{
		tokenEndpoint: creds.TokenEndpoint,
		clientID:      creds.ClientID,
		clientSecret:  creds.ClientSecret,
		scope:         creds.Scope,
		resource:      creds.Resource,
		client:        client,
	}, nil
}

// mintAgentCredential replaces creds' access token with a freshly minted
// one and stores the result.
//
// Nothing is written unless the mint succeeded, and nothing is ever
// deleted: a mint that fails — the network is down, the server is having a
// bad minute, the secret was rotated out from under us — leaves the stored
// credential exactly as it was, so the next command tries again with the
// same client credentials rather than finding an empty store and an
// instruction to log in. There is no invalid_grant equivalent to forget
// here: a refused client_credentials request says the CLIENT is wrong, and
// the operator's remedy is to re-run the login with a good secret, which
// overwrites the credential anyway.
//
// The caller holds m.mu and the credential key's cross-process lock.
func (m *Manager) mintAgentCredential(ctx context.Context, origin string, creds *Credentials) error {
	mint, err := m.prepareAgentMint(creds)
	if err != nil {
		return err
	}
	token, err := m.mintAgentToken(ctx, mint)
	if err != nil {
		return err
	}
	applyAgentToken(creds, token)
	return m.store.Save(origin, creds)
}

// applyAgentToken folds a minted self-token into creds. The access token is
// replaced outright — there is nothing to rotate and nothing to carry
// forward — while the client credentials that minted it stay put. An
// omitted resource or scope in the response means "unchanged", as it does
// on the refresh path; RefreshToken is cleared rather than preserved,
// because a credential of this kind never has one and a stale value would
// send the renewal down the refresh path on the next process to load it.
func applyAgentToken(creds *Credentials, token *oauth.Token) {
	creds.AccessToken = token.AccessToken
	creds.RefreshToken = ""
	if token.Resource != "" {
		creds.Resource = token.Resource
	}
	if token.Scope != "" {
		creds.Scope = token.Scope
	}
	expiry := agentTokenExpiry(token)
	creds.ExpiresAt = expiry.Unix()
	creds.RenewAfter = agentRenewAfter(time.Now(), expiry).Unix()
}

// agentRenewAfter is when a self-token minted now and expiring at expiry
// should be replaced.
//
// Normally that is RefreshWindow early, as for every other credential. The
// exception is a token whose WHOLE LIFETIME is inside that window: at a
// two-minute lifetime the default margin would put every freshly minted
// token straight back inside the renewal window, so every command would
// mint again and twenty concurrent ones would take twenty grants in turn
// instead of sharing one. Below that threshold the margin becomes a
// quarter of the lifetime, which keeps a short token usable for most of
// what it was issued for. The grant's documented lifetime is an hour, so
// this is insurance against a server that issues something else rather
// than the expected path.
func agentRenewAfter(now, expiry time.Time) time.Time {
	margin := RefreshWindow
	if quarter := expiry.Sub(now) / 4; quarter < margin {
		margin = max(quarter, 0)
	}
	return expiry.Add(-margin)
}

// agentTokenExpiry is when a minted token stops being usable: what the
// server reported, or the assumed lifetime when it reported nothing.
func agentTokenExpiry(token *oauth.Token) time.Time {
	if !token.ExpiresAt.IsZero() {
		return token.ExpiresAt
	}
	return time.Now().Add(defaultAgentTokenLifetime)
}

// mintAgentToken POSTs one client_credentials grant and returns the token
// it was answered with.
//
// The SDK's Exchanger has no client_credentials form, so the request is
// made here — but to the same rules its token requests follow, because the
// body carries a client secret: a bounded response read, a refusal to treat
// a redirect as a hop, and RFC 6749 §5.2 error rendering in the shape the
// rest of this package already matches on ("token error: <code> - <text>").
func (m *Manager) mintAgentToken(ctx context.Context, mint *agentMint) (*oauth.Token, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {mint.clientID},
		"client_secret": {mint.clientSecret},
	}
	if mint.scope != "" {
		form.Set("scope", mint.scope)
	}
	// Echo the binding the last token carried, as the refresh path does:
	// re-minting for one agent must not silently widen to another.
	if mint.resource != "" {
		form.Set("resource", mint.resource)
	}

	reqCtx, cancel := context.WithTimeout(ctx, agentMintTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, mint.tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, wrapOAuthError("minting an agent token", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := mint.client.Do(req)
	if err != nil {
		return nil, wrapOAuthError("minting an agent token", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// The lane client refuses to follow a redirect on a credential-carrying
	// POST and hands the 3xx back as the response. Classify it before the
	// body read, as the SDK does: a 3xx that never finishes streaming must
	// surface as this, not as a timeout halfway through a read.
	if isRedirect(resp.StatusCode) {
		return nil, output.ErrAPI(resp.StatusCode,
			fmt.Sprintf("minting an agent token: redirect %d on the token endpoint is not followed", resp.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAgentTokenBytes+1))
	if err != nil {
		return nil, wrapOAuthError("minting an agent token", err)
	}
	if int64(len(body)) > maxAgentTokenBytes {
		return nil, output.ErrAPI(resp.StatusCode,
			fmt.Sprintf("minting an agent token: response body exceeds %d bytes", maxAgentTokenBytes))
	}

	if resp.StatusCode != http.StatusOK {
		return nil, m.agentMintRefusal(resp, body, mint.clientID)
	}

	var token oauth.Token
	if err := json.Unmarshal(body, &token); err != nil {
		return nil, output.ErrAPI(resp.StatusCode, fmt.Sprintf("minting an agent token: parsing the token response: %v", err))
	}
	if token.AccessToken == "" {
		return nil, output.ErrAPI(resp.StatusCode, "minting an agent token: the token response carries no access_token")
	}
	if token.RefreshToken != "" {
		// Not fatal — the token is usable — but worth saying out loud: a
		// refresh token here means the server's idea of this grant has
		// changed, and the CLI is about to drop it on the floor.
		m.warnf("warning: the agent token response carried a refresh token; agent credentials re-mint instead and it will not be stored")
	}
	if token.ExpiresIn > 0 {
		token.ExpiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	return &token, nil
}

// agentMintRefusal renders a non-200 token response. An RFC 6749 §5.2
// error object is rendered in the SDK exchanger's shape; anything else
// keeps the status and a bounded, single-line excerpt of whatever came
// back — the body is server-controlled and reaches terminals and
// transcripts.
func (m *Manager) agentMintRefusal(resp *http.Response, body []byte, clientID string) error {
	var errResp struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	detail := fmt.Sprintf("the server answered HTTP %d: %s",
		resp.StatusCode, truncate(richtext.SanitizeSingleLine(string(body)), maxAgentErrorBytes))
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		detail = "token error: " + errResp.Error
		if desc := truncate(richtext.SanitizeSingleLine(errResp.ErrorDescription), maxAgentErrorBytes); desc != "" {
			detail += " - " + desc
		}
	}

	// A 4xx says the CLIENT is wrong — an unknown client, a rotated
	// secret, a scope it was never granted — which no retry fixes and a
	// fresh login does, so it is an auth-class failure with the login as
	// its remedy. 429 is the exception: the client is fine, the caller is
	// early, and answering "authenticate again" would send an automated
	// caller into a re-login loop against a server already asking it to
	// slow down. Everything else keeps its own class through the
	// package's own classifier, which is how a 5xx stays retryable and a
	// 429 keeps its Retry-After.
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
		return m.errAgentAuth("Minting an agent token was refused ("+detail+")", clientID)
	}
	return statusFailure("minting an agent token: "+detail, resp)
}

// errAgentAuth is an auth_required error about an agent credential, with
// the AGENT login as its remedy.
//
// m.errAuth's remedy is `basecamp auth login -P <profile>`, which signs a
// PERSON in: an operator who followed it after a refused mint would
// replace the agent's credential with their own and only find out later.
// The command here re-runs the login this credential came from, with the
// client it already carries, and says where the secret goes.
func (m *Manager) errAgentAuth(msg, clientID string) *output.Error {
	e := output.ErrAuth(msg)
	e.Hint = "Pipe the agent's client secret in: `... | " + m.agentLoginCommand(clientID) + "`"
	return e
}

// agentLoginCommand is the client-credentials login addressed to the
// active profile, naming the client whose secret is being replaced. Both
// values come from configuration and a credential store, so both are
// shell-quoted: the command is meant to be pasted.
func (m *Manager) agentLoginCommand(clientID string) string {
	cmd := "basecamp auth login --with-client-credentials"
	if clientID != "" {
		cmd += " --client-id " + shellQuote(clientID)
	}
	if m.cfg.ActiveProfile != "" {
		cmd += " -P " + shellQuote(m.cfg.ActiveProfile)
	}
	return cmd
}

// isRedirect reports whether status is one of the redirects a
// credential-carrying POST refuses to follow.
func isRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// truncate bounds a server-supplied string for display.
func truncate(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[:limit-3] + "..."
}

// ClientCredentialsOptions configures an agent login.
type ClientCredentialsOptions struct {
	// ClientID and ClientSecret are the confidential client the agent
	// mints its own tokens with.
	ClientID     string
	ClientSecret string

	// Scope is the access level to ask for ("read" or "full"); empty asks
	// for full, matching the device flow, since BC5 defaults an omitted
	// scope to its least-privilege entry.
	Scope string

	// Logger receives status messages during the login. Nil suppresses them.
	Logger func(msg string)

	// BeforeStore, when set, runs after the mint has proved the client and
	// before the credential is written, with the result the login is about
	// to return. A non-nil error aborts the login and nothing is stored.
	//
	// It is where a caller commits whatever must exist alongside the
	// credential — the profile entry, above all. An entry without a
	// credential is a visible, harmless state; a stored client secret under
	// a profile nothing registered is an orphan nobody will find again.
	BeforeStore func(result *LoginResult) error
}

// LoginClientCredentials authenticates as a Basecamp agent principal and
// stores the result under the active credential key.
//
// It discovers the authorization server the same way an interactive login
// does, mints one self-token — which is what proves the client id and
// secret are good, since there is no person to ask — and stores the client
// credentials alongside the token so every later command can mint another
// when this one expires.
//
// Only Basecamp's own authorization server issues these. A discovery that
// falls back to Launchpad is refused rather than attempted: Launchpad has
// no agent principals, and sending a client secret to it on the strength of
// a fallback would be a guess with a secret.
func (m *Manager) LoginClientCredentials(ctx context.Context, opts ClientCredentialsOptions) (*LoginResult, error) {
	if opts.ClientID == "" || opts.ClientSecret == "" {
		return nil, output.ErrUsage("An agent login needs both a client id and a client secret")
	}
	if opts.Scope != "" && opts.Scope != scopeRead && opts.Scope != scopeFull {
		return nil, output.ErrUsage("Invalid scope. Use 'read' or 'full'")
	}
	scope := opts.Scope
	if scope == "" {
		scope = scopeFull
	}

	log := opts.Logger
	if log == nil {
		log = func(string) {}
	}

	credKey := m.credentialKey()

	disc, err := m.discoverOAuth(ctx, log)
	if err != nil {
		return nil, err
	}
	if disc.oauthType != oauthTypeBC5 {
		return nil, output.ErrUsageHint(
			"This server has no agent grant: OAuth discovery selected the Launchpad fallback",
			"Agent logins need Basecamp's own authorization server. Check BASECAMP_BASE_URL, or sign in as a bot user instead: basecamp auth login --device-code.")
	}
	if err := requireSecureOAuthEndpoint("token endpoint", disc.config.TokenEndpoint); err != nil {
		return nil, err
	}

	creds := &Credentials{
		OAuthType:     oauthTypeAgent,
		ClientID:      opts.ClientID,
		ClientSecret:  opts.ClientSecret,
		TokenEndpoint: disc.config.TokenEndpoint,
		Issuer:        disc.config.Issuer,
		Scope:         scope,
	}

	mint, err := m.prepareAgentMint(creds)
	if err != nil {
		return nil, err
	}
	token, err := m.mintAgentToken(ctx, mint)
	if err != nil {
		return nil, err
	}
	applyAgentToken(creds, token)

	result := &LoginResult{OAuthType: oauthTypeAgent, Scope: creds.Scope}
	if opts.BeforeStore != nil {
		if err := opts.BeforeStore(result); err != nil {
			return nil, err
		}
	}
	if err := m.storeLoginCredential(ctx, credKey, creds); err != nil {
		return nil, err
	}

	return result, nil
}
