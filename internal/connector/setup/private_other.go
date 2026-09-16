//go:build !unix

package setup

import (
	"fmt"
	"os"
)

// On platforms without Unix ownership and modes, access is the ACL of the
// user's profile directory; only the shape of the path is checked.

func checkPrivateDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: %s is not a plain directory", ErrNotPrivate, dir)
	}
	return nil
}

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
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %s is a symlink", ErrNotPrivate, path)
	}
	return os.Open(path)
}

func syncDir(string) {}
