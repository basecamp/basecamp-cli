package setup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

// ErrLockHeld reports a lock another process already holds.
var ErrLockHeld = errors.New("the lock is held by another process")

// EnsurePrivateDir creates dir owner-only when it is missing, and refuses it
// when dir — or any directory above it — could be changed by someone else.
//
// It is the same check connect.json gets, exported for the state the running
// connector keeps beside it: the instance lock and the ledger live under the
// same config root, and a directory another local user can write is a
// directory where a lock file can be swapped for one that excludes nobody.
//
// Only the last component is created. Everything above it must already exist
// and already be trustworthy, which is what makes the refusal meaningful.
func EnsurePrivateDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("%w: no directory given", ErrNotPrivate)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := checkAncestors(filepath.Dir(abs)); err != nil {
		return err
	}
	switch _, err := os.Lstat(abs); {
	case errors.Is(err, os.ErrNotExist):
		if err := os.Mkdir(abs, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create %s: %w", abs, err)
		}
	case err != nil:
		return fmt.Errorf("inspect %s: %w", abs, err)
	}
	return checkPrivateDir(abs)
}

// TryLockPrivate takes an exclusive lock on path without waiting, having first
// established that the lock is a lock: the directory holding it is this user's
// alone, and the file itself is this user's own regular file rather than a
// symlink or something another user left there. A lock on a file two processes
// can be pointed at different inodes excludes nobody.
//
// It reports ErrLockHeld when another process holds it, which is an answer
// rather than a failure. Every other error means the lock could not be
// established, and the caller must refuse rather than carry on unlocked.
func TryLockPrivate(path string) (unlock func() error, err error) {
	if err := EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := checkPrivateLockFile(path); err != nil {
		if errors.Is(err, ErrNotPrivate) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s: %w", ErrLockUnavailable, path, err)
	}
	lock := flock.New(path, flock.SetPermissions(0o600))
	held, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrLockUnavailable, path, err)
	}
	if !held {
		return nil, ErrLockHeld
	}
	// Re-checked with the lock held, and this time the file must be there:
	// a lock file that has been unlinked or replaced since the open excludes
	// nobody, whatever this process is holding.
	if err := checkHeldLockFile(path); err != nil {
		_ = lock.Unlock()
		return nil, err
	}
	return lock.Unlock, nil
}

// checkHeldLockFile is checkPrivateLockFile for a lock already taken, where a
// missing file is itself the tampering it is looking for.
func checkHeldLockFile(path string) error {
	f, err := openNoFollow(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s was removed while it was being locked", ErrNotPrivate, path)
		}
		return fmt.Errorf("%w: %s: %w", ErrLockUnavailable, path, err)
	}
	defer f.Close()
	return checkPrivateFile(f, path)
}

// EnsurePrivateFile creates path owner-only when it is missing, and refuses it
// when it is not this user's own regular file reached through directories only
// this user can change.
//
// It is stricter than the check connect.json gets in one way: a file another
// user can merely READ is refused too. It exists for the connector's ledger,
// which holds the account's feed positions — resumable tokens, so credentials
// — and there a readable file is already the leak.
//
// What is validated is the descriptor, not the name: the file is opened
// without following symlinks and inspected through that open file, so the
// thing checked is the thing the caller will go on to use. Creation is
// exclusive for the same reason — O_CREAT|O_EXCL refuses a symlink outright
// rather than following it somewhere else.
func EnsurePrivateFile(path string) error {
	if err := EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := openNoFollow(path)
	if errors.Is(err, os.ErrNotExist) {
		created, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		switch {
		case createErr == nil:
			// The handle is writable, so its close is reported rather than
			// deferred away: a close that fails is a file that may not be
			// there, and the caller is about to treat it as one that is.
			// Nothing has been written to it, so the durable part is the
			// directory entry, which is what is synced.
			if err := checkPrivateReadableFile(created, path); err != nil {
				_ = created.Close()
				return err
			}
			if err := created.Close(); err != nil {
				return fmt.Errorf("create %s: %w", path, err)
			}
			syncDir(filepath.Dir(path))
			return nil
		case !errors.Is(createErr, os.ErrExist):
			return fmt.Errorf("create %s: %w", path, createErr)
		}
		// Something appeared between the open and the create — including,
		// possibly, a symlink, which is why this reopen still refuses to
		// follow one.
		f, err = openNoFollow(path)
	}
	if err != nil {
		return err
	}
	defer f.Close()
	return checkPrivateReadableFile(f, path)
}

// CheckPrivateFile holds an existing file to EnsurePrivateFile's rules without
// creating anything: every directory on the way must be this user's alone, the
// file must not be a symlink, and — inspected through the open descriptor — it
// must be this user's own file that nobody else can read. A missing file, or
// a missing directory, is an error satisfying errors.Is(err, os.ErrNotExist).
func CheckPrivateFile(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(abs)
	if err := checkAncestors(filepath.Dir(dir)); err != nil {
		return err
	}
	if _, err := os.Lstat(dir); err != nil {
		return fmt.Errorf("inspect %s: %w", dir, err)
	}
	if err := checkPrivateDir(dir); err != nil {
		return err
	}
	f, err := openNoFollow(abs)
	if err != nil {
		return err
	}
	defer f.Close()
	return checkPrivateReadableFile(f, abs)
}

func checkPrivateReadableFile(f *os.File, path string) error {
	if err := checkPrivateFile(f, path); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("%w: %s can be read by other users (mode %04o); it must be 0600", ErrNotPrivate, path, perm)
	}
	return nil
}
