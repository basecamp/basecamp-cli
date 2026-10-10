package auth

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/cli/credstore"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// swapNewCredStore replaces the credstore constructor seam for the test.
func swapNewCredStore(t *testing.T, fn func(credstore.StoreOptions) credStore) {
	t.Helper()
	orig := newCredStore
	newCredStore = fn
	t.Cleanup(func() { newCredStore = orig })
}

// TestNewStoreIsLazy proves the constructor never touches credstore:
// credstore.NewStore probes the OS keyring with a write, and that probe can
// block forever on a locked headless keychain. This test pins constructor
// laziness only — the "credential-free commands never probe" conclusion
// additionally rests on those command paths (setup agents, skill install,
// version, help) not performing credential operations, which is audited at
// the command layer, not asserted here.
func TestNewStoreIsLazy(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1") // keep the delegated construction off the real keyring

	calls := 0
	swapNewCredStore(t, func(opts credstore.StoreOptions) credStore {
		calls++
		return credstore.NewStore(opts)
	})

	store := NewStore(t.TempDir())
	require.NotNil(t, store)
	assert.Equal(t, 0, calls, "NewStore must not construct the credstore")

	_, err := store.Load("https://test.example.com")
	require.Error(t, err, "no credentials saved")
	assert.Equal(t, 1, calls, "first operation constructs the credstore")

	store.UsingKeyring()
	assert.Equal(t, 1, calls, "construction happens once")
}

// swapSessionIsHeadless replaces the interactivity seam for the test.
func swapSessionIsHeadless(t *testing.T, headless bool) {
	t.Helper()
	orig := sessionIsHeadless
	sessionIsHeadless = func() bool { return headless }
	t.Cleanup(func() { sessionIsHeadless = orig })
}

// ensureOptions constructs a store's credstore through the seams and returns
// the options it was built with.
func ensureOptions(t *testing.T, headless bool) credstore.StoreOptions {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1") // keep the delegated construction off the real keyring

	var got credstore.StoreOptions
	swapNewCredStore(t, func(opts credstore.StoreOptions) credStore {
		got = opts
		return credstore.NewStore(opts)
	})
	swapSessionIsHeadless(t, headless)

	NewStore(t.TempDir()).UsingKeyring()
	return got
}

// Headless sessions can never answer a keychain unlock prompt, so the probe
// must be bounded there — the #568 incident class. Interactive sessions keep
// the unbounded probe so a legitimate unlock prompt is never cut off
// mid-answer (which would silently degrade to plaintext file storage).
func TestEnsureBoundsProbeOnlyWhenHeadless(t *testing.T) {
	assert.Equal(t, headlessProbeTimeout, ensureOptions(t, true).ProbeTimeout)
	assert.Zero(t, ensureOptions(t, false).ProbeTimeout)
}

// fallenBackStore stands in for a credstore.Store whose keyring probe failed
// and which is serving the plaintext file instead.
type fallenBackStore struct{ warning string }

func (f *fallenBackStore) Load(string) ([]byte, error) { return []byte(`{"access_token":"tok"}`), nil }
func (f *fallenBackStore) Save(string, []byte) error   { return nil }
func (f *fallenBackStore) Delete(string) error         { return nil }
func (f *fallenBackStore) MigrateToKeyring() error     { return nil }
func (f *fallenBackStore) UsingKeyring() bool          { return false }
func (f *fallenBackStore) FallbackWarning() string     { return f.warning }

// captureStderr returns what the callback wrote to os.Stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

// Regression: the fallback warning printed only on Save, so a process that
// merely read credentials — every command but login — could serve a stale
// credentials.json after a failed keyring probe without a word about it.
// The warning must show on the first read too, and still only once.
func TestLoadWarnsOnceWhenKeyringFellBack(t *testing.T) {
	warning := "system keyring unavailable (User interaction is not allowed.), credentials stored in plaintext at /tmp/credentials.json"
	swapNewCredStore(t, func(credstore.StoreOptions) credStore { return &fallenBackStore{warning: warning} })
	store := NewStore(t.TempDir())

	reads := captureStderr(t, func() {
		for range 2 {
			_, err := store.Load("profile:work")
			require.NoError(t, err)
		}
	})
	assert.Equal(t, 1, strings.Count(reads, "warning: "), "the first read warns, the second does not")
	assert.Contains(t, reads, "warning: "+warning+"\n")

	write := captureStderr(t, func() {
		require.NoError(t, store.Save("profile:work", &Credentials{AccessToken: "tok"}))
	})
	assert.Empty(t, write, "a write after the read has already warned stays quiet")
}

// refusedStore stands in for a credstore.Store whose keyring probe was
// refused by the OS — Codex's Windows sandbox answers Credential Manager
// with ERROR_ACCESS_DENIED — and which fell back to a credentials file that
// holds nothing, because the login went to the keyring. The error is shaped
// exactly as credstore's Load reports that miss.
type refusedStore struct{ credStore }

func (refusedStore) Load(key string) ([]byte, error) {
	return nil, fmt.Errorf("%w: system keyring unavailable (%w), fell back to %s",
		fmt.Errorf("credentials not found for %s", key), syscall.EACCES, "/home/u/.config/basecamp/credentials.json")
}
func (refusedStore) FallbackWarning() string { return "system keyring unavailable (permission denied)" }
func (refusedStore) UsingKeyring() bool      { return false }

// emptyFileStore is a file store with nothing in it and no keyring behind
// it: a plain "never logged in".
type emptyFileStore struct{ credStore }

func (emptyFileStore) Load(key string) ([]byte, error) {
	return nil, fmt.Errorf("credentials not found for %s", key)
}
func (emptyFileStore) FallbackWarning() string { return "" }
func (emptyFileStore) UsingKeyring() bool      { return false }

func managerWithStore(t *testing.T, inner credStore) *Manager {
	t.Helper()
	t.Setenv("BASECAMP_TOKEN", "")
	m := newDeviceTestManager(t, "https://3.basecampapi.com")
	m.store = &Store{fallbackDir: t.TempDir(), inner: inner}
	m.store.initOnce.Do(func() {})
	m.store.warnOnce.Do(func() {})
	return m
}

// Regression: inside an agent's sandbox the OS refuses the keyring, the
// store falls back to an empty file, and the miss was reported as "not
// logged in" with "Run: basecamp auth login" — a login the person already
// has, and one the sandbox would refuse in turn. The error must name the
// refusal and the remedy that never touches the store.
func TestAccessTokenWhenTheKeyringRefusesAccessNamesTheToken(t *testing.T) {
	m := managerWithStore(t, refusedStore{})

	_, err := m.AccessToken(context.Background())
	require.Error(t, err)

	e := output.AsError(err)
	assert.Equal(t, output.CodeAuth, e.Code)
	assert.Contains(t, e.Message, "refused access")
	assert.Contains(t, e.Message, syscall.EACCES.Error(), "the OS's own words stay in the message")
	assert.Equal(t, CredentialStoreRefusedHint, e.Hint)
	assert.NotContains(t, e.Hint, "auth login")
}

// A gate that acts on CheckAuthenticated must not read a refused store as
// "nothing stored" and send the person off to log in again.
func TestCheckAuthenticatedReportsARefusedStore(t *testing.T) {
	m := managerWithStore(t, refusedStore{})

	authenticated, err := m.CheckAuthenticated(context.Background())
	assert.False(t, authenticated)
	require.Error(t, err)
	assert.Equal(t, CredentialStoreRefusedHint, output.AsError(err).Hint)
}

// The refusal is the only miss that changes remedy: a store that simply
// holds nothing still says to log in.
func TestAccessTokenWithNothingStoredStillSaysLogin(t *testing.T) {
	m := managerWithStore(t, emptyFileStore{})

	_, err := m.AccessToken(context.Background())
	require.Error(t, err)
	assert.Equal(t, "Run: basecamp auth login", output.AsError(err).Hint)

	authenticated, err := m.CheckAuthenticated(context.Background())
	assert.False(t, authenticated)
	assert.NoError(t, err)
}
