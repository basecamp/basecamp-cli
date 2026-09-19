package setup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// maxFileBytes bounds a connect.json read. A real one is a few kilobytes.
const maxFileBytes = 1 << 20

// ErrNotPrivate reports a connect.json, or a directory holding it, that
// someone other than this user could have written.
var ErrNotPrivate = errors.New("connect.json is not private to this user")

// privateDirs are the directories setup owns on the way to connect.json: the
// CLI's config directory, connect/ and connect/<profile>/. Each must be this
// user's alone. Everything above them is checked by checkAncestors.
func privateDirs(path string) []string {
	profileDir := filepath.Dir(path)
	connectDir := filepath.Dir(profileDir)
	return []string{filepath.Dir(connectDir), connectDir, profileDir}
}

// ensurePrivateDirs creates what is missing of privateDirs, owner-only, and
// refuses when any of them, or anything above them, could be changed by
// someone else.
func ensurePrivateDirs(path string) error {
	dirs := privateDirs(path)
	if err := checkAncestors(filepath.Dir(dirs[0])); err != nil {
		return err
	}
	for _, dir := range dirs {
		switch _, err := os.Lstat(dir); {
		case errors.Is(err, os.ErrNotExist):
			if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("create %s: %w", dir, err)
			}
		case err != nil:
			return fmt.Errorf("inspect %s: %w", dir, err)
		}
		if err := checkPrivateDir(dir); err != nil {
			return err
		}
	}
	return nil
}

// ErrSetupRunning reports the per-profile policy lock held by somebody else.
// Two holders take it: `connect setup`, across its whole load-change-save,
// and a running connector, for the moment it authorizes a launch or a
// redispatch against the file.
var ErrSetupRunning = errors.New("another command holds this profile's connector policy")

// ErrLockUnavailable reports a host that cannot take the setup lock at all:
// a filesystem without flock, a lock file this user does not own. Setup's
// guarantee is the lock, so it refuses rather than run without one.
var ErrLockUnavailable = errors.New("this host cannot lock the connector's policy")

// LockWait bounds Lock's wait for a holder to finish. The connector takes
// this lock too now, and what it does under it is one file read and one
// SQLite transaction — bounded by the ledger's own five-second busy retry —
// so a setup that refused the instant a dispatcher pass overlapped it would
// fail for no reason a person could act on. Ten seconds is that worst case
// with room, and a setup that waits it out reports a real holder.
//
// A variable so tests can shorten it.
var LockWait = 10 * time.Second

// lockPoll is how often a waiter re-tries. flock has no blocking-with-timeout
// form through this library, so the wait is a poll — the same shape, and for
// the same reason, as the credential lock's (internal/auth/lock.go).
const lockPoll = 20 * time.Millisecond

// Lock takes the per-profile setup lock beside connect.json, so a load, a
// change and a save cannot interleave with another holder's. It waits up to
// LockWait and then reports ErrSetupRunning: the wait is for the connector's
// brief hold, not a queue of setups, and a second setup on one profile is
// still a mistake to report rather than to serialize — it will not have
// finished inside LockWait.
func Lock(path string) (unlock func(), err error) {
	return lock(path, LockWait)
}

// TryLock takes the lock if it is free and reports ErrSetupRunning if it is
// not, without waiting. It is the dispatcher's form: a pass that cannot take
// the lock authorizes nothing and tries again on its next tick, which costs
// one tick of latency. Waiting instead would park the dispatcher behind a
// `connect setup`'s network checks, and a dispatcher that blocks on a file
// another process writes is its own hazard.
func TryLock(path string) (unlock func(), err error) {
	return lock(path, 0)
}

func lock(path string, wait time.Duration) (unlock func(), err error) {
	if err := ensurePrivateDirs(path); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(filepath.Dir(path), ".connect.lock")
	// The lock is only a lock if it is this user's own file: a symlink or a
	// foreign file left in the directory could point two holders at
	// different inodes, and they would not exclude each other.
	if err := checkPrivateLockFile(lockPath); err != nil {
		if errors.Is(err, ErrNotPrivate) {
			return nil, err
		}
		// The lock file is there and cannot even be inspected: this host
		// cannot lock, which is not the same as bad input.
		return nil, fmt.Errorf("%w: %s: %w", ErrLockUnavailable, lockPath, err)
	}
	lock := flock.New(lockPath, flock.SetPermissions(0o600))
	deadline := time.Now().Add(wait)
	for {
		held, err := lock.TryLock()
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrLockUnavailable, lockPath, err)
		}
		if held {
			return func() { _ = lock.Unlock() }, nil
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("%w: %s", ErrSetupRunning, filepath.Dir(path))
		}
		time.Sleep(lockPoll)
	}
}

// checkPrivateLockFile refuses a lock file this user does not solely own. A
// file that does not exist yet is fine: flock creates it in a directory
// ensurePrivateDirs has already vetted.
func checkPrivateLockFile(path string) error {
	f, err := openNoFollow(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	}
	defer f.Close()
	return checkPrivateFile(f, path)
}

func readPrivate(path string) ([]byte, error) {
	dirs := privateDirs(path)
	if err := checkAncestors(filepath.Dir(dirs[0])); err != nil {
		return nil, err
	}
	for _, dir := range dirs {
		if err := checkPrivateDir(dir); err != nil {
			return nil, err
		}
	}
	f, err := openNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := checkPrivateFile(f, path); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxFileBytes)
	}
	return data, nil
}

func writePrivate(path string, data []byte) error {
	if err := ensurePrivateDirs(path); err != nil {
		return err
	}
	// An existing connect.json that is not ours is refused rather than
	// replaced: the rename would succeed, and whoever planted it would learn
	// nothing, but the operator would never hear that it had been there.
	switch f, err := openNoFollow(path); {
	case err == nil:
		checkErr := checkPrivateFile(f, path)
		f.Close()
		if checkErr != nil {
			return checkErr
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".connect-*.json")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // a no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	syncDir(dir)
	return nil
}
