//go:build unix

package auth

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// requirePrivateLockPath refuses a lock directory or lock file that is not
// this user's own: a symlink (which could have been planted while the
// config directory was permissive and now point somewhere else), a file
// another user owns, or one another user can write. A required lock is only
// a lock if every process that takes it takes the same inode, and that is
// what these checks are for.
func requirePrivateLockPath(dir, file string) error {
	if err := requirePrivate(dir, true); err != nil {
		return err
	}
	switch err := requirePrivate(file, false); {
	case errors.Is(err, os.ErrNotExist):
		// The lock file is created, owner-only, by the acquisition itself.
		return nil
	default:
		return err
	}
}

func requirePrivate(path string, wantDir bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("%s is a symlink", path)
	case wantDir && !info.IsDir():
		return fmt.Errorf("%s is not a directory", path)
	case !wantDir && !info.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file", path)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("the owner of %s cannot be read", path)
	}
	if int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%s belongs to uid %d", path, st.Uid)
	}
	if perm := info.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("%s is writable by other users (mode %04o)", path, perm)
	}
	return nil
}
