package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"

	"github.com/basecamp/cli/credstore"

	"github.com/basecamp/basecamp-cli/internal/config"
)

// stallingKeyring stands in for a credstore.Store on a keyring that stops
// answering: once stalled, its first read reports credstore's read timeout
// and every later call the refusal credstore gives after one, for the life
// of the store, exactly as credstore does.
type stallingKeyring struct {
	mu       sync.Mutex
	items    map[string][]byte
	stall    bool  // the next read times out
	latched  error // set once a call timed out; every call is refused
	probeErr error // what a fresh store's probe found; nil means the keyring
	reads    int
}

func (k *stallingKeyring) Load(key string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.reads++
	if k.latched != nil {
		return nil, fmt.Errorf("keyring unavailable after an earlier timeout: %w", k.latched)
	}
	if k.stall {
		k.latched = fmt.Errorf("keyring read timed out after 10s (the operation may have completed or may still complete): %w", context.DeadlineExceeded)
		return nil, k.latched
	}
	data, ok := k.items[key]
	if !ok {
		return nil, fmt.Errorf("credentials not found: %w", keyring.ErrNotFound)
	}
	return data, nil
}

func (k *stallingKeyring) Save(key string, data []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.latched != nil {
		return fmt.Errorf("keyring unavailable after an earlier timeout: %w", k.latched)
	}
	k.items[key] = data
	return nil
}

func (k *stallingKeyring) Delete(key string) error { return nil }
func (k *stallingKeyring) MigrateToKeyring() error { return nil }
func (k *stallingKeyring) UsingKeyring() bool      { return k.probeErr == nil }
func (k *stallingKeyring) FallbackWarning() string { return "" }

// keyringMachine is the OS keyring every store a test builds talks to: one
// set of items, and whether it answers right now.
type keyringMachine struct {
	mu       sync.Mutex
	items    map[string][]byte
	stalled  bool
	built    []*stallingKeyring
	probeErr error
}

func (m *keyringMachine) newStore(credstore.StoreOptions) credStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := &stallingKeyring{items: m.items, stall: m.stalled, probeErr: m.probeErr}
	m.built = append(m.built, k)
	return k
}

// setStalled makes the keyring stop answering (or answer again) for every
// store, the ones already built included.
func (m *keyringMachine) setStalled(stalled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stalled = stalled
	for _, k := range m.built {
		k.mu.Lock()
		k.stall = stalled
		k.mu.Unlock()
	}
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
	clock := &fakeClock{t: time.Date(2026, 11, 6, 18, 15, 0, 0, time.UTC)}
	store := NewStore(t.TempDir())
	store.now = clock.now
	return store, machine, clock
}

func saveCreds(t *testing.T, machine *keyringMachine, key string, creds *Credentials) {
	t.Helper()
	data, err := json.Marshal(creds)
	require.NoError(t, err)
	machine.mu.Lock()
	machine.items[key] = data
	machine.mu.Unlock()
}

// One read that outlives its bound must not cost a long-running process the
// keyring for good. Once the keyring answers again, a later read — after the
// backoff — reaches it through a fresh store.
func TestAKeyringReadTimeoutIsRetriedAfterABackoff(t *testing.T) {
	store, machine, clock := stallingStore(t)
	saveCreds(t, machine, "profile:agent", &Credentials{AccessToken: "token"})

	machine.setStalled(true)
	_, err := store.Load("profile:agent")
	require.Error(t, err)
	assert.True(t, KeyringUnavailable(err), "%v", err)

	machine.setStalled(false)
	_, err = store.Load("profile:agent")
	require.Error(t, err, "inside the backoff the stalled store still refuses: nothing is retried at once")
	assert.Equal(t, 1, machine.stores())

	clock.advance(readStallRetryBase)
	creds, err := store.Load("profile:agent")
	require.NoError(t, err, "past the backoff a fresh store reads the keyring again")
	assert.Equal(t, "token", creds.AccessToken)
	assert.Equal(t, 2, machine.stores())

	// Recovered: a read that succeeds keeps the store it came from.
	_, err = store.Load("profile:agent")
	require.NoError(t, err)
	assert.Equal(t, 2, machine.stores())
}

// While the keyring stays silent, the retries back off rather than build a
// store, and probe the keyring, on every read.
func TestKeyringReadRetriesBackOff(t *testing.T) {
	store, machine, clock := stallingStore(t)
	saveCreds(t, machine, "profile:agent", &Credentials{AccessToken: "token"})
	machine.setStalled(true)

	_, _ = store.Load("profile:agent")
	clock.advance(readStallRetryBase)
	_, _ = store.Load("profile:agent") // builds store 2, which times out too
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
	assert.LessOrEqual(t, machine.stores(), 23, "never more than one retry per capped wait")
}

// A fresh store whose probe could not reach the keyring has fallen back to
// file storage, which would serve credentials.json — possibly nothing,
// possibly stale — in place of the keyring. It is discarded.
func TestAKeyringRetryNeverFallsBackToFileStorage(t *testing.T) {
	store, machine, clock := stallingStore(t)
	saveCreds(t, machine, "profile:agent", &Credentials{AccessToken: "token"})
	machine.setStalled(true)
	_, _ = store.Load("profile:agent")

	machine.mu.Lock()
	machine.probeErr = errors.New("keyring probe timed out after 10s")
	machine.mu.Unlock()
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
	store.UsingKeyring() // build the store
	machine.mu.Lock()
	machine.built[0].latched = fmt.Errorf("keyring write timed out after 10s (the operation may have completed or may still complete): %w", context.DeadlineExceeded)
	machine.mu.Unlock()

	_, err := store.Load("profile:agent")
	require.Error(t, err)
	clock.advance(time.Hour)
	_, err = store.Load("profile:agent")
	require.Error(t, err)
	assert.Equal(t, 1, machine.stores())
}

// The Manager keeps serving the token it last read while the keyring is not
// answering, as long as that token is not due for renewal.
func TestTheHeldTokenIsServedWhileTheKeyringStalls(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "")
	store, machine, _ := stallingStore(t)
	saveCreds(t, machine, "profile:agent", &Credentials{AccessToken: "held-token", OAuthType: "agent", ExpiresAt: time.Now().Add(time.Hour).Unix()})

	mgr := NewManager(&config.Config{ActiveProfile: "agent", BaseURL: "https://3.basecampapi.com"}, nil)
	mgr.SetStore(store)

	token, err := mgr.AccessToken(t.Context())
	require.NoError(t, err)
	require.Equal(t, "held-token", token)

	machine.setStalled(true)
	for range 3 {
		token, err = mgr.AccessToken(t.Context())
		require.NoError(t, err, "the keyring's silence is not the token's")
		assert.Equal(t, "held-token", token)
	}
}

// Nothing held, a token due for renewal, a canceled caller, or a store that
// actually answered: none of them is served from memory.
func TestTheHeldTokenIsServedOnlyForAKeyringStall(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "")

	t.Run("nothing held", func(t *testing.T) {
		store, machine, _ := stallingStore(t)
		saveCreds(t, machine, "profile:agent", &Credentials{AccessToken: "t", ExpiresAt: time.Now().Add(time.Hour).Unix()})
		mgr := NewManager(&config.Config{ActiveProfile: "agent"}, nil)
		mgr.SetStore(store)
		machine.setStalled(true)
		_, err := mgr.AccessToken(t.Context())
		require.Error(t, err)
		assert.True(t, KeyringUnavailable(err), "%v", err)
	})

	t.Run("due for renewal", func(t *testing.T) {
		store, machine, _ := stallingStore(t)
		// Inside the refresh window but not expired: served while the
		// keyring answers, never from memory once it does not.
		saveCreds(t, machine, "profile:agent", &Credentials{AccessToken: "t", ExpiresAt: time.Now().Add(RefreshWindow + time.Minute).Unix()})
		mgr := NewManager(&config.Config{ActiveProfile: "agent"}, nil)
		mgr.SetStore(store)
		_, err := mgr.AccessToken(t.Context())
		require.NoError(t, err)
		mgr.mu.Lock()
		mgr.held.ExpiresAt = time.Now().Add(time.Minute).Unix()
		mgr.mu.Unlock()
		machine.setStalled(true)
		_, err = mgr.AccessToken(t.Context())
		require.Error(t, err)
	})

	t.Run("canceled caller", func(t *testing.T) {
		store, machine, _ := stallingStore(t)
		saveCreds(t, machine, "profile:agent", &Credentials{AccessToken: "t", ExpiresAt: time.Now().Add(time.Hour).Unix()})
		mgr := NewManager(&config.Config{ActiveProfile: "agent"}, nil)
		mgr.SetStore(store)
		_, err := mgr.AccessToken(t.Context())
		require.NoError(t, err)
		machine.setStalled(true)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err = mgr.AccessToken(ctx)
		require.Error(t, err)
	})

	t.Run("the store answered", func(t *testing.T) {
		store, machine, _ := stallingStore(t)
		saveCreds(t, machine, "profile:agent", &Credentials{AccessToken: "t", ExpiresAt: time.Now().Add(time.Hour).Unix()})
		mgr := NewManager(&config.Config{ActiveProfile: "agent"}, nil)
		mgr.SetStore(store)
		_, err := mgr.AccessToken(t.Context())
		require.NoError(t, err)
		machine.mu.Lock()
		delete(machine.items, "profile:agent")
		machine.mu.Unlock()
		_, err = mgr.AccessToken(t.Context())
		require.Error(t, err, "a credential that is gone is gone, whatever is held")
		assert.False(t, KeyringUnavailable(err), "%v", err)
	})
}
