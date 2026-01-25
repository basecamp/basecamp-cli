package sdk

// Credentials holds OAuth tokens and metadata.
type Credentials struct {
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token,omitempty"`
	ExpiresAt     int64  `json:"expires_at,omitempty"`
	TokenEndpoint string `json:"token_endpoint,omitempty"`
	OAuthType     string `json:"oauth_type,omitempty"` // "bc3" or "launchpad"
	Scope         string `json:"scope,omitempty"`
	UserID        string `json:"user_id,omitempty"`
}

// CredentialStore provides persistent storage for OAuth credentials.
// Implementations can use keychain, file storage, or other backends.
type CredentialStore interface {
	// Load retrieves credentials for the given origin (e.g., "https://3.basecampapi.com").
	Load(origin string) (*Credentials, error)

	// Save stores credentials for the given origin.
	Save(origin string, creds *Credentials) error

	// Delete removes credentials for the given origin.
	Delete(origin string) error
}

// StoreError indicates a credential storage error.
type StoreError struct {
	Operation string // "load", "save", "delete"
	Origin    string
	Message   string
	Cause     error
}

func (e *StoreError) Error() string {
	msg := e.Operation + " credentials"
	if e.Origin != "" {
		msg += " for " + e.Origin
	}
	if e.Message != "" {
		msg += ": " + e.Message
	} else if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}

func (e *StoreError) Unwrap() error {
	return e.Cause
}
