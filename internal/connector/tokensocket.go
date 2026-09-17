package connector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
)

// # The task token's carriage to the worker's MCP server
//
// The agent starts the worker's MCP server, not the connector, and an agent
// hands a stdio server only its standard I/O: there is no descriptor to put a
// token on, and the environment and argv are where a token must never be. So
// the MCP server the agent starts is the connector's own bridge (`basecamp
// connect worker-mcp`), and the token reaches it over a one-use unix socket
// that the connector serves for that one attempt:
//
//  1. The socket is bound in the attempt's owner-only (0700) session
//     directory under the per-user runtime directory, so no other user can
//     reach its path.
//  2. It accepts exactly one connection, then closes and unlinks itself,
//     whatever that connection turns out to be. A second connection is
//     refused.
//  3. Before it writes anything it checks the peer's credentials with the
//     kernel (SO_PEERCRED on Linux, LOCAL_PEERCRED and LOCAL_PEERPID on
//     macOS): the peer must be this user, and its process must belong to the
//     worker — in the worker's process group, or a descendant of the worker
//     process, since an agent may start its MCP servers in groups of their
//     own (Codex does). Anything else is closed with no token.
//  4. It expires: if nothing connects within the window, it closes and
//     unlinks, and nothing is handed over.
//
// The bridge puts the token on a pipe and execs `basecamp mcp
// --connect-token-fd`, so after the handoff the token is in no environment, no
// argv and no file. A same-user process outside the worker's group that wins
// the race gets nothing and makes the real bridge fail, which the agent
// reports as a server that did not connect and the session ends as unsafe.
// A process inside the worker's group could take the token — but that is the
// worker, which is who the token is for.

// errUnreadableDescriptor is a socket whose descriptor is not a number the
// syscall wrappers take. It cannot happen on any platform the connector runs
// on; the check is here so no conversion is made on an assumption.
var errUnreadableDescriptor = errors.New("connector: the socket's descriptor is out of range")

// socketDescriptor is a raw connection's descriptor as the int the syscall
// wrappers take. A descriptor is a small non-negative index the kernel handed
// out, but Go hands it over as a uintptr, so the range is checked rather than
// assumed.
func socketDescriptor(fd uintptr) (int, bool) {
	if fd > math.MaxInt32 {
		return 0, false
	}
	return int(int32(fd)), true
}

// DefaultTokenWindow is how long a task token's socket waits for the worker's
// MCP server once the worker exists. It covers an agent's start-up, not a
// task's life, and it does not start until AllowGroup names the worker: a
// launcher or a handshake that takes its time must not spend the window of
// the worker it is still starting (card 23's review). The socket waits the
// same window for the worker to be named at all, so nothing waits forever.
const DefaultTokenWindow = 2 * time.Minute

// startWindows is how many windows the socket waits for the worker to be
// named at all. It is a backstop against a dispatcher that neither names a
// worker nor closes the socket, not a bound on a start: the dispatcher closes
// the socket on every path where a start fails.
const startWindows = 5

// TokenSocketName is the socket's name inside the attempt's session directory.
const TokenSocketName = "token.sock"

// MaxSocketPath is the longest unix socket path every supported platform
// takes: macOS's sun_path is 104 bytes, Linux's 108, both with a NUL.
const MaxSocketPath = 103

// TokenSocketFits reports whether a token socket in dir has a path a unix
// socket can carry.
func TokenSocketFits(dir string) bool {
	return len(filepath.Join(dir, TokenSocketName)) <= MaxSocketPath
}

// TokenSocketDir is where an attempt's token socket goes: its own session
// directory when a socket path there fits, and otherwise a directory of its
// own under shortBase. A unix socket path is 103 bytes at most, and a long
// home, a deep XDG_RUNTIME_DIR or large ids can put a session directory past
// it — which would fail every dispatch rather than one (card 22's review), so
// the connector moves the socket instead of refusing the task. The directory
// it makes is the caller's to remove: temporary is true when it made one.
//
// shortBase is the connector's own (ShortSocketBase), owner-only and swept on
// start, so a directory a crash leaves behind is cleared rather than kept
// forever. Everything else about the socket is unchanged wherever it lands:
// the directory is owner-only, the socket is 0600, and the peer must still be
// this user's process in the worker's group or below it.
func TokenSocketDir(preferred, shortBase string) (dir string, temporary bool, err error) {
	if TokenSocketFits(preferred) {
		return preferred, false, nil
	}
	if shortBase == "" {
		return "", false, fmt.Errorf("connector: a socket path under %s is longer than %d bytes and there is no short directory to use instead", preferred, MaxSocketPath)
	}
	// MkdirTemp makes it 0700, and the name is short on purpose.
	made, err := os.MkdirTemp(shortBase, "s")
	if err != nil {
		return "", false, fmt.Errorf("connector: token socket directory: %w", err)
	}
	if !TokenSocketFits(made) {
		_ = os.RemoveAll(made)
		return "", false, fmt.Errorf("connector: no directory on this machine takes a token socket path of %d bytes or less; %s and %s are both too deep", MaxSocketPath, preferred, shortBase)
	}
	return made, true, nil
}

// ShortSocketBase is the directory the connector keeps for token sockets that
// cannot live beside their session's own files: the per-user runtime
// directory where there is one, /tmp otherwise, under a short name of this
// connector's own (so two connectors never share one, and so a start can
// sweep what a crash left). It is created owner-only, through the same
// private-path check the session and state directories get.
//
// name is what makes it this connector's: the state directory's name, which
// carries the account and the agent.
func ShortSocketBase(name string, lookup func(string) (string, bool)) (string, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	base := "/tmp"
	if runtimeDir, ok := lookup("XDG_RUNTIME_DIR"); ok && filepath.IsAbs(runtimeDir) {
		if info, err := os.Stat(runtimeDir); err == nil && info.IsDir() {
			base = runtimeDir
		}
	}
	// Short on purpose: what is under it must still fit in 103 bytes. The
	// name is a digest of the connector's own, not the ids themselves, which
	// can be 19 digits each.
	sum := sha256.Sum256([]byte(name))
	dir := filepath.Join(base, "bcs-"+hex.EncodeToString(sum[:4]))
	if err := setup.EnsurePrivateDir(dir); err != nil {
		return "", fmt.Errorf("connector: the token socket directory cannot be used: %w", err)
	}
	if !TokenSocketFits(filepath.Join(dir, "s000000000")) {
		return "", fmt.Errorf("connector: %s is too deep for a token socket path of %d bytes or less", dir, MaxSocketPath)
	}
	return dir, nil
}

// Handoff says what became of a token socket.
type Handoff string

const (
	// HandoffDelivered: the worker's MCP server took the token.
	HandoffDelivered Handoff = "delivered"
	// HandoffRefused: something connected that was not the worker's own
	// process, and was given nothing.
	HandoffRefused Handoff = "refused"
	// HandoffExpired: nothing connected within the window.
	HandoffExpired Handoff = "expired"
	// HandoffClosed: the connector closed the socket first.
	HandoffClosed Handoff = "closed"
)

// PeerCredentials are what the kernel says about the other end of a unix
// socket connection.
type PeerCredentials struct {
	PID int
	UID int
}

// TokenSocket serves one task token, once, to the worker's own process group.
type TokenSocket struct {
	path     string
	token    string
	listener *net.UnixListener

	group   chan int
	setOnce sync.Once
	// handoff is what became of the socket, readable once done is closed.
	handoff Handoff
	done    chan struct{}
	stop    chan struct{}
	close   sync.Once

	// peer, groupOf, parentOf and lookup read the kernel; test seams.
	peer     func(*net.UnixConn) (PeerCredentials, error)
	groupOf  func(pid int) (int, error)
	parentOf func(pid int) (int, error)
	lookup   func(pid int) (driver.Process, error)

	mu    sync.Mutex
	taker driver.Process
}

// ServeTaskToken binds the one-use socket for token in dir, which must be the
// attempt's own owner-only directory, and serves it for window.
func ServeTaskToken(dir, token string, window time.Duration) (*TokenSocket, error) {
	return serveTaskToken(dir, token, window, peerCredentials, processGroupOf)
}

func serveTaskToken(dir, token string, window time.Duration, peer func(*net.UnixConn) (PeerCredentials, error), groupOf func(int) (int, error)) (*TokenSocket, error) {
	return serveTaskTokenWith(dir, token, window, peer, groupOf, parentProcessOf)
}

func serveTaskTokenWith(dir, token string, window time.Duration, peer func(*net.UnixConn) (PeerCredentials, error), groupOf, parentOf func(int) (int, error)) (*TokenSocket, error) {
	if token == "" {
		return nil, errors.New("connector: a token socket needs the token")
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("connector: token socket directory: %w", err)
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("connector: token socket directory %s must be a directory only its owner can enter", dir)
	}
	path := filepath.Join(dir, TokenSocketName)
	if len(path) > MaxSocketPath {
		return nil, fmt.Errorf("connector: token socket path %q is longer than a unix socket allows (%d)", path, MaxSocketPath)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("connector: token socket: %w", err)
	}
	listener.SetUnlinkOnClose(true)
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("connector: token socket: %w", err)
	}
	s := &TokenSocket{
		path: path, token: token, listener: listener,
		group: make(chan int, 1), done: make(chan struct{}), stop: make(chan struct{}),
		peer: peer, groupOf: groupOf, parentOf: parentOf, lookup: driver.LookupProcess,
	}
	go s.serve(window)
	return s, nil
}

// Path is where the bridge connects. It carries no secret.
func (s *TokenSocket) Path() string { return s.path }

// AllowGroup names the worker once it exists, by its process group — which,
// for a worker the connector started, is also the worker's own pid, since the
// worker leads its group. Until it is named, a connection waits for it,
// within the window; a group of 1 or less is never allowed.
func (s *TokenSocket) AllowGroup(pgid int) {
	s.setOnce.Do(func() { s.group <- pgid })
}

// Taker is the process that took the token, once one has. It is the worker's
// MCP server, which an agent may have started in a process group of its own
// (Codex does), so the connector keeps its identity: it is a process of the
// connector's own making, holding the task's token, and the release point
// ends it along with the worker.
func (s *TokenSocket) Taker() (driver.Process, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.taker, s.taker.PID > 0
}

// Close stops serving, if it still is. Idempotent.
func (s *TokenSocket) Close() {
	s.close.Do(func() {
		close(s.stop)
		_ = s.listener.Close()
	})
}

// Result waits for what became of the socket. Every caller gets the same
// answer, however many ask.
func (s *TokenSocket) Result() Handoff {
	<-s.done
	return s.handoff
}

// Settled waits up to wait for the socket to be finished with — the token
// handed over, refused, expired or the socket closed — and reports whether it
// is. It is what a caller asks before it reads Taker: a handoff in flight
// while the attempt is being released would otherwise leave the process
// holding the token unknown to the release point.
func (s *TokenSocket) Settled(wait time.Duration) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-s.done:
		return true
	case <-timer.C:
		return false
	}
}

// finish records what became of the socket, once.
func (s *TokenSocket) finish(h Handoff) {
	s.handoff = h
	close(s.done)
}

func (s *TokenSocket) serve(window time.Duration) {
	// Nothing is offered before the worker exists, and the window does not
	// run while it is being started. A connection that arrives first waits in
	// the listener's backlog, which is where the kernel keeps it.
	select {
	case want := <-s.group:
		s.group <- want
	case <-s.stop:
		s.finish(HandoffClosed)
		return
	case <-time.After(startWindows * window):
		s.Close()
		s.finish(HandoffExpired)
		return
	}
	deadline := time.Now().Add(window)
	_ = s.listener.SetDeadline(deadline)
	conn, err := s.listener.AcceptUnix()
	// One connection, whatever it is: the socket is gone before anything is
	// decided about it.
	s.Close()
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			s.finish(HandoffExpired)
		} else {
			s.finish(HandoffClosed)
		}
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(deadline)
	if !s.trusted(conn, deadline) {
		s.finish(HandoffRefused)
		return
	}
	if _, err := conn.Write([]byte(s.token + "\n")); err != nil {
		s.finish(HandoffRefused)
		return
	}
	s.rememberTaker(conn)
	s.finish(HandoffDelivered)
}

// trusted reports whether the peer is this user's process in the worker's
// own process group.
func (s *TokenSocket) trusted(conn *net.UnixConn, deadline time.Time) bool {
	cred, err := s.peer(conn)
	if err != nil || cred.UID != os.Getuid() || cred.PID <= 0 {
		return false
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	var want int
	select {
	case want = <-s.group:
		s.group <- want
	case <-ctx.Done():
		return false
	}
	if want <= 1 {
		return false
	}
	if got, err := s.groupOf(cred.PID); err == nil && got == want {
		return true
	}
	return s.descendsFrom(cred.PID, want)
}

// maxAncestry bounds the walk up a peer's parents.
const maxAncestry = 64

// descendsFrom reports whether pid is a descendant of ancestor.
func (s *TokenSocket) descendsFrom(pid, ancestor int) bool {
	for range maxAncestry {
		parent, err := s.parentOf(pid)
		if err != nil || parent <= 1 {
			return false
		}
		if parent == ancestor {
			return true
		}
		pid = parent
	}
	return false
}

// rememberTaker keeps the identity of the process the token went to, so the
// release point can end it: it is outside the worker's process group whenever
// the agent started it in one of its own.
func (s *TokenSocket) rememberTaker(conn *net.UnixConn) {
	cred, err := s.peer(conn)
	if err != nil || cred.PID <= 0 {
		return
	}
	taker, err := s.lookup(cred.PID)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.taker = taker
	s.mu.Unlock()
}
