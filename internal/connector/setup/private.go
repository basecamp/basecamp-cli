package setup

import (
	"context"
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

// ErrSetupRunning reports the per-profile policy lock held by somebody else:
// another `connect setup` on the profile, across its whole load-change-save.
// See the block above LockWait.
var ErrSetupRunning = errors.New("another command holds this profile's connector policy")

// ErrLockUnavailable reports a host that cannot take the setup lock at all:
// a filesystem without flock, a lock file this user does not own. Setup's
// guarantee is the lock, so it refuses rather than run without one.
var ErrLockUnavailable = errors.New("this host cannot lock the connector's policy")

// Who takes this lock, and for how long. One holder today:
//
//   - `connect setup`, plain or guided (Lock), holds it across its whole
//     load, change and save, network checks included. Seconds, and on a
//     slow or unreachable account longer than that.
//
// Until #815 removed them, `connect redispatch` and the running connector's
// dispatcher took it too, each across one file read and one ledger
// transaction. The connector reads connect.json without it now.
//
// Other locks are taken UNDER this one and never the other way about: the
// credential key's lock on setup's final write. That one order is what keeps
// this from deadlocking, and a caller that waits on this while holding
// something a current holder waits on is how it stops being one. There is no
// such caller today.
//
// LockWait bounds Lock's wait for a holder to finish. It was sized for the
// dispatcher's hold, one file read and one SQLite transaction bounded by the
// ledger's own five-second busy retry, with room. With setup the only holder,
// a second setup waits that long for the first to finish before it is
// refused.
//
// A variable so tests can shorten it.
var LockWait = 10 * time.Second

// lockPoll is how often a waiter re-tries. flock has no blocking-with-timeout
// form through this library, so the wait is a poll — the same shape, and for
// the same reason, as the credential lock's (internal/auth/lock.go).
const lockPoll = 20 * time.Millisecond

// Lock takes the per-profile setup lock beside connect.json, so a load, a
// change and a save cannot interleave with another holder's. It waits up to
// LockWait, or until ctx ends, and then reports ErrSetupRunning or ctx's own
// error.
//
// The wait honors ctx, and a wait that ends takes nothing: a person who
// pressed Ctrl-C during contention is not made to sit out the bound, and a
// command whose context has expired does not go on to acquire a lock and act
// under it (Copilot on #771). What a caller that cannot get the lock gets is
// a refusal, never a fall-through: "the policy could not be checked" is not
// permission to change it.
//
// A second `connect setup` whose predecessor finishes inside LockWait is
// queued rather than refused: the two run one after the other. Nothing is
// written twice by it: each one loads, changes and saves under the lock in
// turn, which is what the lock is for. A second setup whose predecessor is
// still going after LockWait is refused.
func Lock(ctx context.Context, path string) (unlock func(), err error) {
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
	deadline := time.Now().Add(LockWait)
	for {
		// Before the attempt, not only after it: a context that has already
		// ended must not come away holding the lock and go on to act under
		// it.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		held, err := lock.TryLock()
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrLockUnavailable, lockPath, err)
		}
		if held {
			// And again now it is held: a context that ended while this was
			// acquiring must not come away holding the lock either, so it
			// is given back rather than handed to a caller that is no
			// longer entitled to act.
			if err := ctx.Err(); err != nil {
				_ = lock.Unlock()
				return nil, err
			}
			return func() { _ = lock.Unlock() }, nil
		}
		if !time.Now().Before(deadline) {
			// A wait that ran out at the same moment the context ended is
			// reported as the interruption it is: the caller stopped, and
			// "somebody else holds it" would send them back to try again.
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("%w: %s", ErrSetupRunning, filepath.Dir(path))
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
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
