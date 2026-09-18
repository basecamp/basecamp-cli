//go:build unix

package connector

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// handshakeTokenWait is how long the worker in these tests waits for the task
// token before it gives up. It stands in for the bridge's own 30-second dial
// and the driver's two-minute handshake, which is what a deadlocked start
// actually waits out: long enough that a loaded runner cannot fail this by
// being slow, short enough that a regression costs the suite seconds rather
// than half a minute.
const handshakeTokenWait = 5 * time.Second

// A worker whose start does not finish until its MCP server has the task
// token used to wait for a token the connector would not hand over until that
// start had finished.
//
// The socket accepts nothing until AllowGroup names the worker's process
// group. The connector named it only once Driver.NewSession had returned, and
// for the ACP driver the whole handshake — the adapter's own /mcp read-back,
// which is a real prompt turn — runs inside NewSession. An agent that starts
// its MCP servers there and will not answer until they have connected is
// waiting on a socket that is waiting on it. Neither side moves until the
// bridge's 30-second dial or the driver's two-minute handshake runs out, and
// every dispatch through such an adapter fails. That is not hypothetical: a
// fake that bound at session/new did exactly this while the Codex harness row
// was being built.
//
// The fix is the ordering: the socket is armed when the worker's PROCESS
// exists, which the driver says as soon as it has forked (driver invariant 7),
// not when its session is ready.
func TestAHandshakeThatWaitsForItsTaskTokenIsNotDeadlocked(t *testing.T) {
	fake := newFakeDriver()
	// The worker's group is this test's own, so the handshake below may take
	// the token from the socket the way the worker's MCP server would.
	fake.process = driver.Process{PID: os.Getpid(), PGID: syscall.Getpgrp(), StartedAt: time.Now()}
	// The badly-behaved adapter: it starts its MCP server while it is opening
	// its session, and it does not answer until that server has connected —
	// which, for the connector's bridge, means until it has been handed its
	// task token.
	taken := make(chan string, 1)
	fake.handshake = func(cfg driver.SessionConfig) error {
		token, err := takeTaskToken(declaredSocket(cfg), handshakeTokenWait)
		if err != nil {
			taken <- ""
			return fmt.Errorf("%w: the session's MCP server was never handed its task token: %w", driver.ErrSessionUnverified, err)
		}
		taken <- token
		return nil
	}
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) { o.PrivateDir = tokenDir(t) })
	// The "worker's group" is this test's own: confirming it gone would kill
	// the test.
	h.d.confirmGroupGone = func(driver.Process, time.Duration) error { return nil }
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	h.attemptsEnded(t, 1)

	token := <-taken
	require.NotEmpty(t, token, "the handshake was handed its task token while it was still running")
	require.Len(t, fake.sessions, 1, "and the start it was blocking finished")
	assert.NotEmpty(t, fake.sessions[0].promptList(), "so the worker was prompted")
}

// The same ordering, said as the rule rather than as its symptom: the
// connector knows the worker's process group before the driver has opened a
// session on it, and arms the socket then. A driver that announced nothing
// until it returned would leave the socket unarmed here, which is what the
// deadlock is made of.
func TestTheTokenSocketIsArmedBeforeTheSessionIsOpen(t *testing.T) {
	fake := newFakeDriver()
	fake.process = driver.Process{PID: os.Getpid(), PGID: syscall.Getpgrp(), StartedAt: time.Now()}
	watch := &announcingDriver{Driver: fake, seen: make(chan driver.Process, 4)}
	armed := make(chan bool, 1)
	fake.onStart = func(cfg driver.SessionConfig) {
		assert.NotNil(t, cfg.Started, "the dispatcher asks to be told when the worker exists")
	}
	fake.handshake = func(cfg driver.SessionConfig) error {
		// Inside NewSession, after the worker's process exists: by here the
		// dispatcher has been told, and the socket takes a connection from
		// the worker's group.
		token, err := takeTaskToken(declaredSocket(cfg), handshakeTokenWait)
		armed <- err == nil && token != ""
		return nil
	}
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) {
		o.PrivateDir = tokenDir(t)
		o.Driver = watch
	})
	h.d.confirmGroupGone = func(driver.Process, time.Duration) error { return nil }
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	h.attemptsEnded(t, 1)

	require.Len(t, watch.seen, 1, "the worker is announced once, as it starts")
	assert.Equal(t, syscall.Getpgrp(), (<-watch.seen).PGID, "by the process group the token is served to")
	assert.True(t, <-armed, "and the socket was serving that group before the session was open")
}

// announcingDriver records what the driver under test was asked to announce,
// and passes the announcement on. It is the seam the dispatcher's own
// ordering is read at: what it saw, and when.
type announcingDriver struct {
	driver.Driver
	seen chan driver.Process
}

func (d *announcingDriver) NewSession(ctx context.Context, cfg driver.SessionConfig) (driver.Session, error) {
	started := cfg.Started
	cfg.Started = func(p driver.Process) {
		d.seen <- p
		if started != nil {
			started(p)
		}
	}
	return d.Driver.NewSession(ctx, cfg)
}

// declaredSocket is the token socket the session's MCP server declaration
// names, which is the only place the worker learns it.
func declaredSocket(cfg driver.SessionConfig) string {
	if len(cfg.MCPServers) == 0 {
		return ""
	}
	args := cfg.MCPServers[0].Args
	return args[len(args)-1]
}

// takeTaskToken takes the task token from the connector's socket as the
// bridge does: dial, read one line, close.
func takeTaskToken(socket string, wait time.Duration) (string, error) {
	if socket == "" {
		return "", errors.New("the session declares no token socket")
	}
	deadline := time.Now().Add(wait)
	dialer := net.Dialer{Timeout: wait}
	conn, err := dialer.DialContext(context.Background(), "unix", socket)
	if err != nil {
		return "", fmt.Errorf("the connector's token socket: %w", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(deadline)
	line, err := bufio.NewReaderSize(conn, 256).ReadString('\n')
	token := strings.TrimSpace(line)
	if token == "" {
		if err == nil {
			err = errors.New("empty")
		}
		return "", fmt.Errorf("the connector handed over no token: %w", err)
	}
	return token, nil
}
