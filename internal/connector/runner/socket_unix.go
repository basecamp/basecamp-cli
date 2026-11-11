//go:build unix

package runner

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// checkSocketDir holds the directory a socket lives in to what makes the
// socket's own permissions mean anything: a real directory, not a link to
// one, that is this user's and that nobody else can enter. The connector's
// state directory is that already. The fallback for a path too long for a
// socket address is not always: it may sit in a temporary directory other
// users can write in, where one of them could have made the directory, or a
// link in its place, first.
//
// It looks at the directory alone and not at what is above it, so it also
// answers from inside a coding agent's sandbox, where the ownership of
// everything else on the way cannot be told.
func checkSocketDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	switch {
	case info.Mode()&os.ModeSymlink != 0 || !info.IsDir():
		return fmt.Errorf("%s is not a directory of its own: remove it and start the connector again", dir)
	case !ok:
		return errors.New("who owns " + dir + " cannot be told")
	case int(stat.Uid) != os.Getuid():
		return fmt.Errorf("%s belongs to another user: remove it, or set XDG_RUNTIME_DIR to a directory of your own, and start the connector again", dir)
	case info.Mode().Perm()&0o077 != 0:
		return fmt.Errorf("%s can be entered by other users (mode %04o): it must be 0700", dir, info.Mode().Perm())
	}
	return nil
}
