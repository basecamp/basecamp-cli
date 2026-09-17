//go:build unix

package connector

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ownership is read from the file itself: a ledger this user does not own is
// not one this process may read, whoever else can see it.
func TestOwnedByThisUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	info, err := os.Lstat(path)
	require.NoError(t, err)

	assert.True(t, ownedByThisUser(info))
	assert.True(t, sameOwner(info, info))
	assert.False(t, sameOwner(info, otherOwner{info}), "the owner changed since the check")
	err = verifySameFile(path, otherOwner{info})
	require.Error(t, err, "a second open is held to the owner the check passed")
	assert.Contains(t, err.Error(), "no longer owned by the user the check passed")
	assert.False(t, ownedByThisUser(otherOwner{info}), "another user's file")
	assert.False(t, ownedByThisUser(noOwner{info}), "a file whose owner cannot be read")
}

type otherOwner struct{ os.FileInfo }

func (o otherOwner) Sys() any {
	stat := *o.FileInfo.Sys().(*syscall.Stat_t)
	stat.Uid++
	return &stat
}

type noOwner struct{ os.FileInfo }

func (noOwner) Sys() any { return nil }
