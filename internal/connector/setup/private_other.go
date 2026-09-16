//go:build !unix

package setup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Without Unix ownership and modes to read, setup relies on the one place
// whose access it can take as private: the user's own profile directory,
// which Windows gives an owner-only ACL. connect.json anywhere else (a
// config directory redirected by XDG_CONFIG_HOME to a shared folder, say)
// is refused rather than trusted, and no symlink or junction is followed on
// the way down from the profile directory.

func checkAncestors(dir string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("%w: the user's profile directory is unknown: %v", ErrNotPrivate, err)
	}
	home = filepath.Clean(home)
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(home, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%w: %s is outside the user's profile directory %s, whose access this platform cannot verify", ErrNotPrivate, abs, home)
	}
	p := home
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		p = filepath.Join(p, part)
		if err := checkPlainDir(p); err != nil {
			return err
		}
	}
	return nil
}

func checkPlainDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", dir, err)
	}
	if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 || !info.IsDir() {
		return fmt.Errorf("%w: %s is not a plain directory", ErrNotPrivate, dir)
	}
	return nil
}

func checkPrivateDir(dir string) error { return checkPlainDir(dir) }

func checkPrivateFile(f *os.File, path string) error {
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file", ErrNotPrivate, path)
	}
	return nil
}

func openNoFollow(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return nil, fmt.Errorf("%w: %s is a symlink or reparse point", ErrNotPrivate, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	// The name was checked, then opened: compare what was opened with what
	// was checked, so a swap in between is refused.
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		f.Close()
		return nil, errors.Join(fmt.Errorf("%w: %s changed while it was opened", ErrNotPrivate, path), err)
	}
	return f, nil
}

func syncDir(string) {}
