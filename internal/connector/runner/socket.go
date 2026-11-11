package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The runner's socket is how the commands an agent runs reach the connector
// that started it: `basecamp connect dispatch`, `workers`, `say`, `stop`.
// It is a unix socket in a directory only this user can enter, which is the
// whole of its access control: whoever can connect is the person the
// connector already runs as.

// Operations a Request names.
const (
	OpDispatch   = "dispatch"
	OpWorkers    = "workers"
	OpSend       = "send"
	OpStopWorker = "stop_worker"
	OpSay        = "say"
	OpStop       = "stop"
)

// Request is one call on the socket.
type Request struct {
	Op       string          `json:"op"`
	Dispatch DispatchRequest `json:"dispatch,omitzero"`
	// Worker is the worker a send or a stop names.
	Worker string `json:"worker,omitempty"`
	// Message is what a send tells a worker, or what say tells the
	// orchestrator.
	Message string `json:"message,omitempty"`
	// Restart asks a stop to start the connector again.
	Restart bool `json:"restart,omitempty"`
}

// Response answers a Request.
type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`

	Worker *WorkerView `json:"worker,omitempty"`
	// Existing says a dispatch returned the worker its event already had.
	Existing bool         `json:"existing,omitempty"`
	Workers  []WorkerView `json:"workers,omitempty"`
	// Reply is the orchestrator's answer to say.
	Reply  string  `json:"reply,omitempty"`
	Status *Status `json:"status,omitempty"`
}

// Status is the runner as `basecamp connect workers list` and a person
// checking on it see it.
type Status struct {
	Profile      string `json:"profile"`
	Orchestrator Spec   `json:"orchestrator"`
	// Session is the orchestrator's session id, once its CLI confirmed one.
	Session string `json:"session,omitempty"`
	Turns   int    `json:"turns"`
	// Worker is the agent a request runs on when no rule says otherwise.
	Worker Spec `json:"worker"`
	// Waiting counts messages the orchestrator has not handled yet.
	Waiting int `json:"waiting"`
	// SetAside counts messages it failed to handle after every retry.
	SetAside int `json:"set_aside"`
	Running  int `json:"running"`
	Queued   int `json:"queued"`
	// ConfigError says runner.json cannot be read as written, and why. The
	// worker shown is then not the one a request would get: no agent is
	// started until the file is fixed.
	ConfigError string `json:"config_error,omitempty"`
}

// maxRequestBytes bounds one request: a handoff carries a project's
// AGENTS.md, and nothing larger.
const maxRequestBytes = 4 << 20

// requestReadTimeout bounds how long a client has to send its request.
const requestReadTimeout = 30 * time.Second

// maxSocketPath is the longest path a unix socket address is given. The
// kernel's own limit is 104 bytes on macOS and 108 on Linux.
const maxSocketPath = 100

// socketPointer is the file in a runner's directory that says where its
// socket is when that is not in the directory itself.
const socketPointer = "sock.path"

// SocketPath is where the runner in dir listens, for whoever calls it. It is
// inside dir, unless that path is longer than a socket address can be. Then
// it is where the running connector said it listens (socketPointer), so a
// caller whose environment is not the connector's still finds it, and
// failing that where a start in this environment would listen.
func SocketPath(dir string) string {
	path := filepath.Join(dir, "sock")
	if len(path) <= maxSocketPath {
		return path
	}
	if raw, err := os.ReadFile(filepath.Join(dir, socketPointer)); err == nil { //nolint:gosec // G304: the runner's own directory
		if kept := strings.TrimSpace(string(raw)); filepath.IsAbs(kept) && len(kept) <= maxSocketPath {
			return kept
		}
	}
	return fallbackSocket(dir)
}

// listenPath is where a start in dir listens: inside dir, or, for a path
// too long, a short one named for dir.
func listenPath(dir string) string {
	if path := filepath.Join(dir, "sock"); len(path) <= maxSocketPath {
		return path
	}
	return fallbackSocket(dir)
}

// fallbackSocket is a short socket path named for dir: in the user's
// runtime directory where there is one and the path fits, which is theirs
// alone, and otherwise in a directory named for them in the temporary
// directory. listen and Call hold either to checkSocketDir.
func fallbackSocket(dir string) string {
	sum := sha256.Sum256([]byte(dir))
	name := hex.EncodeToString(sum[:8]) + ".sock"
	if runtime := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(runtime) {
		if path := filepath.Join(runtime, "basecamp-connect", name); len(path) <= maxSocketPath {
			return path
		}
	}
	return filepath.Join(os.TempDir(), "basecamp-connect-"+strconv.Itoa(os.Geteuid()), name)
}

// keepSocketPath records where the runner in dir listens, for SocketPath:
// a pointer when the socket is not in dir, and none when it is.
func keepSocketPath(dir, socket string) error {
	pointer := filepath.Join(dir, socketPointer)
	if filepath.Dir(socket) == dir {
		if err := os.Remove(pointer); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	tmp, err := os.CreateTemp(dir, "."+socketPointer+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(socket + "\n"); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), pointer)
}

// listen opens the socket. A socket file an earlier run left is removed
// first: the connector's instance lock already says no other runner is
// listening on it.
func listen(ctx context.Context, path string) (net.Listener, error) {
	// Made if it is not there, and never adopted as it is found: a
	// directory someone else made first, or a link put in its place, is
	// refused rather than given this user's permissions.
	dir := filepath.Dir(path)
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if err := checkSocketDir(dir); err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}

// serve answers calls until the listener closes.
func (r *Runner) serve(ctx context.Context, listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil && (errors.Is(err, net.ErrClosed) || ctx.Err() != nil) {
			return
		}
		if err != nil {
			// Out of descriptors, say: the socket is still bound, and
			// giving up on it would leave every later call hanging.
			r.log.Warn("runner: a call could not be accepted; trying again", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}
		r.wg.Go(func() {
			defer conn.Close()
			// A stop ends the call, whatever it is waiting on, and a client
			// that sends half a request does not hold the connector open.
			stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
			defer stop()
			select {
			case <-r.ready:
			case <-ctx.Done():
				return
			}
			var req Request
			_ = conn.SetReadDeadline(time.Now().Add(requestReadTimeout))
			if err := json.NewDecoder(io.LimitReader(conn, maxRequestBytes)).Decode(&req); err != nil {
				_ = json.NewEncoder(conn).Encode(Response{Error: "the request could not be read: " + err.Error()})
				return
			}
			_ = conn.SetReadDeadline(time.Time{})
			_ = json.NewEncoder(conn).Encode(r.handle(ctx, req))
		})
	}
}

func (r *Runner) handle(ctx context.Context, req Request) Response {
	fail := func(err error) Response { return Response{Error: err.Error()} }
	switch req.Op {
	case OpDispatch:
		view, existing, err := r.Dispatch(ctx, req.Dispatch)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Worker: &view, Existing: existing}
	case OpWorkers:
		status := r.Status()
		return Response{OK: true, Workers: r.Workers(), Status: &status}
	case OpSend:
		view, err := r.Send(req.Worker, req.Message) //nolint:contextcheck // a worker runs under the run's context, not the call's
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Worker: &view}
	case OpStopWorker:
		view, err := r.StopWorker(req.Worker)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Worker: &view}
	case OpSay:
		reply, err := r.Say(ctx, req.Message)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Reply: reply}
	case OpStop:
		if r.opts.Stop == nil {
			return fail(errors.New("this connector cannot be stopped from here"))
		}
		// Answer first: the caller is usually a command the orchestrator
		// ran, in a turn the stop is about to end.
		time.AfterFunc(200*time.Millisecond, func() { r.opts.Stop(req.Restart) })
		return Response{OK: true}
	}
	return fail(fmt.Errorf("unknown operation %q", req.Op))
}

// Say gives the orchestrator a message from the person at this computer and
// waits for its answer.
func (r *Runner) Say(ctx context.Context, message string) (string, error) {
	if message == "" {
		return "", errors.New("the message is empty")
	}
	reply := make(chan sayReply, 1)
	r.mu.Lock()
	id := r.addItem(kindOwner, 0, message)
	r.replies[id] = reply
	err := r.st.save(r.statePath())
	if err != nil {
		// Not kept, so not sent: the caller is told it failed, and a retry
		// must not find the first one still waiting to be answered.
		r.st.Inbox = slices.DeleteFunc(r.st.Inbox, func(it item) bool { return it.ID == id })
		delete(r.replies, id)
	}
	r.mu.Unlock()
	if err != nil {
		return "", err
	}
	r.signal()
	select {
	case answer := <-reply:
		return answer.text, answer.err
	case <-ctx.Done():
		return "", errors.New("the connector stopped before the orchestrator answered; the message is kept and is answered in its log when it starts again")
	}
}

// Status is the runner as it is now.
func (r *Runner) Status() Status {
	config, configErr := r.config()
	r.mu.Lock()
	defer r.mu.Unlock()
	status := Status{
		Profile: r.opts.Profile, Orchestrator: r.st.Orchestrator.Spec, Session: r.st.Orchestrator.ID,
		Turns: r.st.Orchestrator.Turns, Worker: r.baseWorker(config),
		Waiting: len(r.st.Inbox), SetAside: len(r.st.Dropped),
	}
	if configErr != nil {
		status.ConfigError = configErr.Error()
	}
	for _, w := range r.st.Workers {
		switch w.State {
		case workerRunning:
			status.Running++
		case workerQueued:
			status.Queued++
		}
	}
	return status
}

// ErrNotRunning is a call on a socket nothing listens on: no connector is
// running with an orchestrator for the profile.
var ErrNotRunning = errors.New("no connector is running with an orchestrator for this profile")

// Call makes one call on the runner listening at path (SocketPath). A zero
// timeout waits as long as the answer takes, which for say is as long as the
// orchestrator's turn.
func Call(ctx context.Context, path string, req Request, timeout time.Duration) (Response, error) {
	// A socket is only as trustworthy as the directory it is in: one another
	// user could have put there is not called.
	switch err := checkSocketDir(filepath.Dir(path)); {
	case errors.Is(err, os.ErrNotExist):
		return Response{}, fmt.Errorf("%w (%w)", ErrNotRunning, err)
	case err != nil:
		return Response{}, err
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return Response{}, fmt.Errorf("%w (%w)", ErrNotRunning, err)
	}
	defer conn.Close()
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, fmt.Errorf("the connector did not answer: %w", err)
	}
	if !resp.OK {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}
