package auth

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/cli/credstore"
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

// Linux must bound a D-Bus stall independently of GUI/TTY availability.
// Other platforms retain the headless-only probe bound and do not opt in
// to operation timeouts, so their interactive unlock prompts are unchanged.
func TestEnsureBoundsLinuxKeyringAndHeadlessProbes(t *testing.T) {
	headless := ensureOptions(t, true)
	interactive := ensureOptions(t, false)
	assert.Equal(t, headlessProbeTimeout, headless.ProbeTimeout)
	if runtime.GOOS == "linux" {
		assert.Equal(t, headlessProbeTimeout, interactive.ProbeTimeout)
		assert.Equal(t, headlessProbeTimeout, headless.OperationTimeout)
		assert.Equal(t, headlessProbeTimeout, interactive.OperationTimeout)
	} else {
		assert.Zero(t, interactive.ProbeTimeout)
		assert.Zero(t, headless.OperationTimeout)
		assert.Zero(t, interactive.OperationTimeout)
	}
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

type timedOutKeyringStore struct{ fallenBackStore }

func (*timedOutKeyringStore) Load(string) ([]byte, error) { return nil, keyringTimeoutForTest() }
func (*timedOutKeyringStore) Save(string, []byte) error   { return keyringTimeoutForTest() }
func (*timedOutKeyringStore) Delete(string) error         { return keyringTimeoutForTest() }
func (*timedOutKeyringStore) MigrateToKeyring() error     { return keyringTimeoutForTest() }
func (*timedOutKeyringStore) UsingKeyring() bool          { return true }

func keyringTimeoutForTest() error {
	return fmt.Errorf("keyring operation timed out: %w", context.DeadlineExceeded)
}

func TestKeyringTimeoutIsAnErrorWithAnExplicitFileStorageRemedy(t *testing.T) {
	swapNewCredStore(t, func(credstore.StoreOptions) credStore { return &timedOutKeyringStore{} })
	store := NewStore(t.TempDir())
	load := func() error { _, err := store.Load("work"); return err }
	save := func() error { return store.Save("work", &Credentials{AccessToken: "token"}) }
	del := func() error { return store.Delete("work") }
	for _, operation := range []func() error{load, save, del, store.MigrateToKeyring} {
		err := operation()
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.NotErrorIs(t, err, ErrNoCredential)
		assert.ErrorContains(t, err, "BASECAMP_NO_KEYRING=1")
		assert.ErrorContains(t, err, "plaintext")
	}
	assert.True(t, store.UsingKeyring())
	assert.ErrorIs(t, keyringOperationError(context.DeadlineExceeded), context.DeadlineExceeded)
	assert.NotContains(t, keyringOperationError(context.DeadlineExceeded).Error(), "BASECAMP_NO_KEYRING")
}

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
