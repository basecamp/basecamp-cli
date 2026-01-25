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

	"github.com/basecamp/bcq/internal/config"
)

func TestNewStore(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Store should be created (may or may not use keyring depending on system)
	if store == nil {
		t.Fatal("NewStore returned nil")
	}
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
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify file was created with correct permissions
	credFile := filepath.Join(tmpDir, "credentials.json")
	info, err := os.Stat(credFile)
	if err != nil {
		t.Fatalf("Credentials file not created: %v", err)
	}
	perms := info.Mode().Perm()
	if perms != 0600 {
		t.Errorf("File permissions = %o, want 0600", perms)
	}

	// Load credentials
	loaded, err := store.Load(origin)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Verify values match
	if loaded.AccessToken != creds.AccessToken {
		t.Errorf("AccessToken = %q, want %q", loaded.AccessToken, creds.AccessToken)
	}
	if loaded.RefreshToken != creds.RefreshToken {
		t.Errorf("RefreshToken = %q, want %q", loaded.RefreshToken, creds.RefreshToken)
	}
	if loaded.ExpiresAt != creds.ExpiresAt {
		t.Errorf("ExpiresAt = %d, want %d", loaded.ExpiresAt, creds.ExpiresAt)
	}
	if loaded.Scope != creds.Scope {
		t.Errorf("Scope = %q, want %q", loaded.Scope, creds.Scope)
	}
	if loaded.OAuthType != creds.OAuthType {
		t.Errorf("OAuthType = %q, want %q", loaded.OAuthType, creds.OAuthType)
	}
	if loaded.UserID != creds.UserID {
		t.Errorf("UserID = %q, want %q", loaded.UserID, creds.UserID)
	}
}

func TestStoreMultipleOrigins(t *testing.T) {
	tmpDir := t.TempDir()
	store := &Store{useKeyring: false, fallbackDir: tmpDir}

	// Save credentials for two different origins
	origin1 := "https://origin1.example.com"
	origin2 := "https://origin2.example.com"

	creds1 := &Credentials{AccessToken: "token1", ExpiresAt: time.Now().Unix() + 3600}
	creds2 := &Credentials{AccessToken: "token2", ExpiresAt: time.Now().Unix() + 3600}

	if err := store.Save(origin1, creds1); err != nil {
		t.Fatalf("Save origin1 failed: %v", err)
	}
	if err := store.Save(origin2, creds2); err != nil {
		t.Fatalf("Save origin2 failed: %v", err)
	}

	// Load and verify each origin
	loaded1, err := store.Load(origin1)
	if err != nil {
		t.Fatalf("Load origin1 failed: %v", err)
	}
	if loaded1.AccessToken != "token1" {
		t.Errorf("Origin1 token = %q, want %q", loaded1.AccessToken, "token1")
	}

	loaded2, err := store.Load(origin2)
	if err != nil {
		t.Fatalf("Load origin2 failed: %v", err)
	}
	if loaded2.AccessToken != "token2" {
		t.Errorf("Origin2 token = %q, want %q", loaded2.AccessToken, "token2")
	}
}

func TestStoreDelete(t *testing.T) {
	tmpDir := t.TempDir()
	store := &Store{useKeyring: false, fallbackDir: tmpDir}

	origin := "https://delete-test.example.com"
	creds := &Credentials{AccessToken: "to-be-deleted", ExpiresAt: time.Now().Unix() + 3600}

	// Save then delete
	if err := store.Save(origin, creds); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if err := store.Delete(origin); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Load should fail
	_, err := store.Load(origin)
	if err == nil {
		t.Error("Load should fail after delete")
	}
}

func TestStoreLoadMissing(t *testing.T) {
	tmpDir := t.TempDir()
	store := &Store{useKeyring: false, fallbackDir: tmpDir}

	// Load non-existent origin should fail
	_, err := store.Load("https://nonexistent.example.com")
	if err == nil {
		t.Error("Load should fail for non-existent origin")
	}
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
			if result != tt.expected {
				t.Errorf("key(%q) = %q, want %q", tt.origin, result, tt.expected)
			}
		})
	}
}

func TestGenerateCodeVerifier(t *testing.T) {
	// Generate multiple verifiers to check they're unique
	verifiers := make(map[string]bool)
	for i := 0; i < 10; i++ {
		v := generateCodeVerifier()

		// Should be base64url encoded (no padding)
		if v == "" {
			t.Error("generateCodeVerifier returned empty string")
		}

		// Check uniqueness
		if verifiers[v] {
			t.Errorf("generateCodeVerifier produced duplicate: %s", v)
		}
		verifiers[v] = true

		// Should be ~43 characters (32 bytes base64url encoded)
		if len(v) < 40 || len(v) > 50 {
			t.Errorf("generateCodeVerifier length = %d, expected ~43", len(v))
		}
	}
}

func TestGenerateCodeChallenge(t *testing.T) {
	verifier := "test_code_verifier_12345"

	challenge := generateCodeChallenge(verifier)

	// Manually compute expected challenge
	h := sha256.Sum256([]byte(verifier))
	expected := base64.RawURLEncoding.EncodeToString(h[:])

	if challenge != expected {
		t.Errorf("generateCodeChallenge(%q) = %q, want %q", verifier, challenge, expected)
	}
}

func TestGenerateState(t *testing.T) {
	// Generate multiple states to check they're unique
	states := make(map[string]bool)
	for i := 0; i < 10; i++ {
		s := generateState()

		if s == "" {
			t.Error("generateState returned empty string")
		}

		if states[s] {
			t.Errorf("generateState produced duplicate: %s", s)
		}
		states[s] = true

		// Should be ~22 characters (16 bytes base64url encoded)
		if len(s) < 20 || len(s) > 25 {
			t.Errorf("generateState length = %d, expected ~22", len(s))
		}
	}
}

func TestNewManager(t *testing.T) {
	cfg := &config.Config{
		BaseURL: "https://3.basecampapi.com",
	}

	manager := NewManager(cfg, http.DefaultClient)

	if manager == nil {
		t.Fatal("NewManager returned nil")
	}
	if manager.cfg != cfg {
		t.Error("Manager config not set correctly")
	}
	if manager.httpClient != http.DefaultClient {
		t.Error("Manager httpClient not set correctly")
	}
	if manager.store == nil {
		t.Error("Manager store not initialized")
	}
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
	if manager.IsAuthenticated() {
		t.Error("Should not be authenticated without token")
	}

	// With env token
	os.Setenv("BASECAMP_TOKEN", "test-env-token")
	if !manager.IsAuthenticated() {
		t.Error("Should be authenticated with BASECAMP_TOKEN env var")
	}
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
	if manager.IsAuthenticated() {
		t.Error("Should not be authenticated without stored credentials")
	}

	// Save credentials
	creds := &Credentials{
		AccessToken:  "test-token",
		RefreshToken: "test-refresh",
		ExpiresAt:    time.Now().Unix() + 3600,
	}
	manager.store.Save("https://3.basecampapi.com", creds)

	// With stored creds
	if !manager.IsAuthenticated() {
		t.Error("Should be authenticated with stored credentials")
	}
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
	if userID != "12345" {
		t.Errorf("GetUserID() = %q, want %q", userID, "12345")
	}
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
	if err != nil {
		t.Fatalf("SetUserID failed: %v", err)
	}

	// Verify it was saved
	loaded, err := manager.store.Load("https://3.basecampapi.com")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.UserID != "67890" {
		t.Errorf("UserID = %q, want %q", loaded.UserID, "67890")
	}
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
	if err != nil {
		t.Fatalf("Logout failed: %v", err)
	}

	// Should no longer be authenticated
	if manager.IsAuthenticated() {
		t.Error("Should not be authenticated after logout")
	}
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
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var loaded Credentials
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if loaded.AccessToken != creds.AccessToken {
		t.Errorf("AccessToken mismatch")
	}
	if loaded.RefreshToken != creds.RefreshToken {
		t.Errorf("RefreshToken mismatch")
	}
	if loaded.ExpiresAt != creds.ExpiresAt {
		t.Errorf("ExpiresAt mismatch")
	}
	if loaded.Scope != creds.Scope {
		t.Errorf("Scope mismatch")
	}
	if loaded.OAuthType != creds.OAuthType {
		t.Errorf("OAuthType mismatch")
	}
	if loaded.TokenEndpoint != creds.TokenEndpoint {
		t.Errorf("TokenEndpoint mismatch")
	}
	if loaded.UserID != creds.UserID {
		t.Errorf("UserID mismatch")
	}
}

func TestOAuthConfigJSON(t *testing.T) {
	cfg := &OAuthConfig{
		Issuer:                "https://issuer.example.com",
		AuthorizationEndpoint: "https://auth.example.com/authorize",
		TokenEndpoint:         "https://auth.example.com/token",
		RegistrationEndpoint:  "https://auth.example.com/register",
		ScopesSupported:       []string{"read", "write"},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var loaded OAuthConfig
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if loaded.Issuer != cfg.Issuer {
		t.Errorf("Issuer mismatch")
	}
	if loaded.AuthorizationEndpoint != cfg.AuthorizationEndpoint {
		t.Errorf("AuthorizationEndpoint mismatch")
	}
	if loaded.TokenEndpoint != cfg.TokenEndpoint {
		t.Errorf("TokenEndpoint mismatch")
	}
	if loaded.RegistrationEndpoint != cfg.RegistrationEndpoint {
		t.Errorf("RegistrationEndpoint mismatch")
	}
	if len(loaded.ScopesSupported) != 2 {
		t.Errorf("ScopesSupported length = %d, want 2", len(loaded.ScopesSupported))
	}
}

func TestClientCredentialsJSON(t *testing.T) {
	creds := &ClientCredentials{
		ClientID:     "client-id-123",
		ClientSecret: "client-secret-456",
	}

	data, err := json.Marshal(creds)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var loaded ClientCredentials
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if loaded.ClientID != creds.ClientID {
		t.Errorf("ClientID = %q, want %q", loaded.ClientID, creds.ClientID)
	}
	if loaded.ClientSecret != creds.ClientSecret {
		t.Errorf("ClientSecret = %q, want %q", loaded.ClientSecret, creds.ClientSecret)
	}
}

func TestUsingKeyring(t *testing.T) {
	store := &Store{useKeyring: true, fallbackDir: "/tmp"}
	if !store.UsingKeyring() {
		t.Error("UsingKeyring() = false, want true")
	}

	store = &Store{useKeyring: false, fallbackDir: "/tmp"}
	if store.UsingKeyring() {
		t.Error("UsingKeyring() = true, want false")
	}
}
