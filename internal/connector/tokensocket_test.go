//go:build linux || darwin

package connector

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(context.Background(), "unix", path)
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
	s, err := ServeTaskToken(tokenDir(t), socketTestToken, 100*time.Millisecond)
	require.NoError(t, err)
	got, _ := fetch(t, s.Path())
	assert.Empty(t, got, "there is no worker to trust a peer against")
	// A worker that is never named leaves nothing to decide about the peer;
	// the socket gives up on the worker, not on it.
	assert.Equal(t, HandoffExpired, s.Result())
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

// Codex starts its MCP servers in process groups of their own, so a
// descendant of the worker in another group is the worker's too.
func TestAWorkersDescendantInItsOwnGroupGetsTheToken(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is needed for a child in a group of its own")
	}
	s, err := ServeTaskToken(tokenDir(t), socketTestToken, 10*time.Second)
	require.NoError(t, err)
	// This test process plays the worker; the child it starts is its
	// descendant, in a new process group.
	s.AllowGroup(os.Getpid())
	script := "import socket,sys\ns=socket.socket(socket.AF_UNIX)\ns.connect(sys.argv[1])\nprint(s.recv(256).decode().strip())"
	cmd := exec.CommandContext(context.Background(), python, "-c", script, s.Path())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, socketTestToken, strings.TrimSpace(string(out)))
	assert.Equal(t, HandoffDelivered, s.Result())
}

// Card 23's review: the window is the worker's MCP server's, and a slow
// launcher or a handshake that takes as long as the window must not spend it.
func TestTheWindowStartsWhenTheWorkerIsNamed(t *testing.T) {
	window := 300 * time.Millisecond
	s, err := ServeTaskToken(tokenDir(t), socketTestToken, window)
	require.NoError(t, err)
	defer s.Close()

	// A handshake as long as the whole window, and then the worker exists.
	time.Sleep(window + 100*time.Millisecond)
	s.AllowGroup(syscall.Getpgrp())

	got, err := fetch(t, s.Path())
	require.NoError(t, err)
	assert.Equal(t, socketTestToken, strings.TrimSpace(got))
	assert.Equal(t, HandoffDelivered, s.Result())
}

// A worker that is never named does not hold the socket forever.
func TestASocketNoWorkerIsEverNamedForExpires(t *testing.T) {
	s, err := ServeTaskToken(tokenDir(t), socketTestToken, 150*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, HandoffExpired, s.Result())
}
