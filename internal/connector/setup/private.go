package setup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// maxFileBytes bounds a connect.json read. A real one is a few kilobytes.
const maxFileBytes = 1 << 20

// ErrNotPrivate reports a connect.json, or a directory holding it, that
// someone other than this user could have written.
var ErrNotPrivate = errors.New("connect.json is not private to this user")

// privateDirs are the directories between the CLI's config directory and
// connect.json: connect/ and connect/<profile>/. The config directory above
// them is hardened by the CLI on every run.
func privateDirs(path string) []string {
	profileDir := filepath.Dir(path)
	return []string{filepath.Dir(profileDir), profileDir}
}

func readPrivate(path string) ([]byte, error) {
	for _, dir := range privateDirs(path) {
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
	for _, dir := range privateDirs(path) {
		switch _, err := os.Lstat(dir); {
		case errors.Is(err, os.ErrNotExist):
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return fmt.Errorf("create %s: %w", dir, err)
			}
		case err != nil:
			return fmt.Errorf("inspect %s: %w", dir, err)
		}
		if err := checkPrivateDir(dir); err != nil {
			return err
		}
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
