package setup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

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

// Lock takes the per-profile setup lock beside connect.json, so two setups
// cannot interleave a load, a change and a save. It refuses rather than
// waits: a second setup on one profile is a mistake to report, not a queue.
func Lock(path string) (unlock func(), err error) {
	if err := ensurePrivateDirs(path); err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(filepath.Dir(path), ".connect.lock"), flock.SetPermissions(0o600))
	held, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("take the setup lock: %w", err)
	}
	if !held {
		return nil, errors.New("another connect setup is running for this profile")
	}
	return func() { _ = lock.Unlock() }, nil
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
