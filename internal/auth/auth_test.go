package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/oauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/config"
)

func TestNewStore(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Store should be created (may or may not use keyring depending on system)
	require.NotNil(t, store, "NewStore returned nil")
}

func TestStoreFileBackend(t *testing.T) {
	tmpDir := t.TempDir()

	// Force file backend by creating store with useKeyring=false
	store := &Store{useKeyring: false, fallbackDir: tmpDir}

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

	// Verify file was created with correct permissions
	credFile := filepath.Join(tmpDir, "credentials.json")
	info, err := os.Stat(credFile)
	require.NoError(t, err, "Credentials file not created")
	perms := info.Mode().Perm()
	assert.Equal(t, os.FileMode(0600), perms, "File permissions mismatch")

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
	store := &Store{useKeyring: false, fallbackDir: tmpDir}

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
	store := &Store{useKeyring: false, fallbackDir: tmpDir}

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
	store := &Store{useKeyring: false, fallbackDir: tmpDir}

	// Load non-existent origin should fail
	_, err := store.Load("https://nonexistent.example.com")
	assert.Error(t, err, "Load should fail for non-existent origin")
}

func TestKeyFunction(t *testing.T) {
	tests := []struct {
		origin   string
		expected string
	}{
		{"https://3.basecampapi.com", "bcq::https://3.basecampapi.com"},
		{"http://localhost:3000", "bcq::http://localhost:3000"},
		{"https://custom.example.com", "bcq::https://custom.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.origin, func(t *testing.T) {
			result := key(tt.origin)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGenerateCodeVerifier(t *testing.T) {
	// Generate multiple verifiers to check they're unique
	verifiers := make(map[string]bool)
	for range 10 {
		v := generateCodeVerifier()

		// Should be base64url encoded (no padding)
		assert.NotEmpty(t, v, "generateCodeVerifier returned empty string")

		// Check uniqueness
		assert.False(t, verifiers[v], "generateCodeVerifier produced duplicate: %s", v)
		verifiers[v] = true

		// Should be ~43 characters (32 bytes base64url encoded)
		assert.True(t, len(v) >= 40 && len(v) <= 50, "generateCodeVerifier length = %d, expected ~43", len(v))
	}
}

func TestGenerateCodeChallenge(t *testing.T) {
	verifier := "test_code_verifier_12345"

	challenge := generateCodeChallenge(verifier)

	// Manually compute expected challenge
	h := sha256.Sum256([]byte(verifier))
	expected := base64.RawURLEncoding.EncodeToString(h[:])

	assert.Equal(t, expected, challenge)
}

func TestGenerateState(t *testing.T) {
	// Generate multiple states to check they're unique
	states := make(map[string]bool)
	for range 10 {
		s := generateState()

		assert.NotEmpty(t, s, "generateState returned empty string")

		assert.False(t, states[s], "generateState produced duplicate: %s", s)
		states[s] = true

		// Should be ~22 characters (16 bytes base64url encoded)
		assert.True(t, len(s) >= 20 && len(s) <= 25, "generateState length = %d, expected ~22", len(s))
	}
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
	manager.store = &Store{useKeyring: false, fallbackDir: tmpDir}

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
	manager.store = &Store{useKeyring: false, fallbackDir: tmpDir}

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

func TestGetUserID(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		BaseURL: "https://3.basecampapi.com",
	}
	manager := NewManager(cfg, http.DefaultClient)
	manager.store = &Store{useKeyring: false, fallbackDir: tmpDir}

	// Save credentials with user ID
	creds := &Credentials{
		AccessToken: "test-token",
		ExpiresAt:   time.Now().Unix() + 3600,
		UserID:      "12345",
	}
	manager.store.Save("https://3.basecampapi.com", creds)

	userID := manager.GetUserID()
	assert.Equal(t, "12345", userID)
}

func TestSetUserID(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		BaseURL: "https://3.basecampapi.com",
	}
	manager := NewManager(cfg, http.DefaultClient)
	manager.store = &Store{useKeyring: false, fallbackDir: tmpDir}

	// Save initial credentials
	creds := &Credentials{
		AccessToken: "test-token",
		ExpiresAt:   time.Now().Unix() + 3600,
	}
	manager.store.Save("https://3.basecampapi.com", creds)

	// Set user ID
	err := manager.SetUserID("67890")
	require.NoError(t, err, "SetUserID failed")

	// Verify it was saved
	loaded, err := manager.store.Load("https://3.basecampapi.com")
	require.NoError(t, err, "Load failed")
	assert.Equal(t, "67890", loaded.UserID)
}

func TestLogout(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		BaseURL: "https://3.basecampapi.com",
	}
	manager := NewManager(cfg, http.DefaultClient)
	manager.store = &Store{useKeyring: false, fallbackDir: tmpDir}

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
	store := &Store{useKeyring: true, fallbackDir: "/tmp"}
	assert.True(t, store.UsingKeyring(), "UsingKeyring() should be true")

	store = &Store{useKeyring: false, fallbackDir: "/tmp"}
	assert.False(t, store.UsingKeyring(), "UsingKeyring() should be false")
}
