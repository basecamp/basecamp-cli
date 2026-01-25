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
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/basecamp/bcq/internal/config"
	"github.com/basecamp/bcq/internal/output"
)

// OAuthConfig holds discovered OAuth endpoints.
type OAuthConfig struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint,omitempty"`
	ScopesSupported       []string `json:"scopes_supported,omitempty"`
}

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

// AccessToken returns a valid access token, refreshing if needed.
// If BASECAMP_TOKEN env var is set, it's used directly without OAuth.
func (m *Manager) AccessToken(ctx context.Context) (string, error) {
	// Check for BASECAMP_TOKEN environment variable first
	if token := os.Getenv("BASECAMP_TOKEN"); token != "" {
		return token, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	origin := config.NormalizeBaseURL(m.cfg.BaseURL)
	creds, err := m.store.Load(origin)
	if err != nil {
		return "", output.ErrAuth("Not authenticated")
	}

	// Check if token is expired (with 5 minute buffer)
	if time.Now().Unix() >= creds.ExpiresAt-300 {
		if err := m.refreshLocked(ctx, origin, creds); err != nil {
			return "", err
		}
		// Reload refreshed credentials
		creds, err = m.store.Load(origin)
		if err != nil {
			return "", err
		}
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

	origin := config.NormalizeBaseURL(m.cfg.BaseURL)
	creds, err := m.store.Load(origin)
	if err != nil {
		return false
	}
	return creds.AccessToken != ""
}

// Refresh forces a token refresh.
func (m *Manager) Refresh(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	origin := config.NormalizeBaseURL(m.cfg.BaseURL)
	creds, err := m.store.Load(origin)
	if err != nil {
		return output.ErrAuth("Not authenticated")
	}

	return m.refreshLocked(ctx, origin, creds)
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

	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", creds.RefreshToken)

	// Add client credentials if Launchpad
	if creds.OAuthType == "launchpad" {
		// For Launchpad, use legacy format
		data.Set("type", "refresh")
	}

	req, err := http.NewRequestWithContext(ctx, "POST", tokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return output.ErrNetwork(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return output.ErrAPI(resp.StatusCode, fmt.Sprintf("token refresh failed: %s", string(body)))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return err
	}

	creds.AccessToken = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		creds.RefreshToken = tokenResp.RefreshToken
	}
	if tokenResp.ExpiresIn > 0 {
		creds.ExpiresAt = time.Now().Unix() + tokenResp.ExpiresIn
	}

	return m.store.Save(origin, creds)
}

// LoginOptions configures the login flow.
type LoginOptions struct {
	Scope     string
	NoBrowser bool // If true, don't auto-open browser, just print URL
}

// Login initiates the OAuth login flow.
func (m *Manager) Login(ctx context.Context, opts LoginOptions) error {
	origin := config.NormalizeBaseURL(m.cfg.BaseURL)

	// Discover OAuth config
	oauthCfg, oauthType, err := m.discoverOAuth(ctx)
	if err != nil {
		return err
	}

	// Load or register client credentials
	clientCreds, err := m.loadClientCredentials(ctx, oauthCfg, oauthType)
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
	authURL, err := m.buildAuthURL(oauthCfg, oauthType, opts.Scope, state, codeChallenge, clientCreds.ClientID)
	if err != nil {
		return err
	}

	// Start local callback server
	code, err := m.waitForCallback(ctx, state, authURL, opts.NoBrowser)
	if err != nil {
		return err
	}

	// Exchange code for tokens
	creds, err := m.exchangeCode(ctx, oauthCfg, oauthType, code, codeVerifier, clientCreds)
	if err != nil {
		return err
	}

	creds.OAuthType = oauthType
	creds.TokenEndpoint = oauthCfg.TokenEndpoint
	creds.Scope = opts.Scope

	return m.store.Save(origin, creds)
}

// Logout removes stored credentials.
func (m *Manager) Logout() error {
	origin := config.NormalizeBaseURL(m.cfg.BaseURL)
	return m.store.Delete(origin)
}

func (m *Manager) discoverOAuth(ctx context.Context) (*OAuthConfig, string, error) {
	// Try BC3 OAuth 2.1 discovery first
	wellKnownURL := m.cfg.BaseURL + "/.well-known/oauth-authorization-server"
	req, err := http.NewRequestWithContext(ctx, "GET", wellKnownURL, nil)
	if err != nil {
		return nil, "", err
	}

	resp, err := m.httpClient.Do(req)
	if err == nil && resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		var cfg OAuthConfig
		if err := json.NewDecoder(resp.Body).Decode(&cfg); err == nil {
			return &cfg, "bc3", nil
		}
	}

	// Fallback to Launchpad
	return &OAuthConfig{
		AuthorizationEndpoint: "https://launchpad.37signals.com/authorization/new",
		TokenEndpoint:         "https://launchpad.37signals.com/authorization/token",
	}, "launchpad", nil
}

func (m *Manager) loadClientCredentials(ctx context.Context, oauthCfg *OAuthConfig, oauthType string) (*ClientCredentials, error) {
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
		return m.registerBC3Client(ctx, oauthCfg.RegistrationEndpoint)
	}

	// Launchpad: Check environment variables, then use built-in defaults
	// Priority: env vars > built-in defaults
	clientID := os.Getenv("BCQ_CLIENT_ID")
	clientSecret := os.Getenv("BCQ_CLIENT_SECRET")

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

func (m *Manager) registerBC3Client(ctx context.Context, registrationEndpoint string) (*ClientCredentials, error) {
	regReq := map[string]interface{}{
		"client_name":                "bcq",
		"client_uri":                 "https://github.com/basecamp/bcq",
		"redirect_uris":              []string{"http://127.0.0.1:8976/callback"},
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
		respBody, _ := io.ReadAll(resp.Body)
		return nil, output.ErrAPI(resp.StatusCode, fmt.Sprintf("DCR failed: %s", string(respBody)))
	}

	var regResp struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&regResp); err != nil {
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

func (m *Manager) buildAuthURL(cfg *OAuthConfig, oauthType, scope, state, codeChallenge, clientID string) (string, error) {
	u, err := url.Parse(cfg.AuthorizationEndpoint)
	if err != nil {
		return "", err
	}

	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", "http://127.0.0.1:8976/callback")
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

func (m *Manager) waitForCallback(ctx context.Context, expectedState, authURL string, noBrowser bool) (string, error) {
	// Start listener
	lc := net.ListenConfig{}
	listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:8976")
	if err != nil {
		return "", fmt.Errorf("failed to start callback server: %w", err)
	}
	defer func() { _ = listener.Close() }()

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	server := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			state := r.URL.Query().Get("state")
			code := r.URL.Query().Get("code")
			errParam := r.URL.Query().Get("error")

			if errParam != "" {
				errCh <- fmt.Errorf("OAuth error: %s", errParam)
				fmt.Fprint(w, "<html><body><h1>Authentication failed</h1><p>You can close this window.</p></body></html>")
				return
			}

			if state != expectedState {
				errCh <- fmt.Errorf("state mismatch: CSRF protection failed")
				fmt.Fprint(w, "<html><body><h1>Authentication failed</h1><p>State mismatch.</p></body></html>")
				return
			}

			codeCh <- code
			fmt.Fprint(w, "<html><body><h1>Authentication successful!</h1><p>You can close this window.</p></body></html>")
		}),
	}

	go server.Serve(listener)

	// Try to open browser automatically unless --no-browser was specified
	if !noBrowser {
		if err := openBrowser(authURL); err != nil {
			// Fall back to printing URL if browser open fails
			fmt.Printf("\nCouldn't open browser automatically.\nOpen this URL in your browser:\n%s\n\nWaiting for authentication...\n", authURL)
		} else {
			fmt.Println("\nOpening browser for authentication...")
			fmt.Printf("If the browser doesn't open, visit: %s\n\nWaiting for authentication...\n", authURL)
		}
	} else {
		fmt.Printf("\nOpen this URL in your browser:\n%s\n\nWaiting for authentication...\n", authURL)
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

func (m *Manager) exchangeCode(ctx context.Context, cfg *OAuthConfig, oauthType, code, codeVerifier string, clientCreds *ClientCredentials) (*Credentials, error) {
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("code", code)
	data.Set("redirect_uri", "http://127.0.0.1:8976/callback")
	data.Set("client_id", clientCreds.ClientID)

	switch oauthType {
	case "bc3":
		if codeVerifier != "" {
			data.Set("code_verifier", codeVerifier)
		}
		// Only include client_secret for confidential clients
		if clientCreds.ClientSecret != "" {
			data.Set("client_secret", clientCreds.ClientSecret)
		}
	case "launchpad":
		data.Set("type", "web_server")
		data.Set("client_secret", clientCreds.ClientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", cfg.TokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, output.ErrNetwork(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, output.ErrAPI(resp.StatusCode, fmt.Sprintf("token exchange failed: %s", string(body)))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, err
	}

	return &Credentials{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    time.Now().Unix() + tokenResp.ExpiresIn,
	}, nil
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
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "linux":
		cmd = "xdg-open"
		args = []string{url}
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}

	return exec.Command(cmd, args...).Start() //nolint:gosec,noctx // G204: cmd is hardcoded per-platform; fire-and-forget
}

// GetUserID returns the stored user ID for the current origin.
func (m *Manager) GetUserID() string {
	origin := config.NormalizeBaseURL(m.cfg.BaseURL)
	creds, err := m.store.Load(origin)
	if err != nil {
		return ""
	}
	return creds.UserID
}

// SetUserID stores the user ID for the current origin.
func (m *Manager) SetUserID(userID string) error {
	origin := config.NormalizeBaseURL(m.cfg.BaseURL)
	creds, err := m.store.Load(origin)
	if err != nil {
		return err
	}
	creds.UserID = userID
	return m.store.Save(origin, creds)
}

// GetStore returns the credential store.
func (m *Manager) GetStore() *Store {
	return m.store
}
