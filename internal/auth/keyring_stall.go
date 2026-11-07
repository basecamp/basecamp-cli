package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/zalando/go-keyring"

	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
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

func (e *keyringUnavailableError) Error() string { return e.err.Error() }

// Unwrap also yields the CLI error a command reports this as: its own code
// and exit status, retryable, and a hint that never sends anyone to log in
// again, which would replace the credential the keyring still holds.
func (e *keyringUnavailableError) Unwrap() []error {
	return []error{e.err, ErrKeyringUnavailable, &output.Error{
		Code:      output.CodeKeyringUnavailable,
		Message:   "The system keyring did not answer in time: " + richtext.SanitizeSingleLine(e.err.Error()),
		Hint:      keyringUnavailableHint,
		Retryable: true,
	}}
}

// keyringUnavailableHint is the remedy for a keyring that did not answer.
const keyringUnavailableHint = "Nothing was changed, and a credential in the keyring is still there: try again in a moment. " +
	"Logging in again would replace it. If the keyring keeps not answering, check that the Secret Service " +
	"(gnome-keyring or KeePassXC) is running and unlocked, and that the machine is not short of memory or disk."

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

	// slowFile is the file-backed store a slow keyring's probe timed out
	// into, kept once a patient probe replaced it, so every later write
	// can remove the key's plaintext copy beside the keyring (mutate).
	slowFile credStore
	// directReading is set while a direct keyring read is in flight.
	directReading bool

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
	if due && !inner.UsingKeyring() && probeTimedOutOnSlowness(inner) {
		// Reads of a store whose probe timed out go to the keyring
		// directly and retry there, without a write; only a write pays
		// for a new store (writeStore).
		due = false
	}
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
		if err == nil || !isMissingCredential(err) || !probeTimedOutOnSlowness(inner) {
			return data, gen, err
		}
		// credentials.json has nothing for this key because the keyring
		// was too slow for the probe — a write and a delete, each of which
		// a loaded gnome-keyring fsyncs — not because the keyring is
		// empty. Read the keyring itself, which needs no write, patiently.
		kdata, kerr := s.readKeyringDirectly(inner, origin, req)
		if kerr != nil && isMissingCredential(kerr) && s.holds(origin) {
			// As below: a restarting Secret Service can answer with
			// nothing before it has loaded its collection.
			if waitErr := waitOrCancel(keyringRestartGrace, req); waitErr != nil {
				return nil, gen, waitErr
			}
			kdata, kerr = s.readKeyringDirectly(inner, origin, req)
		}
		switch {
		case kerr == nil:
			return kdata, gen, nil
		case isMissingCredential(kerr):
			// The keyring answered: nothing there either.
			return nil, gen, err
		case keyringDidNotAnswer(kerr):
			return nil, gen, &keyringUnavailableError{kerr}
		default:
			// Answered, with a refusal: reported as what it is (#866).
			return nil, gen, kerr
		}
	}
	if err != nil && isMissingCredential(err) && s.holds(origin) {
		if waitErr := waitOrCancel(keyringRestartGrace, req); waitErr != nil {
			return nil, gen, waitErr
		}
		inner = s.ensure()
		data, err = inner.Load(origin)
	}
	if err != nil && isMissingCredential(err) {
		// After a write reached a slow keyring (reachSlowKeyring), a
		// credential nobody has written since still lives only in the
		// credentials.json the timed-out probe fell back to. It stays
		// readable there until a write moves it into the keyring.
		s.stall.mu.Lock()
		file := s.stall.slowFile
		s.stall.mu.Unlock()
		if file != nil && file != inner {
			fdata, ferr := file.Load(origin)
			switch {
			case ferr == nil:
				data, err = fdata, nil
			case !isMissingCredential(ferr):
				// Unreadable is not absent: say so, not "not logged in".
				err = ferr
			}
		}
	}
	s.noteRead(inner, err)
	if err != nil && keyringDidNotAnswer(err) {
		err = &keyringUnavailableError{err}
	}
	return data, gen, err
}

// probeTimedOutOnSlowness reports a file-backed store whose keyring probe
// ran out of time where that means a slow keyring (probeTimeoutIsSlowness).
func probeTimedOutOnSlowness(inner credStore) bool {
	return probeTimeoutIsSlowness() && errors.Is(inner.ProbeError(), context.DeadlineExceeded)
}

// keyringReadDirect reads key from the OS keyring under service, as
// credstore stores it ("<service>::<key>"), bounded by timeout. It writes
// nothing, so it answers where a write probe cannot: a Secret Service that
// fsyncs its whole keyring file on every write, on a disk under pressure,
// still answers a lookup in seconds. go-keyring cannot cancel a call, so a
// read that outlives the bound is abandoned, as credstore abandons its own.
// Replaceable in tests.
var keyringReadDirect = func(service, key string, timeout time.Duration) ([]byte, error) {
	// At most one abandoned lookup per process: while one is still blocked
	// in go-keyring, a new one is not started, so a Secret Service that
	// never answers cannot pile up goroutines and bus calls.
	// One that has been blocked past readStallRetryCap is given up on, and
	// a fresh lookup may reach a keyring that has since recovered: at most
	// one abandoned lookup per readStallRetryCap.
	now := int64(time.Since(processStart))
	if now == 0 {
		now = 1 // zero means none runs
	}
	started := directGetStarted.Load()
	if started != 0 && time.Duration(now-started) < readStallRetryCap {
		return nil, fmt.Errorf("keyring read timed out after %s, still waiting on an earlier lookup: %w", timeout, context.DeadlineExceeded)
	}
	if !directGetStarted.CompareAndSwap(started, now) {
		return nil, fmt.Errorf("keyring read timed out after %s, still waiting on an earlier lookup: %w", timeout, context.DeadlineExceeded)
	}
	type result struct {
		secret string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		defer directGetStarted.CompareAndSwap(now, 0)
		secret, err := keyring.Get(service, service+"::"+key)
		done <- result{secret, err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-done:
		if r.err != nil {
			return nil, fmt.Errorf("credentials not found: %w", r.err)
		}
		return []byte(r.secret), nil
	case <-timer.C:
		return nil, fmt.Errorf("keyring read timed out after %s: %w", timeout, context.DeadlineExceeded)
	}
}

// hasPlaintextBeside reports a keyring store that replaced a slow keyring's
// file fallback (slowFile): writes still rewrite credentials.json to remove
// a key's plaintext copy, so they keep the whole-store file lock.
func (s *Store) hasPlaintextBeside() bool {
	s.stall.mu.Lock()
	defer s.stall.mu.Unlock()
	return s.stall.slowFile != nil
}

// directGetStarted is when the running direct go-keyring lookup started,
// as nanoseconds since processStart on the monotonic clock (a wall-clock
// step cannot age or freeze it), including one its caller has given up
// on; zero while none runs.
var directGetStarted atomic.Int64

// processStart anchors directGetStarted to the monotonic clock.
var processStart = time.Now()

// readKeyringDirectly is a read of key from the keyring, for a store whose
// probe timed out (probeTimedOutOnSlowness), on slowKeyringTimeout. One
// the keyring did not answer starts the stall backoff, and a read inside
// the backoff is refused at once rather than paying the bound again.
func (s *Store) readKeyringDirectly(inner credStore, key string, req lockRequest) ([]byte, error) {
	if err := lockCause(req.cause); err != nil {
		return nil, err
	}
	k := &s.stall
	k.mu.Lock()
	if k.directReading || (k.stalled && k.clock().Before(k.retryAt)) {
		// One long read at a time: others are refused at once, as a
		// stalled store refuses, rather than each waiting out the bound.
		k.mu.Unlock()
		return nil, slowKeyringError(inner.ProbeError())
	}
	k.directReading = true
	service := s.opts.ServiceName
	k.mu.Unlock()

	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := keyringReadDirect(service, key, slowKeyringTimeout)
		k.mu.Lock()
		k.directReading = false
		if s.inner == inner {
			switch {
			case err != nil && keyringDidNotAnswer(err):
				k.stalled = true
				k.stalls++
				k.retryAt = k.clock().Add(readStallBackoff(k.stalls))
			default:
				k.stalled, k.stalls = false, 0
			}
		}
		k.mu.Unlock()
		done <- result{data, err}
	}()
	// A caller that stops waiting is not held for the bound; the read
	// finishes on its own, and keeps others out until it does.
	select {
	case r := <-done:
		if r.err != nil && keyringDidNotAnswer(r.err) {
			return nil, slowKeyringError(r.err)
		}
		return r.data, r.err
	case <-req.done:
		if err := lockCause(req.cause); err != nil {
			return nil, err
		}
		return nil, context.Canceled
	}
}

// writeStore is the store a write or delete goes to. Where the probe timed
// out on a slow keyring (probeTimedOutOnSlowness), that is never
// credentials.json: a slow keyring is not an absent one, and a credential
// written in plaintext beside it would be read in its place. The write
// needs the keyring, so this is where its write probe is paid, patiently.
func (s *Store) writeStore() (credStore, error) {
	inner := s.ensure()
	if inner.UsingKeyring() || !probeTimedOutOnSlowness(inner) {
		return inner, nil
	}
	slow, err := s.reachSlowKeyring(inner)
	switch {
	case slow != nil:
		return slow, nil
	case keyringDidNotAnswer(err):
		return nil, &keyringUnavailableError{err}
	default:
		return nil, err
	}
}

// mutate runs a save or delete of key on writeStore. When that moved the
// write from credentials.json to the keyring, the key's plaintext copy is
// removed after it: reads after a probe timeout still serve the file first,
// so a copy left behind would be read in place of what was just written —
// a spent refresh token replayed, or a logged-out credential back.
func (s *Store) mutate(key string, deleting bool, op func(credStore) error) error {
	store, err := s.writeStore()
	if err != nil {
		return err
	}
	opErr := op(store)
	s.stall.mu.Lock()
	file := s.stall.slowFile
	s.stall.mu.Unlock()
	if file == nil || file == store {
		return opErr
	}
	if _, loadErr := file.Load(key); loadErr != nil {
		keyringMiss := deleting && opErr != nil && isMissingCredential(opErr)
		if isMissingCredential(loadErr) || (opErr != nil && !keyringMiss) {
			// No plaintext copy, or the keyring operation already failed.
			return opErr
		}
		// A copy may be there and could not be checked: the change is
		// not known to be the one later reads will see.
		return fmt.Errorf("the system keyring has the change, but credentials.json, whose copy would be read in its place, could not be checked: %w", loadErr)
	}
	if deleting && opErr != nil && isMissingCredential(opErr) {
		// Only the plaintext copy existed; removing it is the delete.
		opErr = nil
	}
	if opErr != nil {
		return opErr
	}
	if err := file.Delete(key); err != nil {
		return fmt.Errorf("the system keyring has the change, but the older plaintext copy in credentials.json, which would be read in its place, could not be removed: %w", err)
	}
	return nil
}

// reachSlowKeyring replaces inner, a file-backed store whose probe timed
// out, with one built on slowKeyringTimeout, and returns it if its probe
// reached the keyring. It returns nil, with why, when the patient probe
// failed too, or when one did recently and its backoff has not run out:
// a store that keeps missing pays the long probe once per backoff, not
// once per read. Later retries (current) are built patient as well.
func (s *Store) reachSlowKeyring(inner credStore) (credStore, error) {
	k := &s.stall
	k.mu.Lock()
	if s.inner != inner {
		replaced := s.inner
		k.mu.Unlock()
		if replaced.UsingKeyring() {
			return replaced, nil
		}
		return nil, slowKeyringError(replaced.ProbeError())
	}
	if k.retrying || (k.stalled && k.clock().Before(k.retryAt)) {
		k.mu.Unlock()
		return nil, slowKeyringError(inner.ProbeError())
	}
	k.retrying = true
	opts := s.opts
	opts.ProbeTimeout = max(opts.ProbeTimeout, slowKeyringTimeout)
	opts.OperationTimeout = max(opts.OperationTimeout, slowKeyringTimeout)
	k.mu.Unlock()

	fresh := newCredStore(opts)

	k.mu.Lock()
	defer k.mu.Unlock()
	k.retrying = false
	s.opts = opts
	if s.inner != inner {
		return nil, slowKeyringError(inner.ProbeError())
	}
	if !fresh.UsingKeyring() {
		probeErr := fresh.ProbeError()
		if probeErr != nil && !keyringDidNotAnswer(probeErr) {
			// Answered, with a refusal: no outage to wait out (#866).
			return nil, probeErr
		}
		k.stalled = true
		k.stalls++
		k.retryAt = k.clock().Add(readStallBackoff(k.stalls))
		return nil, slowKeyringError(probeErr)
	}
	k.slowFile = inner
	s.inner = fresh
	k.stalled, k.stalls = false, 0
	return fresh, nil
}

// slowKeyringError is the read error for a keyring that did not answer even
// the patient probe. It is never a missing credential: the keyring may hold
// one, and nothing here could ask it.
func slowKeyringError(probeErr error) error {
	if probeErr == nil {
		probeErr = context.DeadlineExceeded
	}
	return fmt.Errorf("the system keyring did not answer in time, so the credential it may hold was not read: %w", probeErr)
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
