package auth

import (
	"encoding/json"
	"fmt"
	"os"

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
}

// Store wraps credstore.Store with typed Credentials marshaling.
type Store struct {
	inner *credstore.Store
}

// NewStore creates a credential store.
func NewStore(fallbackDir string) *Store {
	s := credstore.NewStore(credstore.StoreOptions{
		ServiceName:   "basecamp",
		DisableEnvVar: "BASECAMP_NO_KEYRING",
		FallbackDir:   fallbackDir,
	})
	if w := s.FallbackWarning(); w != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	return &Store{inner: s}
}

// Load retrieves credentials for the given origin.
func (s *Store) Load(origin string) (*Credentials, error) {
	data, err := s.inner.Load(origin)
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
	data, err := json.Marshal(creds)
	if err != nil {
		return err
	}
	return s.inner.Save(origin, data)
}

// Delete removes credentials for the given origin.
func (s *Store) Delete(origin string) error { return s.inner.Delete(origin) }

// MigrateToKeyring migrates credentials from file to keyring.
func (s *Store) MigrateToKeyring() error { return s.inner.MigrateToKeyring() }

// UsingKeyring returns true if the store is using the system keyring.
func (s *Store) UsingKeyring() bool { return s.inner.UsingKeyring() }
