// Package auth provides OAuth 2.1 authentication for Basecamp.
package auth

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/oauth"
	"github.com/basecamp/cli/pkce"

	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/hostutil"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
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

// bc5ClientID is the pre-registered public client for the BC5 device flow.
// Public client: no secret.
const bc5ClientID = "basecamp-cli"

// OAuth provider types stored in credentials. Legacy "bc3" (the removed
// DCR/PKCE development flow) may still appear in stored credentials.
const (
	oauthTypeBC5       = "bc5"
	oauthTypeLaunchpad = "launchpad"
)

// OAuth scopes the BC5 client is registered for. Launchpad ignores scope
// entirely; its tokens are read-write.
const (
	scopeRead = "read"
	scopeFull = "full"
)

// CredentialSourceToken marks a credential imported from a personal access
// token rather than obtained through an OAuth flow.
const CredentialSourceToken = "token"

// RefreshWindow is how long before its expiry a stored access token stops
// being served as-is: AccessToken refreshes inside this window, and a
// credential with nothing to refresh with (an imported token) is refused
// there instead. Exported so an import can decline a token that would be
// unusable from its first command.
const RefreshWindow = 5 * time.Minute

// Default OAuth callback address and redirect URI.
const (
	defaultCallbackAddr = "127.0.0.1:8976"
	defaultRedirectURI  = "http://127.0.0.1:8976/callback"
)

// Manager handles OAuth authentication.
type Manager struct {
	cfg   *config.Config
	store *Store

	// httpClient is the caller-owned injection seam, used by tests to stub
	// OAuth traffic. When non-nil it carries EVERY OAuth request: it
	// collapses the per-provenance lanes below and bypasses the SDK's
	// address enforcement by design (the SDK's "yours, enforcement
	// included" contract). Production construction (appctx) passes nil and
	// the Manager builds the per-lane policed clients itself.
	httpClient *http.Client

	// Per-provenance OAuth egress lanes (see client.go): BC5 traffic rides a
	// client whose address policy derives from cfg.BaseURL, Launchpad
	// traffic one derived from launchpadURL(). Built lazily so a malformed
	// anchor fails the OAuth operation that needed it, cached for the
	// Manager's lifetime.
	bc5Lane oauthLane
	lpLane  oauthLane

	// proxyEnv is the one construction-time proxy-environment snapshot both
	// lanes share, built under proxyOnce on first lane use.
	proxyOnce sync.Once
	proxyEnv  *proxyEnvState

	// Warnf receives transport-policy warnings (a proxy ignored for OAuth
	// traffic, a malformed opt-out value). Test seam; nil means stderr.
	Warnf func(format string, args ...any)

	mu sync.Mutex
}

// NewManager creates a new auth manager.
//
// A nil httpClient is the production configuration: OAuth requests ride
// per-provenance clients that enforce the SDK's address policy at dial time.
// A non-nil httpClient is caller-owned (test-only in this codebase) and
// carries every OAuth request as-is — no address policy is applied on top.
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
	if creds.ExpiresAt > 0 && time.Now().Unix() >= creds.ExpiresAt-int64(RefreshWindow.Seconds()) {
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

	// Check if token is expired (with the refresh-window buffer)
	if creds.ExpiresAt > 0 && time.Now().Unix() >= creds.ExpiresAt-int64(RefreshWindow.Seconds()) {
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
		creds.OAuthType = oauthTypeLaunchpad
	}

	// Migrate old credentials missing TokenEndpoint
	if creds.TokenEndpoint == "" {
		if creds.OAuthType == "bc3" || creds.OAuthType == oauthTypeBC5 {
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
	// credentials. A poisoned store could carry userinfo (https://user@evil/),
	// empty-host, or opaque/malformed https forms, so apply the same strict
	// check used for the other OAuth endpoints before any POST.
	if err := requireSecureOAuthEndpoint("token endpoint", tokenEndpoint); err != nil {
		return err
	}

	// Resolve client credentials for the refresh request
	var clientID, clientSecret string
	switch creds.OAuthType {
	case "bc3":
		// DCR-era development flow, removed. Its per-install dynamic clients
		// can't be resolved anymore, so the refresh token is unusable.
		return output.ErrAuth("Stored credentials are from a removed development flow — please re-authenticate: basecamp auth login")
	case oauthTypeBC5:
		// Pre-registered public client: no secret.
		clientID = bc5ClientID
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

	// The refresh lane follows the stored credential's provenance: bc5-typed
	// credentials refresh against a BC5-discovered endpoint, everything else
	// is Launchpad. OAuthType and TokenEndpoint are persisted independently —
	// this selects a policy anchor, it does not validate their binding.
	laneClient, laneErr := m.launchpadClient()
	if creds.OAuthType == oauthTypeBC5 {
		laneClient, laneErr = m.bc5Client()
	}
	if laneErr != nil {
		return laneErr
	}
	exchanger := oauth.NewExchanger(laneClient)

	req := oauth.RefreshRequest{
		TokenEndpoint: tokenEndpoint,
		RefreshToken:  creds.RefreshToken,
		ClientID:      clientID,
		ClientSecret:  clientSecret,
		// Echo the stored RFC 8707 resource indicator (sent only when set):
		// BC5 multi-account refresh tokens are rejected without it.
		Resource:        creds.Resource,
		UseLegacyFormat: creds.OAuthType == oauthTypeLaunchpad,
	}

	token, err := exchanger.Refresh(ctx, req)
	if err != nil {
		return wrapOAuthError("token refresh failed", err)
	}

	creds.AccessToken = token.AccessToken
	if token.RefreshToken != "" {
		creds.RefreshToken = token.RefreshToken
	}
	// An omitted resource preserves the stored binding (carry-forward, like an
	// omitted rotated refresh token); a present one replaces it.
	if token.Resource != "" {
		creds.Resource = token.Resource
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
	OAuthType string // "bc5" or "launchpad" (stored credentials may also carry legacy "bc3")
	Scope     string // effective scope: "read"/"full" for BC5, "" for Launchpad
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

	// LoginHint names the account (email address) the user should sign in
	// as. The device flow sends it as Basecamp's login_hint extension to the
	// device authorization request, where it steers the sign-in page and
	// never authenticates on its own; Launchpad's authorization-code flow
	// ignores it.
	LoginHint string

	// Verify, when set, is called with the freshly issued access token and
	// its provider type ("bc5" or "launchpad") before anything is stored. A
	// non-nil error aborts the login and nothing is written: the credential
	// is proven first and persisted second.
	Verify func(ctx context.Context, accessToken, oauthType string) error

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

	// deviceOptions are appended last to the SDK device-flow options.
	// Test seam: lets tests inject WithDeviceSleep/WithDeviceClock.
	deviceOptions []oauth.DeviceOption
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

// Login initiates the OAuth login flow. Discovery selects the provider:
// a BC5 issuer runs the RFC 8628 device flow; the Launchpad fallback runs
// the authorization-code flow with a loopback (or pasted) callback.
func (m *Manager) Login(ctx context.Context, opts LoginOptions) (*LoginResult, error) {
	if opts.Remote && opts.Local {
		return nil, output.ErrUsage("--remote and --local are mutually exclusive")
	}

	// Validate scope early (single source of truth)
	if opts.Scope != "" && opts.Scope != scopeRead && opts.Scope != scopeFull {
		return nil, output.ErrUsage("Invalid scope. Use 'read' or 'full'")
	}

	opts.defaults()

	credKey := m.credentialKey()

	disc, err := m.discoverOAuth(ctx, opts.log)
	if err != nil {
		return nil, err
	}

	if disc.oauthType == oauthTypeBC5 {
		return m.loginDevice(ctx, credKey, disc.config, &opts)
	}
	return m.loginLaunchpad(ctx, credKey, disc.config, &opts)
}

// loginLaunchpad runs the authorization-code flow against Launchpad:
// browser (or printed) auth URL, then a loopback callback or pasted
// callback URL in remote mode.
func (m *Manager) loginLaunchpad(ctx context.Context, credKey string, oauthCfg *oauth.Config, opts *LoginOptions) (*LoginResult, error) {
	// Resolve redirect URI and listener address
	redirectURI, listenAddr, err := resolveOAuthCallback(opts)
	if err != nil {
		return nil, err
	}
	opts.RedirectURI = redirectURI

	// Log overrides
	if redirectURI != defaultRedirectURI {
		opts.log(fmt.Sprintf("Using custom redirect URI: %s", redirectURI))
	}

	if opts.Scope != "" {
		opts.log("Launchpad does not support OAuth scopes; --scope ignored")
	}
	if opts.LoginHint != "" {
		opts.log("Launchpad does not support login hints; --login-hint ignored")
	}

	clientCreds, err := launchpadClientCredentials(opts.log)
	if err != nil {
		return nil, err
	}

	// Generate state for CSRF protection
	state := pkce.GenerateState()

	if oauthCfg.AuthorizationEndpoint == nil {
		return nil, output.ErrAuth("authorization server did not advertise an authorization endpoint")
	}

	// Build authorization URL
	authURL, err := m.buildAuthURL(*oauthCfg.AuthorizationEndpoint, state, clientCreds.ClientID, opts)
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
	creds, err := m.exchangeCode(ctx, oauthCfg, code, clientCreds, opts)
	if err != nil {
		return nil, err
	}

	creds.OAuthType = oauthTypeLaunchpad
	creds.TokenEndpoint = oauthCfg.TokenEndpoint
	creds.Scope = ""

	if opts.Verify != nil {
		if err := opts.Verify(ctx, creds.AccessToken, oauthTypeLaunchpad); err != nil {
			return nil, err
		}
	}
	if err := m.store.Save(credKey, creds); err != nil {
		return nil, err
	}

	resolve(true)
	return &LoginResult{OAuthType: oauthTypeLaunchpad, Scope: ""}, nil
}

// loginDevice runs the RFC 8628 device authorization grant against a
// discovered BC5 issuer as the pre-registered public client. The display
// callback is the trust boundary for the server-controlled device
// authorization response: URIs are validated before launch and all printed
// copies are sanitized.
func (m *Manager) loginDevice(ctx context.Context, credKey string, oauthCfg *oauth.Config, opts *LoginOptions) (*LoginResult, error) {
	// Both endpoints come from the server-controlled discovery document;
	// reject unsafe forms before any POST. A nil device endpoint is left to
	// the SDK's capability guard, which fails without making a request.
	if err := requireSecureOAuthEndpoint("token endpoint", oauthCfg.TokenEndpoint); err != nil {
		return nil, err
	}
	if oauthCfg.DeviceAuthorizationEndpoint != nil {
		if err := requireSecureOAuthEndpoint("device authorization endpoint", *oauthCfg.DeviceAuthorizationEndpoint); err != nil {
			return nil, err
		}
	}

	// Request a scope explicitly rather than letting the server pick. BC5
	// defaults an omitted scope to its least-privilege entry (read), which
	// would silently hand every write command a 403 — Launchpad logins have
	// always been read-write, so an unqualified `auth login` keeps meaning
	// that here. --scope read is how a caller asks for less.
	requestedScope := opts.Scope
	if requestedScope == "" {
		requestedScope = scopeFull
	}

	// The device-authorization POST and the token polling both ride the BC5
	// lane client (the SDK carries them on the one WithDeviceHTTPClient
	// client), so both endpoints the discovery document named are judged by
	// the policy cfg.BaseURL earned.
	deviceClient, err := m.bc5Client()
	if err != nil {
		return nil, err
	}
	devOpts := make([]oauth.DeviceOption, 0, 3+len(opts.deviceOptions))
	devOpts = append(devOpts,
		oauth.WithDeviceHTTPClient(deviceClient),
		oauth.WithDeviceScope(requestedScope),
	)
	if opts.LoginHint != "" {
		devOpts = append(devOpts, oauth.WithDeviceLoginHint(opts.LoginHint))
	}
	devOpts = append(devOpts, opts.deviceOptions...)

	// The SDK display hook can't return an error, and the SDK proceeds into
	// polling regardless. On malformed display data the callback records
	// displayErr and cancels this derived context to abort before polling.
	devCtx, cancelDev := context.WithCancel(ctx)
	defer cancelDev()

	var displayErr error
	display := func(devAuth oauth.DeviceAuthorization) {
		// Validate the raw server-supplied URIs before printing or launching
		// anything: browser target is the code-embedding URI when valid,
		// falling back to the plain verification URI.
		target := ""
		if devAuth.VerificationURIComplete != nil {
			target = validVerificationURL(*devAuth.VerificationURIComplete)
		}
		if target == "" {
			target = validVerificationURL(devAuth.VerificationURI)
		}

		// The command logger prints raw to the terminal, so strip
		// ANSI/OSC/control sequences from everything displayed. Trim after
		// sanitizing: a code that reduces to whitespace is as unusable as an
		// empty one — without the trim it would be displayed and polled until
		// expiry.
		userCode := strings.TrimSpace(richtext.SanitizeSingleLine(devAuth.UserCode))
		shownURI := strings.TrimSpace(richtext.SanitizeSingleLine(target))
		if target == "" || userCode == "" || shownURI == "" {
			displayErr = output.ErrAPI(0, "authorization server returned malformed device authorization")
			cancelDev()
			return
		}

		opts.log("\nTo authenticate, open this URL in a browser on any device:")
		opts.log("  " + shownURI)
		opts.log("")
		opts.log("and enter the code: " + userCode)
		if devAuth.ExpiresIn > 0 {
			opts.log(fmt.Sprintf("The code expires in %v.", time.Duration(devAuth.ExpiresIn)*time.Second))
		}
		// Flag matrix: default/--local launch the browser; --remote,
		// --device-code, and --no-browser (Remote implies NoBrowser) print
		// only. defaults() leaves BrowserLauncher nil in headless modes, but
		// honor NoBrowser too so an injected launcher can't override it.
		if !opts.NoBrowser && opts.BrowserLauncher != nil {
			if launchErr := opts.BrowserLauncher(target); launchErr != nil {
				opts.log("\nCouldn't open browser automatically — use the URL above.")
			} else {
				opts.log("\nOpening browser for authentication...")
			}
		}
		opts.log("\nWaiting for approval...")
	}

	token, err := oauth.PerformDeviceLogin(devCtx, oauthCfg, bc5ClientID, display, devOpts...)
	if displayErr != nil {
		// The malformed display data — not the cancellation it triggered —
		// is the real cause.
		return nil, displayErr
	}
	if err != nil {
		return nil, err
	}

	// The granted scope is whatever the server says it granted; fall back to
	// what was asked for only when the token response omits it.
	effectiveScope := token.Scope
	if effectiveScope == "" {
		effectiveScope = requestedScope
	}

	creds := &Credentials{
		AccessToken:   token.AccessToken,
		RefreshToken:  token.RefreshToken,
		OAuthType:     oauthTypeBC5,
		TokenEndpoint: oauthCfg.TokenEndpoint,
		Scope:         effectiveScope,
		// The RFC 8707 account binding: a trusted-client device login mints a
		// multi-account refresh token, and refreshing one without echoing this
		// is rejected (400 invalid_request) — losing it here would strand the
		// login at first token expiry.
		Resource: token.Resource,
	}
	if !token.ExpiresAt.IsZero() {
		creds.ExpiresAt = token.ExpiresAt.Unix()
	}

	if opts.Verify != nil {
		if err := opts.Verify(ctx, creds.AccessToken, oauthTypeBC5); err != nil {
			return nil, err
		}
	}
	if err := m.store.Save(credKey, creds); err != nil {
		return nil, err
	}

	return &LoginResult{OAuthType: oauthTypeBC5, Scope: effectiveScope}, nil
}

// validVerificationURL validates a server-supplied verification URI with the
// same policy as other OAuth browser URLs (https, or http on loopback, no
// userinfo). Returns the raw URL when valid, "" otherwise.
func validVerificationURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || !isSecureEndpointURL(u) {
		return ""
	}
	return raw
}

// ImportToken stores an externally issued BC5 access token (a personal
// access token) as the current credential, in one write, together with the
// identity it was verified to authenticate as and the expiry the server
// reported for it (zero when it reported none). The token has no refresh
// token and no token endpoint: a zero expiry is the non-expiring path
// AccessToken already takes, and a reported one makes AccessToken refuse
// the token near expiry with "No refresh token available" rather than
// letting requests start failing — either way the remedy is to import
// again. Scope is what the token was verified or declared to carry.
func (m *Manager) ImportToken(token, scope, userID, userEmail string, expiresAt time.Time) error {
	if scope != scopeRead && scope != scopeFull {
		return output.ErrUsage("Invalid scope. Use 'read' or 'full'")
	}
	creds := &Credentials{
		AccessToken: token,
		OAuthType:   oauthTypeBC5,
		Scope:       scope,
		UserID:      userID,
		UserEmail:   userEmail,
		Source:      CredentialSourceToken,
	}
	if !expiresAt.IsZero() {
		creds.ExpiresAt = expiresAt.Unix()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store.Save(m.credentialKey(), creds)
}

// Logout removes stored credentials.
func (m *Manager) Logout() error {
	credKey := m.credentialKey()
	return m.store.Delete(credKey)
}

// discovery is the outcome of provider selection: the OAuth config to use
// and which login flow it drives.
type discovery struct {
	config    *oauth.Config
	oauthType string
	issuer    string
}

// discoverOAuth performs resource-first OAuth discovery (RFC 9728 → RFC 8414)
// from the configured base URL's origin. Only the two soft pre-selection
// outcomes fall back to Launchpad; once a BC5 issuer is selected, every
// failure is returned loudly — never converted into a Launchpad attempt.
func (m *Manager) discoverOAuth(ctx context.Context, log func(string)) (*discovery, error) {
	if issuer := os.Getenv(oauthIssuerEnv); issuer != "" {
		return pinnedIssuerDiscovery(issuer, log)
	}

	origin, err := resourceOrigin(m.cfg.BaseURL)
	if err != nil {
		return nil, err
	}

	// Both discovery hops ride the BC5 lane client. Hop 2 (the advertised
	// issuer's metadata) would otherwise ride the SDK's internal default
	// client, whose policy blocks loopback and knows nothing of the CLI's
	// local configuration — a local resource's local advertised issuer would
	// be refused right after hop 1 succeeded. The lane client carries the
	// per-provenance policy (AllowLoopback iff cfg.BaseURL is local), so
	// enforcement is preserved in both modes, and the SDK still adds its own
	// redirect suppression around it.
	discoveryClient, err := m.bc5Client()
	if err != nil {
		return nil, err
	}
	discoverer := oauth.NewDiscoverer(discoveryClient, oauth.WithIssuerHTTPClient(discoveryClient))
	res, err := discoverer.DiscoverFromResource(ctx, origin)
	if err != nil {
		// Hard selection failure: propagate unchanged. output.AsError at the
		// root maps the wrapped *basecamp.Error taxonomy to exit codes.
		return nil, err
	}

	if res.IsFallback() {
		if res.FallbackReason == oauth.FallbackResourceDiscoveryFailed {
			log(fmt.Sprintf("warning: OAuth discovery failed for %s, using Launchpad fallback", origin))
		}
		lpURL, lpErr := m.launchpadURL()
		if lpErr != nil {
			return nil, lpErr
		}
		authz := lpURL + "/authorization/new"
		fallbackCfg := &oauth.Config{
			AuthorizationEndpoint: &authz,
			TokenEndpoint:         lpURL + "/authorization/token",
		}
		log(fmt.Sprintf("Authenticating via launchpad (%s)", authz))
		return &discovery{config: fallbackCfg, oauthType: oauthTypeLaunchpad}, nil
	}

	log(fmt.Sprintf("Authenticating via %s (device flow)", res.Issuer))
	return &discovery{config: res.Config, oauthType: oauthTypeBC5, issuer: res.Issuer}, nil
}

// oauthIssuerEnv pins the BC5 authorization server, bypassing discovery.
//
// It exists for the production dark pilot: a server whose OAuth surface is
// dark answers discovery with 404 while still serving piloted clients, so the
// only way to reach it is to name it. It is not a configuration surface and
// is removed once the server advertises itself.
const oauthIssuerEnv = "BASECAMP_OAUTH_ISSUER"

// pinnedIssuerDiscovery builds the BC5 device-flow config from a pinned
// issuer origin, deriving the endpoints Basecamp mounts under /oauth. The
// issuer is operator-supplied environment, so it passes the same endpoint
// checks a discovery document would, and the derived endpoints are checked
// again by loginDevice before any POST. A rejected value is never echoed
// (like a base URL, it can carry userinfo or a query string); the accepted
// one is announced, reduced to one line, so the operator sees what was
// pinned — as the discovered issuer is.
func pinnedIssuerDiscovery(issuer string, log func(string)) (*discovery, error) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	u, err := url.Parse(issuer)
	if err != nil || u.Opaque != "" || !isSecureEndpointURL(u) || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, output.ErrAuth("invalid " + oauthIssuerEnv + ": must be an origin — an absolute https URL (or http on loopback) with a hostname and no path, userinfo, query, or fragment")
	}
	deviceEndpoint := issuer + "/oauth/device_authorizations"
	cfg := &oauth.Config{
		Issuer:                      issuer,
		TokenEndpoint:               issuer + "/oauth/tokens",
		DeviceAuthorizationEndpoint: &deviceEndpoint,
		GrantTypesSupported:         []string{oauth.DeviceCodeGrantType, "refresh_token"},
	}
	log(fmt.Sprintf("Authenticating via %s (device flow, pinned by %s)", richtext.SanitizeSingleLine(issuer), oauthIssuerEnv))
	return &discovery{config: cfg, oauthType: oauthTypeBC5, issuer: issuer}, nil
}

// resourceOrigin reduces the configured base URL to a bare scheme://host[:port]
// origin for RFC 9728 protected-resource discovery, CANONICALIZED: lowercase
// scheme and host, explicit default ports stripped. The SDK binds the
// protected-resource metadata's resource identifier to this string code-point
// exact, so an equivalent spelling (HTTPS://3.BasecampAPI.com,
// https://3.basecampapi.com:443) would otherwise mismatch the advertised
// identifier and silently soft-fall back to Launchpad. Only the path is
// stripped and case/default-port normalized; every other deviation is rejected
// rather than laundered. Validation failures never echo the raw URL: userinfo,
// query strings, and fragments can carry secrets, and parse errors can
// reproduce their input.
func resourceOrigin(baseURL string) (string, error) {
	fail := func(rule string) (string, error) {
		return "", output.ErrUsage("invalid base URL: " + rule)
	}

	u, err := url.Parse(baseURL)
	if err != nil {
		return fail("not a parseable URL")
	}
	if u.Opaque != "" || !u.IsAbs() {
		return fail("must be an absolute URL")
	}
	if u.Hostname() == "" {
		return fail("must include a hostname")
	}
	// The localhost carve-out checks the LOWERCASED host: DNS names are
	// case-insensitive, so http://LocalHost is as local as http://localhost.
	if u.Scheme != "https" && (u.Scheme != "http" || !hostutil.IsLocalhost(strings.ToLower(u.Host))) {
		return fail("scheme must be https (or http on localhost)")
	}
	if u.User != nil {
		return fail("userinfo is not allowed")
	}
	// ForceQuery catches the bare-"?" form ("https://host?"), which parses
	// with an empty RawQuery but still carries a query component.
	if u.RawQuery != "" || u.ForceQuery {
		return fail("query string is not allowed")
	}
	if u.Fragment != "" {
		return fail("fragment is not allowed")
	}
	// url.Parse lowercases the scheme; the host keeps its input case and must
	// be lowered here (DNS names are case-insensitive, the code-point binding
	// is not). Hostname() strips IPv6 brackets — restore them so the origin
	// stays a parseable URL.
	hostname := strings.ToLower(u.Hostname())
	if strings.Contains(hostname, ":") {
		hostname = "[" + hostname + "]"
	}
	host := hostname
	if port := u.Port(); port != "" {
		// url.Parse requires a numeric port but does not range-check it.
		n, portErr := strconv.Atoi(port)
		if portErr != nil || n < 1 || n > 65535 {
			return fail("port must be between 1 and 65535")
		}
		// Strip an explicit default port (the canonical origin form) and
		// normalize leading zeros; any other port is kept numerically.
		if (u.Scheme != "https" || n != 443) && (u.Scheme != "http" || n != 80) {
			host += ":" + strconv.Itoa(n)
		}
	}

	return u.Scheme + "://" + host, nil
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

// launchpadClientCredentials resolves the Launchpad OAuth client: env var
// overrides first, then the built-in production credentials.
func launchpadClientCredentials(log func(string)) (*ClientCredentials, error) {
	creds, err := resolveClientCredentials(log)
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

// isSecureEndpointURL reports whether u uses a scheme safe for OAuth endpoints
// derived from the server-controlled discovery document: https, or http only on
// loopback for local development. The URL must also be absolute with a hostname —
// url.Parse accepts opaque forms like "https:foo" and port-only authorities like
// "https://:3000/" that carry the right scheme but no hostname, which would
// otherwise slip through to the transport or browser launcher. URLs carrying
// userinfo (user:pass@host) are rejected outright: they enable phishing
// displays in browsers and net/http synthesizes a Basic Authorization header
// from them. Centralizing the rule keeps the authorization, token, device, and
// verification-URI checks consistent.
func isSecureEndpointURL(u *url.URL) bool {
	if u.Hostname() == "" {
		return false
	}
	// Userinfo enables phishing display in browsers ("evil.example@real.host")
	// and Basic-auth synthesis in net/http requests.
	if u.User != nil {
		return false
	}
	// url.Parse requires a numeric port but does not range-check it, so
	// https://host:70000/ parses cleanly yet is undialable and unlaunchable.
	if port := u.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	// IsLocalhost takes the host:port form and strips the port itself. DNS
	// hostnames are case-insensitive: lowercase before matching so a
	// mixed-case loopback (http://LocalHost:3001) is not rejected as
	// insecure — the same normalization resourceOrigin applies.
	return u.Scheme == "https" || (u.Scheme == "http" && hostutil.IsLocalhost(strings.ToLower(u.Host)))
}

// requireSecureOAuthEndpoint parses and validates a server-controlled OAuth
// endpoint URL with isSecureEndpointURL, returning an auth-class error naming
// the endpoint when it fails.
func requireSecureOAuthEndpoint(name, endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return output.ErrAuth(fmt.Sprintf("invalid %s %q: %v", name, endpoint, err))
	}
	if !isSecureEndpointURL(u) {
		return output.ErrAuth(fmt.Sprintf("invalid %s %q: must be an absolute https URL (or http on loopback) with a hostname, no userinfo, and a valid port", name, endpoint))
	}
	return nil
}

func (m *Manager) buildAuthURL(authorizationEndpoint, state, clientID string, opts *LoginOptions) (string, error) {
	u, err := url.Parse(authorizationEndpoint)
	if err != nil {
		return "", output.ErrAuth(fmt.Sprintf("invalid authorization endpoint %q: %v", authorizationEndpoint, err))
	}

	// The authorization endpoint comes from the server-controlled discovery
	// document and is later dispatched to the OS browser handler (xdg-open /
	// open). Restrict it to https (or http on loopback for local development)
	// so a hostile discovery doc can't hand the OS a file:// (or other) URL.
	if !isSecureEndpointURL(u) {
		return "", output.ErrAuth(fmt.Sprintf("invalid authorization endpoint %q: must be an absolute https URL (or http on loopback)", authorizationEndpoint))
	}

	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", opts.RedirectURI)
	q.Set("state", state)
	q.Set("type", "web_server")

	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (m *Manager) exchangeCode(ctx context.Context, cfg *oauth.Config, code string, clientCreds *ClientCredentials, opts *LoginOptions) (*Credentials, error) {
	// The token endpoint comes from the server-controlled discovery document
	// and receives the authorization code plus client credentials. The SDK's
	// RequireSecureEndpoint only checks scheme==https, which lets userinfo
	// (https://legit@evil.com/token) and empty-host forms through. Apply the
	// same strict check used for the other OAuth endpoints.
	if err := requireSecureOAuthEndpoint("token endpoint", cfg.TokenEndpoint); err != nil {
		return nil, err
	}

	// The web-flow code exchange is Launchpad-provenance traffic (BC5 logins
	// go through the device flow), so it rides the Launchpad lane client.
	laneClient, laneErr := m.launchpadClient()
	if laneErr != nil {
		return nil, laneErr
	}
	exchanger := oauth.NewExchanger(laneClient)

	req := oauth.ExchangeRequest{
		TokenEndpoint:   cfg.TokenEndpoint,
		Code:            code,
		RedirectURI:     opts.RedirectURI,
		ClientID:        clientCreds.ClientID,
		ClientSecret:    clientCreds.ClientSecret,
		UseLegacyFormat: true,
	}

	token, err := exchanger.Exchange(ctx, req)
	if err != nil {
		return nil, wrapOAuthError("token exchange failed", err)
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

	return m.AuthorizationEndpointFor(m.GetOAuthType())
}

// AuthorizationEndpointFor returns the authorization info endpoint for a
// credential of the given OAuth type, whether or not it is stored yet.
func (m *Manager) AuthorizationEndpointFor(oauthType string) (string, error) {
	switch oauthType {
	case "bc3", oauthTypeBC5:
		// resourceOrigin, not NormalizeBaseURL: the latter only trims a
		// trailing slash, so a pathful BaseURL (https://host/api/v1 —
		// explicitly supported by resourceOrigin) would yield a misrouted
		// https://host/api/v1/authorization.json.
		origin, err := resourceOrigin(m.cfg.BaseURL)
		if err != nil {
			return "", err
		}
		return origin + "/authorization.json", nil
	case oauthTypeLaunchpad, "":
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

// GetOAuthType returns the OAuth type for the current credential key
// ("bc5", "launchpad", or legacy "bc3").
func (m *Manager) GetOAuthType() string {
	credKey := m.credentialKey()
	creds, err := m.store.Load(credKey)
	if err != nil {
		return ""
	}
	return creds.OAuthType
}

// accountResourceURNPrefix is the RFC 8707 resource indicator BC5 binds an
// account-scoped token to. The trailing segment is the account's public ID —
// the same one that appears in Basecamp URLs.
const accountResourceURNPrefix = "urn:bc:account:"

// AccountID returns the account a BC5 token is bound to, derived from its
// stored RFC 8707 resource indicator, or "" when the credentials carry no
// account binding (Launchpad tokens, or a resource naming the service
// origin rather than one account).
//
// The binding is authoritative: the token grants access to exactly this
// account, so there is nothing to discover over the network and no picker to
// show. It also works where account discovery cannot — /authorization.json
// is served only on the API host, which beta deployments don't route.
//
// BASECAMP_TOKEN wins — match AccessToken() precedence. Requests carry the
// environment token, which is bound to whatever the operator issued it for;
// answering with a stored token's account would silently address the wrong
// one. Fall through to discovery, which asks using the token in play.
func (m *Manager) AccountID() string {
	if os.Getenv("BASECAMP_TOKEN") != "" {
		return ""
	}

	creds, err := m.store.Load(m.credentialKey())
	if err != nil {
		return ""
	}

	id := strings.TrimPrefix(creds.Resource, accountResourceURNPrefix)
	if id == creds.Resource || id == "" {
		return ""
	}

	// Digits only: this feeds URL construction, and a resource indicator is
	// server-controlled data.
	for _, r := range id {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return id
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
//
// BASECAMP_TOKEN wins — match AccessToken() precedence. The email was
// fetched with the environment token, so it names that token's user, not
// whoever the stored credentials belong to; writing it there would
// mislabel them. Skipping the store also keeps a token session off the
// keyring probe and the fallback warning it can raise.
func (m *Manager) SetUserEmail(email string) error {
	if os.Getenv("BASECAMP_TOKEN") != "" {
		return nil
	}

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
