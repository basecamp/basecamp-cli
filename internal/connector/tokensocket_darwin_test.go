//go:build darwin

package connector

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// macOS names a peer's process only while it is connected, and still names
// its user. peerCredentials maps that to errPeerGone with the user, which is
// what keeps an early close undelivered rather than refused.
func TestMacOSNamesAClosedPeerAsGoneWithItsUser(t *testing.T) {
	path := filepath.Join(tokenDir(t), "s")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	require.NoError(t, err)
	defer ln.Close()

	dialer := net.Dialer{Timeout: 2 * time.Second}
	client, err := dialer.DialContext(context.Background(), "unix", path)
	require.NoError(t, err)
	conn, err := ln.AcceptUnix()
	require.NoError(t, err)
	defer conn.Close()

	open, err := peerCredentials(conn)
	require.NoError(t, err)
	assert.Equal(t, os.Getpid(), open.PID)
	assert.Equal(t, os.Getuid(), open.UID)

	require.NoError(t, client.Close())
	require.Eventually(t, func() bool {
		_, err := peerCredentials(conn)
		return err != nil
	}, 5*time.Second, 10*time.Millisecond)
	gone, err := peerCredentials(conn)
	require.ErrorIs(t, err, errPeerGone)
	assert.Equal(t, os.Getuid(), gone.UID)
}
