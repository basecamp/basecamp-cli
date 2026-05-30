//go:build !windows

package cli

import (
	"os"
	"syscall"
)

// isForeignOwnerWritable reports whether fi is owner-writable and owned by a
// user other than the current effective user and other than root. Such an
// ancestor lets its owner substitute a path component and win the Lstat->Chmod
// TOCTOU race. Root-owned dirs are trusted (root already has full access);
// self-owned dirs are under our control.
func isForeignOwnerWritable(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return fi.Mode()&0o200 != 0 && st.Uid != uint32(os.Geteuid()) && st.Uid != 0 //nolint:gosec // G115: Geteuid() returns a valid non-negative uid that fits in uint32
}
