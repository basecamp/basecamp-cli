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
// connect worker-mcp`), and the token reaches it over a unix socket the
// connector serves for that one attempt:
//
//  1. The socket is bound in the attempt's owner-only (0700) session
//     directory under the per-user runtime directory, so no other user can
//     reach its path.
//  2. It serves ONE handoff per start of the worker's MCP server, up to
//     MaxTokenHandoffs. An MCP host that restarts a stdio server re-runs its
//     command, and the bridge takes the token again on every start, so a
//     socket that closed after the first handoff would leave a restarted
//     server with no Basecamp tools and no way to say so. Anything but a
//     delivery — a peer that is not the worker's, a window that runs out —
//     ends the socket there and then.
//  3. Between handoffs the socket does not accept. After a delivery it waits
//     for the process that took the token to be gone before it will hand the
//     token to anything again (ProcessGone on the recorded taker), because
//     that is exactly what a restart is: while the server that holds the
//     token lives, nothing else may ask for it. Only where the taker's
//     identity could not be read does it fall back to arming for one more
//     window.
//  4. Before it writes anything it checks the peer's credentials with the
//     kernel (SO_PEERCRED on Linux, LOCAL_PEERCRED and LOCAL_PEERPID on
//     macOS), on every handoff and not only the first: the peer must be this
//     user, and its process must belong to the worker — in the worker's
//     process group, or a descendant of the worker process, since an agent
//     may start its MCP servers in groups of their own (Codex does).
//     Anything else is closed with no token.
//  5. It expires: if nothing connects within the window, it closes and
//     unlinks, and nothing is handed over. The release point closes it too,
//     so no handoff outlives its attempt.
//
// The bridge puts the token on a pipe and execs `basecamp mcp
// --connect-token-fd`, so after the handoff the token is in no environment, no
// argv and no file. A same-user process outside the worker's group that wins
// the race gets nothing and makes the real bridge fail, which the agent
// reports as a server that did not connect and the session ends as unsafe.
//
// Where this can still be broken: a process inside the worker's group can
// take the token — but that is the worker, which is who the token is for. An
// agent's own tools run in that group, so an agent that goes looking can ask
// for the token while the socket is armed: at the start of the session, and
// after its MCP server has died, which is the window rule (3) exists to keep
// short. What it gets is a token for the tools it already has.

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
	// In order, and the first that takes a socket path wins: the per-user
	// runtime directory is the right home, but a deep one is exactly the
	// case this exists for, so /tmp remains the escape hatch.
	var bases []string
	if runtimeDir, ok := lookup("XDG_RUNTIME_DIR"); ok && filepath.IsAbs(runtimeDir) {
		bases = append(bases, runtimeDir)
	}
	bases = append(bases, os.TempDir(), "/tmp")

	// Short on purpose: what is under it must still fit in 103 bytes. The
	// name is a digest of the connector's own, not the ids themselves, which
	// can be 19 digits each.
	sum := sha256.Sum256([]byte(name))
	short := "bcs-" + hex.EncodeToString(sum[:4])
	var last error
	for _, base := range bases {
		if info, err := os.Stat(base); err != nil || !info.IsDir() {
			continue
		}
		dir := filepath.Join(base, short)
		// MkdirTemp appends a random uint32 in decimal, so the longest name
		// it can make under this prefix is "s" and ten digits.
		if !TokenSocketFits(filepath.Join(dir, "s0123456789")) {
			last = fmt.Errorf("connector: %s is too deep for a token socket path of %d bytes or less", dir, MaxSocketPath)
			continue
		}
		if err := setup.EnsurePrivateDir(dir); err != nil {
			last = fmt.Errorf("connector: the token socket directory cannot be used: %w", err)
			continue
		}
		return dir, nil
	}
	if last == nil {
		last = errors.New("connector: no directory on this machine can hold a token socket")
	}
	return "", last
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
	// HandoffUndelivered: the peer was the worker's and the connector could
	// not write the token to it — the host killed its server between the
	// connect and the read, say. It is not a refusal (nothing untrusted
	// asked) and not fatal: the socket arms again for the next start.
	HandoffUndelivered Handoff = "undelivered"
	// HandoffSpent: the worker's MCP server started more times than the
	// connector serves its token (MaxTokenHandoffs). A start after this one
	// comes up without a token, and its Basecamp tools fail; no adapter
	// reports that on the wire (card 23 measured both), so this is the only
	// place it can be seen.
	HandoffSpent Handoff = "spent"
)

// PeerCredentials are what the kernel says about the other end of a unix
// socket connection.
type PeerCredentials struct {
	PID int
	UID int
}

// TokenSocket serves one task token to the worker's own process group, once
// per start of the worker's MCP server.
type TokenSocket struct {
	path     string
	token    string
	listener *net.UnixListener

	group   chan int
	setOnce sync.Once
	// handoff is what became of the socket's first handoff, readable once
	// done is closed; ended is closed when no handoff is in flight or to
	// come.
	handoff   Handoff
	firstOnce sync.Once
	done      chan struct{}
	ended     chan struct{}
	stop      chan struct{}
	close     sync.Once

	// peer, groupOf, parentOf and lookup read the kernel; test seams.
	peer     func(*net.UnixConn) (PeerCredentials, error)
	groupOf  func(pid int) (int, error)
	parentOf func(pid int) (int, error)
	lookup   func(pid int) (driver.Process, error)

	mu        sync.Mutex
	taker     driver.Process
	onHandoff func(Handoff, driver.Process, bool)
}

// ServeTaskToken binds the socket for token in dir, which must be the
// attempt's own owner-only directory, and serves it for window.
func ServeTaskToken(dir, token string, window time.Duration) (*TokenSocket, error) {
	return serveTaskToken(dir, token, window, peerCredentials, processGroupOf)
}

func serveTaskToken(dir, token string, window time.Duration, peer func(*net.UnixConn) (PeerCredentials, error), groupOf func(int) (int, error)) (*TokenSocket, error) {
	return serveTaskTokenWith(dir, token, window, peer, groupOf, parentProcessOf, driver.LookupProcess)
}

func serveTaskTokenWith(dir, token string, window time.Duration, peer func(*net.UnixConn) (PeerCredentials, error), groupOf, parentOf func(int) (int, error), lookup func(int) (driver.Process, error)) (*TokenSocket, error) {
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
		group: make(chan int, 1), done: make(chan struct{}), ended: make(chan struct{}), stop: make(chan struct{}),
		peer: peer, groupOf: groupOf, parentOf: parentOf, lookup: lookup,
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

// MaxTokenHandoffs is how many times one attempt's token may be handed over.
// An MCP host that restarts a stdio server re-runs its command, and the
// bridge takes the token again on every start, so a socket that served once
// and closed would leave a restarted server with no Basecamp tools and no
// way to say so.
//
// Five, deliberately, and not more: the socket only arms again once the
// server that holds the token is gone, so the rate is already the rate at
// which that server dies, and this bound is not about rate. It is about when
// an attempt's socket ends. A server that has restarted five times in one
// task is not going to settle down, and the connector should stop offering
// its token rather than keep a socket armed for the rest of a long task —
// every moment it is armed is a moment the agent's own tools, which run in
// the worker's group, could ask for the token instead.
//
// Exhaustion is loud rather than quiet: no adapter tells its client that a
// restarted MCP server came up without a token (card 23 measured both), so
// the socket reports HandoffSpent and the connector warns against the
// attempt. A person sees a worker whose tools stopped working and why.
const MaxTokenHandoffs = 5

// Result waits for what became of the socket's FIRST handoff. Every caller
// gets the same answer, however many ask. Later handoffs are reported to the
// function OnHandoff was given.
func (s *TokenSocket) Result() Handoff {
	<-s.done
	return s.handoff
}

// OnHandoff is called for every handoff the socket makes or refuses, with the
// process that took the token where one did. It is set before the worker is
// named, and is how the connector keeps up with a restarted MCP server.
func (s *TokenSocket) OnHandoff(f func(handoff Handoff, taker driver.Process, afterADelivery bool)) {
	s.mu.Lock()
	s.onHandoff = f
	s.mu.Unlock()
}

// Settled waits up to wait for the socket to be finished with for good — no
// handoff in flight and none to come — and reports whether it is. It is what
// a caller asks before it reads Taker: a handoff still deciding while the
// attempt is released would otherwise leave the process holding the token
// unknown to the release point. Close first, or this waits out the window.
func (s *TokenSocket) Settled(wait time.Duration) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-s.ended:
		return true
	case <-timer.C:
		return false
	}
}

// waitForTakerGone waits for the process that took the token to be gone,
// which is what a restart of the worker's MCP server looks like from here. It
// reports whether the socket should arm again. The wait itself has no
// deadline — MaxTokenHandoffs is what bounds the socket, not a clock — so the
// only false is a socket that was closed.
//
// A taker whose identity could not be read cannot be waited for, so the
// socket arms for one more window instead — the same bound as the first
// handoff.
func (s *TokenSocket) waitForTakerGone() bool {
	s.mu.Lock()
	taker := s.taker
	s.mu.Unlock()
	if taker.PID <= 0 {
		return true
	}
	wait := takerPoll
	errors := 0
	for {
		timer := time.NewTimer(wait)
		select {
		case <-s.stop:
			timer.Stop()
			return false
		case <-timer.C:
		}
		// The poll backs off: a task runs for hours, and asking the kernel
		// about one process every second for all of it is a cost with no
		// reader.
		if wait < takerPollMax {
			wait *= 2
		}
		gone, err := driver.ProcessGone(taker)
		switch {
		case err == nil && gone:
			// The server that held the token is gone; the next start of it is
			// what the socket arms for.
			return true
		case err == nil:
			errors = 0
		default:
			// A kernel this process cannot read cannot answer whether that
			// server is gone. Waiting forever on an unanswerable question
			// would leave a restarted server with no token and say nothing,
			// so after a while the socket arms as it does for a taker whose
			// identity it never had.
			errors++
			if errors >= takerErrorLimit {
				return true
			}
		}
	}
}

const (
	// takerPoll is how soon the socket first looks to see whether the process
	// that took the token is gone, and takerPollMax how far that backs off.
	takerPoll    = time.Second
	takerPollMax = 15 * time.Second
	// takerErrorLimit is how many times running the question past the kernel
	// may fail before the socket stops waiting for an answer.
	takerErrorLimit = 10
)

// handed records one handoff: the first is what Result answers, and every one
// goes to OnHandoff's function. after says whether a delivery had already
// been made, so a terminal handoff on a healthy attempt is not reported as a
// worker that never took its token.
func (s *TokenSocket) handed(h Handoff, taker driver.Process, after bool) {
	s.mu.Lock()
	switch {
	case taker.PID > 0:
		s.taker = taker
	case h == HandoffDelivered:
		// The token is out and the connector could not say to whom: keeping
		// the last taker would have the socket waiting on a process that is
		// not the one holding the token, and the release point ending the
		// wrong thing (Opus r9). Nothing is better than something wrong.
		s.taker = driver.Process{}
	}
	f := s.onHandoff
	s.mu.Unlock()
	s.firstOnce.Do(func() {
		s.handoff = h
		close(s.done)
	})
	if f != nil {
		f(h, taker, after)
	}
}

func (s *TokenSocket) serve(window time.Duration) {
	defer close(s.ended)
	// Nothing is offered before the worker exists, and the window does not
	// run while it is being started. A connection that arrives first waits in
	// the listener's backlog, which is where the kernel keeps it.
	select {
	case want := <-s.group:
		s.group <- want
	case <-s.stop:
		s.handed(HandoffClosed, driver.Process{}, false)
		return
	case <-time.After(startWindows * window):
		s.Close()
		s.handed(HandoffExpired, driver.Process{}, false)
		return
	}
	// One handoff per start of the worker's MCP server, up to
	// MaxTokenHandoffs: a host that restarts a stdio server re-runs it, and
	// the bridge takes the token again. Each gets the same peer checks, and
	// anything but a delivery ends the socket — a connection that is not the
	// worker's is not something to wait past.
	delivered := false
	for range MaxTokenHandoffs {
		if delivered && !s.waitForTakerGone() {
			// Closed, or the process that took the token is still running:
			// nothing else may have it while that server lives.
			return
		}
		h, taker := s.handOne(window)
		s.handed(h, taker, delivered)
		switch h {
		case HandoffDelivered:
			delivered = true
		case HandoffUndelivered:
			// Nothing was handed over and nothing untrusted asked: the next
			// start of the server is still owed its token.
		default:
			s.Close()
			return
		}
	}
	// The budget is spent: a worker whose MCP server restarts more often than
	// this is not one the connector keeps handing its token to, and the next
	// start of it will have no Basecamp tools. Nothing else would say so.
	s.handed(HandoffSpent, driver.Process{}, true)
	s.Close()
}

// handOne waits for one connection within its own window and hands the token
// over, or says why it did not.
func (s *TokenSocket) handOne(window time.Duration) (Handoff, driver.Process) {
	deadline := time.Now().Add(window)
	_ = s.listener.SetDeadline(deadline)
	conn, err := s.listener.AcceptUnix()
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return HandoffExpired, driver.Process{}
		}
		return HandoffClosed, driver.Process{}
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(deadline)
	if !s.trusted(conn, deadline) {
		return HandoffRefused, driver.Process{}
	}
	if _, err := conn.Write([]byte(s.token + "\n")); err != nil {
		// The peer was the worker's; the write is what failed. On a unix
		// socket a peer that has gone makes this EPIPE at once.
		return HandoffUndelivered, driver.Process{}
	}
	return HandoffDelivered, s.takerOfConn(conn)
}

// allowedGroup is the worker's process group, or 0 before it is named.
func (s *TokenSocket) allowedGroup() int {
	select {
	case want := <-s.group:
		s.group <- want
		return want
	default:
		return 0
	}
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

// takerOfConn is the identity of the process the token just went to, so the
// release point can end it: it is outside the worker's process group whenever
// the agent started it in one of its own. A restarted MCP server is a new
// process, and the newest is the one holding the token.
func (s *TokenSocket) takerOfConn(conn *net.UnixConn) driver.Process {
	cred, err := s.peer(conn)
	if err != nil || cred.PID <= 0 {
		return driver.Process{}
	}
	taker, err := s.lookup(cred.PID)
	if err != nil {
		return driver.Process{}
	}
	// The group read here is the one the release point would signal, and it
	// is a second reading of the kernel: it must still satisfy the rule the
	// peer passed, or this attempt does not own it (Opus r8).
	want := s.allowedGroup()
	if want <= 1 || (taker.PGID != want && !s.descendsFrom(taker.PID, want)) {
		return driver.Process{}
	}
	return taker
}
