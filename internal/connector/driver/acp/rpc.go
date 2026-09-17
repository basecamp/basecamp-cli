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
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// JSON-RPC 2.0 over newline-delimited JSON, hand-rolled: ACP v1's stdio
// transport is one JSON object per line in each direction, and the surface
// the connector uses is a handful of methods. The community Go SDKs track the
// protocol's unstable drafts; a transcript of exactly what went over the wire
// is worth more here than their generated types.

// maxLine is the longest line the connector reads from an agent. A session/load
// replay or a large tool result can be long; a line past this ends the session
// rather than growing without bound.
// A variable so tests need not write one.
var maxLine = 64 << 20

// maxHandlers bounds the agent requests answered at once.
const maxHandlers = 16

// JSON-RPC error codes the client sends.
const (
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	// codeBusy is JSON-RPC's implementation-defined server error range.
	codeBusy = -32000
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
	// onBusy hears a request refused at the handler bound, before its answer
	// is written, so the refusal is on the record.
	onBusy func(method string, params json.RawMessage)
	// onRequest runs on its own goroutine per request; it must answer with
	// reply or replyError.
	onRequest func(id json.RawMessage, method string, params json.RawMessage)

	// handlers bounds the requests being answered at once: a flood of them
	// spawns no more than this many goroutines, and the rest are refused as
	// they are read.
	handlers chan struct{}

	done chan struct{}

	// trace, set only by this package's tests, sees every line in each
	// direction ("->" to the agent, "<-" from it).
	trace func(dir string, line []byte)
}

func newConn(w io.Writer) *conn {
	return &conn{
		w: w, pending: map[int64]chan wireMessage{},
		handlers: make(chan struct{}, maxHandlers),
		done:     make(chan struct{}),
	}
}

// read dispatches lines until r ends, then fails every pending call. It
// returns the scanner's error: a line past maxLine, or a failed read.
func (c *conn) read(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), maxLine)
	defer func() {
		c.mu.Lock()
		c.closed = true
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.mu.Unlock()
		close(c.done)
	}()
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
			select {
			case c.handlers <- struct{}{}:
			default:
				// Already answering as many as this client answers at once.
				if c.onBusy != nil {
					c.onBusy(m.Method, m.Params)
				}
				c.replyError(m.ID, codeBusy, "too many requests at once")
				continue
			}
			id, method, params := m.ID, m.Method, m.Params
			go func() {
				defer func() { <-c.handlers }()
				c.onRequest(id, method, params)
			}()
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
	return scanner.Err()
}

// call sends a request and decodes its result into out. A ctx that ends
// abandons the wait, not the request; out is written only when the result is
// delivered to this caller.
func (c *conn) call(ctx context.Context, method string, params, out any) error {
	p := c.register(method)
	type answer struct {
		raw json.RawMessage
		err error
	}
	answers := make(chan answer, 1)
	go func() {
		// The write is on this goroutine too: an agent that has stopped
		// reading its input would otherwise hold the caller past its context.
		if err := c.sendCall(p, params); err != nil {
			answers <- answer{nil, err}
			return
		}
		raw, err := p.result()
		answers <- answer{raw, err}
	}()
	select {
	case a := <-answers:
		if a.err != nil || out == nil {
			return a.err
		}
		if err := json.Unmarshal(a.raw, out); err != nil {
			return fmt.Errorf("acp: %s: unreadable result: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		c.abandon(p)
		return ctx.Err()
	}
}

// pendingCall is a request on the wire, waiting for its response.
type pendingCall struct {
	id     int64
	method string
	ch     chan wireMessage
}

// register reserves an id and a response slot for a request not yet sent. On
// a closed connection the slot is already closed.
func (c *conn) register(method string) *pendingCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	p := &pendingCall{id: c.nextID, method: method, ch: make(chan wireMessage, 1)}
	if c.closed {
		close(p.ch)
	} else {
		c.pending[p.id] = p.ch
	}
	return p
}

// sendCall writes a registered request.
func (c *conn) sendCall(p *pendingCall, params any) error {
	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": p.id, "method": p.method, "params": params}); err != nil {
		c.abandon(p)
		return fmt.Errorf("%w: %s: %w", driver.ErrSessionEnded, p.method, err)
	}
	return nil
}

// result blocks until the response arrives, the call is abandoned, or the
// connection ends.
func (p *pendingCall) result() (json.RawMessage, error) {
	m, ok := <-p.ch
	if !ok {
		return nil, errConnClosed
	}
	if m.Error != nil {
		return nil, &rpcError{Method: p.method, Code: m.Error.Code, Message: agentText(m.Error.Message)}
	}
	return m.Result, nil
}

// wait is result decoded into out.
func (p *pendingCall) wait(out any) error {
	raw, err := p.result()
	if err != nil || out == nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("acp: %s: unreadable result: %w", p.method, err)
	}
	return nil
}

// abandon stops waiting for a call: its slot is closed, so whoever waits on
// it gets errConnClosed, and a response that arrives later is dropped.
func (c *conn) abandon(p *pendingCall) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.pending[p.id]; ok {
		delete(c.pending, p.id)
		close(ch)
	}
}

// notifyIf writes a notification only if still() holds once the write lock is
// taken: a notification that waited behind a stuck write is dropped if what
// it was about has ended while it waited.
func (c *conn) notifyIf(still func() bool, method string, params any) error {
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if !still() {
		return nil
	}
	if c.trace != nil {
		c.trace("->", data)
	}
	_, err = c.w.Write(append(data, '\n'))
	return err
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

// closeWrite closes the agent's input. Not under the write lock: a write
// stuck on a full pipe holds that lock, and closing the pipe is what unblocks
// it.
func (c *conn) closeWrite(closer io.Closer) {
	_ = closer.Close()
}

// agentText is text the agent wrote, made fit for an error string that ends
// up in a log: redacted (driver invariant 6), stripped of the escapes and
// controls a terminal would act on, on one line, and short.
func agentText(s string) string {
	// Cut first: a line from the agent may be megabytes, and none of it past
	// the first few hundred bytes reaches the error anyway.
	if len(s) > 4<<10 {
		s = s[:4<<10]
	}
	out := []rune(richtext.SanitizeSingleLine(driver.Redact(s)))
	if len(out) > 120 {
		out = out[:120]
	}
	return string(out)
}
