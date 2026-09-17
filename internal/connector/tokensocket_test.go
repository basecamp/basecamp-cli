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
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
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

func TestTheTokenGoesToTheWorkersOwnGroupOnly(t *testing.T) {
	s, err := ServeTaskToken(tokenDir(t), socketTestToken, 5*time.Second)
	require.NoError(t, err)
	// This test process connects, so the worker's group here is its own.
	s.AllowGroup(syscall.Getpgrp())

	got, err := fetch(t, s.Path())
	require.NoError(t, err)
	assert.Equal(t, socketTestToken+"\n", got)
	assert.Equal(t, HandoffDelivered, s.Result())

	s.Close()
	require.True(t, s.Settled(5*time.Second))
	_, err = os.Lstat(s.Path())
	assert.True(t, os.IsNotExist(err), "the socket is unlinked when the connector is done with it")
	_, err = fetch(t, s.Path())
	assert.Error(t, err, "and nothing else is served")
}

// An MCP host that restarts a stdio server re-runs its command, and the
// bridge takes the token again on every start: a socket that served once and
// closed would leave the restarted server with no Basecamp tools. Each start
// is a handoff of its own, with the same peer checks, up to a bound.
func TestARestartedMCPServerTakesTheTokenAgain(t *testing.T) {
	// The taker this test reports is a pid that no longer exists, which is
	// what the socket waits for between handoffs: a server that has gone.
	s, err := serveTaskTokenWith(tokenDir(t), socketTestToken, 5*time.Second, peerCredentials,
		processGroupOf, parentProcessOf, func(int) (driver.Process, error) {
			// A pid above the kernel's maximum, in the worker's own group: it
			// passes the trust rule and is gone the moment it is asked about.
			return driver.Process{PID: 1 << 30, PGID: syscall.Getpgrp(), StartedAt: time.Now()}, nil
		})
	require.NoError(t, err)
	defer s.Close()
	handoffs := make(chan Handoff, MaxTokenHandoffs+2)
	s.OnHandoff(func(h Handoff, _ driver.Process, _ bool) { handoffs <- h })
	s.AllowGroup(syscall.Getpgrp())

	for i := range MaxTokenHandoffs {
		got, fetchErr := fetch(t, s.Path())
		require.NoErrorf(t, fetchErr, "handoff %d", i+1)
		require.Equal(t, socketTestToken, strings.TrimSpace(got), "handoff %d", i+1)
		assert.Equal(t, HandoffDelivered, <-handoffs)
		taker, ok := s.Taker()
		require.True(t, ok)
		assert.Positive(t, taker.PID, "the newest server is the one holding the token")
	}

	require.True(t, s.Settled(5*time.Second), "the budget is spent and the socket is finished with")
	_, err = fetch(t, s.Path())
	assert.Error(t, err, "a host that restarts its server more often than that is not served forever")
	assert.Equal(t, HandoffDelivered, s.Result(), "the first handoff is still what Result says")
}

// The peer check is per handoff, not only on the first: a stranger that
// connects after a legitimate restart gets nothing, and ends the socket.
func TestThePeerCheckAppliesToEveryHandoff(t *testing.T) {
	// The first connection is the worker's; the second is a process of some
	// other group, as the kernel reports it. The taker reported for the first
	// is a pid that is gone, so the socket arms again at once.
	var handoffCount atomic.Int64
	s, err := serveTaskTokenWith(tokenDir(t), socketTestToken, 5*time.Second, peerCredentials,
		func(pid int) (int, error) {
			if handoffCount.Add(1) > 1 {
				return syscall.Getpgrp() + 100000, nil
			}
			return processGroupOf(pid)
		},
		func(int) (int, error) { return 1, nil },
		func(int) (driver.Process, error) {
			return driver.Process{PID: 1 << 30, PGID: syscall.Getpgrp(), StartedAt: time.Now()}, nil
		})
	require.NoError(t, err)
	defer s.Close()
	handoffs := make(chan Handoff, 4)
	s.OnHandoff(func(h Handoff, _ driver.Process, _ bool) { handoffs <- h })
	s.AllowGroup(syscall.Getpgrp())

	got, err := fetch(t, s.Path())
	require.NoError(t, err)
	require.Equal(t, socketTestToken, strings.TrimSpace(got))
	assert.Equal(t, HandoffDelivered, <-handoffs)

	second, _ := fetch(t, s.Path())
	assert.Empty(t, strings.TrimSpace(second), "the second handoff is checked like the first")
	assert.Equal(t, HandoffRefused, <-handoffs)
	assert.True(t, s.Settled(5*time.Second), "and a refusal ends the socket")
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

// Card 23's review: the connector keeps the identity of the process that took
// the token, because an agent may have started it outside the worker's group.
func TestTheSocketRemembersWhoTookTheToken(t *testing.T) {
	s, err := ServeTaskToken(tokenDir(t), socketTestToken, time.Second)
	require.NoError(t, err)
	defer s.Close()
	s.AllowGroup(syscall.Getpgrp())

	_, ok := s.Taker()
	assert.False(t, ok, "nobody has taken it yet")

	got, err := fetch(t, s.Path())
	require.NoError(t, err)
	require.Equal(t, socketTestToken, strings.TrimSpace(got))
	require.Equal(t, HandoffDelivered, s.Result())

	taker, ok := s.Taker()
	require.True(t, ok)
	assert.Equal(t, os.Getpid(), taker.PID, "this test took it")
	assert.Equal(t, syscall.Getpgrp(), taker.PGID)
	assert.False(t, taker.StartedAt.IsZero(), "with the start time that tells it from a later pid")
}

// Opus r7: the short base is chosen so that what MkdirTemp makes under it
// still fits, and a runtime directory too deep for one falls through to /tmp
// rather than leaving the connector with nowhere to put a socket.
func TestTheShortSocketBaseIsChosenSoTheSocketFits(t *testing.T) {
	deep, err := os.MkdirTemp("/tmp", "bcrt-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(deep) })
	deep = filepath.Join(deep, strings.Repeat("d", 40), strings.Repeat("e", 40))
	require.NoError(t, os.MkdirAll(deep, 0o700))

	base, err := ShortSocketBase("2914079-52007412", func(k string) (string, bool) {
		if k == "XDG_RUNTIME_DIR" {
			return deep, true
		}
		return "", false
	})
	require.NoError(t, err, "a runtime directory too deep is not the end of it")
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	assert.False(t, strings.HasPrefix(base, deep), "the deep one is skipped")

	// Whatever MkdirTemp makes under it fits, with its longest possible name.
	dir, temporary, err := TokenSocketDir(filepath.Join(deep, strings.Repeat("a", AttemptIDLength)), base)
	require.NoError(t, err)
	require.True(t, temporary)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	assert.True(t, TokenSocketFits(filepath.Join(base, "s0123456789")), "the longest name MkdirTemp can make")
	assert.True(t, TokenSocketFits(dir))

	socket, err := ServeTaskToken(dir, socketTestToken, time.Second)
	require.NoError(t, err, "and a socket actually binds there")
	socket.Close()
}

// Opus r6/r7: a handoff in flight when an attempt ends is finished with
// before anything reads who took the token, so the release point never sees
// an empty taker for a token that was in fact handed over.
func TestAHandoffInFlightIsFinishedBeforeTheTakerIsRead(t *testing.T) {
	// The identity lookup is where the handoff is slowest; hold it there.
	slow := make(chan struct{})
	s, err := serveTaskTokenWith(tokenDir(t), socketTestToken, 2*time.Second,
		peerCredentials, processGroupOf, parentProcessOf,
		func(pid int) (driver.Process, error) {
			<-slow
			return driver.LookupProcess(pid)
		})
	require.NoError(t, err)
	defer s.Close()
	s.AllowGroup(syscall.Getpgrp())

	got := make(chan string, 1)
	go func() {
		token, _ := fetch(t, s.Path())
		got <- token
	}()
	require.Equal(t, socketTestToken, strings.TrimSpace(<-got), "the token is out before the taker is known")
	_, ok := s.Taker()
	require.False(t, ok, "the fixture must have the handoff still deciding")

	// The release point's move: stop the socket, wait for it, then read.
	s.Close()
	close(slow)
	assert.True(t, s.Settled(5*time.Second), "the socket finishes what it was doing")
	taker, ok := s.Taker()
	require.True(t, ok, "and the process that took the token is known by then")
	assert.Equal(t, os.Getpid(), taker.PID)
	assert.Equal(t, HandoffDelivered, s.Result())
}

// Opus r8: after a delivery the socket does not arm again while the process
// that took the token is still running — a restart is that process ending —
// so the token is not there for the asking for the rest of the window.
func TestTheSocketDoesNotArmAgainWhileTheServerHoldingTheTokenLives(t *testing.T) {
	// The taker reported is this test process, which is very much alive.
	s, err := serveTaskTokenWith(tokenDir(t), socketTestToken, 300*time.Millisecond, peerCredentials,
		processGroupOf, parentProcessOf, driver.LookupProcess)
	require.NoError(t, err)
	defer s.Close()
	handoffs := make(chan Handoff, 4)
	s.OnHandoff(func(h Handoff, _ driver.Process, _ bool) { handoffs <- h })
	s.AllowGroup(syscall.Getpgrp())

	got, err := fetch(t, s.Path())
	require.NoError(t, err)
	require.Equal(t, socketTestToken, strings.TrimSpace(got))
	require.Equal(t, HandoffDelivered, <-handoffs)
	taker, ok := s.Taker()
	require.True(t, ok)
	require.Equal(t, os.Getpid(), taker.PID)

	// Two windows' worth of asking, while the server that has the token runs.
	for range 3 {
		second, _ := fetch(t, s.Path())
		assert.Empty(t, strings.TrimSpace(second), "nothing is handed out while that server lives")
	}
	select {
	case h := <-handoffs:
		t.Fatalf("a second handoff was made while the first server was still running: %s", h)
	default:
	}
	assert.False(t, s.Settled(100*time.Millisecond), "and the socket is still this attempt's, waiting")
}
