package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"

	"github.com/basecamp/cli/credstore"

	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
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
	if k.probeErr != nil {
		// Fell back to credentials.json: a miss there is what credstore's
		// file backend says after a failed probe.
		k.m.mu.Lock()
		data, ok := k.m.file[key]
		fileErr := k.m.fileErr
		k.m.mu.Unlock()
		if fileErr != nil {
			return nil, fileErr
		}
		if ok {
			return data, nil
		}
		return nil, fmt.Errorf("credentials not found for %s: system keyring unavailable (%w), fell back to credentials.json", key, k.probeErr)
	}
	if err := k.refused(); err != nil {
		return nil, err
	}
	k.m.mu.Lock()
	stalled, down, vanish, readErr, onRead := k.m.stalled, k.m.down, k.m.vanishReads, k.m.readErr, k.m.onRead
	if vanish > 0 {
		k.m.vanishReads--
	}
	k.m.reads++
	data, ok := k.m.items[key]
	k.m.mu.Unlock()
	if onRead != nil {
		onRead()
	}
	switch {
	case readErr != nil:
		return nil, fmt.Errorf("credentials not found: %w", readErr)
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
	if k.probeErr != nil {
		k.m.mu.Lock()
		defer k.m.mu.Unlock()
		if k.m.file == nil {
			k.m.file = map[string][]byte{}
		}
		k.m.file[key] = data
		return nil
	}
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
	if k.probeErr != nil {
		k.m.mu.Lock()
		defer k.m.mu.Unlock()
		delete(k.m.file, key)
		return nil
	}
	if err := k.refused(); err != nil {
		return err
	}
	k.m.mu.Lock()
	if _, ok := k.m.items[key]; !ok {
		k.m.mu.Unlock()
		return fmt.Errorf("credentials not found: %w", keyring.ErrNotFound)
	}
	delete(k.m.items, key)
	k.m.mu.Unlock()
	return nil
}

func (k *stallingKeyring) MigrateToKeyring() error { return nil }
func (k *stallingKeyring) UsingKeyring() bool      { return k.probeErr == nil }
func (k *stallingKeyring) FallbackWarning() string { return "" }
func (k *stallingKeyring) ProbeError() error       { return k.probeErr }

// keyringMachine is the OS keyring every store a test builds talks to.
type keyringMachine struct {
	mu          sync.Mutex
	items       map[string][]byte
	stalled     bool          // reads time out
	stallWrites bool          // writes time out
	down        bool          // the Secret Service is not on the bus
	vanishReads int           // the next n reads find nothing, as a restarting service can
	probeErr    error         // what a fresh store's probe finds; nil is the keyring
	probeNeeds  time.Duration // a probe bounded shorter than this times out
	probeBounds []time.Duration
	directReads int               // reads that went to the keyring without a store
	file        map[string][]byte // credentials.json, as a fallen-back store sees it
	directBlock chan struct{}     // a direct read waits for this, when set
	fileErr     error             // what reading credentials.json fails with, when set
	readErr     error             // what every read fails with, when set
	onRead      func()            // runs inside each read, after the item is looked up
	onBuild     func()            // runs as each store is built, after the first
	reads       int
	built       []*stallingKeyring
}

func (m *keyringMachine) newStore(opts credstore.StoreOptions) credStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := &stallingKeyring{m: m, probeErr: m.probeErr}
	m.probeBounds = append(m.probeBounds, opts.ProbeTimeout)
	if k.probeErr == nil && m.probeNeeds > 0 && opts.ProbeTimeout < m.probeNeeds {
		k.probeErr = fmt.Errorf("keyring probe timed out after %s: %w", opts.ProbeTimeout, context.DeadlineExceeded)
	}
	if m.onBuild != nil && len(m.built) > 0 {
		m.onBuild()
	}
	m.built = append(m.built, k)
	return k
}

// readDirect is keyringReadDirect against the machine: a read with no
// store and no probe.
func (m *keyringMachine) readDirect(service, key string, timeout time.Duration) ([]byte, error) {
	m.mu.Lock()
	block := m.directBlock
	m.mu.Unlock()
	if block != nil {
		<-block
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.directReads++
	if service != "basecamp" {
		return nil, fmt.Errorf("service %q", service)
	}
	vanish := m.vanishReads > 0
	if vanish {
		m.vanishReads--
	}
	switch data, ok := m.items[key]; {
	case m.readErr != nil:
		return nil, fmt.Errorf("credentials not found: %w", m.readErr)
	case m.stalled:
		return nil, fmt.Errorf("keyring read timed out after %s: %w", timeout, context.DeadlineExceeded)
	case !ok || vanish:
		return nil, fmt.Errorf("credentials not found: %w", keyring.ErrNotFound)
	default:
		return data, nil
	}
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
	origDirect := keyringReadDirect
	keyringReadDirect = machine.readDirect
	t.Cleanup(func() { keyringReadDirect = origDirect })
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
		"bus hung up":         func(m *keyringMachine) { m.readErr = io.EOF },
		"bus cut mid-reply":   func(m *keyringMachine) { m.readErr = io.ErrUnexpectedEOF },
		"bus socket reset": func(m *keyringMachine) {
			m.readErr = &net.OpError{Op: "read", Net: "unix", Err: os.NewSyscallError("read", syscall.ECONNRESET)}
		},
		"bus socket refused": func(m *keyringMachine) {
			m.readErr = &net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
		},
		"bus write broke": func(m *keyringMachine) { m.readErr = os.NewSyscallError("write", syscall.EPIPE) },
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

	t.Run("file backend after a failed probe", func(t *testing.T) {
		probeFailed := errors.New("The name org.freedesktop.secrets was not provided by any .service files")
		swapNewCredStore(t, func(credstore.StoreOptions) credStore {
			return &probeFailedStore{err: fmt.Errorf("credentials not found for profile:agent: system keyring unavailable (%w)", probeFailed), probeErr: probeFailed}
		})
		t.Setenv("BASECAMP_NO_KEYRING", "")
		store := NewStore(t.TempDir())
		_, err := store.Load("profile:agent")
		require.Error(t, err)
		assert.False(t, KeyringUnavailable(err), "a probe the keyring answered makes a file miss a miss: %v", err)
		assert.ErrorIs(t, err, ErrNoCredential)
	})
}

// slowProbe makes timed-out probes mean a slow keyring, as on Linux, or a
// keychain nobody in this session can unlock, as on headless macOS.
func slowProbe(t *testing.T, slowness bool) {
	t.Helper()
	orig, origHeadless := probeTimeoutIsSlowness, sessionIsHeadless
	probeTimeoutIsSlowness = func() bool { return slowness }
	// The probe is bounded on every platform the test runs on, as it is on
	// Linux and in a headless macOS session.
	sessionIsHeadless = func() bool { return true }
	t.Cleanup(func() { probeTimeoutIsSlowness, sessionIsHeadless = orig, origHeadless })
}

// The 2026-11-07 failure: on a machine under I/O pressure gnome-keyring
// fsyncs its whole keyring file on every write, so the probe — a write and
// a delete — took past its ten seconds while a lookup still answered in
// under four. The store fell back to a credentials.json holding nothing
// for the profile, and `basecamp connect` said the profile "holds no
// credential", sending its operator to log in again, which would have
// replaced the credential sitting in the keyring. A slow keyring is not an
// absent one: the read must reach it, and without a write.
func TestASlowKeyringProbeDoesNotMakeAStoredCredentialMissing(t *testing.T) {
	slowProbe(t, true)
	store, machine, _ := stallingStore(t)
	saveCreds(t, machine, "profile:chef", liveCreds("keyring-token"))
	machine.set(func(m *keyringMachine) { m.probeNeeds = 20 * time.Second })

	creds, err := store.Load("profile:chef")
	require.NoError(t, err, "the credential is in the keyring; only the write probe was too slow for its bound")
	assert.NotErrorIs(t, err, ErrNoCredential)
	assert.Equal(t, "keyring-token", creds.AccessToken)
	assert.Equal(t, 1, machine.directReads)
	assert.Equal(t, 1, machine.stores(), "the read paid no second write probe")

	// A write needs the keyring: it goes there, after a patient probe,
	// and never to credentials.json.
	require.NoError(t, store.Save("profile:chef", liveCreds("renewed")))
	assert.True(t, store.UsingKeyring())
	require.Len(t, machine.probeBounds, 2)
	assert.Equal(t, headlessProbeTimeout, machine.probeBounds[0])
	assert.Equal(t, slowKeyringTimeout, machine.probeBounds[1])
	creds, err = store.Load("profile:chef")
	require.NoError(t, err)
	assert.Equal(t, "renewed", creds.AccessToken)
	_, statErr := os.Stat(filepath.Join(store.fallbackDir, "credentials.json"))
	assert.ErrorIs(t, statErr, os.ErrNotExist, "nothing written in plaintext")
}

// A keyring that answers that it holds nothing has answered: a miss.
func TestASlowKeyringThatHoldsNothingIsAMiss(t *testing.T) {
	slowProbe(t, true)
	store, machine, _ := stallingStore(t)
	machine.set(func(m *keyringMachine) { m.probeNeeds = 20 * time.Second })
	_, err := store.Load("profile:chef")
	require.ErrorIs(t, err, ErrNoCredential)
	assert.False(t, KeyringUnavailable(err))
}

// When the keyring does not answer the read either, it is a keyring that
// did not answer: never "no credential", never a login remedy, its own
// try-again exit status, and the long read is paid once per backoff.
func TestAKeyringThatDoesNotAnswerIsUnavailableNotMissing(t *testing.T) {
	slowProbe(t, true)
	store, machine, clock := stallingStore(t)
	saveCreds(t, machine, "profile:chef", liveCreds("keyring-token"))
	machine.set(func(m *keyringMachine) { m.probeNeeds = 2 * slowKeyringTimeout; m.stalled = true })

	_, err := store.Load("profile:chef")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoCredential)
	assert.True(t, KeyringUnavailable(err), "%v", err)
	cliErr := output.AsError(err)
	assert.Equal(t, output.CodeKeyringUnavailable, cliErr.Code)
	assert.Equal(t, output.ExitRateLimit, output.ExitCodeFor(cliErr.Code))
	assert.True(t, cliErr.Retryable)
	assert.NotContains(t, cliErr.Hint, "auth login")
	assert.NotContains(t, cliErr.Message, "not found")

	_, err = store.Load("profile:chef")
	assert.True(t, KeyringUnavailable(err), "%v", err)
	assert.Equal(t, 1, machine.directReads, "no second long read inside the backoff")

	// Nor does a write fall back to plaintext.
	err = store.Save("profile:chef", liveCreds("new"))
	assert.True(t, KeyringUnavailable(err), "%v", err)
	_, statErr := os.Stat(filepath.Join(store.fallbackDir, "credentials.json"))
	assert.ErrorIs(t, statErr, os.ErrNotExist, "nothing written in plaintext")

	machine.set(func(m *keyringMachine) { m.stalled = false })
	clock.advance(readStallRetryCap)
	creds, err := store.Load("profile:chef")
	require.NoError(t, err, "the read after the backoff reaches the keyring")
	assert.Equal(t, "keyring-token", creds.AccessToken)
}

// After a probe timeout, reads still serve credentials.json first, so a
// write that goes to the keyring must not leave the key's plaintext copy
// behind to be read in its place: a spent refresh token replayed, or a
// logged-out credential back.
func TestAWriteToASlowKeyringRemovesThePlaintextCopy(t *testing.T) {
	slowProbe(t, true)
	store, machine, _ := stallingStore(t)
	stale, err := json.Marshal(liveCreds("stale-file-token"))
	require.NoError(t, err)
	machine.set(func(m *keyringMachine) {
		m.probeNeeds = 20 * time.Second
		m.file = map[string][]byte{"profile:chef": stale, "profile:other": stale}
	})

	require.NoError(t, store.Save("profile:chef", liveCreds("renewed")))
	machine.set(func(m *keyringMachine) {
		assert.NotContains(t, m.file, "profile:chef", "the stale plaintext copy is gone")
		assert.Contains(t, m.file, "profile:other", "other keys are left alone")
		assert.Contains(t, m.items, "profile:chef")
	})

	// A delete of a key only the file held removes it, though the keyring
	// has nothing to delete.
	require.NoError(t, store.Delete("profile:other"))
	machine.set(func(m *keyringMachine) { assert.NotContains(t, m.file, "profile:other") })
}

// After a write moves this process onto the slow keyring, a credential
// only credentials.json held (no write has moved it yet) stays readable,
// and storage is reported as the keyring throughout.
func TestUntouchedFileCredentialsStayReadableAfterTheSwitch(t *testing.T) {
	slowProbe(t, true)
	store, machine, _ := stallingStore(t)
	other, err := json.Marshal(liveCreds("file-only-token"))
	require.NoError(t, err)
	machine.set(func(m *keyringMachine) {
		m.probeNeeds = 20 * time.Second
		m.file = map[string][]byte{"profile:other": other}
	})
	assert.True(t, store.UsingKeyring(), "a slow keyring is still where credentials live")

	require.NoError(t, store.Save("profile:chef", liveCreds("renewed")))
	assert.True(t, store.UsingKeyring())

	creds, err := store.Load("profile:other")
	require.NoError(t, err)
	assert.Equal(t, "file-only-token", creds.AccessToken)

	// Where the keyring also holds an older entry for it (one an earlier
	// fallback's refresh replaced in the file), the switch does not bring
	// that older entry back: the file's copy is read, as before the switch.
	saveCreds(t, machine, "profile:other", liveCreds("retired-keyring-token"))
	creds, err = store.Load("profile:other")
	require.NoError(t, err)
	assert.Equal(t, "file-only-token", creds.AccessToken)

	_, err = store.Load("profile:missing")
	require.ErrorIs(t, err, ErrNoCredential)
	require.True(t, store.holds("profile:other"))

	// A file that cannot be read is reported as that, not as "not logged in".
	machine.set(func(m *keyringMachine) { m.fileErr = errors.New("unexpected end of JSON input") })
	_, err = store.Load("profile:other")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoCredential)
	assert.ErrorContains(t, err, "unexpected end of JSON input")
	assert.False(t, store.holds("profile:other"), "a later stall must not serve what could not be read")

	// Nor as a keyring outage, though a truncated file fails with the
	// io.ErrUnexpectedEOF a dropped bus connection also gives.
	machine.set(func(m *keyringMachine) { m.fileErr = fmt.Errorf("decode credentials.json: %w", io.ErrUnexpectedEOF) })
	_, err = store.Load("profile:other")
	require.Error(t, err)
	assert.False(t, KeyringUnavailable(err), "%v", err)
	assert.NotErrorIs(t, err, ErrNoCredential)
	assert.False(t, store.stall.stalled, "the keyring answered: no backoff")
}

// Reaching a slow keyring for a write can take a minute: a caller that
// stops meanwhile does not have its credential written after all.
func TestASaveCanceledWhileReachingASlowKeyringIsAbandoned(t *testing.T) {
	slowProbe(t, true)
	store, machine, _ := stallingStore(t)
	machine.set(func(m *keyringMachine) { m.probeNeeds = 20 * time.Second })
	require.True(t, store.UsingKeyring())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	machine.set(func(m *keyringMachine) { m.onBuild = cancel })
	err := store.SaveContext(ctx, "profile:chef", liveCreds("stopped"))
	require.ErrorIs(t, err, context.Canceled)
	machine.set(func(m *keyringMachine) {
		assert.NotContains(t, m.items, "profile:chef")
		assert.NotContains(t, m.file, "profile:chef")
	})
}

// A restarting Secret Service can answer a direct read with nothing for a
// credential it gave this process a moment ago: asked again before the
// miss is believed, as a store read is.
func TestADirectReadMissOfAHeldCredentialIsAskedAgain(t *testing.T) {
	slowProbe(t, true)
	store, machine, _ := stallingStore(t)
	saveCreds(t, machine, "profile:chef", liveCreds("keyring-token"))
	machine.set(func(m *keyringMachine) { m.probeNeeds = 20 * time.Second })
	_, err := store.Load("profile:chef")
	require.NoError(t, err)

	machine.set(func(m *keyringMachine) { m.vanishReads = 1 })
	creds, err := store.Load("profile:chef")
	require.NoError(t, err)
	assert.Equal(t, "keyring-token", creds.AccessToken)
	assert.Equal(t, 3, machine.directReads)
}

// A refusal is an answer (#866), on the direct read and on the patient
// probe alike: reported as itself, not as an outage to wait out.
func TestARefusalAfterASlowProbeIsNotAnOutage(t *testing.T) {
	slowProbe(t, true)
	store, machine, _ := stallingStore(t)
	refused := &net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", syscall.EACCES)}
	machine.set(func(m *keyringMachine) { m.probeNeeds = 20 * time.Second; m.readErr = refused })
	_, err := store.Load("profile:chef")
	require.Error(t, err)
	assert.False(t, KeyringUnavailable(err), "%v", err)
	assert.NotErrorIs(t, err, ErrNoCredential)

	machine.set(func(m *keyringMachine) { m.probeNeeds = 0; m.probeErr = fmt.Errorf("probe: %w", refused) })
	err = store.Save("profile:chef", liveCreds("t"))
	require.Error(t, err)
	assert.False(t, KeyringUnavailable(err), "%v", err)
	assert.False(t, store.stall.stalled, "no backoff for an answer")
}

// A caller that stops waiting is not held for the long read, and while
// one long read is in flight, others are refused at once rather than each
// starting their own.
func TestADirectReadIsCancelableAndOneAtATime(t *testing.T) {
	slowProbe(t, true)
	store, machine, _ := stallingStore(t)
	saveCreds(t, machine, "profile:chef", liveCreds("keyring-token"))
	release := make(chan struct{})
	machine.set(func(m *keyringMachine) { m.probeNeeds = 20 * time.Second; m.directBlock = release })

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { _, err := store.LoadContext(ctx, "profile:chef"); result <- err }()
	require.Eventually(t, func() bool {
		store.stall.mu.Lock()
		defer store.stall.mu.Unlock()
		return store.stall.directReading
	}, time.Second, time.Millisecond)

	_, err := store.Load("profile:chef")
	assert.True(t, KeyringUnavailable(err), "a second reader is refused while the first reads: %v", err)

	cancel()
	select {
	case err := <-result:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("a canceled caller waited for the keyring")
	}

	close(release)
	require.Eventually(t, func() bool {
		store.stall.mu.Lock()
		defer store.stall.mu.Unlock()
		return !store.stall.directReading
	}, time.Second, time.Millisecond)
	machine.set(func(m *keyringMachine) { m.directBlock = nil })
	creds, err := store.Load("profile:chef")
	require.NoError(t, err)
	assert.Equal(t, "keyring-token", creds.AccessToken)
}

// Headless macOS keeps the #568 design: a timed-out probe there is a locked
// keychain nobody can unlock, the file is the store, and a miss is a miss.
func TestATimedOutProbeOnHeadlessMacOSStillFallsBackToTheFile(t *testing.T) {
	slowProbe(t, false)
	store, machine, _ := stallingStore(t)
	machine.set(func(m *keyringMachine) { m.probeNeeds = 20 * time.Second })

	_, err := store.Load("profile:chef")
	require.ErrorIs(t, err, ErrNoCredential)
	assert.False(t, KeyringUnavailable(err))
	assert.Equal(t, 1, machine.stores(), "no patient probe")
}

// probeFailedStore is a store on the file backend after a failed probe.
type probeFailedStore struct{ err, probeErr error }

func (f *probeFailedStore) Load(string) ([]byte, error) { return nil, f.err }
func (f *probeFailedStore) Save(string, []byte) error   { return nil }
func (f *probeFailedStore) Delete(string) error         { return nil }
func (f *probeFailedStore) MigrateToKeyring() error     { return nil }
func (f *probeFailedStore) UsingKeyring() bool          { return false }
func (f *probeFailedStore) FallbackWarning() string     { return "" }
func (f *probeFailedStore) ProbeError() error           { return nil }

// A keyring that refuses the read has answered: that is not served from
// memory, and is not reported as an outage (#866).
func TestARefusedKeyringReadIsNotAnOutage(t *testing.T) {
	store, machine, _ := stallingStore(t)
	saveCreds(t, machine, "profile:agent", liveCreds("held-token"))
	mgr := stallingManager(t, store)
	_, err := mgr.AccessToken(t.Context())
	require.NoError(t, err)

	machine.set(func(m *keyringMachine) {
		m.readErr = &net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", syscall.EACCES)}
	})
	_, err = mgr.AccessToken(t.Context())
	require.Error(t, err)
	assert.False(t, KeyringUnavailable(err), "%v", err)
	assert.False(t, keyringDidNotAnswer(fmt.Errorf("credentials not found: %w", os.NewSyscallError("connect", syscall.EACCES))), "a permission error is an answer")
	assert.True(t, errors.Is(os.NewSyscallError("connect", syscall.EACCES), fs.ErrPermission))
}

// A delete that lands while a read is in flight wins: the read must not put
// back the credential the delete removed.
func TestAReadRacingADeleteDoesNotRestoreTheCredential(t *testing.T) {
	store, machine, _ := stallingStore(t)
	saveCreds(t, machine, "profile:agent", liveCreds("token"))
	_, err := store.Load("profile:agent")
	require.NoError(t, err)

	var once sync.Once
	machine.set(func(m *keyringMachine) {
		m.onRead = func() {
			once.Do(func() { require.NoError(t, store.Delete("profile:agent")) })
		}
	})
	creds, err := store.Load("profile:agent")
	require.NoError(t, err, "the read looked the item up before the delete")
	assert.Equal(t, "token", creds.AccessToken)
	assert.Nil(t, store.heldCredential("profile:agent"), "the deleted credential is not held")
}

// A caller that gives up during the wait before asking a restarting keyring
// again gets its cancellation at once, and the keyring is not asked again.
func TestTheAskAgainWaitEndsWithTheCaller(t *testing.T) {
	store, machine, _ := stallingStore(t)
	keyringRestartGrace = time.Minute
	saveCreds(t, machine, "profile:agent", liveCreds("token"))
	_, err := store.Load("profile:agent")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	machine.set(func(m *keyringMachine) {
		m.vanishReads = 1
		m.reads = 0
		m.onRead = cancel
	})
	start := time.Now()
	_, err = store.LoadContext(ctx, "profile:agent")
	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), 5*time.Second, "the wait ends with the caller")
	machine.set(func(m *keyringMachine) { assert.Equal(t, 1, m.reads, "no second read after the caller left") })
}

// A save that lands while a read is in flight wins: the read must not
// replace the credential just written with the one it found before.
func TestAReadRacingASaveDoesNotRestoreTheOldCredential(t *testing.T) {
	store, machine, _ := stallingStore(t)
	saveCreds(t, machine, "profile:agent", liveCreds("old"))
	_, err := store.Load("profile:agent")
	require.NoError(t, err)

	var once sync.Once
	machine.set(func(m *keyringMachine) {
		m.onRead = func() {
			once.Do(func() { require.NoError(t, store.Save("profile:agent", liveCreds("new"))) })
		}
	})
	creds, err := store.Load("profile:agent")
	require.NoError(t, err)
	assert.Equal(t, "old", creds.AccessToken, "the read looked the item up before the save")
	held := store.heldCredential("profile:agent")
	require.NotNil(t, held)
	assert.Equal(t, "new", held.AccessToken, "what was saved is what is held")
}

// A renewal that was saved, followed by a keyring too slow to read it back,
// serves the token the renewal saved.
func TestARenewedTokenIsServedWhenItsReadBackStalls(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(int) (int, string) {
		return http.StatusOK, `{"access_token":"minted-1","token_type":"bearer","expires_in":3600,"resource":"urn:bc:agent:42","scope":"full"}`
	}
	store, machine, _ := stallingStore(t)
	m := newDeviceTestManager(t, as.srv.URL)
	m.SetStore(store)
	key := m.credentialKey()
	require.NoError(t, store.Save(key, agentCredential(as.srv.URL+"/oauth/token", time.Now().Add(-time.Minute))))

	// The first read finds the token spent, the second (under the key lock)
	// confirms it; the keyring stops answering from the read-back on.
	machine.set(func(m *keyringMachine) {
		m.reads = 0
		m.onRead = func() {
			machine.set(func(m *keyringMachine) {
				if m.reads == 2 {
					m.stalled = true
				}
			})
		}
	})
	token, err := m.AccessToken(t.Context())
	require.NoError(t, err, "the renewal was saved; the keyring's silence is not the token's")
	assert.Equal(t, "minted-1", token)
	machine.set(func(m *keyringMachine) { assert.Equal(t, 3, m.reads, "the read-back was attempted") })
	assert.Len(t, as.tokenCalls(), 1)
}

// After the switch to the keyring, a write still rewrites credentials.json
// to remove a plaintext copy, so it keeps the whole-store file lock; and a
// file that cannot be checked is reported, not taken for "no copy".
func TestPlaintextCleanupStaysLockedAndReportsUnreadableFiles(t *testing.T) {
	slowProbe(t, true)
	store, machine, _ := stallingStore(t)
	machine.set(func(m *keyringMachine) { m.probeNeeds = 20 * time.Second })
	assert.False(t, store.hasPlaintextBeside())
	require.NoError(t, store.Save("profile:chef", liveCreds("first")))
	require.True(t, store.UsingKeyring())
	assert.True(t, store.hasPlaintextBeside(), "the file lock is still taken for writes")

	// Serialized for real: another holder of the whole-store lock (as
	// another process would hold it) keeps the write waiting.
	other := NewStore(store.fallbackDir)
	held, release, saved := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_ = other.withStoreFileLock(func() error { close(held); <-release; return nil })
	}()
	<-held
	go func() { saved <- store.Save("profile:chef", liveCreds("waited")) }()
	select {
	case err := <-saved:
		t.Fatalf("the write did not wait for the store lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-saved)

	// Reads too: one may fall back to credentials.json, so it waits out a
	// writer rewriting it.
	readHeld, readRelease, loaded := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_ = other.withStoreFileLock(func() error { close(readHeld); <-readRelease; return nil })
	}()
	<-readHeld
	go func() { _, err := store.Load("profile:chef"); loaded <- err }()
	select {
	case err := <-loaded:
		t.Fatalf("the read did not wait for the store lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(readRelease)
	require.NoError(t, <-loaded)

	machine.set(func(m *keyringMachine) { m.fileErr = errors.New("unexpected end of JSON input") })
	err := store.Save("profile:chef", liveCreds("second"))
	require.Error(t, err)
	assert.ErrorContains(t, err, "could not be checked")
	// A delete the keyring has nothing for does not hide an unchecked file.
	err = store.Delete("profile:nobody")
	require.Error(t, err)
	assert.ErrorContains(t, err, "could not be checked")
}
