package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/oauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/config"
)

// syncLogger is a thread-safe log collector for remote-mode login tests.
// It captures all log messages under a mutex and signals authReady when it
// sees the auth URL (a line starting with "http" containing "/authorize").
type syncLogger struct {
	mu        sync.Mutex
	logs      []string
	authReady chan string // receives the auth URL once seen
	signaled  bool
}

func newSyncLogger() *syncLogger {
	return &syncLogger{authReady: make(chan string, 1)}
}

func (sl *syncLogger) log(msg string) {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	sl.logs = append(sl.logs, msg)

	if !sl.signaled {
		trimmed := strings.TrimSpace(msg)
		if strings.HasPrefix(trimmed, "http") && strings.Contains(trimmed, "/authorize") {
			sl.signaled = true
			sl.authReady <- trimmed
		}
	}
}

func (sl *syncLogger) snapshot() []string {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	cp := make([]string, len(sl.logs))
	copy(cp, sl.logs)
	return cp
}

// newTestStore creates a file-backed credential store for testing.
func newTestStore(t *testing.T, dir string) *Store {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	return NewStore(dir)
}

func TestNewStore(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Store should be created (may or may not use keyring depending on system)
	require.NotNil(t, store, "NewStore returned nil")
}

func TestStoreFileBackend(t *testing.T) {
	tmpDir := t.TempDir()
	store := newTestStore(t, tmpDir)

	origin := "https://test.example.com"
	creds := &Credentials{
		AccessToken:  "test-access-token",
		RefreshToken: "test-refresh-token",
		ExpiresAt:    time.Now().Unix() + 3600,
		Scope:        "read",
		OAuthType:    "launchpad",
		UserID:       "12345",
	}

	// Save credentials
	err := store.Save(origin, creds)
	require.NoError(t, err, "Save failed")

	// Load credentials
	loaded, err := store.Load(origin)
	require.NoError(t, err, "Load failed")

	// Verify values match
	assert.Equal(t, creds.AccessToken, loaded.AccessToken)
	assert.Equal(t, creds.RefreshToken, loaded.RefreshToken)
	assert.Equal(t, creds.ExpiresAt, loaded.ExpiresAt)
	assert.Equal(t, creds.Scope, loaded.Scope)
	assert.Equal(t, creds.OAuthType, loaded.OAuthType)
	assert.Equal(t, creds.UserID, loaded.UserID)
}

func TestStoreMultipleOrigins(t *testing.T) {
	tmpDir := t.TempDir()
	store := newTestStore(t, tmpDir)

	// Save credentials for two different origins
	origin1 := "https://origin1.example.com"
	origin2 := "https://origin2.example.com"

	creds1 := &Credentials{AccessToken: "token1", ExpiresAt: time.Now().Unix() + 3600}
	creds2 := &Credentials{AccessToken: "token2", ExpiresAt: time.Now().Unix() + 3600}

	require.NoError(t, store.Save(origin1, creds1), "Save origin1 failed")
	require.NoError(t, store.Save(origin2, creds2), "Save origin2 failed")

	// Load and verify each origin
	loaded1, err := store.Load(origin1)
	require.NoError(t, err, "Load origin1 failed")
	assert.Equal(t, "token1", loaded1.AccessToken)

	loaded2, err := store.Load(origin2)
	require.NoError(t, err, "Load origin2 failed")
	assert.Equal(t, "token2", loaded2.AccessToken)
}

func TestStoreDelete(t *testing.T) {
	tmpDir := t.TempDir()
	store := newTestStore(t, tmpDir)

	origin := "https://delete-test.example.com"
	creds := &Credentials{AccessToken: "to-be-deleted", ExpiresAt: time.Now().Unix() + 3600}

	// Save then delete
	require.NoError(t, store.Save(origin, creds), "Save failed")
	require.NoError(t, store.Delete(origin), "Delete failed")

	// Load should fail
	_, err := store.Load(origin)
	assert.Error(t, err, "Load should fail after delete")
}

func TestStoreLoadMissing(t *testing.T) {
	tmpDir := t.TempDir()
	store := newTestStore(t, tmpDir)

	// Load non-existent origin should fail
	_, err := store.Load("https://nonexistent.example.com")
	assert.Error(t, err, "Load should fail for non-existent origin")
}

func TestNewManager(t *testing.T) {
	cfg := &config.Config{
		BaseURL: "https://3.basecampapi.com",
	}

	manager := NewManager(cfg, http.DefaultClient)

	require.NotNil(t, manager, "NewManager returned nil")
	assert.Equal(t, cfg, manager.cfg, "Manager config not set correctly")
	assert.Equal(t, http.DefaultClient, manager.httpClient, "Manager httpClient not set correctly")
	assert.NotNil(t, manager.store, "Manager store not initialized")
}

func TestIsAuthenticatedWithEnvToken(t *testing.T) {
	// Save and restore env var
	original := os.Getenv("BASECAMP_TOKEN")
	defer func() {
		if original == "" {
			os.Unsetenv("BASECAMP_TOKEN")
		} else {
			os.Setenv("BASECAMP_TOKEN", original)
		}
	}()

	tmpDir := t.TempDir()

	cfg := &config.Config{
		BaseURL: "https://3.basecampapi.com",
	}
	manager := NewManager(cfg, http.DefaultClient)
	// Use file backend with empty temp dir to ensure no stored creds
	manager.store = newTestStore(t, tmpDir)

	// Without env token
	os.Unsetenv("BASECAMP_TOKEN")
	assert.False(t, manager.IsAuthenticated(), "Should not be authenticated without token")

	// With env token
	os.Setenv("BASECAMP_TOKEN", "test-env-token")
	assert.True(t, manager.IsAuthenticated(), "Should be authenticated with BASECAMP_TOKEN env var")
}

func TestIsAuthenticatedWithStoredCreds(t *testing.T) {
	// Ensure no env token
	original := os.Getenv("BASECAMP_TOKEN")
	defer func() {
		if original == "" {
			os.Unsetenv("BASECAMP_TOKEN")
		} else {
			os.Setenv("BASECAMP_TOKEN", original)
		}
	}()
	os.Unsetenv("BASECAMP_TOKEN")

	tmpDir := t.TempDir()

	// Override config dir for test
	cfg := &config.Config{
		BaseURL: "https://3.basecampapi.com",
	}
	manager := NewManager(cfg, http.DefaultClient)
	manager.store = newTestStore(t, tmpDir)

	// Without stored creds
	assert.False(t, manager.IsAuthenticated(), "Should not be authenticated without stored credentials")

	// Save credentials
	creds := &Credentials{
		AccessToken:  "test-token",
		RefreshToken: "test-refresh",
		ExpiresAt:    time.Now().Unix() + 3600,
	}
	manager.store.Save("https://3.basecampapi.com", creds)

	// With stored creds
	assert.True(t, manager.IsAuthenticated(), "Should be authenticated with stored credentials")
}

func TestSetUserEmail(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		BaseURL: "https://3.basecampapi.com",
	}
	manager := NewManager(cfg, http.DefaultClient)
	manager.store = newTestStore(t, tmpDir)

	// Save initial credentials with a user ID
	creds := &Credentials{
		AccessToken: "test-token",
		ExpiresAt:   time.Now().Unix() + 3600,
		UserID:      "original-id",
	}
	require.NoError(t, manager.store.Save("https://3.basecampapi.com", creds))

	// Set email only
	err := manager.SetUserEmail("test@example.com")
	require.NoError(t, err)

	// Verify email was saved and UserID was not modified
	loaded, err := manager.store.Load("https://3.basecampapi.com")
	require.NoError(t, err)
	assert.Equal(t, "test@example.com", loaded.UserEmail)
	assert.Equal(t, "original-id", loaded.UserID)
}

func TestSetUserIdentity(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		BaseURL: "https://3.basecampapi.com",
	}
	manager := NewManager(cfg, http.DefaultClient)
	manager.store = newTestStore(t, tmpDir)

	// Save initial credentials
	creds := &Credentials{
		AccessToken: "test-token",
		ExpiresAt:   time.Now().Unix() + 3600,
	}
	require.NoError(t, manager.store.Save("https://3.basecampapi.com", creds))

	// Set user identity
	err := manager.SetUserIdentity("67890", "test@example.com")
	require.NoError(t, err)

	// Verify both were saved
	loaded, err := manager.store.Load("https://3.basecampapi.com")
	require.NoError(t, err)
	assert.Equal(t, "67890", loaded.UserID)
	assert.Equal(t, "test@example.com", loaded.UserEmail)
}

func TestLogout(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		BaseURL: "https://3.basecampapi.com",
	}
	manager := NewManager(cfg, http.DefaultClient)
	manager.store = newTestStore(t, tmpDir)

	// Save credentials
	creds := &Credentials{
		AccessToken: "test-token",
		ExpiresAt:   time.Now().Unix() + 3600,
	}
	manager.store.Save("https://3.basecampapi.com", creds)

	// Logout
	err := manager.Logout()
	require.NoError(t, err, "Logout failed")

	// Should no longer be authenticated
	assert.False(t, manager.IsAuthenticated(), "Should not be authenticated after logout")
}

func TestCredentialsJSON(t *testing.T) {
	creds := &Credentials{
		AccessToken:   "access",
		RefreshToken:  "refresh",
		ExpiresAt:     1234567890,
		Scope:         "read",
		OAuthType:     "launchpad",
		TokenEndpoint: "https://example.com/token",
		UserID:        "12345",
	}

	data, err := json.Marshal(creds)
	require.NoError(t, err, "Marshal failed")

	var loaded Credentials
	require.NoError(t, json.Unmarshal(data, &loaded), "Unmarshal failed")

	assert.Equal(t, creds.AccessToken, loaded.AccessToken, "AccessToken mismatch")
	assert.Equal(t, creds.RefreshToken, loaded.RefreshToken, "RefreshToken mismatch")
	assert.Equal(t, creds.ExpiresAt, loaded.ExpiresAt, "ExpiresAt mismatch")
	assert.Equal(t, creds.Scope, loaded.Scope, "Scope mismatch")
	assert.Equal(t, creds.OAuthType, loaded.OAuthType, "OAuthType mismatch")
	assert.Equal(t, creds.TokenEndpoint, loaded.TokenEndpoint, "TokenEndpoint mismatch")
	assert.Equal(t, creds.UserID, loaded.UserID, "UserID mismatch")
}

func TestOAuthConfigJSON(t *testing.T) {
	cfg := &oauth.Config{
		Issuer:                "https://issuer.example.com",
		AuthorizationEndpoint: "https://auth.example.com/authorize",
		TokenEndpoint:         "https://auth.example.com/token",
		RegistrationEndpoint:  "https://auth.example.com/register",
		ScopesSupported:       []string{"read", "write"},
	}

	data, err := json.Marshal(cfg)
	require.NoError(t, err, "Marshal failed")

	var loaded oauth.Config
	require.NoError(t, json.Unmarshal(data, &loaded), "Unmarshal failed")

	assert.Equal(t, cfg.Issuer, loaded.Issuer, "Issuer mismatch")
	assert.Equal(t, cfg.AuthorizationEndpoint, loaded.AuthorizationEndpoint, "AuthorizationEndpoint mismatch")
	assert.Equal(t, cfg.TokenEndpoint, loaded.TokenEndpoint, "TokenEndpoint mismatch")
	assert.Equal(t, cfg.RegistrationEndpoint, loaded.RegistrationEndpoint, "RegistrationEndpoint mismatch")
	assert.Len(t, loaded.ScopesSupported, 2, "ScopesSupported length mismatch")
}

func TestClientCredentialsJSON(t *testing.T) {
	creds := &ClientCredentials{
		ClientID:     "client-id-123",
		ClientSecret: "client-secret-456",
	}

	data, err := json.Marshal(creds)
	require.NoError(t, err, "Marshal failed")

	var loaded ClientCredentials
	require.NoError(t, json.Unmarshal(data, &loaded), "Unmarshal failed")

	assert.Equal(t, creds.ClientID, loaded.ClientID)
	assert.Equal(t, creds.ClientSecret, loaded.ClientSecret)
}

func TestUsingKeyring(t *testing.T) {
	tmpDir := t.TempDir()

	// With keyring disabled, UsingKeyring returns false
	store := newTestStore(t, tmpDir)
	assert.False(t, store.UsingKeyring(), "UsingKeyring() should be false when BASECAMP_NO_KEYRING is set")
}

func TestLaunchpadURL_InsecureRejected(t *testing.T) {
	m := &Manager{cfg: config.Default()}

	t.Setenv("BASECAMP_LAUNCHPAD_URL", "http://evil.example.com")
	_, err := m.launchpadURL()
	require.Error(t, err, "insecure non-localhost launchpad URL must be rejected")
	assert.Contains(t, err.Error(), "BASECAMP_LAUNCHPAD_URL")
}

func TestLaunchpadURL_LocalhostHTTPAllowed(t *testing.T) {
	m := &Manager{cfg: config.Default()}

	t.Setenv("BASECAMP_LAUNCHPAD_URL", "http://localhost:3000")
	url, err := m.launchpadURL()
	require.NoError(t, err, "localhost http should be allowed")
	assert.Equal(t, "http://localhost:3000", url)
}

func TestLaunchpadURL_DefaultWhenUnset(t *testing.T) {
	m := &Manager{cfg: config.Default()}

	t.Setenv("BASECAMP_LAUNCHPAD_URL", "")
	url, err := m.launchpadURL()
	require.NoError(t, err)
	assert.Equal(t, "https://launchpad.37signals.com", url)
}

func TestDiscoverOAuth_PropagatesInsecureLaunchpadError(t *testing.T) {
	// Server that fails OAuth discovery (404 on .well-known), forcing Launchpad fallback.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.BaseURL = srv.URL // discovery will fail (404)

	m := &Manager{cfg: cfg, httpClient: srv.Client()}

	// Set insecure Launchpad URL — should cause hard error, not silent fallback.
	t.Setenv("BASECAMP_LAUNCHPAD_URL", "http://evil.example.com")

	noop := func(string) {}
	_, _, err := m.discoverOAuth(context.Background(), noop)
	require.Error(t, err, "insecure launchpad URL error must propagate through discoverOAuth")
	assert.Contains(t, err.Error(), "BASECAMP_LAUNCHPAD_URL")
}

func TestResolveOAuthCallback(t *testing.T) {
	tests := []struct {
		name       string
		opts       LoginOptions
		envURI     string
		wantURI    string
		wantAddr   string
		wantErrMsg string
	}{
		{
			name:     "default",
			opts:     LoginOptions{},
			wantURI:  "http://127.0.0.1:8976/callback",
			wantAddr: "127.0.0.1:8976",
		},
		{
			name:     "env var override",
			opts:     LoginOptions{},
			envURI:   "http://localhost:9999/callback",
			wantURI:  "http://localhost:9999/callback",
			wantAddr: "localhost:9999",
		},
		{
			name:     "LoginOptions.RedirectURI overrides env",
			opts:     LoginOptions{RedirectURI: "http://127.0.0.1:4000/callback"},
			envURI:   "http://localhost:9999/callback",
			wantURI:  "http://127.0.0.1:4000/callback",
			wantAddr: "127.0.0.1:4000",
		},
		{
			name:     "CallbackAddr without RedirectURI",
			opts:     LoginOptions{CallbackAddr: "127.0.0.1:5555"},
			wantURI:  "http://127.0.0.1:5555/callback",
			wantAddr: "127.0.0.1:5555",
		},
		{
			name:       "non-loopback host rejected",
			opts:       LoginOptions{RedirectURI: "http://evil.example.com:8976/callback"},
			wantErrMsg: "host must be loopback",
		},
		{
			name:       "https scheme rejected",
			opts:       LoginOptions{RedirectURI: "https://127.0.0.1:8976/callback"},
			wantErrMsg: "scheme must be http",
		},
		{
			name:       "missing port rejected",
			opts:       LoginOptions{RedirectURI: "http://localhost/callback"},
			wantErrMsg: "port is required",
		},
		{
			name:       "userinfo rejected",
			opts:       LoginOptions{RedirectURI: "http://user:pass@127.0.0.1:8976/callback"},
			wantErrMsg: "userinfo not allowed",
		},
		{
			name:       "fragment rejected",
			opts:       LoginOptions{RedirectURI: "http://127.0.0.1:8976/callback#frag"},
			wantErrMsg: "fragment not allowed",
		},
		{
			name:       "query string rejected",
			opts:       LoginOptions{RedirectURI: "http://127.0.0.1:8976/callback?foo=bar"},
			wantErrMsg: "query string not allowed",
		},
		{
			name:     "localhost subdomain accepted",
			opts:     LoginOptions{RedirectURI: "http://app.localhost:3000/callback"},
			wantURI:  "http://app.localhost:3000/callback",
			wantAddr: "app.localhost:3000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BASECAMP_OAUTH_REDIRECT_URI", tt.envURI)

			uri, addr, err := resolveOAuthCallback(&tt.opts)
			if tt.wantErrMsg != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrMsg)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantURI, uri)
			assert.Equal(t, tt.wantAddr, addr)
		})
	}
}

func TestResolveClientCredentials(t *testing.T) {
	noop := func(string) {}

	tests := []struct {
		name       string
		envVars    map[string]string
		wantID     string
		wantSecret string
		wantNil    bool
		wantErrMsg string
	}{
		{
			name:    "no env vars returns nil",
			wantNil: true,
		},
		{
			name:       "both env vars",
			envVars:    map[string]string{"BASECAMP_OAUTH_CLIENT_ID": "my-id", "BASECAMP_OAUTH_CLIENT_SECRET": "my-secret"},
			wantID:     "my-id",
			wantSecret: "my-secret",
		},
		{
			name:       "ID only, no secret",
			envVars:    map[string]string{"BASECAMP_OAUTH_CLIENT_ID": "my-id"},
			wantErrMsg: "BASECAMP_OAUTH_CLIENT_SECRET is required",
		},
		{
			name:       "secret only, no ID",
			envVars:    map[string]string{"BASECAMP_OAUTH_CLIENT_SECRET": "my-secret"},
			wantErrMsg: "BASECAMP_OAUTH_CLIENT_ID is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BASECAMP_OAUTH_CLIENT_ID", "")
			t.Setenv("BASECAMP_OAUTH_CLIENT_SECRET", "")
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			creds, err := resolveClientCredentials(noop)
			if tt.wantErrMsg != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrMsg)
				return
			}
			require.NoError(t, err)
			if tt.wantNil {
				assert.Nil(t, creds)
				return
			}
			require.NotNil(t, creds)
			assert.Equal(t, tt.wantID, creds.ClientID)
			assert.Equal(t, tt.wantSecret, creds.ClientSecret)
		})
	}
}

func TestBuildAuthURL_UsesResolvedRedirectURI(t *testing.T) {
	m := &Manager{cfg: config.Default(), httpClient: http.DefaultClient}
	oauthCfg := &oauth.Config{
		AuthorizationEndpoint: "https://auth.example.com/authorize",
	}
	opts := &LoginOptions{RedirectURI: "http://localhost:9999/my-callback"}

	authURL, err := m.buildAuthURL(oauthCfg, "launchpad", "", "state123", "", "client-id", opts)
	require.NoError(t, err)
	assert.Contains(t, authURL, "redirect_uri=http%3A%2F%2Flocalhost%3A9999%2Fmy-callback")
}

func TestExchangeCode_UsesResolvedRedirectURI(t *testing.T) {
	// Capture the request body sent to the token endpoint
	var receivedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"tok","token_type":"bearer"}`)
	}))
	defer srv.Close()

	m := &Manager{cfg: config.Default(), httpClient: srv.Client()}
	oauthCfg := &oauth.Config{TokenEndpoint: srv.URL + "/token"}
	clientCreds := &ClientCredentials{ClientID: "cid", ClientSecret: "csecret"}
	opts := &LoginOptions{RedirectURI: "http://localhost:7777/cb"}

	_, err := m.exchangeCode(context.Background(), oauthCfg, "launchpad", "code123", "", clientCreds, opts)
	require.NoError(t, err)
	// Body is URL-encoded form data
	assert.Contains(t, receivedBody, "redirect_uri=http%3A%2F%2Flocalhost%3A7777%2Fcb")
}

func TestRegisterBC3Client_UsesResolvedRedirectURI(t *testing.T) {
	var receivedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"client_id":"dcr-id","client_secret":"dcr-secret"}`)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	m := &Manager{
		cfg:        config.Default(),
		httpClient: srv.Client(),
		store:      newTestStore(t, tmpDir),
	}
	opts := &LoginOptions{RedirectURI: "http://localhost:7777/cb"}

	creds, err := m.registerBC3Client(context.Background(), srv.URL+"/register", opts)
	require.NoError(t, err)
	assert.Equal(t, "dcr-id", creds.ClientID)

	// Verify the redirect URI was sent in the DCR request
	redirectURIs, ok := receivedBody["redirect_uris"].([]any)
	require.True(t, ok)
	assert.Equal(t, "http://localhost:7777/cb", redirectURIs[0])
}

func TestRegisterBC3Client_CustomRedirectNotPersisted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"client_id":"dcr-id","client_secret":"dcr-secret"}`)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	// Override XDG_CONFIG_HOME so saveBC3Client would write to tmpDir (but shouldn't)
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	m := &Manager{
		cfg:        config.Default(),
		httpClient: srv.Client(),
		store:      newTestStore(t, tmpDir),
	}
	opts := &LoginOptions{RedirectURI: "http://localhost:7777/cb"}

	// Custom redirect: should NOT persist
	_, err := m.registerBC3Client(context.Background(), srv.URL+"/register", opts)
	require.NoError(t, err)

	clientFile := filepath.Join(tmpDir, "basecamp", "client.json")
	_, statErr := os.Stat(clientFile)
	assert.True(t, os.IsNotExist(statErr), "client.json should not be written for custom redirect URI")
}

func TestRegisterBC3Client_DefaultRedirectPersisted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"client_id":"dcr-id","client_secret":"dcr-secret"}`)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	// Override XDG_CONFIG_HOME so saveBC3Client writes to tmpDir
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	m := &Manager{
		cfg:        config.Default(),
		httpClient: srv.Client(),
		store:      newTestStore(t, tmpDir),
	}
	opts := &LoginOptions{RedirectURI: defaultRedirectURI}

	// Default redirect: should persist
	_, err := m.registerBC3Client(context.Background(), srv.URL+"/register", opts)
	require.NoError(t, err)

	clientFile := filepath.Join(tmpDir, "basecamp", "client.json")
	_, statErr := os.Stat(clientFile)
	assert.NoError(t, statErr, "client.json should be written for default redirect URI")
}

func TestLoadClientCredentials_BC3_CustomRedirect_SkipsStoredClient(t *testing.T) {
	// DCR server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"client_id":"dcr-fresh","client_secret":"dcr-secret"}`)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	m := &Manager{
		cfg:        config.Default(),
		httpClient: srv.Client(),
		store:      newTestStore(t, tmpDir),
	}

	// Pre-populate client.json
	storedCreds := &ClientCredentials{ClientID: "stored-id", ClientSecret: "stored-secret"}
	require.NoError(t, m.saveBC3Client(storedCreds))

	oauthCfg := &oauth.Config{RegistrationEndpoint: srv.URL + "/register"}

	// Custom redirect: should skip stored client and do DCR
	opts := &LoginOptions{RedirectURI: "http://localhost:7777/cb"}
	creds, err := m.loadClientCredentials(context.Background(), oauthCfg, "bc3", opts)
	require.NoError(t, err)
	assert.Equal(t, "dcr-fresh", creds.ClientID, "should use DCR result, not stored client")
}

func TestParseCallbackURL(t *testing.T) {
	const state = "test-state-123"

	tests := []struct {
		name    string
		input   string
		wantErr string
		want    string
	}{
		{
			name:  "valid URL",
			input: "http://127.0.0.1:8976/callback?code=abc123&state=test-state-123",
			want:  "abc123",
		},
		{
			name:  "quoted URL",
			input: `"http://127.0.0.1:8976/callback?code=abc123&state=test-state-123"`,
			want:  "abc123",
		},
		{
			name:  "single-quoted URL",
			input: "'http://127.0.0.1:8976/callback?code=abc123&state=test-state-123'",
			want:  "abc123",
		},
		{
			name:  "backticked URL",
			input: "`http://127.0.0.1:8976/callback?code=abc123&state=test-state-123`",
			want:  "abc123",
		},
		{
			name:  "URL with whitespace",
			input: "  http://127.0.0.1:8976/callback?code=abc123&state=test-state-123  \n",
			want:  "abc123",
		},
		{
			name:    "missing code",
			input:   "http://127.0.0.1:8976/callback?state=test-state-123",
			wantErr: "no authorization code",
		},
		{
			name:    "state mismatch",
			input:   "http://127.0.0.1:8976/callback?code=abc123&state=wrong-state",
			wantErr: "state mismatch",
		},
		{
			name:    "empty input",
			input:   "",
			wantErr: "empty callback URL",
		},
		{
			name:    "OAuth error param",
			input:   "http://127.0.0.1:8976/callback?error=access_denied&error_description=User+denied",
			wantErr: "OAuth error: access_denied",
		},
		{
			name:    "OAuth error without description",
			input:   "http://127.0.0.1:8976/callback?error=server_error",
			wantErr: "OAuth error: server_error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, err := parseCallbackURL(tt.input, state)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, code)
		})
	}
}

func TestReadCallbackInput_Timeout(t *testing.T) {
	r, w := io.Pipe()
	defer w.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := readCallbackInput(ctx, r, "state")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

func TestReadCallbackInput_Cancel(t *testing.T) {
	r, w := io.Pipe()
	defer w.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := readCallbackInput(ctx, r, "state")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "canceled")
}

func TestLoginRemoteAndLocalMutuallyExclusive(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{BaseURL: "https://3.basecampapi.com"}
	m := NewManager(cfg, http.DefaultClient)
	m.store = newTestStore(t, tmpDir)

	_, err := m.Login(context.Background(), LoginOptions{
		Remote: true,
		Local:  true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestLoginRemoteMode(t *testing.T) {
	// Set up httptest server that handles discovery + token exchange
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			w.Header().Set("Content-Type", "application/json")
			base := "http://" + r.Host
			fmt.Fprintf(w, `{
				"authorization_endpoint": "%s/authorize",
				"token_endpoint": "%s/token",
				"registration_endpoint": "%s/register"
			}`, base, base, base)
		case "/register":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"client_id":"test-client","client_secret":"test-secret"}`)
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"remote-tok","token_type":"bearer","refresh_token":"remote-refresh"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg := &config.Config{BaseURL: srv.URL}
	m := NewManager(cfg, srv.Client())
	m.store = newTestStore(t, tmpDir)

	sl := newSyncLogger()

	pr, pw := io.Pipe()
	defer pr.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := m.Login(context.Background(), LoginOptions{
			Remote:      true,
			Logger:      sl.log,
			InputReader: pr,
		})
		errCh <- err
	}()

	// Wait for the auth URL to be logged (deterministic, no sleep)
	var authURL string
	select {
	case authURL = <-sl.authReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for auth URL to be logged")
	}

	u, err := url.Parse(authURL)
	require.NoError(t, err)
	state := u.Query().Get("state")
	require.NotEmpty(t, state, "auth URL should contain state parameter")

	// Write callback URL to the pipe
	callbackURL := fmt.Sprintf("http://127.0.0.1:8976/callback?code=test-code&state=%s\n", state)
	_, err = pw.Write([]byte(callbackURL))
	require.NoError(t, err)
	pw.Close()

	select {
	case err := <-errCh:
		require.NoError(t, err, "Login should succeed in remote mode")
	case <-time.After(5 * time.Second):
		t.Fatal("Login timed out")
	}

	// Verify instructions reference the redirectURI (safe snapshot after Login returns)
	var foundRedirectHint bool
	for _, log := range sl.snapshot() {
		if strings.Contains(log, "127.0.0.1:8976") && strings.Contains(log, "?code=") {
			foundRedirectHint = true
			break
		}
	}
	assert.True(t, foundRedirectHint, "instructions should reference redirect URI")

	// Verify credentials were stored
	creds, err := m.store.Load(srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "remote-tok", creds.AccessToken)
}

func TestLoginRemoteMode_PromptWording(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			w.Header().Set("Content-Type", "application/json")
			base := "http://" + r.Host
			fmt.Fprintf(w, `{
				"authorization_endpoint": "%s/authorize",
				"token_endpoint": "%s/token",
				"registration_endpoint": "%s/register"
			}`, base, base, base)
		case "/register":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"client_id":"test-client","client_secret":"test-secret"}`)
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"tok","token_type":"bearer","refresh_token":"ref"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg := &config.Config{BaseURL: srv.URL}
	m := NewManager(cfg, srv.Client())
	m.store = newTestStore(t, tmpDir)

	sl := newSyncLogger()
	pr, pw := io.Pipe()
	defer pr.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := m.Login(context.Background(), LoginOptions{
			Remote:      true,
			Logger:      sl.log,
			InputReader: pr,
		})
		errCh <- err
	}()

	var authURL string
	select {
	case authURL = <-sl.authReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for auth URL")
	}

	u, err := url.Parse(authURL)
	require.NoError(t, err)
	state := u.Query().Get("state")
	require.NotEmpty(t, state)

	callbackURL := fmt.Sprintf("http://127.0.0.1:8976/callback?code=c&state=%s\n", state)
	_, _ = pw.Write([]byte(callbackURL))
	pw.Close()

	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Login timed out")
	}

	logs := sl.snapshot()
	joined := strings.Join(logs, "\n")

	assert.Contains(t, joined, "Remote Authentication", "should show heading")
	assert.Contains(t, joined, "1. Open this URL", "should show step 1")
	assert.Contains(t, joined, "4. Copy the full URL", "should show step 4")
	assert.Contains(t, joined, "Paste the callback URL", "should show updated prompt")
}

func TestLoginRemoteMode_StateMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			w.Header().Set("Content-Type", "application/json")
			base := "http://" + r.Host
			fmt.Fprintf(w, `{
				"authorization_endpoint": "%s/authorize",
				"token_endpoint": "%s/token",
				"registration_endpoint": "%s/register"
			}`, base, base, base)
		case "/register":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"client_id":"test-client"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg := &config.Config{BaseURL: srv.URL}
	m := NewManager(cfg, srv.Client())
	m.store = newTestStore(t, tmpDir)

	sl := newSyncLogger()
	pr, pw := io.Pipe()
	defer pr.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := m.Login(context.Background(), LoginOptions{
			Remote:      true,
			Logger:      sl.log,
			InputReader: pr,
		})
		errCh <- err
	}()

	// Wait until Login has logged the auth URL (meaning it's now reading from pr)
	select {
	case <-sl.authReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for auth URL")
	}

	// Write callback with wrong state
	_, _ = pw.Write([]byte("http://127.0.0.1:8976/callback?code=test-code&state=wrong-state\n"))
	pw.Close()

	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "state mismatch")
	case <-time.After(5 * time.Second):
		t.Fatal("Login timed out")
	}
}

func TestLoginRemoteMode_EmptyInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			w.Header().Set("Content-Type", "application/json")
			base := "http://" + r.Host
			fmt.Fprintf(w, `{
				"authorization_endpoint": "%s/authorize",
				"token_endpoint": "%s/token",
				"registration_endpoint": "%s/register"
			}`, base, base, base)
		case "/register":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"client_id":"test-client"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg := &config.Config{BaseURL: srv.URL}
	m := NewManager(cfg, srv.Client())
	m.store = newTestStore(t, tmpDir)

	sl := newSyncLogger()
	pr, pw := io.Pipe()
	defer pr.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := m.Login(context.Background(), LoginOptions{
			Remote:      true,
			Logger:      sl.log,
			InputReader: pr,
		})
		errCh <- err
	}()

	select {
	case <-sl.authReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for auth URL")
	}

	// Close pipe immediately → EOF / no input
	pw.Close()

	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no input received")
	case <-time.After(5 * time.Second):
		t.Fatal("Login timed out")
	}
}

func TestLoginRemoteMode_CustomRedirectURI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			w.Header().Set("Content-Type", "application/json")
			base := "http://" + r.Host
			fmt.Fprintf(w, `{
				"authorization_endpoint": "%s/authorize",
				"token_endpoint": "%s/token",
				"registration_endpoint": "%s/register"
			}`, base, base, base)
		case "/register":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"client_id":"test-client"}`)
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"tok","token_type":"bearer"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg := &config.Config{BaseURL: srv.URL}
	m := NewManager(cfg, srv.Client())
	m.store = newTestStore(t, tmpDir)

	sl := newSyncLogger()
	pr, pw := io.Pipe()
	defer pr.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := m.Login(context.Background(), LoginOptions{
			Remote:      true,
			RedirectURI: "http://localhost:9999/my-cb",
			Logger:      sl.log,
			InputReader: pr,
		})
		errCh <- err
	}()

	// Wait for auth URL deterministically
	var authURL string
	select {
	case authURL = <-sl.authReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for auth URL")
	}

	u, err := url.Parse(authURL)
	require.NoError(t, err)
	state := u.Query().Get("state")
	require.NotEmpty(t, state)

	// Write callback with custom redirect URI
	callbackURL := fmt.Sprintf("http://localhost:9999/my-cb?code=c&state=%s\n", state)
	_, _ = pw.Write([]byte(callbackURL))
	pw.Close()

	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Login timed out")
	}

	// Verify instructions reference the custom redirect URI (safe snapshot after Login returns)
	var foundCustomURI bool
	for _, log := range sl.snapshot() {
		if strings.Contains(log, "localhost:9999/my-cb?code=") {
			foundCustomURI = true
			break
		}
	}
	assert.True(t, foundCustomURI, "instructions should show custom redirect URI")
}

func TestDefaults_AutoDetectsSSH(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "10.0.0.1 12345 10.0.0.2 22")

	opts := &LoginOptions{}
	opts.defaults()
	assert.True(t, opts.Remote, "should auto-detect SSH and set Remote")
	assert.True(t, opts.NoBrowser, "Remote should imply NoBrowser")
}

func TestDefaults_LocalOverridesSSHDetection(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "10.0.0.1 12345 10.0.0.2 22")

	opts := &LoginOptions{Local: true}
	opts.defaults()
	assert.False(t, opts.Remote, "Local should prevent SSH auto-detection")
}

func TestCredentialWrite_OverwriteExisting(t *testing.T) {
	tmpDir := t.TempDir()
	store := newTestStore(t, tmpDir)
	origin := "https://test.example.com"

	// Write initial credentials
	creds1 := &Credentials{
		AccessToken:  "token-1",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().Unix() + 3600,
		OAuthType:    "launchpad",
	}
	require.NoError(t, store.Save(origin, creds1))

	// Overwrite with new credentials
	creds2 := &Credentials{
		AccessToken:  "token-2",
		RefreshToken: "refresh-2",
		ExpiresAt:    time.Now().Unix() + 7200,
		OAuthType:    "bc3",
	}
	require.NoError(t, store.Save(origin, creds2), "overwrite of existing credential must succeed")

	// Verify the new value persists
	loaded, err := store.Load(origin)
	require.NoError(t, err)
	assert.Equal(t, "token-2", loaded.AccessToken)
	assert.Equal(t, "bc3", loaded.OAuthType)
}

func TestLoginLaunchpadClearsScope(t *testing.T) {
	// Server that fails OAuth discovery (404 on .well-known), forcing Launchpad fallback.
	var tokenCalled atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			http.NotFound(w, r) // force Launchpad fallback
		case "/authorization/token":
			tokenCalled.Store(true)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"lp-tok","token_type":"bearer","refresh_token":"lp-ref"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	// Point Launchpad URL at our test server
	t.Setenv("BASECAMP_LAUNCHPAD_URL", srv.URL)

	cfg := &config.Config{BaseURL: srv.URL}
	m := NewManager(cfg, srv.Client())
	m.store = newTestStore(t, tmpDir)

	// Custom logger that detects Launchpad auth URL (/authorization/new)
	authReady := make(chan string, 1)
	var logMu sync.Mutex
	signaled := false
	logger := func(msg string) {
		logMu.Lock()
		defer logMu.Unlock()
		if !signaled {
			trimmed := strings.TrimSpace(msg)
			if strings.HasPrefix(trimmed, "http") && strings.Contains(trimmed, "/authorization/new") {
				signaled = true
				authReady <- trimmed
			}
		}
	}

	pr, pw := io.Pipe()
	defer pr.Close()

	type loginOut struct {
		result *LoginResult
		err    error
	}
	ch := make(chan loginOut, 1)
	go func() {
		result, err := m.Login(context.Background(), LoginOptions{
			Scope:       "read", // explicit scope should be cleared for Launchpad
			Remote:      true,
			Logger:      logger,
			InputReader: pr,
		})
		ch <- loginOut{result, err}
	}()

	// Wait for auth URL
	var authURL string
	select {
	case authURL = <-authReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for auth URL")
	}

	u, err := url.Parse(authURL)
	require.NoError(t, err)
	state := u.Query().Get("state")

	// Write callback
	callbackURL := fmt.Sprintf("http://127.0.0.1:8976/callback?code=test-code&state=%s\n", state)
	_, _ = pw.Write([]byte(callbackURL))
	pw.Close()

	out := <-ch
	require.NoError(t, out.err)
	require.True(t, tokenCalled.Load(), "token endpoint should have been called")

	// Result scope should be empty for Launchpad
	assert.Equal(t, "", out.result.Scope, "Launchpad login should clear scope")
	assert.Equal(t, "launchpad", out.result.OAuthType)

	// Stored credentials should have empty scope
	creds, err := m.store.Load(srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "", creds.Scope, "stored scope should be empty for Launchpad")
}

func TestLoginBC3DefaultsToRead(t *testing.T) {
	// BC3 mock server with successful discovery
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			w.Header().Set("Content-Type", "application/json")
			base := "http://" + r.Host
			fmt.Fprintf(w, `{
				"authorization_endpoint": "%s/authorize",
				"token_endpoint": "%s/token",
				"registration_endpoint": "%s/register"
			}`, base, base, base)
		case "/register":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"client_id":"test-client"}`)
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"bc3-tok","token_type":"bearer","refresh_token":"bc3-ref"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg := &config.Config{BaseURL: srv.URL}
	m := NewManager(cfg, srv.Client())
	m.store = newTestStore(t, tmpDir)

	sl := newSyncLogger()
	pr, pw := io.Pipe()
	defer pr.Close()

	type loginOut struct {
		result *LoginResult
		err    error
	}
	ch := make(chan loginOut, 1)
	go func() {
		result, err := m.Login(context.Background(), LoginOptions{
			// No scope specified — should default to "read" for BC3
			Remote:      true,
			Logger:      sl.log,
			InputReader: pr,
		})
		ch <- loginOut{result, err}
	}()

	var authURL string
	select {
	case authURL = <-sl.authReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for auth URL")
	}

	u, err := url.Parse(authURL)
	require.NoError(t, err)
	state := u.Query().Get("state")

	callbackURL := fmt.Sprintf("http://127.0.0.1:8976/callback?code=test-code&state=%s\n", state)
	_, _ = pw.Write([]byte(callbackURL))
	pw.Close()

	out := <-ch
	require.NoError(t, out.err)
	assert.Equal(t, "read", out.result.Scope, "BC3 should default to read scope")
	assert.Equal(t, "bc3", out.result.OAuthType)

	creds, err := m.store.Load(srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "read", creds.Scope, "stored scope should be 'read' for BC3 default")
}

func TestRefreshLocked_LaunchpadSendsClientID(t *testing.T) {
	t.Setenv("BASECAMP_OAUTH_CLIENT_ID", "")
	t.Setenv("BASECAMP_OAUTH_CLIENT_SECRET", "")

	var mu sync.Mutex
	var capturedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		capturedBody = string(body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"new-token","refresh_token":"new-refresh"}`)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	cfg := config.Default()
	cfg.BaseURL = srv.URL

	m := &Manager{
		cfg:        cfg,
		httpClient: srv.Client(),
		store:      newTestStore(t, tmpDir),
	}

	creds := &Credentials{
		AccessToken:   "old-token",
		RefreshToken:  "old-refresh",
		OAuthType:     "launchpad",
		TokenEndpoint: srv.URL + "/authorization/token",
		ExpiresAt:     time.Now().Add(-1 * time.Hour).Unix(),
	}
	require.NoError(t, m.store.Save(srv.URL, creds))

	err := m.refreshLocked(context.Background(), srv.URL, creds)
	require.NoError(t, err)

	mu.Lock()
	body := capturedBody
	mu.Unlock()
	assert.Contains(t, body, "client_id="+launchpadClientID)
	assert.Contains(t, body, "client_secret="+launchpadClientSecret)
}

func TestRefreshLocked_BC3SendsClientID(t *testing.T) {
	var mu sync.Mutex
	var capturedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		capturedBody = string(body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"new-token","refresh_token":"new-refresh","expires_in":3600}`)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg := config.Default()
	cfg.BaseURL = srv.URL

	m := &Manager{
		cfg:        cfg,
		httpClient: srv.Client(),
		store:      newTestStore(t, tmpDir),
	}

	// Pre-populate client.json
	require.NoError(t, m.saveBC3Client(&ClientCredentials{
		ClientID:     "bc3-client-id",
		ClientSecret: "bc3-client-secret",
	}))

	creds := &Credentials{
		AccessToken:   "old-token",
		RefreshToken:  "old-refresh",
		OAuthType:     "bc3",
		TokenEndpoint: srv.URL + "/token",
		ExpiresAt:     time.Now().Add(-1 * time.Hour).Unix(),
	}
	require.NoError(t, m.store.Save(srv.URL, creds))

	err := m.refreshLocked(context.Background(), srv.URL, creds)
	require.NoError(t, err)

	mu.Lock()
	body := capturedBody
	mu.Unlock()
	assert.Contains(t, body, "client_id=bc3-client-id")
	assert.Contains(t, body, "client_secret=bc3-client-secret")
}

func TestRefreshLocked_BC3WithoutClientJSON(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg := config.Default()
	m := &Manager{
		cfg:        cfg,
		httpClient: http.DefaultClient,
		store:      newTestStore(t, tmpDir),
	}

	creds := &Credentials{
		AccessToken:   "old-token",
		RefreshToken:  "old-refresh",
		OAuthType:     "bc3",
		TokenEndpoint: "https://example.com/token",
		ExpiresAt:     time.Now().Add(-1 * time.Hour).Unix(),
	}

	err := m.refreshLocked(context.Background(), "test", creds)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Cannot load BC3 client credentials")
	assert.Contains(t, err.Error(), "custom-redirect")
}

func TestRefreshLocked_ClearsExpiresAtWhenServerOmits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// No expires_in in response
		fmt.Fprint(w, `{"access_token":"new-token","refresh_token":"new-refresh"}`)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	cfg := config.Default()
	cfg.BaseURL = srv.URL

	m := &Manager{
		cfg:        cfg,
		httpClient: srv.Client(),
		store:      newTestStore(t, tmpDir),
	}

	creds := &Credentials{
		AccessToken:   "old-token",
		RefreshToken:  "old-refresh",
		OAuthType:     "launchpad",
		TokenEndpoint: srv.URL + "/authorization/token",
		ExpiresAt:     time.Now().Add(-1 * time.Hour).Unix(),
	}
	require.NoError(t, m.store.Save(srv.URL, creds))

	err := m.refreshLocked(context.Background(), srv.URL, creds)
	require.NoError(t, err)

	// Reload and verify ExpiresAt is 0 (non-expiring)
	reloaded, err := m.store.Load(srv.URL)
	require.NoError(t, err)
	assert.Equal(t, int64(0), reloaded.ExpiresAt,
		"ExpiresAt should be 0 when server omits expires_in")
}

func TestRefreshLocked_EmptyOAuthTypeDefaultsToLaunchpad(t *testing.T) {
	t.Setenv("BASECAMP_OAUTH_CLIENT_ID", "")
	t.Setenv("BASECAMP_OAUTH_CLIENT_SECRET", "")

	var mu sync.Mutex
	var capturedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		capturedBody = string(body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"new-token","refresh_token":"new-refresh"}`)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	cfg := config.Default()
	cfg.BaseURL = srv.URL

	m := &Manager{
		cfg:        cfg,
		httpClient: srv.Client(),
		store:      newTestStore(t, tmpDir),
	}

	creds := &Credentials{
		AccessToken:   "old-token",
		RefreshToken:  "old-refresh",
		OAuthType:     "", // Old credentials with no OAuthType
		TokenEndpoint: srv.URL + "/authorization/token",
		ExpiresAt:     time.Now().Add(-1 * time.Hour).Unix(),
	}
	require.NoError(t, m.store.Save(srv.URL, creds))

	err := m.refreshLocked(context.Background(), srv.URL, creds)
	require.NoError(t, err)

	mu.Lock()
	body := capturedBody
	mu.Unlock()
	// Should have used launchpad legacy format (type=refresh, not grant_type=refresh_token)
	assert.Contains(t, body, "type=refresh")
	assert.Contains(t, body, "client_id="+launchpadClientID)
}

func TestLoginRejectsInvalidScope(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{BaseURL: "https://3.basecampapi.com"}
	m := NewManager(cfg, http.DefaultClient)
	m.store = newTestStore(t, tmpDir)

	_, err := m.Login(context.Background(), LoginOptions{
		Scope: "admin",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Invalid scope")
}

// --- AuthorizationEndpoint tests ---

func TestAuthorizationEndpoint_StoredBC3(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "")

	tmpDir := t.TempDir()
	cfg := &config.Config{BaseURL: "https://3.basecampapi.com"}
	m := NewManager(cfg, nil)
	m.store = newTestStore(t, tmpDir)

	// Store bc3-type credentials
	origin := config.NormalizeBaseURL(cfg.BaseURL)
	require.NoError(t, m.store.Save(origin, &Credentials{
		AccessToken: "tok",
		OAuthType:   "bc3",
	}))

	ep, err := m.AuthorizationEndpoint(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://3.basecampapi.com/authorization.json", ep)
}

func TestAuthorizationEndpoint_StoredLaunchpad(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "")

	tmpDir := t.TempDir()
	cfg := &config.Config{BaseURL: "https://3.basecampapi.com"}
	m := NewManager(cfg, nil)
	m.store = newTestStore(t, tmpDir)

	origin := config.NormalizeBaseURL(cfg.BaseURL)
	require.NoError(t, m.store.Save(origin, &Credentials{
		AccessToken: "tok",
		OAuthType:   "launchpad",
	}))

	ep, err := m.AuthorizationEndpoint(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://launchpad.37signals.com/authorization.json", ep)
}

func TestAuthorizationEndpoint_StoredLaunchpadOverrideURL(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "")
	t.Setenv("BASECAMP_LAUNCHPAD_URL", "https://custom-lp.example.com")

	tmpDir := t.TempDir()
	cfg := &config.Config{BaseURL: "https://3.basecampapi.com"}
	m := NewManager(cfg, nil)
	m.store = newTestStore(t, tmpDir)

	origin := config.NormalizeBaseURL(cfg.BaseURL)
	require.NoError(t, m.store.Save(origin, &Credentials{
		AccessToken: "tok",
		OAuthType:   "launchpad",
	}))

	ep, err := m.AuthorizationEndpoint(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://custom-lp.example.com/authorization.json", ep)
}

func TestAuthorizationEndpoint_TokenWithBC3Prefix(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "bc_at_abc123")

	// Isolate from real credential store
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg := &config.Config{BaseURL: "https://3.basecampapi.com"}
	m := NewManager(cfg, nil)
	m.store = newTestStore(t, tmpDir)

	ep, err := m.AuthorizationEndpoint(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://3.basecampapi.com/authorization.json", ep)
}

func TestAuthorizationEndpoint_TokenWithoutBC3Prefix(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "some-launchpad-token")

	// Isolate from real credential store
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg := &config.Config{BaseURL: "https://3.basecampapi.com"}
	m := NewManager(cfg, nil)
	m.store = newTestStore(t, tmpDir)

	ep, err := m.AuthorizationEndpoint(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://launchpad.37signals.com/authorization.json", ep)
}

func TestAuthorizationEndpoint_UnknownStoredType(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "")

	tmpDir := t.TempDir()
	cfg := &config.Config{BaseURL: "https://3.basecampapi.com"}
	m := NewManager(cfg, nil)
	m.store = newTestStore(t, tmpDir)

	origin := config.NormalizeBaseURL(cfg.BaseURL)
	require.NoError(t, m.store.Save(origin, &Credentials{
		AccessToken: "tok",
		OAuthType:   "unknown",
	}))

	_, err := m.AuthorizationEndpoint(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Unknown OAuth type")
}

// Regression: BASECAMP_TOKEN must override conflicting stored credentials.
// A user may export BASECAMP_TOKEN=bc_at_... while stale launchpad creds
// remain on disk. The endpoint must follow the token, not the stored type.

func TestAuthorizationEndpoint_BC3TokenOverridesStoredLaunchpad(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "bc_at_override_test")

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg := &config.Config{BaseURL: "https://3.basecampapi.com"}
	m := NewManager(cfg, nil)
	m.store = newTestStore(t, tmpDir)

	// Stale stored credentials say "launchpad"
	origin := config.NormalizeBaseURL(cfg.BaseURL)
	require.NoError(t, m.store.Save(origin, &Credentials{
		AccessToken: "stale-lp-token",
		OAuthType:   "launchpad",
	}))

	ep, err := m.AuthorizationEndpoint(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://3.basecampapi.com/authorization.json", ep,
		"bc_at_ env token must route to BC3, not stored launchpad")
}

func TestAuthorizationEndpoint_LaunchpadTokenOverridesStoredBC3(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "plain-launchpad-token")

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg := &config.Config{BaseURL: "https://3.basecampapi.com"}
	m := NewManager(cfg, nil)
	m.store = newTestStore(t, tmpDir)

	// Stale stored credentials say "bc3"
	origin := config.NormalizeBaseURL(cfg.BaseURL)
	require.NoError(t, m.store.Save(origin, &Credentials{
		AccessToken: "stale-bc3-token",
		OAuthType:   "bc3",
	}))

	ep, err := m.AuthorizationEndpoint(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://launchpad.37signals.com/authorization.json", ep,
		"non-bc_at_ env token must route to launchpad, not stored bc3")
}
