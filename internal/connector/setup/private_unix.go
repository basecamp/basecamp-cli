//go:build unix

package setup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// checkAncestors refuses a path to the config directory that someone other
// than this user or root could redirect: every directory on it, as written
// and as its symlinks resolve, must belong to root or this user and must not
// be writable by group or others unless it is sticky (as /tmp is, where
// nobody may rename another's entry). A symlink on the way must belong to
// root or this user too. With every ancestor settled, nothing between the
// checks and the open can be swapped by anyone else.
func checkAncestors(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := checkChain(abs); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s does not exist", ErrNotPrivate, abs)
		}
		return fmt.Errorf("resolve %s: %w", abs, err)
	}
	if resolved == abs {
		return nil
	}
	return checkChain(resolved)
}

// checkChain checks each prefix of an absolute path, root first.
func checkChain(abs string) error {
	var prefixes []string
	for p := abs; ; p = filepath.Dir(p) {
		prefixes = append(prefixes, p)
		if filepath.Dir(p) == p {
			break
		}
	}
	for i := len(prefixes) - 1; i >= 0; i-- {
		p := prefixes[i]
		info, err := os.Lstat(p)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", p, err)
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("%w: cannot read the owner of %s", ErrNotPrivate, p)
		}
		if st.Uid != 0 && int(st.Uid) != os.Geteuid() {
			return fmt.Errorf("%w: %s belongs to uid %d, neither root nor you (in a user-namespace sandbox, run setup outside it)", ErrNotPrivate, p, st.Uid)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return fmt.Errorf("%w: %s is writable by other users (mode %04o); anyone who can write it could replace connect.json below it. Run chmod go-w %s", ErrNotPrivate, p, info.Mode().Perm(), p)
		}
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
