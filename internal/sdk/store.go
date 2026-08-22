package sdk

// Credentials holds OAuth tokens and metadata.
type Credentials struct {
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token,omitempty"`
	ExpiresAt     int64  `json:"expires_at,omitempty"`
	TokenEndpoint string `json:"token_endpoint,omitempty"`
	OAuthType     string `json:"oauth_type,omitempty"` // "bc5", "launchpad", or legacy "bc3"
	Scope         string `json:"scope,omitempty"`
	UserID        string `json:"user_id,omitempty"`

	// Resource is the RFC 8707 resource indicator the tokens are bound to
	// (BC5: urn:bc:account:<id>), echoed on refresh — multi-account refresh
	// tokens are rejected without it. Mirrors internal/auth.Credentials.
	Resource string `json:"resource,omitempty"`
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
