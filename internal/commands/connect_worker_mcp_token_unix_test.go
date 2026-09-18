//go:build unix

package commands

import (
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serveOnce answers one connection on a unix socket with reply, closing the
// connection afterwards, and returns the socket's path.
func serveOnce(t *testing.T, reply string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.sock")
	l, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		_, _ = conn.Write([]byte(reply))
		_ = conn.Close()
	}()
	return path
}

// Copilot on #738: a socket handoff that succeeded is one newline-terminated
// line, so anything ReadString reports an error on is a partial answer. A
// truncated token is not a token: the bridge refuses it rather than starting
// `basecamp mcp` with a credential that will not authenticate.
func TestTheBridgeRefusesATokenTheSocketDidNotFinishHandingOver(t *testing.T) {
	token, err := receiveTaskToken(serveOnce(t, "test-token-not-re"), 2*time.Second)
	assert.Empty(t, token, "a partial line is not a token")
	require.Error(t, err, "a read that did not finish is a refusal, not a token")
	assert.Contains(t, err.Error(), "no token")
}

// And a whole handoff is still taken.
func TestTheBridgeTakesAWholeToken(t *testing.T) {
	token, err := receiveTaskToken(serveOnce(t, "test-token-not-real\n"), 2*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "test-token-not-real", token)
}
