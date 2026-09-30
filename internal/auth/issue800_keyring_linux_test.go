//go:build linux

package auth

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/cli/credstore"
)

// Exercise the real credstore -> go-keyring -> godbus path, without accessing
// the desktop's actual keyring. A subprocess isolates godbus's shared connection
// and lets the test kill an unbounded handshake rather than hang the test suite.
func TestIssue800DesktopKeyringHandshakeMustBeBounded(t *testing.T) {
	t.Run("desktop", func(t *testing.T) { issue800ReadWithStalledBus(t, false, false) })
	t.Run("headless_control", func(t *testing.T) { issue800ReadWithStalledBus(t, true, false) })
	t.Run("no_keyring_control", func(t *testing.T) { issue800ReadWithStalledBus(t, false, true) })
}

func issue800ReadWithStalledBus(t *testing.T, headless, disable bool) {
	t.Helper()
	dir := t.TempDir()
	fileStore := credstore.NewStore(credstore.StoreOptions{ForceFile: true, FallbackDir: dir})
	require.NoError(t, fileStore.Save("profile:issue800", []byte(`{"access_token":"fallback-token"}`)))

	// Use a short socket path: Unix socket addresses have a small length limit.
	socketDir, err := os.MkdirTemp("", "issue800-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", filepath.Join(socketDir, "bus"))
	require.NoError(t, err)
	defer listener.Close()

	handshake := make(chan error, 1)
	release := make(chan struct{})
	defer close(release)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			handshake <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		reader := bufio.NewReader(conn)
		line, err := reader.ReadString('\n')
		if err == nil && line != "\x00AUTH\r\n" {
			err = fmt.Errorf("unexpected initial authentication: %q", line)
		}
		if err == nil {
			_, err = fmt.Fprint(conn, "REJECTED EXTERNAL\r\n")
		}
		if err == nil {
			line, err = reader.ReadString('\n')
			if err == nil && !strings.HasPrefix(line, "AUTH EXTERNAL") {
				err = fmt.Errorf("unexpected authentication mechanism: %q", line)
			}
		}
		handshake <- err
		// Deliberately never send OK or an error. Holding the connection open
		// recreates a stalled SASL/EXTERNAL exchange, not a connection failure.
		<-release
	}()

	t.Setenv("ISSUE800_CREDENTIAL_HELPER", dir)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(socketDir, "bus"))
	t.Setenv("BASECAMP_NO_KEYRING", "")
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	if !headless {
		t.Setenv("WAYLAND_DISPLAY", "wayland-issue800")
	}
	if disable {
		t.Setenv("BASECAMP_NO_KEYRING", "1")
	}

	executable, err := os.Executable()
	require.NoError(t, err)
	// Allow the existing ten-second headless probe budget plus scheduling
	// margin. The desktop must also have a finite bound; currently it has none.
	ctx, cancel := context.WithTimeout(context.Background(), headlessProbeTimeout+2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestIssue800CredentialReadHelper$", "-test.v")
	out, runErr := cmd.CombinedOutput()
	if !disable {
		select {
		case handshakeErr := <-handshake:
			require.NoError(t, handshakeErr)
		default:
			t.Fatal("credential read never reached the fake D-Bus EXTERNAL handshake")
		}
	}
	require.NoError(t, ctx.Err(), "credential read remained blocked in D-Bus authentication after %s; child output: %s", headlessProbeTimeout+2*time.Second, out)
	require.NoError(t, runErr, "%s", out)
	assert.Contains(t, string(out), "fallback-token loaded")
	if !disable {
		assert.Contains(t, string(out), "keyring probe timed out")
	} else {
		assert.NotContains(t, string(out), "warning:")
	}
}

func TestIssue800CredentialReadHelper(t *testing.T) {
	dir := os.Getenv("ISSUE800_CREDENTIAL_HELPER")
	if dir == "" {
		t.Skip("subprocess helper")
	}
	creds, err := NewStore(dir).Load("profile:issue800")
	require.NoError(t, err)
	require.Equal(t, "fallback-token", creds.AccessToken)
	t.Log("fallback-token loaded")
}
