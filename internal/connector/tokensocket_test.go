//go:build linux || darwin

package connector

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const socketTestToken = "test-token-not-real"

func tokenDir(t *testing.T) string {
	t.Helper()
	// Unix socket paths are short; a test's own temp directory may not be.
	dir, err := os.MkdirTemp("/tmp", "bc-tok-")
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0o700))
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// fetch connects and reads whatever the socket hands over.
func fetch(t *testing.T, path string) (string, error) {
	t.Helper()
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	data, err := io.ReadAll(conn)
	return string(data), err
}

func TestTheTokenGoesOnceToTheWorkersOwnGroup(t *testing.T) {
	s, err := ServeTaskToken(tokenDir(t), socketTestToken, 5*time.Second)
	require.NoError(t, err)
	// This test process connects, so the worker's group here is its own.
	s.AllowGroup(syscall.Getpgrp())

	got, err := fetch(t, s.Path())
	require.NoError(t, err)
	assert.Equal(t, socketTestToken+"\n", got)
	assert.Equal(t, HandoffDelivered, s.Result())

	_, err = os.Lstat(s.Path())
	assert.True(t, os.IsNotExist(err), "the socket is unlinked once it has been used")
	_, err = fetch(t, s.Path())
	assert.Error(t, err, "a second connection is refused")
}

func TestAPeerOutsideTheWorkersGroupGetsNothing(t *testing.T) {
	s, err := ServeTaskToken(tokenDir(t), socketTestToken, 5*time.Second)
	require.NoError(t, err)
	s.AllowGroup(syscall.Getpgrp() + 100000)

	got, _ := fetch(t, s.Path())
	assert.Empty(t, got)
	assert.Equal(t, HandoffRefused, s.Result())
}

func TestAnotherUsersPeerGetsNothing(t *testing.T) {
	other := func(conn *net.UnixConn) (PeerCredentials, error) {
		cred, err := peerCredentials(conn)
		cred.UID++
		return cred, err
	}
	s, err := serveTaskToken(tokenDir(t), socketTestToken, 5*time.Second, other, processGroupOf)
	require.NoError(t, err)
	s.AllowGroup(syscall.Getpgrp())

	got, _ := fetch(t, s.Path())
	assert.Empty(t, got)
	assert.Equal(t, HandoffRefused, s.Result())
}

func TestAWorkerGroupNeverNamedHandsNothingOver(t *testing.T) {
	s, err := ServeTaskToken(tokenDir(t), socketTestToken, 300*time.Millisecond)
	require.NoError(t, err)
	got, _ := fetch(t, s.Path())
	assert.Empty(t, got)
	assert.Equal(t, HandoffRefused, s.Result())
}

func TestATokenSocketNobodyUsesExpires(t *testing.T) {
	s, err := ServeTaskToken(tokenDir(t), socketTestToken, 150*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, HandoffExpired, s.Result())
	_, err = os.Lstat(s.Path())
	assert.True(t, os.IsNotExist(err), "an expired socket is unlinked")
	_, err = fetch(t, s.Path())
	assert.Error(t, err)
}

func TestATokenSocketNeedsAPrivateDirectory(t *testing.T) {
	dir := tokenDir(t)
	require.NoError(t, os.Chmod(dir, 0o755))
	_, err := ServeTaskToken(dir, socketTestToken, time.Second)
	assert.Error(t, err)
	_, statErr := os.Lstat(filepath.Join(dir, TokenSocketName))
	assert.True(t, os.IsNotExist(statErr))
}
