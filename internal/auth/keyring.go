package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/basecamp/cli/credstore"
)

// Credentials holds OAuth tokens and metadata.
type Credentials struct {
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token"`
	ExpiresAt     int64  `json:"expires_at"`
	Scope         string `json:"scope"`
	OAuthType     string `json:"oauth_type"` // "bc3" or "launchpad"
	TokenEndpoint string `json:"token_endpoint"`
	UserID        string `json:"user_id,omitempty"`
	UserEmail     string `json:"user_email,omitempty"`
}

// Store wraps credstore.Store with typed Credentials marshaling.
//
// Construction of the underlying credstore.Store is deferred to first use:
// credstore.NewStore probes the OS keyring availability with a write, and on
// a locked keychain with no TTY or GUI (headless macOS) that probe blocks
// forever in an uncancellable `security` child process. Credential-free
// commands must never pay it.
type Store struct {
	fallbackDir string
	initOnce    sync.Once
	inner       *credstore.Store
	warnOnce    sync.Once
}

// newCredStore is replaceable in tests to avoid real keyring access.
var newCredStore = credstore.NewStore

// NewStore creates a credential store. The OS keyring is not touched until
// the first credential operation.
func NewStore(fallbackDir string) *Store {
	return &Store{fallbackDir: fallbackDir}
}

// ensure constructs the underlying store on first use. Callers reach here
// from paths that don't hold Manager.mu (e.g. IsAuthenticated), so the
// sync.Once provides the synchronization.
func (s *Store) ensure() *credstore.Store {
	s.initOnce.Do(func() {
		s.inner = newCredStore(credstore.StoreOptions{
			ServiceName:   "basecamp",
			DisableEnvVar: "BASECAMP_NO_KEYRING",
			FallbackDir:   s.fallbackDir,
		})
	})
	return s.inner
}

// warnFallback prints the keyring fallback warning once, on first credential write.
func (s *Store) warnFallback() {
	inner := s.ensure()
	s.warnOnce.Do(func() {
		if w := inner.FallbackWarning(); w != "" {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}
	})
}

// Load retrieves credentials for the given origin.
func (s *Store) Load(origin string) (*Credentials, error) {
	data, err := s.ensure().Load(origin)
	if err != nil {
		return nil, err
	}
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("invalid credentials: %w", err)
	}
	return &creds, nil
}

// Save stores credentials for the given origin.
func (s *Store) Save(origin string, creds *Credentials) error {
	s.warnFallback()
	data, err := json.Marshal(creds)
	if err != nil {
		return err
	}
	return s.ensure().Save(origin, data)
}

// Delete removes credentials for the given origin.
func (s *Store) Delete(origin string) error { return s.ensure().Delete(origin) }

// MigrateToKeyring migrates credentials from file to keyring.
func (s *Store) MigrateToKeyring() error { return s.ensure().MigrateToKeyring() }

// UsingKeyring returns true if the store is using the system keyring.
func (s *Store) UsingKeyring() bool { return s.ensure().UsingKeyring() }
