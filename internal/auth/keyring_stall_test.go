package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"

	"github.com/basecamp/cli/credstore"

	"github.com/basecamp/basecamp-cli/internal/config"
)

func readTimeout() error {
	return fmt.Errorf("keyring read timed out after 10s (the operation may have completed or may still complete): %w", context.DeadlineExceeded)
}

func writeTimeout() error {
	return fmt.Errorf("keyring write timed out after 10s (the operation may have completed or may still complete): %w", context.DeadlineExceeded)
}

// stallingKeyring stands in for a credstore.Store over the machine's
// keyring. Like credstore, once a call times out it refuses every later
// call for the life of the store.
type stallingKeyring struct {
	m        *keyringMachine
	mu       sync.Mutex
	latched  error
	probeErr error
}

func (k *stallingKeyring) refused() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.latched != nil {
		return fmt.Errorf("keyring unavailable after an earlier timeout: %w", k.latched)
	}
	return nil
}

func (k *stallingKeyring) latch(err error) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.latched = err
	return err
}

func (k *stallingKeyring) Load(key string) ([]byte, error) {
	if err := k.refused(); err != nil {
		return nil, err
	}
	k.m.mu.Lock()
	stalled, down, vanish := k.m.stalled, k.m.down, k.m.vanishReads
	if vanish > 0 {
		k.m.vanishReads--
	}
	data, ok := k.m.items[key]
	k.m.mu.Unlock()
	switch {
	case stalled:
		return nil, k.latch(readTimeout())
	case down:
		return nil, fmt.Errorf("credentials not found: %w", dbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown", Body: []any{"The name org.freedesktop.secrets was not provided by any .service files"}})
	case !ok || vanish > 0:
		return nil, fmt.Errorf("credentials not found: %w", keyring.ErrNotFound)
	}
	return data, nil
}

func (k *stallingKeyring) Save(key string, data []byte) error {
	if err := k.refused(); err != nil {
		return err
	}
	k.m.mu.Lock()
	stallWrites := k.m.stallWrites
	if !stallWrites {
		k.m.items[key] = data
	}
	k.m.mu.Unlock()
	if stallWrites {
		return k.latch(writeTimeout())
	}
	return nil
}

func (k *stallingKeyring) Delete(key string) error {
	if err := k.refused(); err != nil {
		return err
	}
	k.m.mu.Lock()
	delete(k.m.items, key)
	k.m.mu.Unlock()
	return nil
}

func (k *stallingKeyring) MigrateToKeyring() error { return nil }
func (k *stallingKeyring) UsingKeyring() bool      { return k.probeErr == nil }
func (k *stallingKeyring) FallbackWarning() string { return "" }

// keyringMachine is the OS keyring every store a test builds talks to.
type keyringMachine struct {
	mu          sync.Mutex
	items       map[string][]byte
	stalled     bool  // reads time out
	stallWrites bool  // writes time out
	down        bool  // the Secret Service is not on the bus
	vanishReads int   // the next n reads find nothing, as a restarting service can
	probeErr    error // what a fresh store's probe finds; nil is the keyring
	built       []*stallingKeyring
}

func (m *keyringMachine) newStore(credstore.StoreOptions) credStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := &stallingKeyring{m: m, probeErr: m.probeErr}
	m.built = append(m.built, k)
	return k
}

func (m *keyringMachine) set(fn func(m *keyringMachine)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(m)
}

func (m *keyringMachine) stores() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.built)
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func stallingStore(t *testing.T) (*Store, *keyringMachine, *fakeClock) {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "")
	machine := &keyringMachine{items: map[string][]byte{}}
	swapNewCredStore(t, machine.newStore)
	orig := keyringRestartGrace
	keyringRestartGrace = time.Millisecond
	t.Cleanup(func() { keyringRestartGrace = orig })
	clock := &fakeClock{t: time.Date(2026, 11, 6, 18, 15, 0, 0, time.UTC)}
	store := NewStore(t.TempDir())
	store.stall.now = clock.now
	return store, machine, clock
}

func saveCreds(t *testing.T, machine *keyringMachine, key string, creds *Credentials) {
	t.Helper()
	data, err := json.Marshal(creds)
	require.NoError(t, err)
	machine.set(func(m *keyringMachine) { m.items[key] = data })
}

func stallingManager(t *testing.T, store *Store) *Manager {
	t.Helper()
	t.Setenv("BASECAMP_TOKEN", "")
	mgr := NewManager(&config.Config{ActiveProfile: "agent", BaseURL: "https://3.basecampapi.com"}, nil)
	mgr.SetStore(store)
	return mgr
}

func liveCreds(token string) *Credentials {
	return &Credentials{AccessToken: token, OAuthType: "agent", ExpiresAt: time.Now().Add(time.Hour).Unix()}
}

// One read that outlives its bound must not cost a long-running process the
// keyring for good. Once the keyring answers again, a later read — after the
// backoff — reaches it through a fresh store.
func TestAKeyringReadTimeoutIsRetriedAfterABackoff(t *testing.T) {
	store, machine, clock := stallingStore(t)
	saveCreds(t, machine, "profile:agent", liveCreds("token"))

	machine.set(func(m *keyringMachine) { m.stalled = true })
	_, err := store.Load("profile:agent")
	require.Error(t, err)
	assert.True(t, KeyringUnavailable(err), "%v", err)

	machine.set(func(m *keyringMachine) { m.stalled = false })
	_, err = store.Load("profile:agent")
	require.Error(t, err, "inside the backoff the stalled store still refuses: nothing is retried at once")
	assert.Equal(t, 1, machine.stores())

	clock.advance(readStallRetryBase)
	creds, err := store.Load("profile:agent")
	require.NoError(t, err, "past the backoff a fresh store reads the keyring again")
	assert.Equal(t, "token", creds.AccessToken)
	assert.Equal(t, 2, machine.stores())

	_, err = store.Load("profile:agent")
	require.NoError(t, err)
	assert.Equal(t, 2, machine.stores(), "a store that reads keeps being used")
}

// While the keyring stays silent, retries back off to the cap, and each
// capped wait brings exactly one.
func TestKeyringReadRetriesBackOff(t *testing.T) {
	store, machine, clock := stallingStore(t)
	saveCreds(t, machine, "profile:agent", liveCreds("token"))
	machine.set(func(m *keyringMachine) { m.stalled = true })

	_, _ = store.Load("profile:agent")
	clock.advance(readStallRetryBase)
	_, _ = store.Load("profile:agent") // store 2, which times out too
	require.Equal(t, 2, machine.stores())

	clock.advance(readStallRetryBase)
	_, _ = store.Load("profile:agent")
	assert.Equal(t, 2, machine.stores(), "the second wait is longer than the first")

	clock.advance(readStallRetryBase)
	_, _ = store.Load("profile:agent")
	assert.Equal(t, 3, machine.stores())

	for range 20 {
		clock.advance(readStallRetryCap)
		_, _ = store.Load("profile:agent")
	}
	assert.Equal(t, 23, machine.stores(), "one retry per capped wait, no more and no fewer")
}

// A fresh store whose probe could not reach the keyring has fallen back to
// file storage, which would serve credentials.json — possibly nothing,
// possibly stale — in place of the keyring (#664, #805). It is discarded.
func TestAKeyringRetryNeverFallsBackToFileStorage(t *testing.T) {
	store, machine, clock := stallingStore(t)
	saveCreds(t, machine, "profile:agent", liveCreds("token"))
	machine.set(func(m *keyringMachine) { m.stalled = true })
	_, _ = store.Load("profile:agent")

	machine.set(func(m *keyringMachine) { m.probeErr = errors.New("keyring probe timed out after 10s") })
	clock.advance(readStallRetryBase)
	_, err := store.Load("profile:agent")
	require.Error(t, err)
	assert.True(t, KeyringUnavailable(err), "still the keyring's refusal, not a file backend's miss: %v", err)
	assert.True(t, store.UsingKeyring())
}

// A timed-out WRITE keeps credstore's latch: its outcome is unknown, and a
// retry could overlap it.
func TestAKeyringWriteTimeoutIsNotRetried(t *testing.T) {
	store, machine, clock := stallingStore(t)
	machine.set(func(m *keyringMachine) { m.stallWrites = true })
	require.Error(t, store.Save("profile:agent", liveCreds("token")))

	_, err := store.Load("profile:agent")
	require.Error(t, err)
	clock.advance(time.Hour)
	_, err = store.Load("profile:agent")
	require.Error(t, err)
	assert.Equal(t, 1, machine.stores(), "no fresh store after a write of unknown outcome")
	assert.Nil(t, store.heldCredential("profile:agent"), "nor is the unknown write served from memory")
}

// A timeout reported by a store that has since been replaced says nothing
// about its replacement: it must not mark the new store stalled, or a later
// retry could replace it past a write of unknown outcome on it.
func TestALateTimeoutFromAReplacedStoreIsIgnored(t *testing.T) {
	store, machine, clock := stallingStore(t)
	saveCreds(t, machine, "profile:agent", liveCreds("token"))
	machine.set(func(m *keyringMachine) { m.stalled = true })
	_, _ = store.Load("profile:agent")
	old := store.current()

	machine.set(func(m *keyringMachine) { m.stalled = false })
	clock.advance(readStallRetryBase)
	_, err := store.Load("profile:agent")
	require.NoError(t, err)
	require.Equal(t, 2, machine.stores())

	store.noteRead(old, readTimeout())
	machine.set(func(m *keyringMachine) { m.stallWrites = true })
	require.Error(t, store.Save("profile:agent", liveCreds("token2")))
	clock.advance(time.Hour)
	_, _ = store.Load("profile:agent")
	assert.Equal(t, 2, machine.stores())
}

// The Manager keeps serving the token it last had while the keyring is not
// answering — timing out, or not on the bus at all — as long as the token is
// not due for renewal.
func TestTheHeldTokenIsServedWhileTheKeyringStalls(t *testing.T) {
	for name, fail := range map[string]func(m *keyringMachine){
		"timeout":             func(m *keyringMachine) { m.stalled = true },
		"secret service gone": func(m *keyringMachine) { m.down = true },
	} {
		t.Run(name, func(t *testing.T) {
			store, machine, _ := stallingStore(t)
			saveCreds(t, machine, "profile:agent", liveCreds("held-token"))
			mgr := stallingManager(t, store)

			token, err := mgr.AccessToken(t.Context())
			require.NoError(t, err)
			require.Equal(t, "held-token", token)

			machine.set(fail)
			for range 3 {
				token, err = mgr.AccessToken(t.Context())
				require.NoError(t, err, "the keyring's silence is not the token's")
				assert.Equal(t, "held-token", token)
			}
		})
	}
}

// A Secret Service that is restarting can report a credential it gave this
// process a moment ago as missing. The read asks again before believing it;
// a credential that is really gone is still gone, and is forgotten.
func TestACredentialThatVanishesIsAskedForAgain(t *testing.T) {
	store, machine, _ := stallingStore(t)
	saveCreds(t, machine, "profile:agent", liveCreds("token"))
	_, err := store.Load("profile:agent")
	require.NoError(t, err)

	machine.set(func(m *keyringMachine) { m.vanishReads = 1 })
	creds, err := store.Load("profile:agent")
	require.NoError(t, err, "one empty answer from a restarting keyring is asked again")
	assert.Equal(t, "token", creds.AccessToken)

	machine.set(func(m *keyringMachine) { delete(m.items, "profile:agent") })
	_, err = store.Load("profile:agent")
	require.ErrorIs(t, err, ErrNoCredential)
	assert.Nil(t, store.heldCredential("profile:agent"))
}

// Nothing held, a token due for renewal, a canceled caller, a store that
// answered, or a file backend: none of them is served from memory.
func TestTheHeldTokenIsServedOnlyForAKeyringStall(t *testing.T) {
	t.Run("nothing held", func(t *testing.T) {
		store, machine, _ := stallingStore(t)
		saveCreds(t, machine, "profile:agent", liveCreds("t"))
		mgr := stallingManager(t, store)
		machine.set(func(m *keyringMachine) { m.stalled = true })
		_, err := mgr.AccessToken(t.Context())
		require.Error(t, err)
		assert.True(t, KeyringUnavailable(err), "%v", err)
	})

	t.Run("due for renewal", func(t *testing.T) {
		store, machine, _ := stallingStore(t)
		saveCreds(t, machine, "profile:agent", &Credentials{AccessToken: "t", OAuthType: "agent", ExpiresAt: time.Now().Add(RefreshWindow + time.Minute).Unix()})
		mgr := stallingManager(t, store)
		_, err := mgr.AccessToken(t.Context())
		require.NoError(t, err)
		store.keep("profile:agent", &Credentials{AccessToken: "t", OAuthType: "agent", ExpiresAt: time.Now().Add(time.Minute).Unix()})
		machine.set(func(m *keyringMachine) { m.stalled = true })
		_, err = mgr.AccessToken(t.Context())
		require.Error(t, err)
	})

	t.Run("canceled caller", func(t *testing.T) {
		store, machine, _ := stallingStore(t)
		saveCreds(t, machine, "profile:agent", liveCreds("t"))
		mgr := stallingManager(t, store)
		_, err := mgr.AccessToken(t.Context())
		require.NoError(t, err)
		machine.set(func(m *keyringMachine) { m.stalled = true })
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err = mgr.AccessToken(ctx)
		require.ErrorIs(t, err, context.Canceled)
		assert.Nil(t, mgr.heldWhileKeyringStalls(ctx, "profile:agent", &keyringUnavailableError{readTimeout()}), "a canceled caller is never served")
	})

	t.Run("missing, then a stall", func(t *testing.T) {
		store, machine, _ := stallingStore(t)
		saveCreds(t, machine, "profile:agent", liveCreds("t"))
		mgr := stallingManager(t, store)
		_, err := mgr.AccessToken(t.Context())
		require.NoError(t, err)
		machine.set(func(m *keyringMachine) { delete(m.items, "profile:agent") })
		_, err = mgr.AccessToken(t.Context())
		require.Error(t, err, "a credential that is gone is gone")
		assert.False(t, KeyringUnavailable(err), "%v", err)

		machine.set(func(m *keyringMachine) { m.stalled = true })
		_, err = mgr.AccessToken(t.Context())
		require.Error(t, err, "and is not served from memory by a later stall")
	})

	t.Run("file backend after a timed-out probe", func(t *testing.T) {
		probeTimeout := fmt.Errorf("keyring probe timed out after 10s: %w", context.DeadlineExceeded)
		swapNewCredStore(t, func(credstore.StoreOptions) credStore {
			return &probeFailedStore{err: fmt.Errorf("credentials not found for profile:agent: system keyring unavailable (%w)", probeTimeout)}
		})
		t.Setenv("BASECAMP_NO_KEYRING", "")
		store := NewStore(t.TempDir())
		_, err := store.Load("profile:agent")
		require.Error(t, err)
		assert.False(t, KeyringUnavailable(err), "a file backend's miss is not a keyring outage: %v", err)
	})
}

// probeFailedStore is a store on the file backend after a failed probe.
type probeFailedStore struct{ err error }

func (f *probeFailedStore) Load(string) ([]byte, error) { return nil, f.err }
func (f *probeFailedStore) Save(string, []byte) error   { return nil }
func (f *probeFailedStore) Delete(string) error         { return nil }
func (f *probeFailedStore) MigrateToKeyring() error     { return nil }
func (f *probeFailedStore) UsingKeyring() bool          { return false }
func (f *probeFailedStore) FallbackWarning() string     { return "" }
