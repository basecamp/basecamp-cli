package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// JSON-RPC 2.0 over newline-delimited JSON, hand-rolled: ACP v1's stdio
// transport is one JSON object per line in each direction, and the surface
// the connector uses is a handful of methods. The community Go SDKs track the
// protocol's unstable drafts; a transcript of exactly what went over the wire
// is worth more here than their generated types.

// maxLine is the longest line the connector reads from an agent. A session/load
// replay or a large tool result can be long; a line past this ends the session
// rather than growing without bound.
const maxLine = 64 << 20

// JSON-RPC error codes the client sends.
const (
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

type wireMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *wireError      `json:"error,omitempty"`
}

type wireError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rpcError is an error response from the agent. Its message is the agent's
// text, so it is redacted and cut short before it becomes an error string.
type rpcError struct {
	Method  string
	Code    int
	Message string
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("acp: %s: agent error %d: %s", e.Method, e.Code, e.Message)
}

// errConnClosed is a call on a connection whose agent has stopped writing.
var errConnClosed = fmt.Errorf("%w: the agent closed its output", driver.ErrSessionEnded)

// conn is one JSON-RPC connection to an agent process.
type conn struct {
	w       io.Writer
	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan wireMessage
	closed  bool

	// onNotification runs on the reading goroutine, in wire order, so a mode
	// update is applied before the response that follows it is delivered.
	onNotification func(method string, params json.RawMessage)
	// onRequest runs on its own goroutine per request; it must answer with
	// reply or replyError.
	onRequest func(id json.RawMessage, method string, params json.RawMessage)

	done chan struct{}

	// trace, set only by this package's tests, sees every line in each
	// direction ("->" to the agent, "<-" from it).
	trace func(dir string, line []byte)
}

func newConn(w io.Writer) *conn {
	return &conn{w: w, pending: map[int64]chan wireMessage{}, done: make(chan struct{})}
}

// read dispatches lines until r ends, then fails every pending call.
func (c *conn) read(r io.Reader) {
	defer func() {
		c.mu.Lock()
		c.closed = true
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.mu.Unlock()
		close(c.done)
		// Drain what is left so the agent never blocks on a full pipe.
		_, _ = io.Copy(io.Discard, r)
	}()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), maxLine)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if c.trace != nil {
			c.trace("<-", line)
		}
		var m wireMessage
		if json.Unmarshal(line, &m) != nil || m.JSONRPC != "2.0" {
			continue
		}
		switch {
		case m.Method != "" && len(m.ID) > 0:
			if c.onRequest == nil {
				c.replyError(m.ID, codeMethodNotFound, "method not supported by this client")
				continue
			}
			go c.onRequest(m.ID, m.Method, m.Params)
		case m.Method != "":
			if c.onNotification != nil {
				c.onNotification(m.Method, m.Params)
			}
		default:
			id, err := strconv.ParseInt(string(m.ID), 10, 64)
			if err != nil {
				continue
			}
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
}

// call sends a request and decodes its result into out. A ctx that ends
// abandons the wait, not the request.
func (c *conn) call(ctx context.Context, method string, params, out any) error {
	p, err := c.start(method, params)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- p.wait(out) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		c.forget(p.id)
		return ctx.Err()
	}
}

// pendingCall is a request on the wire, waiting for its response.
type pendingCall struct {
	c      *conn
	id     int64
	method string
	ch     chan wireMessage
}

// start writes a request and returns its pending response.
func (c *conn) start(method string, params any) (*pendingCall, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errConnClosed
	}
	c.nextID++
	p := &pendingCall{c: c, id: c.nextID, method: method, ch: make(chan wireMessage, 1)}
	c.pending[p.id] = p.ch
	c.mu.Unlock()

	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": p.id, "method": method, "params": params}); err != nil {
		c.forget(p.id)
		return nil, fmt.Errorf("%w: %s: %w", driver.ErrSessionEnded, method, err)
	}
	return p, nil
}

// wait blocks until the response arrives or the connection ends.
func (p *pendingCall) wait(out any) error {
	m, ok := <-p.ch
	if !ok {
		return errConnClosed
	}
	if m.Error != nil {
		return &rpcError{Method: p.method, Code: m.Error.Code, Message: agentText(m.Error.Message)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(m.Result, out); err != nil {
		return fmt.Errorf("acp: %s: unreadable result: %w", p.method, err)
	}
	return nil
}

func (c *conn) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *conn) notify(method string, params any) error {
	return c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *conn) reply(id json.RawMessage, result any) {
	_ = c.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (c *conn) replyError(id json.RawMessage, code int, message string) {
	_ = c.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}

func (c *conn) send(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.trace != nil {
		c.trace("->", data)
	}
	if _, err := c.w.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

// closeWrite closes the agent's input, under the write lock so no line is cut.
func (c *conn) closeWrite(closer io.Closer) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = closer.Close()
}

// agentText is text the agent wrote, made fit for an error string: redacted
// (driver invariant 6), on one line, and short.
func agentText(s string) string {
	s = driver.Redact(s)
	out := make([]rune, 0, 120)
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			r = ' '
		}
		out = append(out, r)
		if len(out) >= 120 {
			break
		}
	}
	return string(out)
}
