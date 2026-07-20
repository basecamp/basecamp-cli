// Package auth provides OAuth 2.1 authentication for Basecamp.
package auth

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/oauth"
	"github.com/basecamp/cli/pkce"

	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/hostutil"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// ClientCredentials holds OAuth client ID and secret.
type ClientCredentials struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
}

// Built-in Launchpad OAuth credentials for production.
// These are public client credentials for the native CLI app, not secrets.
const (
	launchpadClientID     = "5fdd0da8e485ae6f80f4ce0a4938640bb22f1348"
	launchpadClientSecret = "a3dc33d78258e828efd6768ac2cd67f32ec1910a" //nolint:gosec // G101: Public OAuth client secret for native app
)

// Default OAuth callback address and redirect URI.
const (
	defaultCallbackAddr = "127.0.0.1:8976"
	defaultRedirectURI  = "http://127.0.0.1:8976/callback"
)

// Manager handles OAuth authentication.
type Manager struct {
	cfg        *config.Config
	store      *Store
	httpClient *http.Client

	mu sync.Mutex
}

// NewManager creates a new auth manager.
func NewManager(cfg *config.Config, httpClient *http.Client) *Manager {
	return &Manager{
		cfg:        cfg,
		store:      NewStore(config.GlobalConfigDir()),
		httpClient: httpClient,
	}
}

// credentialKey returns the storage key for credentials.
// Profile mode: "profile:<name>", No-profile mode: origin URL.
func (m *Manager) credentialKey() string {
	if m.cfg.ActiveProfile != "" {
		return "profile:" + m.cfg.ActiveProfile
	}
	return config.NormalizeBaseURL(m.cfg.BaseURL)
}

// AccessToken returns a valid access token, refreshing if needed.
// If BASECAMP_TOKEN env var is set, it's used directly without OAuth.
func (m *Manager) AccessToken(ctx context.Context) (string, error) {
	// Check for BASECAMP_TOKEN environment variable first
	if token := os.Getenv("BASECAMP_TOKEN"); token != "" {
		return token, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	credKey := m.credentialKey()
	creds, err := m.store.Load(credKey)
	if err != nil {
		return "", output.ErrAuth(fmt.Sprintf("Not authenticated for %s: %v", credKey, err))
	}

	// Check if token is expired (with 5 minute buffer).
	// ExpiresAt==0 means non-expiring token (e.g., from BASECAMP_TOKEN env var),
	// so only refresh if ExpiresAt > 0 and is within the expiry window.
	if creds.ExpiresAt > 0 && time.Now().Unix() >= creds.ExpiresAt-300 {
		if err := m.refreshLocked(ctx, credKey, creds); err != nil {
			return "", err
		}
		// Reload refreshed credentials
		creds, err = m.store.Load(credKey)
		if err != nil {
			return "", output.ErrAuth(fmt.Sprintf("Failed to load refreshed credentials for %s: %v", credKey, err))
		}
	}

	if creds.AccessToken == "" {
		return "", output.ErrAuth(fmt.Sprintf("Stored credentials for %s have empty access token", credKey))
	}

	return creds.AccessToken, nil
}

// StoredAccessToken returns a valid access token from the credential store,
// refreshing if needed. Unlike AccessToken, this ignores the BASECAMP_TOKEN
// environment variable and always uses stored OAuth credentials.
func (m *Manager) StoredAccessToken(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	credKey := m.credentialKey()
	creds, err := m.store.Load(credKey)
	if err != nil {
		return "", output.ErrAuth(fmt.Sprintf("No stored credentials for %s: %v", credKey, err))
	}

	// Check if token is expired (with 5 minute buffer)
	if creds.ExpiresAt > 0 && time.Now().Unix() >= creds.ExpiresAt-300 {
		if err := m.refreshLocked(ctx, credKey, creds); err != nil {
			// Preserve the original error type (API, network, etc.)
			return "", err
		}
		// Reload refreshed credentials
		creds, err = m.store.Load(credKey)
		if err != nil {
			return "", output.ErrAuth(fmt.Sprintf("Failed to load refreshed credentials for %s: %v", credKey, err))
		}
	}

	if creds.AccessToken == "" {
		return "", output.ErrAuth(fmt.Sprintf("Stored credentials for %s have empty access token", credKey))
	}

	return creds.AccessToken, nil
}

// IsAuthenticated checks if there are valid credentials.
// Returns true if BASECAMP_TOKEN env var is set or if OAuth credentials exist.
func (m *Manager) IsAuthenticated() bool {
	// Check for BASECAMP_TOKEN environment variable first
	if os.Getenv("BASECAMP_TOKEN") != "" {
		return true
	}

	credKey := m.credentialKey()
	creds, err := m.store.Load(credKey)
	if err != nil {
		return false
	}
	return creds.AccessToken != ""
}

// Refresh forces a token refresh.
func (m *Manager) Refresh(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	credKey := m.credentialKey()
	creds, err := m.store.Load(credKey)
	if err != nil {
		return output.ErrAuth(fmt.Sprintf("Not authenticated for %s: %v", credKey, err))
	}

	return m.refreshLocked(ctx, credKey, creds)
}

func (m *Manager) refreshLocked(ctx context.Context, origin string, creds *Credentials) error {
	if creds.RefreshToken == "" {
		return output.ErrAuth("No refresh token available")
	}

	// Migrate old credentials missing OAuthType
	if creds.OAuthType == "" {
		creds.OAuthType = "launchpad"
	}

	// Migrate old credentials missing TokenEndpoint
	if creds.TokenEndpoint == "" {
		if creds.OAuthType == "bc3" {
			return output.ErrAuth("Stored credentials missing token endpoint — please re-authenticate: basecamp auth login")
		}
		lpURL, lpErr := m.launchpadURL()
		if lpErr != nil {
			return lpErr
		}
		creds.TokenEndpoint = lpURL + "/authorization/token"
	}

	tokenEndpoint := creds.TokenEndpoint

	// The token endpoint here is a persisted (possibly migrated) value from
	// the credential store and receives the refresh token plus client
	// credentials. The SDK's RequireSecureEndpoint only checks scheme==https,
	// so a poisoned store could still carry userinfo (https://user@evil/),
	// empty-host, or opaque/malformed https forms that it would let through.
	// Apply the same strict check used for the other OAuth endpoints before
	// any POST.
	if u, err := url.Parse(tokenEndpoint); err != nil {
		return output.ErrAuth(fmt.Sprintf("invalid token endpoint %q: %v", tokenEndpoint, err))
	} else if !isSecureEndpointURL(u) {
		return output.ErrAuth(fmt.Sprintf("invalid token endpoint %q: must be an absolute https URL (or http on loopback)", tokenEndpoint))
	}

	// Resolve client credentials for the refresh request
	var clientID, clientSecret string
	switch creds.OAuthType {
	case "bc3":
		cc, err := m.loadBC3Client()
		if err != nil {
			if os.IsNotExist(err) {
				// DCR credentials from custom-redirect logins are intentionally
				// not persisted (see registerBC3Client). After a process restart
				// the client.json won't exist and refresh is impossible.
				return output.ErrAuth("Cannot load BC3 client credentials for token refresh. " +
					"This can happen after a custom-redirect login (credentials are session-only). " +
					"Please re-authenticate: basecamp auth login")
			}
			return output.ErrAuth(fmt.Sprintf("Cannot load BC3 client credentials for token refresh: %v", err))
		}
		clientID = cc.ClientID
		clientSecret = cc.ClientSecret
	default:
		// Launchpad (or old credentials defaulted to launchpad)
		if envCreds, err := resolveClientCredentials(func(string) {}); err != nil {
			return err
		} else if envCreds != nil {
			clientID = envCreds.ClientID
			clientSecret = envCreds.ClientSecret
		} else {
			clientID = launchpadClientID
			clientSecret = launchpadClientSecret
		}
	}

	exchanger := oauth.NewExchanger(m.httpClient)

	req := oauth.RefreshRequest{
		TokenEndpoint:   tokenEndpoint,
		RefreshToken:    creds.RefreshToken,
		ClientID:        clientID,
		ClientSecret:    clientSecret,
		UseLegacyFormat: creds.OAuthType == "launchpad",
	}

	token, err := exchanger.Refresh(ctx, req)
	if err != nil {
		return output.ErrAPI(0, fmt.Sprintf("token refresh failed: %v", err))
	}

	creds.AccessToken = token.AccessToken
	if token.RefreshToken != "" {
		creds.RefreshToken = token.RefreshToken
	}
	if !token.ExpiresAt.IsZero() {
		creds.ExpiresAt = token.ExpiresAt.Unix()
	} else {
		// Server didn't return expiry — clear to zero. The existing
		// contract (auth.go:93) treats ExpiresAt==0 as non-expiring,
		// so this won't re-trigger refresh on the next call.
		creds.ExpiresAt = 0
	}

	return m.store.Save(origin, creds)
}

// LoginResult holds the outcome of a successful Login().
// Callers use this to determine the effective scope instead of their input.
type LoginResult struct {
	OAuthType string // "bc3" or "launchpad"
	Scope     string // effective scope: "read"/"full" for BC3, "" for Launchpad
}

// LoginOptions configures the login flow.
type LoginOptions struct {
	Scope     string
	NoBrowser bool // If true, don't auto-open browser, just print URL

	// Remote forces remote/headless mode: skip the loopback listener and
	// prompt the user to paste the callback URL. Auto-detected when SSH
	// env vars are present (unless Local is set).
	Remote bool

	// Local forces local mode, overriding SSH auto-detection.
	// Mutually exclusive with Remote.
	Local bool

	// InputReader is the source for pasted callback URLs in remote mode.
	// If nil, os.Stdin is used.
	InputReader io.Reader

	// RedirectURI overrides the OAuth redirect URI.
	// Takes precedence over BASECAMP_OAUTH_REDIRECT_URI and CallbackAddr.
	RedirectURI string

	// CallbackAddr is the address for the local OAuth callback server.
	// Default: "127.0.0.1:8976"
	CallbackAddr string

	// BrowserLauncher opens the authorization URL in a browser.
	// If nil, uses the default system browser launcher.
	BrowserLauncher func(url string) error

	// Logger receives status messages during the login flow.
	// If nil, messages are suppressed for headless/SDK use.
	Logger func(msg string)
}

// defaults fills in default values for LoginOptions.
func (o *LoginOptions) defaults() {
	if !o.Remote && !o.Local && hostutil.IsRemoteSession() {
		o.Remote = true
	}
	if o.Remote {
		o.NoBrowser = true
	}
	if o.BrowserLauncher == nil && !o.NoBrowser {
		o.BrowserLauncher = openBrowser
	}
}

// log outputs a message if a logger is configured.
func (o *LoginOptions) log(msg string) {
	if o.Logger != nil {
		o.Logger(msg)
	}
}

// resolveOAuthCallback determines the redirect URI and listener address for
// the OAuth callback. Precedence: LoginOptions.RedirectURI > env var
// BASECAMP_OAUTH_REDIRECT_URI > CallbackAddr-derived > hardcoded default.
func resolveOAuthCallback(opts *LoginOptions) (redirectURI string, listenAddr string, err error) {
	raw := opts.RedirectURI
	if raw == "" {
		raw = os.Getenv("BASECAMP_OAUTH_REDIRECT_URI")
	}
	if raw == "" && opts.CallbackAddr != "" {
		raw = "http://" + opts.CallbackAddr + "/callback"
	}
	if raw == "" {
		return defaultRedirectURI, defaultCallbackAddr, nil
	}

	u, parseErr := url.Parse(raw)
	if parseErr != nil || !u.IsAbs() {
		return "", "", output.ErrAuth(fmt.Sprintf("invalid redirect URI %q: must be an absolute URL", raw))
	}
	if u.Scheme != "http" {
		return "", "", output.ErrAuth(fmt.Sprintf("invalid redirect URI %q: scheme must be http (RFC 8252 loopback)", raw))
	}
	if !hostutil.IsLocalhost(u.Host) {
		return "", "", output.ErrAuth(fmt.Sprintf("invalid redirect URI %q: host must be loopback (localhost, 127.0.0.1, [::1])", raw))
	}
	if u.Port() == "" {
		return "", "", output.ErrAuth(fmt.Sprintf("invalid redirect URI %q: port is required", raw))
	}
	if u.User != nil {
		return "", "", output.ErrAuth(fmt.Sprintf("invalid redirect URI %q: userinfo not allowed", raw))
	}
	if u.RawQuery != "" {
		return "", "", output.ErrAuth(fmt.Sprintf("invalid redirect URI %q: query string not allowed", raw))
	}
	if u.Fragment != "" {
		return "", "", output.ErrAuth(fmt.Sprintf("invalid redirect URI %q: fragment not allowed", raw))
	}

	return raw, u.Host, nil
}

// Login initiates the OAuth login flow.
func (m *Manager) Login(ctx context.Context, opts LoginOptions) (*LoginResult, error) {
	if opts.Remote && opts.Local {
		return nil, output.ErrUsage("--remote and --local are mutually exclusive")
	}

	// Validate scope early (single source of truth)
	if opts.Scope != "" && opts.Scope != "read" && opts.Scope != "full" {
		return nil, output.ErrUsage("Invalid scope. Use 'read' or 'full'")
	}

	opts.defaults()

	// Resolve redirect URI and listener address
	redirectURI, listenAddr, err := resolveOAuthCallback(&opts)
	if err != nil {
		return nil, err
	}
	opts.RedirectURI = redirectURI

	// Log overrides
	if redirectURI != defaultRedirectURI {
		opts.log(fmt.Sprintf("Using custom redirect URI: %s", redirectURI))
	}

	credKey := m.credentialKey()

	// Discover OAuth config
	oauthCfg, oauthType, err := m.discoverOAuth(ctx, opts.log)
	if err != nil {
		return nil, err
	}

	// Device-only authorization servers omit the authorization endpoint.
	// Assert authorization-code capability before scope handling and client
	// registration so an unsupported flow can't trigger DCR side effects.
	if oauthCfg.AuthorizationEndpoint == nil || *oauthCfg.AuthorizationEndpoint == "" {
		return nil, output.ErrAuth("OAuth server does not advertise an authorization endpoint (authorization-code flow unsupported)")
	}

	// Apply provider-aware scope rules
	effectiveScope := opts.Scope
	if oauthType == "launchpad" {
		if effectiveScope != "" {
			opts.log("Launchpad does not support OAuth scopes; --scope ignored")
		}
		effectiveScope = ""
	} else {
		// BC3: default to "read" when no scope specified
		if effectiveScope == "" {
			effectiveScope = "read"
		}
	}

	// Load or register client credentials
	clientCreds, err := m.loadClientCredentials(ctx, oauthCfg, oauthType, &opts)
	if err != nil {
		return nil, err
	}

	// Generate PKCE challenge (for BC3)
	var codeVerifier, codeChallenge string
	if oauthType == "bc3" {
		codeVerifier = pkce.GenerateVerifier()
		codeChallenge = pkce.GenerateChallenge(codeVerifier)
	}

	// Generate state for CSRF protection
	state := pkce.GenerateState()

	// Build authorization URL
	authURL, err := m.buildAuthURL(oauthCfg, oauthType, effectiveScope, state, codeChallenge, clientCreds.ClientID, &opts)
	if err != nil {
		return nil, err
	}

	var code string
	resolve := func(bool) {} // no-op default for remote mode

	if opts.Remote {
		// Remote/headless mode: prompt user to paste callback URL
		opts.log("\nRemote Authentication")
		opts.log("")
		opts.log("  1. Open this URL in a browser on any device:")
		opts.log("     " + authURL)
		opts.log("")
		opts.log("  2. Sign in to Basecamp when prompted.")
		opts.log("")
		opts.log("  3. Your browser will redirect to a URL starting with:")
		opts.log("     " + redirectURI + "?code=...&state=...")
		opts.log("     The page will show a connection error — that's expected.")
		opts.log("")
		opts.log("  4. Copy the full URL from your browser's address bar and")
		opts.log("     paste it below.")
		opts.log("")

		reader := opts.InputReader
		if reader == nil {
			reader = os.Stdin
		}
		opts.log("Paste the callback URL: ")

		waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()

		code, err = readCallbackInput(waitCtx, reader, state)
		if err != nil {
			return nil, err
		}
	} else {
		// Local mode: start listener and wait for callback
		lc := net.ListenConfig{}
		listener, listenErr := lc.Listen(ctx, "tcp", listenAddr)
		if listenErr != nil {
			return nil, fmt.Errorf("failed to start callback server: %w", listenErr)
		}
		defer func() { _ = listener.Close() }()

		// Open browser for authentication
		if opts.BrowserLauncher != nil {
			if launchErr := opts.BrowserLauncher(authURL); launchErr != nil {
				opts.log("\nCouldn't open browser automatically.\nOpen this URL in your browser:\n" + authURL + "\n\nWaiting for authentication...")
			} else {
				opts.log("\nOpening browser for authentication...")
				opts.log("If the browser doesn't open, visit: " + authURL + "\n\nWaiting for authentication...")
			}
		} else {
			opts.log("\nOpen this URL in your browser:\n" + authURL + "\n\nWaiting for authentication...")
		}

		// Wait for OAuth callback with a hard timeout to avoid hanging indefinitely
		waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()

		code, resolve, err = waitForCallback(waitCtx, state, listener)
		if err != nil {
			return nil, err
		}
	}
	defer resolve(false) // safety net: signal failure if we return without explicit resolve

	// Exchange code for tokens
	creds, err := m.exchangeCode(ctx, oauthCfg, oauthType, code, codeVerifier, clientCreds, &opts)
	if err != nil {
		return nil, err
	}

	creds.OAuthType = oauthType
	creds.TokenEndpoint = oauthCfg.TokenEndpoint
	creds.Scope = effectiveScope

	if err := m.store.Save(credKey, creds); err != nil {
		return nil, err
	}

	resolve(true)
	return &LoginResult{OAuthType: oauthType, Scope: effectiveScope}, nil
}

// Logout removes stored credentials.
func (m *Manager) Logout() error {
	credKey := m.credentialKey()
	return m.store.Delete(credKey)
}

func (m *Manager) discoverOAuth(ctx context.Context, log func(string)) (*oauth.Config, string, error) {
	// The SDK binds the discovered issuer to this string code-point exact
	// (RFC 8414), so a trailing slash in the configured base URL would
	// mismatch the server's issuer and incorrectly fall back to Launchpad.
	baseURL := config.NormalizeBaseURL(m.cfg.BaseURL)
	discoverer := oauth.NewDiscoverer(m.httpClient)
	cfg, err := discoverer.Discover(ctx, baseURL)
	if err != nil {
		log(fmt.Sprintf("warning: OAuth discovery failed for %s, using Launchpad fallback", baseURL))
		// Fallback to Launchpad
		lpURL, lpErr := m.launchpadURL()
		if lpErr != nil {
			return nil, "", lpErr
		}
		authzEndpoint := lpURL + "/authorization/new"
		fallbackCfg := &oauth.Config{
			AuthorizationEndpoint: &authzEndpoint,
			TokenEndpoint:         lpURL + "/authorization/token",
		}
		log(fmt.Sprintf("Authenticating via launchpad (%s)", authzEndpoint))
		return fallbackCfg, "launchpad", nil
	}
	log(fmt.Sprintf("Authenticating via bc3 (%s)", strOrEmpty(cfg.AuthorizationEndpoint)))
	return cfg, "bc3", nil
}

// strOrEmpty returns the value of p, or "" when p is nil.
func strOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (m *Manager) launchpadURL() (string, error) {
	if u := os.Getenv("BASECAMP_LAUNCHPAD_URL"); u != "" {
		if err := hostutil.RequireSecureURL(u); err != nil {
			return "", fmt.Errorf("BASECAMP_LAUNCHPAD_URL: %w", err)
		}
		return u, nil
	}
	return "https://launchpad.37signals.com", nil
}

func (m *Manager) loadClientCredentials(ctx context.Context, oauthCfg *oauth.Config, oauthType string, opts *LoginOptions) (*ClientCredentials, error) {
	if oauthType == "bc3" {
		// BC3 with default redirect: try stored client first
		if opts.RedirectURI == defaultRedirectURI {
			creds, err := m.loadBC3Client()
			if err == nil {
				return creds, nil
			}
		}

		// Register new client via DCR
		if oauthCfg.RegistrationEndpoint == nil || *oauthCfg.RegistrationEndpoint == "" {
			return nil, output.ErrAuth("OAuth server does not support Dynamic Client Registration")
		}
		return m.registerBC3Client(ctx, *oauthCfg.RegistrationEndpoint, opts)
	}

	// Launchpad: resolve client credentials from env vars
	creds, err := resolveClientCredentials(opts.log)
	if err != nil {
		return nil, err
	}
	if creds != nil {
		return creds, nil
	}

	// Use built-in defaults for production Launchpad
	return &ClientCredentials{
		ClientID:     launchpadClientID,
		ClientSecret: launchpadClientSecret,
	}, nil
}

// resolveClientCredentials reads OAuth client credentials from environment
// variables BASECAMP_OAUTH_CLIENT_ID and BASECAMP_OAUTH_CLIENT_SECRET.
// Both must be set together. Returns nil, nil when neither is set.
func resolveClientCredentials(log func(string)) (*ClientCredentials, error) {
	clientID := os.Getenv("BASECAMP_OAUTH_CLIENT_ID")
	clientSecret := os.Getenv("BASECAMP_OAUTH_CLIENT_SECRET")

	if clientID == "" && clientSecret == "" {
		return nil, nil
	}
	if clientID == "" {
		return nil, output.ErrAuth("BASECAMP_OAUTH_CLIENT_ID is required when BASECAMP_OAUTH_CLIENT_SECRET is set")
	}
	if clientSecret == "" {
		return nil, output.ErrAuth("BASECAMP_OAUTH_CLIENT_SECRET is required when BASECAMP_OAUTH_CLIENT_ID is set")
	}

	log("Using custom OAuth client credentials from BASECAMP_OAUTH_CLIENT_ID/SECRET")
	return &ClientCredentials{ClientID: clientID, ClientSecret: clientSecret}, nil
}

func (m *Manager) loadBC3Client() (*ClientCredentials, error) {
	clientFile := config.GlobalConfigDir() + "/client.json"
	data, err := os.ReadFile(clientFile) //nolint:gosec // G304: Path is from trusted config dir
	if err != nil {
		return nil, err
	}

	var creds ClientCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}

	if creds.ClientID == "" {
		return nil, fmt.Errorf("no client_id in stored credentials")
	}

	return &creds, nil
}

// isSecureEndpointURL reports whether u uses a scheme safe for OAuth endpoints
// derived from the server-controlled discovery document: https, or http only on
// loopback for local development. The URL must also be absolute with a hostname —
// url.Parse accepts opaque forms like "https:foo" and port-only authorities like
// "https://:3000/" that carry the right scheme but no hostname, which would
// otherwise slip through to the transport or browser launcher. URLs carrying
// userinfo (user:pass@host) are rejected outright: they enable phishing
// displays in browsers and net/http synthesizes a Basic Authorization header
// from them. Centralizing the rule keeps the registration, authorization, and
// redirect-following checks consistent.
func isSecureEndpointURL(u *url.URL) bool {
	if u.Hostname() == "" {
		return false
	}
	// Userinfo enables phishing display in browsers ("evil.example@real.host")
	// and Basic-auth synthesis in net/http requests.
	if u.User != nil {
		return false
	}
	// IsLocalhost takes the host:port form and strips the port itself.
	return u.Scheme == "https" || (u.Scheme == "http" && hostutil.IsLocalhost(u.Host))
}

func (m *Manager) registerBC3Client(ctx context.Context, registrationEndpoint string, opts *LoginOptions) (*ClientCredentials, error) {
	// The registration endpoint comes from the server-controlled discovery
	// document. Restrict it to https (or http on loopback for local
	// development) so a hostile discovery doc can't hand the DCR POST a
	// file:// (or other) scheme that RequireSecureURL would let through.
	// Mirrors buildAuthURL's scheme whitelist.
	u, err := url.Parse(registrationEndpoint)
	if err != nil {
		return nil, output.ErrAuth(fmt.Sprintf("invalid registration endpoint %q: %v", registrationEndpoint, err))
	}
	if !isSecureEndpointURL(u) {
		return nil, output.ErrAuth(fmt.Sprintf("invalid registration endpoint %q: must be an absolute https URL (or http on loopback)", registrationEndpoint))
	}

	customRedirect := opts.RedirectURI != defaultRedirectURI
	regReq := map[string]any{
		"client_name":                "basecamp-cli",
		"client_uri":                 "https://github.com/basecamp/basecamp-cli",
		"redirect_uris":              []string{opts.RedirectURI},
		"grant_types":                []string{"authorization_code"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}

	body, err := json.Marshal(regReq)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", registrationEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	// Use a dedicated client with its own CheckRedirect guard. The DCR POST body
	// carries only client metadata (no auth code or refresh token), so following a
	// proxy-canonicalized 3xx redirect is safe — and necessary, since the manager's
	// guarded client would silently fail first-time login on such redirects. But Go
	// replays the POST body on 307/308, so re-validate EACH hop's target with the
	// same scheme rule applied to the registration endpoint; otherwise a hostile
	// server could 307 the body to a file:// or non-loopback http:// URL, escaping
	// the whitelist that only covered the original endpoint.
	checkRedirect := func(req *http.Request, via []*http.Request) error {
		if !isSecureEndpointURL(req.URL) {
			return output.ErrAuth(fmt.Sprintf("refusing DCR redirect to %q: must be an absolute https URL (or http on loopback)", req.URL.String()))
		}
		// Only 307/308 preserve the POST method and body. On 301/302/303 Go
		// downgrades the upcoming request (req here) to a body-less GET, so the
		// registration would silently arrive empty and first-time login would
		// fail confusingly. Refuse instead of resending as GET.
		if req.Method != http.MethodPost {
			return output.ErrAuth(fmt.Sprintf("refusing DCR redirect to %q: redirect downgraded the registration POST to %s, dropping the request body; the endpoint must redirect with 307/308", req.URL.String(), req.Method))
		}
		// A redirect loop is a deterministic endpoint misconfiguration, not a
		// transient network failure: return an auth-class error so the
		// errors.As unwrap below surfaces it directly instead of masking it
		// as a retryable output.ErrNetwork.
		if len(via) >= 10 {
			return output.ErrAuth(fmt.Sprintf("registration endpoint redirect loop: stopped after 10 redirects at %q", req.URL.String()))
		}
		return nil
	}
	var dcrClient *http.Client
	if m.httpClient != nil {
		c := *m.httpClient // http.Client has no locks; value copy is safe
		c.CheckRedirect = checkRedirect
		dcrClient = &c
	} else {
		dcrClient = &http.Client{Timeout: 30 * time.Second, CheckRedirect: checkRedirect}
	}
	resp, err := dcrClient.Do(req)
	if err != nil {
		// A CheckRedirect rejection surfaces as a *url.Error wrapping our
		// output.ErrAuth. Surface it directly rather than masking the security
		// failure as a retryable network error.
		var outErr *output.Error
		if errors.As(err, &outErr) {
			return nil, outErr
		}
		return nil, output.ErrNetwork(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10)) // 64 KB limit
		return nil, output.ErrAPI(resp.StatusCode, fmt.Sprintf("DCR failed: %s", string(respBody)))
	}

	var regResp struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret,omitempty"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&regResp); err != nil { // 64 KB limit
		return nil, err
	}

	if regResp.ClientID == "" {
		return nil, fmt.Errorf("no client_id in DCR response")
	}

	creds := &ClientCredentials{
		ClientID:     regResp.ClientID,
		ClientSecret: regResp.ClientSecret,
	}

	// Only persist DCR credentials when using the default redirect URI.
	// Custom redirect URIs are session-only to prevent stale client.json
	// entries that would fail on subsequent runs without the override.
	if !customRedirect {
		if err := m.saveBC3Client(creds); err != nil {
			return nil, err
		}
	}

	return creds, nil
}

func (m *Manager) saveBC3Client(creds *ClientCredentials) error {
	configDir := config.GlobalConfigDir()
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return err
	}

	data, err := json.Marshal(creds)
	if err != nil {
		return err
	}

	clientFile := configDir + "/client.json"
	return os.WriteFile(clientFile, data, 0600)
}

func (m *Manager) buildAuthURL(cfg *oauth.Config, oauthType, scope, state, codeChallenge, clientID string, opts *LoginOptions) (string, error) {
	// Login asserts this before any side effects; kept as a defensive check
	// for other callers.
	if cfg.AuthorizationEndpoint == nil || *cfg.AuthorizationEndpoint == "" {
		return "", output.ErrAuth("OAuth server does not advertise an authorization endpoint (authorization-code flow unsupported)")
	}
	u, err := url.Parse(*cfg.AuthorizationEndpoint)
	if err != nil {
		return "", output.ErrAuth(fmt.Sprintf("invalid authorization endpoint %q: %v", *cfg.AuthorizationEndpoint, err))
	}

	// The authorization endpoint comes from the server-controlled discovery
	// document and is later dispatched to the OS browser handler (xdg-open /
	// open). Restrict it to https (or http on loopback for local development)
	// so a hostile discovery doc can't hand the OS a file:// (or other) URL.
	if !isSecureEndpointURL(u) {
		return "", output.ErrAuth(fmt.Sprintf("invalid authorization endpoint %q: must be an absolute https URL (or http on loopback)", *cfg.AuthorizationEndpoint))
	}

	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", opts.RedirectURI)
	q.Set("state", state)

	if oauthType == "bc3" {
		q.Set("code_challenge", codeChallenge)
		q.Set("code_challenge_method", "S256")
		if scope != "" {
			q.Set("scope", scope)
		}
	} else {
		q.Set("type", "web_server")
	}

	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (m *Manager) exchangeCode(ctx context.Context, cfg *oauth.Config, oauthType, code, codeVerifier string, clientCreds *ClientCredentials, opts *LoginOptions) (*Credentials, error) {
	// The token endpoint comes from the server-controlled discovery document
	// and receives the authorization code plus client credentials. The SDK's
	// RequireSecureEndpoint only checks scheme==https, which lets userinfo
	// (https://legit@evil.com/token) and empty-host forms through. Apply the
	// same strict check used for the registration and authorization endpoints.
	u, err := url.Parse(cfg.TokenEndpoint)
	if err != nil {
		return nil, output.ErrAuth(fmt.Sprintf("invalid token endpoint %q: %v", cfg.TokenEndpoint, err))
	}
	if !isSecureEndpointURL(u) {
		return nil, output.ErrAuth(fmt.Sprintf("invalid token endpoint %q: must be an absolute https URL (or http on loopback)", cfg.TokenEndpoint))
	}

	exchanger := oauth.NewExchanger(m.httpClient)

	req := oauth.ExchangeRequest{
		TokenEndpoint:   cfg.TokenEndpoint,
		Code:            code,
		RedirectURI:     opts.RedirectURI,
		ClientID:        clientCreds.ClientID,
		ClientSecret:    clientCreds.ClientSecret,
		CodeVerifier:    codeVerifier,
		UseLegacyFormat: oauthType == "launchpad",
	}

	token, err := exchanger.Exchange(ctx, req)
	if err != nil {
		return nil, output.ErrAPI(0, fmt.Sprintf("token exchange failed: %v", err))
	}

	creds := &Credentials{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
	}
	if !token.ExpiresAt.IsZero() {
		creds.ExpiresAt = token.ExpiresAt.Unix()
	}
	return creds, nil
}

// parseCallbackURL extracts the authorization code from a pasted callback URL.
// It trims whitespace, strips surrounding quotes/backticks, validates the state
// parameter, and checks for OAuth error responses.
func parseCallbackURL(rawURL, expectedState string) (string, error) {
	// Trim whitespace and surrounding quotes/backticks
	rawURL = strings.TrimSpace(rawURL)
	rawURL = strings.Trim(rawURL, "\"'`")
	rawURL = strings.TrimSpace(rawURL)

	if rawURL == "" {
		return "", fmt.Errorf("empty callback URL")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid callback URL: %w", err)
	}

	q := u.Query()

	// Check for OAuth error response
	if errParam := q.Get("error"); errParam != "" {
		desc := q.Get("error_description")
		if desc != "" {
			return "", fmt.Errorf("OAuth error: %s — %s", errParam, desc)
		}
		return "", fmt.Errorf("OAuth error: %s", errParam)
	}

	code := q.Get("code")
	if code == "" {
		return "", fmt.Errorf("no authorization code in callback URL")
	}

	state := q.Get("state")
	if state != expectedState {
		return "", fmt.Errorf("state mismatch: expected %q, got %q", expectedState, state)
	}

	return code, nil
}

// readCallbackInput reads one line from reader and parses it as a callback URL.
// It respects context cancellation for timeout support.
//
// On context cancellation the blocked read goroutine is orphaned. This is
// acceptable for a CLI process that exits shortly after Login returns. Callers
// in long-lived processes should pass an io.ReadCloser and close it on error
// to unblock the goroutine.
func readCallbackInput(ctx context.Context, reader io.Reader, expectedState string) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)

	go func() {
		scanner := bufio.NewScanner(reader)
		if scanner.Scan() {
			ch <- result{line: scanner.Text()}
		} else if err := scanner.Err(); err != nil {
			ch <- result{err: err}
		} else {
			ch <- result{err: fmt.Errorf("no input received")}
		}
	}()

	select {
	case <-ctx.Done():
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("timed out waiting for callback URL: %w", ctx.Err())
		}
		return "", fmt.Errorf("canceled waiting for callback URL: %w", ctx.Err())
	case r := <-ch:
		if r.err != nil {
			return "", r.err
		}
		return parseCallbackURL(r.line, expectedState)
	}
}

// openBrowser opens the specified URL in the default browser.
func openBrowser(url string) error {
	return hostutil.OpenBrowser(url)
}

// bc3TokenPrefix is the prefix for tokens issued by Basecamp 3's OAuth server.
const bc3TokenPrefix = "bc_at_"

// AuthorizationEndpoint returns the authorization info endpoint URL for the
// current authentication context. BASECAMP_TOKEN takes precedence over stored
// credentials (mirroring AccessToken), with the token prefix used to determine
// the issuer. When no env token is set, stored OAuth type drives selection.
func (m *Manager) AuthorizationEndpoint(ctx context.Context) (string, error) {
	// BASECAMP_TOKEN wins — match AccessToken() precedence (auth.go line 75).
	if envToken := os.Getenv("BASECAMP_TOKEN"); envToken != "" {
		if strings.HasPrefix(envToken, bc3TokenPrefix) {
			return config.NormalizeBaseURL(m.cfg.BaseURL) + "/authorization.json", nil
		}
		lpURL, err := m.launchpadURL()
		if err != nil {
			return "", err
		}
		return strings.TrimSuffix(lpURL, "/") + "/authorization.json", nil
	}

	oauthType := m.GetOAuthType()
	switch oauthType {
	case "bc3":
		return config.NormalizeBaseURL(m.cfg.BaseURL) + "/authorization.json", nil
	case "launchpad", "":
		// "launchpad" = stored credentials; "" = no stored credentials and
		// no env token (shouldn't normally reach here since IsAuthenticated
		// would have caught it, but handle gracefully).
		lpURL, err := m.launchpadURL()
		if err != nil {
			return "", err
		}
		return strings.TrimSuffix(lpURL, "/") + "/authorization.json", nil
	default:
		return "", output.ErrAuth("Unknown OAuth type: " + oauthType)
	}
}

// GetOAuthType returns the OAuth type for the current credential key ("bc3" or "launchpad").
func (m *Manager) GetOAuthType() string {
	credKey := m.credentialKey()
	creds, err := m.store.Load(credKey)
	if err != nil {
		return ""
	}
	return creds.OAuthType
}

// GetUserEmail returns the stored user email for the current credential key.
func (m *Manager) GetUserEmail() string {
	credKey := m.credentialKey()
	creds, err := m.store.Load(credKey)
	if err != nil {
		return ""
	}
	return creds.UserEmail
}

// SetUserEmail stores the user email for the current credential key
// without modifying the stored user ID.
func (m *Manager) SetUserEmail(email string) error {
	credKey := m.credentialKey()
	creds, err := m.store.Load(credKey)
	if err != nil {
		return err
	}
	creds.UserEmail = email
	return m.store.Save(credKey, creds)
}

// SetUserIdentity stores the user ID and email for the current credential key.
func (m *Manager) SetUserIdentity(userID, email string) error {
	credKey := m.credentialKey()
	creds, err := m.store.Load(credKey)
	if err != nil {
		return err
	}
	creds.UserID = userID
	creds.UserEmail = email
	return m.store.Save(credKey, creds)
}

// CredentialKey returns the current credential storage key.
// This is exported for use in commands that need to display or lookup credentials.
func (m *Manager) CredentialKey() string {
	return m.credentialKey()
}

// GetStore returns the credential store.
func (m *Manager) GetStore() *Store {
	return m.store
}

// SetStore replaces the credential store. Used in tests to inject
// a file-backed store rooted in a temp directory.
func (m *Manager) SetStore(s *Store) {
	m.store = s
}
