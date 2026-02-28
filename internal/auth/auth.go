// Package auth provides OAuth 2.1 authentication for Basecamp.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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

	// Use stored token endpoint (survives discovery failures)
	tokenEndpoint := creds.TokenEndpoint
	if tokenEndpoint == "" {
		return output.ErrAuth("No token endpoint stored")
	}

	exchanger := oauth.NewExchanger(m.httpClient)

	req := oauth.RefreshRequest{
		TokenEndpoint:   tokenEndpoint,
		RefreshToken:    creds.RefreshToken,
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
	}

	return m.store.Save(origin, creds)
}

// LoginOptions configures the login flow.
type LoginOptions struct {
	Scope     string
	NoBrowser bool // If true, don't auto-open browser, just print URL

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
	if o.CallbackAddr == "" {
		o.CallbackAddr = "127.0.0.1:8976"
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

// Login initiates the OAuth login flow.
func (m *Manager) Login(ctx context.Context, opts LoginOptions) error {
	opts.defaults()
	credKey := m.credentialKey()

	// Discover OAuth config
	oauthCfg, oauthType, err := m.discoverOAuth(ctx, opts.log)
	if err != nil {
		return err
	}

	// Load or register client credentials
	clientCreds, err := m.loadClientCredentials(ctx, oauthCfg, oauthType, &opts)
	if err != nil {
		return err
	}

	// Generate PKCE challenge (for BC3)
	var codeVerifier, codeChallenge string
	if oauthType == "bc3" {
		codeVerifier = generateCodeVerifier()
		codeChallenge = generateCodeChallenge(codeVerifier)
	}

	// Generate state for CSRF protection
	state := generateState()

	// Build authorization URL
	authURL, err := m.buildAuthURL(oauthCfg, oauthType, opts.Scope, state, codeChallenge, clientCreds.ClientID, &opts)
	if err != nil {
		return err
	}

	// Start local callback server
	code, err := m.waitForCallback(ctx, state, authURL, &opts)
	if err != nil {
		return err
	}

	// Exchange code for tokens
	creds, err := m.exchangeCode(ctx, oauthCfg, oauthType, code, codeVerifier, clientCreds, &opts)
	if err != nil {
		return err
	}

	creds.OAuthType = oauthType
	creds.TokenEndpoint = oauthCfg.TokenEndpoint
	creds.Scope = opts.Scope

	return m.store.Save(credKey, creds)
}

// Logout removes stored credentials.
func (m *Manager) Logout() error {
	credKey := m.credentialKey()
	return m.store.Delete(credKey)
}

func (m *Manager) discoverOAuth(ctx context.Context, log func(string)) (*oauth.Config, string, error) {
	discoverer := oauth.NewDiscoverer(m.httpClient)
	cfg, err := discoverer.Discover(ctx, m.cfg.BaseURL)
	if err != nil {
		log(fmt.Sprintf("warning: OAuth discovery failed for %s, using Launchpad fallback", m.cfg.BaseURL))
		// Fallback to Launchpad
		lpURL, lpErr := m.launchpadURL()
		if lpErr != nil {
			return nil, "", lpErr
		}
		fallbackCfg := &oauth.Config{
			AuthorizationEndpoint: lpURL + "/authorization/new",
			TokenEndpoint:         lpURL + "/authorization/token",
		}
		log(fmt.Sprintf("Authenticating via launchpad (%s)", fallbackCfg.AuthorizationEndpoint))
		return fallbackCfg, "launchpad", nil
	}
	log(fmt.Sprintf("Authenticating via bc3 (%s)", cfg.AuthorizationEndpoint))
	return cfg, "bc3", nil
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
		// BC3: Try to load from stored file, otherwise register via DCR
		creds, err := m.loadBC3Client()
		if err == nil {
			return creds, nil
		}

		// Register new client via DCR
		if oauthCfg.RegistrationEndpoint == "" {
			return nil, output.ErrAuth("OAuth server does not support Dynamic Client Registration")
		}
		return m.registerBC3Client(ctx, oauthCfg.RegistrationEndpoint, opts)
	}

	// Launchpad: Check environment variables, then use built-in defaults
	// Priority: env vars > built-in defaults
	clientID := os.Getenv("BASECAMP_CLIENT_ID")
	clientSecret := os.Getenv("BASECAMP_CLIENT_SECRET")

	if clientID != "" && clientSecret != "" {
		return &ClientCredentials{
			ClientID:     clientID,
			ClientSecret: clientSecret,
		}, nil
	}

	// Use built-in defaults for production Launchpad
	return &ClientCredentials{
		ClientID:     launchpadClientID,
		ClientSecret: launchpadClientSecret,
	}, nil
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

func (m *Manager) registerBC3Client(ctx context.Context, registrationEndpoint string, opts *LoginOptions) (*ClientCredentials, error) {
	redirectURI := "http://" + opts.CallbackAddr + "/callback"
	regReq := map[string]any{
		"client_name":                "basecamp-cli",
		"client_uri":                 "https://github.com/basecamp/basecamp-cli",
		"redirect_uris":              []string{redirectURI},
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

	resp, err := m.httpClient.Do(req)
	if err != nil {
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

	// Save client credentials
	creds := &ClientCredentials{
		ClientID:     regResp.ClientID,
		ClientSecret: regResp.ClientSecret,
	}

	if err := m.saveBC3Client(creds); err != nil {
		return nil, err
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
	u, err := url.Parse(cfg.AuthorizationEndpoint)
	if err != nil {
		return "", err
	}

	redirectURI := "http://" + opts.CallbackAddr + "/callback"

	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
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

func (m *Manager) waitForCallback(ctx context.Context, expectedState, authURL string, opts *LoginOptions) (string, error) {
	// Start listener
	lc := net.ListenConfig{}
	listener, err := lc.Listen(ctx, "tcp", opts.CallbackAddr)
	if err != nil {
		return "", fmt.Errorf("failed to start callback server: %w", err)
	}
	defer func() { _ = listener.Close() }()

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	var shutdownOnce sync.Once

	server := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := r.URL.Query().Get("state")
		code := r.URL.Query().Get("code")
		errParam := r.URL.Query().Get("error")

		if errParam != "" {
			errCh <- fmt.Errorf("OAuth error: %s", errParam)
			fmt.Fprint(w, "<html><body><h1>Authentication failed</h1><p>You can close this window.</p></body></html>")
			shutdownOnce.Do(func() { go server.Shutdown(context.Background()) }) //nolint:errcheck,contextcheck // best-effort shutdown; Background intentional (outer ctx may be canceled)
			return
		}

		if state != expectedState {
			errCh <- fmt.Errorf("state mismatch: CSRF protection failed")
			fmt.Fprint(w, "<html><body><h1>Authentication failed</h1><p>State mismatch.</p></body></html>")
			shutdownOnce.Do(func() { go server.Shutdown(context.Background()) }) //nolint:errcheck,contextcheck // best-effort shutdown; Background intentional
			return
		}

		codeCh <- code
		fmt.Fprint(w, "<html><body><h1>Authentication successful!</h1><p>You can close this window.</p></body></html>")
		shutdownOnce.Do(func() { go server.Shutdown(context.Background()) }) //nolint:errcheck,contextcheck // best-effort shutdown; Background intentional
	})

	go server.Serve(listener) //nolint:errcheck // server.Serve returns ErrServerClosed on Shutdown

	// Try to open browser automatically unless --no-browser was specified
	if opts.BrowserLauncher != nil {
		if err := opts.BrowserLauncher(authURL); err != nil {
			// Fall back to printing URL if browser open fails
			opts.log("\nCouldn't open browser automatically.\nOpen this URL in your browser:\n" + authURL + "\n\nWaiting for authentication...")
		} else {
			opts.log("\nOpening browser for authentication...")
			opts.log("If the browser doesn't open, visit: " + authURL + "\n\nWaiting for authentication...")
		}
	} else {
		opts.log("\nOpen this URL in your browser:\n" + authURL + "\n\nWaiting for authentication...")
	}

	// Wait for callback or timeout
	select {
	case code := <-codeCh:
		return code, nil
	case err := <-errCh:
		return "", err
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(5 * time.Minute):
		return "", fmt.Errorf("authentication timeout")
	}
}

func (m *Manager) exchangeCode(ctx context.Context, cfg *oauth.Config, oauthType, code, codeVerifier string, clientCreds *ClientCredentials, opts *LoginOptions) (*Credentials, error) {
	exchanger := oauth.NewExchanger(m.httpClient)

	req := oauth.ExchangeRequest{
		TokenEndpoint:   cfg.TokenEndpoint,
		Code:            code,
		RedirectURI:     "http://" + opts.CallbackAddr + "/callback",
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

// PKCE helpers

func generateCodeVerifier() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func generateCodeChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func generateState() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// openBrowser opens the specified URL in the default browser.
func openBrowser(url string) error {
	return hostutil.OpenBrowser(url)
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

// GetUserID returns the stored user ID for the current credential key.
func (m *Manager) GetUserID() string {
	credKey := m.credentialKey()
	creds, err := m.store.Load(credKey)
	if err != nil {
		return ""
	}
	return creds.UserID
}

// SetUserID stores the user ID for the current credential key.
func (m *Manager) SetUserID(userID string) error {
	credKey := m.credentialKey()
	creds, err := m.store.Load(credKey)
	if err != nil {
		return err
	}
	creds.UserID = userID
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
