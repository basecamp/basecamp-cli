package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/basecamp/cli/credstore"
	"github.com/charmbracelet/x/term"
)

// Credentials holds OAuth tokens and metadata.
type Credentials struct {
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token"`
	ExpiresAt     int64  `json:"expires_at"`
	Scope         string `json:"scope"`
	OAuthType     string `json:"oauth_type"` // "bc5", "launchpad", or legacy "bc3"
	TokenEndpoint string `json:"token_endpoint"`
	UserID        string `json:"user_id,omitempty"`
	UserEmail     string `json:"user_email,omitempty"`

	// Source records how the credential was obtained when that is not the
	// OAuth flow: "token" for an imported personal access token, which has
	// no refresh material. Empty means an OAuth login.
	Source string `json:"source,omitempty"`

	// Resource is the RFC 8707 resource indicator the tokens are bound to
	// (BC5: urn:bc:account:<id>). BC5 device logins as the trusted
	// basecamp-cli client mint MULTI-ACCOUNT refresh tokens, and the refresh
	// grant rejects them without this echo — refresh sends it when set and
	// preserves it when a refresh response omits it.
	Resource string `json:"resource,omitempty"`
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
	inner       credStore
	warnOnce    sync.Once
}

// credStore is the slice of credstore.Store this wrapper uses, as an
// interface so tests can substitute a store that fell back to file storage
// without failing a real keyring probe.
type credStore interface {
	Load(key string) ([]byte, error)
	Save(key string, data []byte) error
	Delete(key string) error
	MigrateToKeyring() error
	UsingKeyring() bool
	FallbackWarning() string
}

// newCredStore is replaceable in tests to avoid real keyring access.
var newCredStore = func(opts credstore.StoreOptions) credStore { return credstore.NewStore(opts) }

// sessionIsHeadless reports that no human can answer a keyring unlock
// prompt: stdin, stdout, and stderr are all non-terminals AND no GUI
// session is available. Any attached stream counts as interactive —
// redirecting a stream or two (`2>auth.log`, `| jq`) is routine interactive
// use — and a GUI session (an app or IDE task runner launching the CLI with
// all streams detached) can still present the unlock dialog. Replaceable in
// tests.
var sessionIsHeadless = func() bool {
	return !term.IsTerminal(os.Stdin.Fd()) &&
		!term.IsTerminal(os.Stdout.Fd()) &&
		!term.IsTerminal(os.Stderr.Fd()) &&
		!guiSessionAvailable()
}

// headlessProbeTimeout bounds the keyring availability probe when no
// terminal is attached to any standard stream. A headless session (CI,
// piped installers, ssh without a TTY) can never answer a keychain unlock
// prompt, so an unavailable keyring must fall back to file storage instead
// of hanging forever in an uncancellable `security` child — the #568
// incident class. Interactive sessions keep the unbounded probe: a locked
// keychain there raises an unlock prompt, and cutting it off mid-answer
// would silently degrade the user to plaintext file storage.
const headlessProbeTimeout = 10 * time.Second

// NewStore creates a credential store. The OS keyring is not touched until
// the first credential operation.
func NewStore(fallbackDir string) *Store {
	return &Store{fallbackDir: fallbackDir}
}

// ensure constructs the underlying store on first use. Callers reach here
// from paths that don't hold Manager.mu (e.g. IsAuthenticated), so the
// sync.Once provides the synchronization.
func (s *Store) ensure() credStore {
	s.initOnce.Do(func() {
		opts := credstore.StoreOptions{
			ServiceName:   "basecamp",
			DisableEnvVar: "BASECAMP_NO_KEYRING",
			FallbackDir:   s.fallbackDir,
		}
		if sessionIsHeadless() {
			opts.ProbeTimeout = headlessProbeTimeout
		}
		s.inner = newCredStore(opts)
	})
	return s.inner
}

// warnFallback prints the keyring fallback warning once per process, on the
// first credential read or write. Reads warn too: a fallback read returns
// whatever an earlier fallback left in credentials.json — possibly months
// stale — rather than the credentials the keyring holds, and the probe
// failure behind it would otherwise stay invisible until the next login.
// Hosts that mean to use file storage set BASECAMP_NO_KEYRING, which skips
// the probe and so never warns.
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
	s.warnFallback()
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
