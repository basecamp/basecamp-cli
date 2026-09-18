//go:build !unix

package connector

import "os"

// ownedByThisUser cannot be answered without POSIX owners, and a ledger whose
// privacy cannot be established is refused rather than opened.
func ownedByThisUser(os.FileInfo) bool { return false }

// sameOwner cannot be answered without POSIX owners.
func sameOwner(os.FileInfo, os.FileInfo) bool { return false }
