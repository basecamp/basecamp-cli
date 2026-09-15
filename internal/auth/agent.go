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

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/oauth"

	"github.com/basecamp/basecamp-cli/internal/output"
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
	mint, err := m.resolveAgentMint(creds)
	if err != nil {
		return nil, m.agentRemedy(err, creds.ClientID, creds.Scope)
	}
	return mint, nil
}

// resolveAgentMint is prepareAgentMint without the remedy: it raises the
// plain auth errors, which its caller re-hints.
func (m *Manager) resolveAgentMint(creds *Credentials) (*agentMint, error) {
	switch {
	case creds.ClientID == "":
		return nil, output.ErrAuth("Agent credentials are missing their OAuth client id and cannot mint a token")
	case creds.ClientSecret == "":
		return nil, output.ErrAuth("Agent credentials are missing their OAuth client secret and cannot mint a token")
	case creds.TokenEndpoint == "":
		return nil, output.ErrAuth("Agent credentials are missing their token endpoint and cannot mint a token")
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

// maxAgentTokenLifetime caps what a reported expires_in is believed to
// mean. A self-token lives an hour; anything past a day is not a lifetime
// this CLI should plan around, and an unbounded one overflows the
// conversion to a Duration outright.
const maxAgentTokenLifetime = 24 * time.Hour

// applyTokenLifetime sets the token's expiry from the response's
// expires_in.
//
// The field is re-decoded through a *int because oauth.Token's plain int
// cannot tell an ABSENT expires_in from an explicit zero, and the two mean
// opposite things: absent leaves the grant's documented lifetime to be
// assumed, while a zero or negative one is the server saying the token it
// just issued is already spent. Assuming an hour for that would serve a
// retired token for an hour.
func applyTokenLifetime(token *oauth.Token, body []byte) error {
	var reported struct {
		ExpiresIn *int `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &reported); err != nil {
		return errors.New("the token response could not be parsed")
	}
	if reported.ExpiresIn == nil {
		// Absent: agentTokenExpiry assumes the documented lifetime.
		return nil
	}
	if *reported.ExpiresIn <= 0 {
		return errors.New("the server issued a token that has already expired")
	}
	lifetime := time.Duration(*reported.ExpiresIn) * time.Second
	if *reported.ExpiresIn > int(maxAgentTokenLifetime/time.Second) {
		lifetime = maxAgentTokenLifetime
	}
	token.ExpiresAt = time.Now().Add(lifetime)
	return nil
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
		return nil, m.agentMintRefusal(resp, body, mint)
	}

	var token oauth.Token
	if err := json.Unmarshal(body, &token); err != nil {
		// Not even the parser's complaint: it quotes a byte of the body.
		return nil, output.ErrAPI(resp.StatusCode, "minting an agent token: the token response could not be parsed")
	}
	if token.AccessToken == "" {
		return nil, output.ErrAPI(resp.StatusCode, "minting an agent token: the token response carries no access_token")
	}
	// The CLI can only represent read and full, and this value is stored
	// with the credential, written into the profile entry, and printed.
	// A response naming anything else is refused rather than persisted —
	// the same judgment the interactive login's verifier makes.
	if token.Scope != "" && token.Scope != scopeRead && token.Scope != scopeFull {
		// The value is not repeated, for the reason oauthErrorCodes gives:
		// it is another field the server chose on a request that carried
		// the secret.
		return nil, output.ErrAPI(resp.StatusCode,
			"minting an agent token: the server reported a scope other than read or full, and only those can be stored")
	}
	// Every request this CLI makes sends the token as a Bearer credential,
	// so a scheme it cannot send is not a successful login — it is one
	// that would be stored and then fail every command. An omitted type is
	// Bearer by RFC 6750 convention; the value itself is not repeated,
	// for the reason oauthErrorCodes gives.
	if token.TokenType != "" && !strings.EqualFold(token.TokenType, "bearer") {
		return nil, output.ErrAPI(resp.StatusCode,
			"minting an agent token: the server issued a token of a type this CLI cannot send; it only sends Bearer credentials")
	}
	if token.RefreshToken != "" {
		// Not fatal — the token is usable — but worth saying out loud: a
		// refresh token here means the server's idea of this grant has
		// changed, and the CLI is about to drop it on the floor.
		m.warnf("warning: the agent token response carried a refresh token; agent credentials re-mint instead and it will not be stored")
	}
	if err := applyTokenLifetime(&token, body); err != nil {
		return nil, output.ErrAPI(resp.StatusCode, "minting an agent token: "+err.Error())
	}
	return &token, nil
}

// oauthErrorCodes are the RFC 6749 §5.2 token-endpoint error codes (and
// the RFC 8628 additions a Basecamp endpoint may reuse). They are the ONLY
// thing a mint repeats from a response body.
//
// A client_credentials request puts a client secret on the wire, and a
// token endpoint is free to quote back what it rejected. Redacting the
// secret out of an echoed body is an arms race that cannot be won — a
// percent escape, an HTML entity, a control sequence splitting it in two,
// and each one only found after someone thinks of it — so nothing
// free-form is repeated at all. What is left is a fixed vocabulary, which
// is what an operator acts on anyway, and the status.
//
// The cost is `error_description`, which sometimes says something useful.
// It is not worth a credential that a terminal, a transcript and a JSON
// envelope would all carry.
var oauthErrorCodes = map[string]bool{
	"invalid_request":        true,
	"invalid_client":         true,
	"invalid_grant":          true,
	"unauthorized_client":    true,
	"unsupported_grant_type": true,
	"invalid_scope":          true,
	"access_denied":          true,
	"authorization_pending":  true,
	"expired_token":          true,
	"slow_down":              true,
}

// agentMintRefusal renders a non-200 token response: the RFC 6749 §5.2
// error code when the body carries one this package knows, and the HTTP
// status otherwise. See oauthErrorCodes for why nothing else is repeated.
func (m *Manager) agentMintRefusal(resp *http.Response, body []byte, mint *agentMint) error {
	detail := fmt.Sprintf("the server answered HTTP %d", resp.StatusCode)
	var errResp struct {
		Error string `json:"error"`
	}
	code := ""
	if json.Unmarshal(body, &errResp) == nil && oauthErrorCodes[errResp.Error] {
		code = errResp.Error
		detail = "token error: " + code
	}

	// The STATUS decides first, whatever code the body carries. A 429 is a
	// rate limit with a Retry-After to honor and a 5xx is the server's own
	// trouble and retryable, and a body naming invalid_client alongside
	// either does not make it a verdict on the caller's credentials — it
	// makes it a server saying two things at once, of which the status is
	// the one that says what to do next.
	if resp.StatusCode < 400 || resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return statusFailure("minting an agent token: "+detail, resp)
	}

	// Among the remaining 4xx: when the server NAMED its reason, that name
	// decides and the status does not get a second vote — a 403 saying
	// access_denied is a policy refusal however it is numbered, and the
	// narrowing above would mean nothing if the status could undo it.
	// When it named nothing, a 401 or 403 is a credential verdict,
	// because nothing but the credentials produces one.
	//
	// Everything else keeps its own class. A proxy's bare 400, a 408
	// nobody meant as a verdict, a 404 at a misconfigured endpoint — none
	// of them say the secret is wrong, and telling an automated caller to
	// fetch its secret again for one is advice that cannot help.
	bareUnauthorized := code == "" && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden)
	if clientRefusalCodes[code] || bareUnauthorized {
		return m.agentRemedy(output.ErrAuth("Minting an agent token was refused ("+detail+")"), mint.clientID, mint.scope)
	}
	return statusFailure("minting an agent token: "+detail, resp)
}

// clientRefusalCodes are the RFC 6749 §5.2 codes that say THE CREDENTIALS
// PRESENTED are wrong, which is the only thing piping the secret in again
// can repair.
//
// The near misses are all deliberately absent. invalid_scope refuses the
// scope, not the client. unauthorized_client refuses the client's use of
// this grant. access_denied is a policy verdict. invalid_request is about
// the request's shape and unsupported_grant_type about the server's
// capabilities. Re-running the login with the same client fixes none of
// them, and telling someone to fetch their secret again for one is advice
// that sends them looking in the wrong place.
var clientRefusalCodes = map[string]bool{
	"invalid_client": true,
	"invalid_grant":  true,
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

// agentRemedy replaces the default login remedy on an auth error raised
// on a path that already knows it is minting for an agent.
//
// The general remedy (Manager.loginRemedy) derives this from the STORED
// credential, which is right everywhere it is reached from — but not here.
// A login's first mint has not touched the store yet, so deriving the
// remedy would make rendering a refusal the thing that probes the OS
// keyring, and would answer with the interactive login (or a previous
// person's) for an operation that is explicitly an agent's. The client id
// in hand is also the one being authenticated, which the stored credential
// need not be.
func (m *Manager) agentRemedy(err error, clientID, scope string) error {
	var e *output.Error
	if !errors.As(err, &e) || e.Code != output.CodeAuth || (e.Hint != "" && e.Hint != output.DefaultAuthHint) {
		return err
	}
	hinted := *e
	hinted.Hint = "Pipe the agent's client secret in: " + m.agentLoginCommand(clientID, scope)
	return &hinted
}

// agentLoginCommand is the client-credentials login addressed to the
// active profile, naming the client whose secret is being replaced. Both
// values come from configuration and a credential store, so both are
// shell-quoted; either one that is missing — which is itself a reason this
// command is being suggested — becomes a placeholder, so the command reads
// as something to fill in rather than something to paste and watch fail.
func (m *Manager) agentLoginCommand(clientID, scope string) string {
	id := "<client-id>"
	if clientID != "" {
		id = shellQuote(clientID)
	}
	profile := "<profile>"
	if m.cfg.ActiveProfile != "" {
		profile = shellQuote(m.cfg.ActiveProfile)
	}
	command := "... | basecamp auth login --with-client-credentials --client-id " + id + " -P " + profile
	// A profile with no entry yet is one the login creates, and creating
	// one needs the account it addresses. That is not an edge case here: a
	// refused FIRST mint registers nothing, which is exactly when this
	// command is handed over. An entry that exists without an account is
	// the same situation — the login binds one, and refuses without it.
	if entry, registered := m.cfg.Profiles[m.cfg.ActiveProfile]; !registered || entry == nil || entry.AccountID == "" {
		account := "<account-id>"
		if m.cfg.AccountID != "" {
			account = shellQuote(m.cfg.AccountID)
		}
		command += " --account " + account
	}
	// A read-only agent told to re-authenticate without this would come
	// back with full access, or be refused for asking for more than its
	// client is allowed. full is the default and adding it says nothing.
	if scope != "" && scope != scopeFull {
		command += " --scope " + shellQuote(scope)
	}
	return command
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
