package auth

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

// Riding out a desktop keyring that stops answering.
//
// On Linux the keyring is the Secret Service over D-Bus, usually
// gnome-keyring-daemon. On a loaded machine it can answer slowly, and some
// versions crash outright when a client hangs up mid-call (systemd restarts
// it, and every call in flight is lost). None of that says anything about
// the credential, which is sitting in the keyring, intact. A command that
// lasts a second can simply fail and be run again; a process that runs for
// days — the connector — must not lose its token to one bad moment. Three
// things here keep it working, with nothing for anyone to configure:
//
//  1. A read the keyring did not answer is marked ErrKeyringUnavailable, so
//     callers can tell it from a missing login and wait rather than give up.
//  2. The Store keeps the credential it last read or wrote for each key, so
//     the token can be served from memory while the keyring is silent
//     (Manager.storedAccessToken decides when).
//  3. A timed-out read is retried on a backoff with a fresh credstore, which
//     is credstore's own documented way out of the latch it sets after a
//     timeout. Only reads earn it: a write or delete whose outcome is
//     unknown keeps the latch for the life of the process, as credstore
//     intends, so no write is ever overlapped. The fresh store is kept only
//     if its probe reached the keyring, so a retry never swaps the keyring
//     for a possibly stale credentials.json (#664, #805).

// ErrKeyringUnavailable marks a credential read the OS keyring did not
// answer — it timed out, or the store is refusing calls after an earlier
// timeout, or the Secret Service was not there to ask — as opposed to one
// it answered. Only ever set while the keyring is the store in use.
var ErrKeyringUnavailable = errors.New("the system keyring did not answer")

// KeyringUnavailable reports whether err is a read the keyring did not
// answer (ErrKeyringUnavailable).
func KeyringUnavailable(err error) bool { return errors.Is(err, ErrKeyringUnavailable) }

type keyringUnavailableError struct{ err error }

func (e *keyringUnavailableError) Error() string   { return e.err.Error() }
func (e *keyringUnavailableError) Unwrap() []error { return []error{e.err, ErrKeyringUnavailable} }

const (
	readStallRetryBase = 15 * time.Second
	readStallRetryCap  = 5 * time.Minute
)

// keyringRestartGrace is how long a read waits before asking again when
// the keyring says a credential it gave this process earlier is gone. A
// Secret Service that is restarting can answer a search with nothing
// before it has loaded its collection; a credential that was really
// removed is still gone a moment later. Replaceable in tests.
var keyringRestartGrace = 500 * time.Millisecond

// keyringStall is a Store's state for riding out a silent keyring.
type keyringStall struct {
	mu sync.Mutex
	// stalled is set while inner refuses keyring calls after a timed-out
	// read; retryAt is when a fresh store may be tried, stalls counts
	// consecutive stalls for the backoff, and retrying is set while one
	// goroutine builds the fresh store.
	stalled  bool
	retrying bool
	retryAt  time.Time
	stalls   int
	// held is the credential last read or written per key, and gen counts
	// each key's writes and invalidations, so a read that started before
	// one cannot put back what it replaced or removed.
	held map[string]Credentials
	gen  map[string]uint64

	// now is the backoff's clock. Test seam; nil means time.Now.
	now func() time.Time
}

func (k *keyringStall) clock() time.Time {
	if k.now != nil {
		return k.now()
	}
	return time.Now()
}

// current is the store to use, after building a fresh one in place of a
// stalled one when its retry is due. The fresh store's probe can take as
// long as the probe bound, so it runs outside the lock: other callers keep
// using the stalled store, which refuses at once, meanwhile.
func (s *Store) current() credStore {
	k := &s.stall
	k.mu.Lock()
	inner := s.inner
	due := k.stalled && !k.retrying && !k.clock().Before(k.retryAt)
	if due {
		k.retrying = true
	}
	k.mu.Unlock()
	if !due {
		return inner
	}

	fresh := newCredStore(s.opts)

	k.mu.Lock()
	defer k.mu.Unlock()
	k.retrying = false
	if !fresh.UsingKeyring() {
		// Still silent. Stay on the stalled store, which refuses rather
		// than read a fallback file, and wait longer.
		k.stalls++
		k.retryAt = k.clock().Add(readStallBackoff(k.stalls))
		return s.inner
	}
	s.inner = fresh
	// Not recovered until a read succeeds: a timeout on the fresh store
	// carries the backoff on from here.
	k.stalled = false
	return fresh
}

// read is one credential read with the stall handling around it. It
// returns the key's generation as of the start of the read, for keepRead.
// A caller's cancellation (req) ends the grace wait, and no second read
// starts after it.
func (s *Store) read(origin string, req lockRequest) ([]byte, uint64, error) {
	gen := s.generation(origin)
	inner := s.ensure()
	data, err := inner.Load(origin)
	if !inner.UsingKeyring() {
		return data, gen, err
	}
	if err != nil && isMissingCredential(err) && s.holds(origin) {
		if waitErr := waitOrCancel(keyringRestartGrace, req); waitErr != nil {
			return nil, gen, waitErr
		}
		inner = s.ensure()
		data, err = inner.Load(origin)
	}
	s.noteRead(inner, err)
	if err != nil && keyringDidNotAnswer(err) {
		err = &keyringUnavailableError{err}
	}
	return data, gen, err
}

// waitOrCancel waits d, or returns the caller's cancellation if it comes
// first or has already come.
func waitOrCancel(d time.Duration, req lockRequest) error {
	if err := lockCause(req.cause); err != nil {
		return err
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-req.done:
	}
	return lockCause(req.cause)
}

// noteRead records how a read on inner went. A timeout, or the refusal a
// store gives after one, schedules a retry; anything the keyring answered
// ends the stall. A result from a store that has since been replaced says
// nothing about its replacement and is ignored — otherwise a late timeout
// from the old store could mark the new one stalled and have it replaced
// in turn, past a write of unknown outcome on it.
func (s *Store) noteRead(inner credStore, err error) {
	k := &s.stall
	k.mu.Lock()
	defer k.mu.Unlock()
	if inner != s.inner {
		return
	}
	switch {
	case readTimedOut(err):
		if k.stalled {
			// Already waiting: a refusal meanwhile must not push the
			// retry further out.
			return
		}
		k.stalled = true
		k.stalls++
		k.retryAt = k.clock().Add(readStallBackoff(k.stalls))
	case err == nil || !keyringDidNotAnswer(err):
		k.stalled, k.stalls = false, 0
	}
}

// readStallBackoff is the wait before the nth consecutive retry.
func readStallBackoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	if n > 6 {
		return readStallRetryCap
	}
	return min(readStallRetryBase<<(n-1), readStallRetryCap)
}

// readTimedOut reports a keyring read that outlived its bound, or a
// store's refusal of every call after one did. A queued call that was
// never attempted does not count: it leaves the store usable. Nor does a
// refusal after a timed-out write or delete, which keeps its latch.
func readTimedOut(err error) bool {
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "keyring read timed out after") && !strings.Contains(msg, "waiting for another operation")
}

// keyringDidNotAnswer reports a keyring call that got no answer about the
// credential: a timeout or the refusal after one, or a D-Bus failure — the
// Secret Service gone, restarting, or dropping the call. Never a missing
// item, which is an answer.
func keyringDidNotAnswer(err error) bool {
	if err == nil || isMissingCredential(err) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) && strings.Contains(err.Error(), "keyring ") {
		return true
	}
	// A refusal is an answer, and is classified as one (#866).
	if errors.Is(err, fs.ErrPermission) {
		return false
	}
	var dbusErr dbus.Error
	var dbusErrPtr *dbus.Error
	var opErr *net.OpError
	return errors.As(err, &dbusErr) || errors.As(err, &dbusErrPtr) || errors.Is(err, dbus.ErrClosed) ||
		// The bus connection itself: a socket that is missing, refused,
		// reset or closed under the call.
		errors.As(err, &opErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}

// generation is key's invalidation count.
func (s *Store) generation(key string) uint64 {
	k := &s.stall
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.gen[key]
}

// keepRead holds what a read returned, unless key was invalidated since the
// read began: a read that raced a delete must not restore the credential
// the delete removed.
func (s *Store) keepRead(key string, creds *Credentials, gen uint64) {
	k := &s.stall
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.gen[key] != gen {
		return
	}
	if k.held == nil {
		k.held = make(map[string]Credentials)
	}
	k.held[key] = *creds
}

// keep holds creds as the credential last written for key. A write is an
// invalidation too: a read that began before it must not replace what it
// wrote with what was there before.
func (s *Store) keep(key string, creds *Credentials) {
	k := &s.stall
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.held == nil {
		k.held = make(map[string]Credentials)
	}
	k.held[key] = *creds
	if k.gen == nil {
		k.gen = make(map[string]uint64)
	}
	k.gen[key]++
}

// forget drops what is held for key: the store said it is gone or
// unreadable, or a write of unknown outcome went to it.
func (s *Store) forget(key string) {
	k := &s.stall
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.held, key)
	if k.gen == nil {
		k.gen = make(map[string]uint64)
	}
	k.gen[key]++
}

func (s *Store) holds(key string) bool {
	k := &s.stall
	k.mu.Lock()
	defer k.mu.Unlock()
	_, ok := k.held[key]
	return ok
}

// heldCredential is a copy of the credential held for key, or nil.
func (s *Store) heldCredential(key string) *Credentials {
	k := &s.stall
	k.mu.Lock()
	defer k.mu.Unlock()
	creds, ok := k.held[key]
	if !ok {
		return nil
	}
	return &creds
}
