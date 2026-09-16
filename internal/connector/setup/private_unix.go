//go:build unix

package setup

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// checkPrivateDir refuses a directory that is a symlink, belongs to another
// user, or is writable by group or others: anyone who can write the
// directory can replace connect.json in it.
func checkPrivateDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s is a symlink", ErrNotPrivate, dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrNotPrivate, dir)
	}
	return checkOwnerAndMode(info, dir)
}

// checkPrivateFile refuses an open connect.json that is not a regular file,
// belongs to another user, or is writable by group or others. It inspects
// the descriptor, not the name, so what is checked is what is read.
func checkPrivateFile(f *os.File, path string) error {
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file", ErrNotPrivate, path)
	}
	return checkOwnerAndMode(info, path)
}

func checkOwnerAndMode(info os.FileInfo, path string) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%w: cannot read the owner of %s", ErrNotPrivate, path)
	}
	if int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%w: %s belongs to uid %d", ErrNotPrivate, path, st.Uid)
	}
	if perm := info.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("%w: %s is writable by other users (mode %04o); chmod go-w it, or remove it and run setup again", ErrNotPrivate, path, perm)
	}
	return nil
}

// openNoFollow opens path for reading, refusing a symlink at the final
// component and never blocking on a FIFO planted there.
func openNoFollow(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%w: %s is a symlink", ErrNotPrivate, path)
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return f, nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}
