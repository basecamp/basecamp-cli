//go:build unix

package connector

import (
	"os"
	"syscall"
)

// ownedByThisUser reports a file this user owns. The connector runs on Unix
// only — its ledger's privacy cannot be established elsewhere — so this is
// where ownership is read.
func ownedByThisUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

// sameOwner reports two stats of a file with the same owner.
func sameOwner(a, b os.FileInfo) bool {
	left, okA := a.Sys().(*syscall.Stat_t)
	right, okB := b.Sys().(*syscall.Stat_t)
	return okA && okB && left.Uid == right.Uid
}
