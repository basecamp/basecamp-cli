package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// Cross-process credential locking.
//
// Several `basecamp` processes routinely share one profile — a connector
// beside a handful of `basecamp mcp -P <agent>` workers, a shell running
// commands in parallel, a script with xargs -P. Nothing used to keep them
// out of each other's way, and two failures followed from that:
//
//  1. Two processes enter the refresh window together, both refresh, both
//     save. The authorization server rotates the refresh token on every
//     grant, so the second save overwrites the first's rotated token with
//     its own — or the first's with a token the server has already retired.
//     The next command is refused with invalid_grant and the credential is
//     gone: "token refresh failed".
//  2. The file store (the keyring fallback) keeps every credential in one
//     credentials.json and rewrites the whole document on every Save. Two
//     processes saving DIFFERENT keys at the same time therefore drop one
//     of them outright — a profile that was logged in a moment ago reports
//     "Not authenticated for profile:".
//
// Both are read-modify-write races, so both are fixed by holding a lock
// across the whole cycle rather than around the write. The two cycles have
// different extents, so there are two locks:
//
//   - the KEY lock (one file per credential key) is held across load →
//     renew → save, network round trip included. It is what makes the
//     rotated refresh token safe, and it is per key so a refresh on one
//     profile does not stall a command on another.
//   - the STORE lock (one file for the whole store) is held for the
//     duration of a single Save or Delete and nothing else. It is what
//     makes credentials.json's whole-document rewrite safe across keys. No
//     network happens under it.
//
// The order is always key → store, never the reverse, so the two cannot
// deadlock.
//
// The lock is an flock(2) (LockFileEx on Windows) on a file in the config
// directory, not a lock on the credential store itself — which is usually
// the OS keyring, where a file lock would guard nothing. That is
// deliberate: what has to be serialized is the CLI's own critical section,
// and every basecamp process reaches it through this code whatever backend
// holds the bytes. The kernel drops an flock when the holder's descriptor
// closes, process death included, so a crashed holder cannot wedge the
// lock and there is no stale-lock reaping to get wrong.
//
// Three limits, all of which degrade to the behavior that shipped before
// this file rather than to something worse:
//
//   - flock's mutual exclusion is only as good as the filesystem's. On
//     Linux an flock over NFS is translated into a POSIX record lock and
//     works between hosts; on some other platforms and on a few network
//     filesystems it is local to the host, or a no-op. A config directory
//     on such a share, with processes on more than one host, is
//     unsynchronized.
//   - a host where the lock cannot be created at all — an unwritable
//     configuration directory, a filesystem that refuses flock — warns and
//     runs unsynchronized rather than refusing to run. Failing closed there
//     would take a setup that works today (a read-only config directory
//     over a keyring-backed store, say) and break it outright, which is a
//     worse outcome than the race it would prevent.
//   - the locks live under the configuration directory, so two processes
//     that disagree about XDG_CONFIG_HOME do not lock against each other
//     even when the OS keyring underneath them is the same. Those two
//     processes disagree about which profiles exist in the first place —
//     the credential key is a profile name, and profiles are read from the
//     configuration — so a shared keyring entry between them is an
//     existing ambiguity in the store's key namespace, not one this adds.

// credentialLockWait bounds the wait for another process's credential
// lock. The work under a key lock is one credential load, at most one
// token-endpoint round trip, and one save; the round trip is itself
// bounded by the OAuth lane client's 30-second timeout, so this is roughly
// twice the worst case a well-behaved holder can take.
//
// A variable so tests can shorten it.
var credentialLockWait = 60 * time.Second

// credentialLockPoll is how often a waiter re-tries the lock. flock has no
// blocking-with-timeout form through this library, so the wait is a poll.
const credentialLockPoll = 10 * time.Millisecond

// storeLockName guards one whole-store read-modify-write. One file for the
// whole store, because that is the extent of the file backend's rewrite.
const storeLockName = "store.lock"

// keyLockName is the lock file for one credential key. Keys are profile
// names ("profile:bot") and base URLs ("https://3.basecampapi.com"),
// neither of which is a filename, so the key is hashed rather than escaped
// — no separator to get wrong, no length limit to hit, and no way for two
// keys to collide on a case-insensitive filesystem.
func keyLockName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "key-" + hex.EncodeToString(sum[:]) + ".lock"
}

// lockDir is where the lock files live: a subdirectory of the config
// directory that also holds the file-storage fallback, so the locks sit
// next to what they guard and inherit its 0700 mode.
func (s *Store) lockDir() string {
	return filepath.Join(s.fallbackDir, "locks")
}

// withKeyLock runs fn while holding the cross-process lock for one
// credential key. Callers hold it across the whole load → renew → save
// cycle, not around the save.
func (s *Store) withKeyLock(ctx context.Context, key string, fn func() error) error {
	release, err := s.acquire(ctx, keyLockName(key))
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

// withStoreLock runs fn while holding the whole-store lock. fn must be one
// store operation and must not make a network request: every process's
// Save and Delete queue behind it.
//
// It applies to the FILE backend only. The lock exists for one reason —
// credentials.json holds every key and is rewritten whole — and the
// keyring writes one entry at a time, atomically, so there is nothing
// there for it to protect. Taking it anyway would be worse than useless:
// a macOS keychain that locks after the availability probe leaves
// `security` waiting on a person who may never answer, inside a child
// process nothing can cancel, and every other profile's save would queue
// behind that until the wait expired. Per-key exclusion still holds on the
// keyring: that is the key lock's job, and it is taken either way.
//
// The wait is not cancellable. Callers reach it through Store.Save and
// Store.Delete, which carry no context to honor, and the section it guards
// is local file I/O that is over in well under a millisecond — there is
// nothing here long enough to want to interrupt, and the wait is bounded
// either way.
func (s *Store) withStoreLock(fn func() error) error {
	if s.ensure().UsingKeyring() {
		return fn()
	}
	return s.withStoreFileLock(fn)
}

// withStoreLockContext is withStoreLock for a caller that can be canceled.
//
// The wait ends when ctx does, and — this is the point — the cancellation
// is checked again once the lock is in hand. A login that has already
// passed its last check under the KEY lock can still be sitting here when
// the person presses Ctrl-C, and without the second check it would go on
// to store the credential they stopped as soon as the holder released.
func (s *Store) withStoreLockContext(ctx context.Context, fn func() error) error {
	if s.ensure().UsingKeyring() {
		// No lock to take, so make the check lockFile would have made.
		if err := ctx.Err(); err != nil {
			return err
		}
		return fn()
	}
	release, err := s.lockFile(storeLockName, "credential store", ctx.Done(), ctx.Err)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

// withStoreFileLock runs fn while holding the whole-store lock whatever
// the backend. It is for the one operation that rewrites credentials.json
// on a KEYRING-backed store: the migration, which reads the file, saves
// every key it finds into the keyring, and removes the file. Routing that
// through withStoreLock would skip the lock precisely when it is doing the
// file work the lock exists for.
func (s *Store) withStoreFileLock(fn func() error) error {
	release, err := s.lockFile(storeLockName, "credential store", nil, nil)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

// acquire takes the exclusive lock named name on behalf of a caller with a
// context, so a canceled command stops waiting. Every such caller is
// locking one credential; the whole-store lock has no context and goes
// through lockFile directly.
func (s *Store) acquire(ctx context.Context, name string) (func(), error) {
	return s.lockFile(name, "credential", ctx.Done(), ctx.Err)
}

// lockFile takes the exclusive lock named name, waiting up to
// credentialLockWait for whoever holds it, and returns the release to
// defer. The release is always safe to call. done and cause are a caller's
// cancellation, or both nil for a wait that runs to its bound.
//
// The two failure modes are answered differently on purpose. A lock that
// cannot be created AT ALL — no config directory, an unwritable one, a
// filesystem that refuses flock — warns once and proceeds unlocked: every
// process on that host is equally unable to lock, so refusing to run would
// turn a degraded environment into a broken one, and unlocked is exactly
// the behavior that shipped before this. A lock that exists but is HELD is
// the opposite: someone is demonstrably inside the critical section, so an
// expired wait is an error rather than a race, and the caller is told what
// to look for.
func (s *Store) lockFile(name, what string, done <-chan struct{}, cause func() error) (func(), error) {
	noop := func() {}

	// A caller whose wait is already over gets nothing — this before
	// anything else, including the unlocked fall-throughs below, which
	// would otherwise run the critical section (a credential revoked,
	// deleted, replaced) for a command that has been canceled.
	if err := lockCause(cause); err != nil {
		return noop, err
	}

	if s.fallbackDir == "" {
		return s.unlocked(what, "no configuration directory is set", cause)
	}
	if err := os.MkdirAll(s.lockDir(), 0o700); err != nil {
		return s.unlocked(what, err.Error(), cause)
	}

	fl := flock.New(filepath.Join(s.lockDir(), name))
	deadline := time.Now().Add(credentialLockWait)
	for {
		locked, err := fl.TryLock()
		switch {
		case locked:
			// The same check again, because the acquisition itself is a
			// window: a waiter can be canceled in the instant the holder
			// releases, and would otherwise proceed as if nothing had
			// happened.
			if cancelErr := lockCause(cause); cancelErr != nil {
				_ = fl.Close()
				return noop, cancelErr
			}
			return func() { _ = fl.Close() }, nil
		case err != nil:
			// The lock file could not be opened or locked at all — a
			// permission this process does not have, a filesystem with no
			// flock.
			return s.unlocked(what, err.Error(), cause)
		}

		if err := lockCause(cause); err != nil {
			// The caller's own wait ended (Ctrl-C, a request deadline);
			// that is theirs to report, not a lock failure.
			return noop, err
		}
		if !time.Now().Before(deadline) {
			// Not an auth_required failure: the credential is fine and
			// logging in again would not help. It is a contention fault,
			// and it is retryable — the holder is bounded by its own
			// timeouts.
			return noop, &output.Error{
				Code: output.CodeRateLimit,
				Message: fmt.Sprintf("Timed out after %s waiting for another basecamp process to release the %s lock",
					credentialLockWait, what),
				Hint:      "Another basecamp process is holding it. Wait for it to finish, or stop it.",
				Retryable: true,
			}
		}

		// A nil done channel blocks forever in a select, so an uncancelled
		// wait is advanced by the poll timer alone.
		select {
		case <-done:
			// done is only ever set together with cause, and a Done that
			// has fired always yields an error. The fallback is there so
			// that a broken pairing can never return "no lock, no error",
			// which would run the critical section unsynchronized and say
			// nothing.
			if err := lockCause(cause); err != nil {
				return noop, err
			}
			return noop, context.Canceled
		case <-time.After(credentialLockPoll):
		}
	}
}

// unlocked is the answer to a lock that could not be created at all: warn
// once and let the critical section run unsynchronized, which is where
// every process was before this file existed.
//
// Every such fall-through goes through here so that none of them can
// forget the cancellation check. The check at the top of lockFile is not
// enough on its own: the steps between it and here touch a filesystem that
// can block, and a wait that ended while one of them ran must leave
// nothing to fall through to.
func (s *Store) unlocked(what, reason string, cause func() error) (func(), error) {
	noop := func() {}
	if err := lockCause(cause); err != nil {
		return noop, err
	}
	s.warnUnlockable(what, reason)
	return noop, nil
}

// lockCause is why the caller's wait ended, or nil for a caller that has
// no wait to end.
func lockCause(cause func() error) error {
	if cause == nil {
		return nil
	}
	return cause()
}

// warnUnlockable says once per process that credential operations are
// running unsynchronized, and why. Once, because the reason is a property
// of the host and would otherwise repeat on every store operation; and
// loudly at all, because the concurrent-process failures this file exists
// to prevent are back for as long as it holds.
func (s *Store) warnUnlockable(what, reason string) {
	s.lockWarnOnce.Do(func() {
		fmt.Fprintf(os.Stderr,
			"warning: cannot lock the %s (%s); concurrent basecamp processes may lose a credential\n",
			what, reason)
	})
}
