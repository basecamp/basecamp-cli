package fakebasecamp

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// The live lane: stream tickets and the Action Cable connection they open,
// as eventfeed speaks it (its cable.go, and the loopback server in its
// scenario_harness_test.go).

const (
	cableSubprotocol = "actioncable-v1-json"
	cableChannel     = "EventsChannel"
	// cableWriteTimeout bounds one frame's write: a client that stops
	// reading for longer is dropped, as a server drops it.
	cableWriteTimeout = 10 * time.Second
	// streamTicketLifetime is how long a stream ticket opens the cable:
	// about 120 seconds, the SDK's StreamTicket says.
	streamTicketLifetime = 120 * time.Second
)

// streamTicket is who a ticket was minted for, and until when it opens the
// cable. A ticket is not spent by the connection it opens: the SDK's
// StreamTicket is "an opaque replayable bearer for its window", minted
// statelessly, so it opens any number of connections until it expires.
type streamTicket struct {
	caller  int64
	expires time.Time
}

// streamTicket mints a ticket for the caller, and the cable URL that
// carries it: a ws:// URL on the fake's own loopback port. Minting forgets
// the tickets that have expired.
func (c *call) streamTicket() answer {
	now := c.s.opts.now()
	maps.DeleteFunc(c.s.tickets, func(_ string, t streamTicket) bool { return !now.Before(t.expires) })
	ticket := fmt.Sprintf("fake-ticket-%d", c.s.nextSerialLocked())
	c.s.tickets[ticket] = streamTicket{caller: c.caller, expires: now.Add(streamTicketLifetime)}
	return c.jsonAnswer(http.StatusOK, map[string]any{
		"ticket":     ticket,
		"expires_in": int(streamTicketLifetime / time.Second),
		"url":        "ws" + strings.TrimPrefix(c.base(), "http") + "/cable?ticket=" + ticket,
	})
}

// ticketLocked is who the request's stream ticket was minted for, and
// whether it carried one the fake minted that has not expired.
func (s *Server) ticketLocked(req *http.Request) (int64, bool) {
	t, ok := s.tickets[req.URL.Query().Get("ticket")]
	if !ok || !s.opts.now().Before(t.expires) {
		return 0, false
	}
	return t.caller, true
}

// cableConn is one live connection.
type cableConn struct {
	s      *Server
	ws     *websocket.Conn
	caller int64
	// cancel ends the connection's context when it is detached, which
	// stops its reader, its writer and its pings. wake has a frame to
	// write.
	cancel context.CancelFunc
	wake   chan struct{}

	// Guarded by the Server's lock. subscriptions are the confirmed ones,
	// by identifier.
	subscriptions map[string]filters
	queue         [][]byte
	silent        bool
	detached      bool
}

// statusRecorder logs the status an upgrade answers before it is written,
// and hands the connection over to the WebSocket.
type statusRecorder struct {
	http.ResponseWriter
	logStatus func(int)
}

func (r *statusRecorder) WriteHeader(code int) {
	r.logStatus(code)
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// serveCable upgrades a ticketed request and serves it until either side
// ends it.
func (s *Server) serveCable(w http.ResponseWriter, req *http.Request) {
	c, ok := s.begin(RouteCable, w, req)
	if !ok {
		return
	}
	s.mu.Lock()
	caller, known := s.ticketLocked(req)
	s.mu.Unlock()
	if !known {
		s.finish(c, w, answer{status: http.StatusUnauthorized})
		return
	}
	rec := &statusRecorder{ResponseWriter: w, logStatus: func(status int) { s.setStatus(c.index, status) }}
	ws, err := websocket.Accept(rec, req, &websocket.AcceptOptions{Subprotocols: []string{cableSubprotocol}})
	if err != nil {
		return
	}
	if ws.Subprotocol() != cableSubprotocol {
		s.r.Errorf("fakebasecamp: a cable client did not negotiate %s", cableSubprotocol)
		_ = ws.CloseNow()
		return
	}

	// The connection outlives the request that opened it: it keeps the
	// request's values, and ends when either side closes it.
	ctx, cancel := context.WithCancel(context.WithoutCancel(req.Context()))
	cc := &cableConn{
		s: s, ws: ws, caller: caller, cancel: cancel, wake: make(chan struct{}, 1),
		subscriptions: map[string]filters{},
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		_ = ws.CloseNow()
		return
	}
	// httptest does not wait for a hijacked connection, so Close waits for
	// these three: this reader, the writer and the pings.
	s.cables.Add(3)
	s.conns[cc] = struct{}{}
	cc.enqueueLocked([]byte(`{"type":"welcome"}`))
	s.notifyLocked()
	s.mu.Unlock()

	go cc.write(ctx)
	go cc.ping(ctx)
	defer s.cables.Done()
	cc.read(ctx)
}

// read handles the client's commands until the connection ends.
func (cc *cableConn) read(ctx context.Context) {
	defer cc.drop()
	for {
		typ, data, err := cc.ws.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			cc.s.r.Errorf("fakebasecamp: a cable client sent a binary frame")
			continue
		}
		var cmd struct {
			Command    string `json:"command"`
			Identifier string `json:"identifier"`
		}
		if err := json.Unmarshal(data, &cmd); err != nil {
			cc.s.r.Errorf("fakebasecamp: a cable client sent a frame that is not a command: %q", data)
			continue
		}
		switch cmd.Command {
		case "subscribe":
			cc.subscribe(cmd.Identifier)
		case "unsubscribe":
			cc.s.mu.Lock()
			if _, ok := cc.subscriptions[cmd.Identifier]; ok {
				delete(cc.subscriptions, cmd.Identifier)
				cc.s.notifyLocked()
			}
			cc.s.mu.Unlock()
		default:
			cc.s.r.Errorf("fakebasecamp: a cable client sent the command %q", cmd.Command)
		}
	}
}

// subscribe confirms a subscription to the account's events, or rejects
// one the fake does not serve. As in Action Cable, a connection holds any
// number of subscriptions, one per identifier, each confirmed and pushed to
// on its own; subscribing again to an identifier already held answers
// nothing at all (Action Cable raises AlreadySubscribedError on the server,
// and sends the client no frame).
func (cc *cableConn) subscribe(identifier string) {
	var params map[string]any
	f, err := filters{}, json.Unmarshal([]byte(identifier), &params)
	if err == nil {
		f, err = subscriptionFilters(params)
	}
	cc.s.mu.Lock()
	defer cc.s.mu.Unlock()
	_, held := cc.subscriptions[identifier]
	switch {
	case err != nil:
		cc.s.r.Errorf("fakebasecamp: rejecting the cable subscription %s: %v", identifier, err)
		cc.enqueueLocked(cableFrame("reject_subscription", identifier))
	case held:
	default:
		cc.subscriptions[identifier] = f
		cc.enqueueLocked(cableFrame("confirm_subscription", identifier))
		cc.s.notifyLocked()
	}
}

// subscriptionFilters reads an EventsChannel identifier's filters. The
// fake serves the account lane only.
func subscriptionFilters(params map[string]any) (filters, error) {
	if params["channel"] != cableChannel {
		return filters{}, fmt.Errorf("the channel is not %s", cableChannel)
	}
	if _, inbox := params["inbox"]; inbox {
		return filters{}, fmt.Errorf("the fake serves the account lane, not the inbox")
	}
	var bad error
	f, err := parseFilters(func(key string) string {
		v, ok := params[key]
		if !ok {
			return ""
		}
		s, isString := v.(string)
		if !isString {
			bad = fmt.Errorf("%s is not a string", key)
		}
		return s
	})
	if bad != nil {
		return filters{}, bad
	}
	return f, err
}

func cableFrame(typ, identifier string) []byte {
	frame, _ := json.Marshal(map[string]string{"type": typ, "identifier": identifier})
	return frame
}

// pushLocked queues ev to every subscription that sees it.
func (s *Server) pushLocked(ev Event) {
	payload := pushPayload{pollRow: rowOf(ev), ActorType: ev.ActorType, VisibleToClients: ev.VisibleToClients}
	for cc := range s.conns {
		// In identifier order, so the frames' order does not hang on map
		// iteration.
		for _, identifier := range slices.Sorted(maps.Keys(cc.subscriptions)) {
			if !s.visibleLocked(ev, cc.subscriptions[identifier], cc.caller) {
				continue
			}
			frame, err := json.Marshal(struct {
				Identifier string      `json:"identifier"`
				Message    pushPayload `json:"message"`
			}{identifier, payload})
			if err != nil {
				s.r.Errorf("fakebasecamp: rendering event %d: %v", ev.ID, err)
				return
			}
			cc.enqueueLocked(frame)
		}
	}
}

// enqueueLocked queues a frame for the writer.
func (cc *cableConn) enqueueLocked(frame []byte) {
	if cc.detached {
		return
	}
	cc.queue = append(cc.queue, frame)
	select {
	case cc.wake <- struct{}{}:
	default:
	}
}

// write sends queued frames, in order, until the connection is detached.
func (cc *cableConn) write(ctx context.Context) {
	defer cc.s.cables.Done()
	for {
		select {
		case <-cc.wake:
		case <-ctx.Done():
			return
		}
		cc.s.mu.Lock()
		frames := cc.queue
		cc.queue = nil
		cc.s.mu.Unlock()
		for _, frame := range frames {
			writeCtx, cancel := context.WithTimeout(ctx, cableWriteTimeout)
			err := cc.ws.Write(writeCtx, websocket.MessageText, frame)
			cancel()
			if err != nil {
				cc.drop()
				return
			}
		}
	}
}

// ping queues a ping every interval, unless the connection was silenced.
func (cc *cableConn) ping(ctx context.Context) {
	defer cc.s.cables.Done()
	ticker := time.NewTicker(cc.s.opts.pingInterval)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			cc.s.mu.Lock()
			if !cc.silent {
				cc.enqueueLocked([]byte(fmt.Sprintf(`{"type":"ping","message":%d}`, now.Unix())))
			}
			cc.s.mu.Unlock()
		case <-ctx.Done():
			return
		}
	}
}

// drop detaches the connection and closes its socket.
func (cc *cableConn) drop() {
	cc.s.mu.Lock()
	cc.detachLocked()
	cc.s.notifyLocked()
	cc.s.mu.Unlock()
	cc.sever()
}

// detachLocked takes the connection out of the fake: nothing more is queued
// to it, and its writer and pings stop.
func (cc *cableConn) detachLocked() {
	if cc.detached {
		return
	}
	cc.detached = true
	delete(cc.s.conns, cc)
	cc.cancel()
}

// sever closes the socket without a closing handshake, as a dropped
// connection ends.
func (cc *cableConn) sever() { _ = cc.ws.CloseNow() }

// detachConnsLocked detaches every connection, and returns them to sever.
func (s *Server) detachConnsLocked() []*cableConn {
	conns := make([]*cableConn, 0, len(s.conns))
	for cc := range s.conns {
		conns = append(conns, cc)
	}
	for _, cc := range conns {
		cc.detachLocked()
	}
	return conns
}

// DropCable severs every live connection, and reports how many there were.
// A client reconnects with a fresh ticket, and catches up from the poll
// lane before it streams again.
func (s *Server) DropCable() int {
	s.mu.Lock()
	conns := s.detachConnsLocked()
	s.notifyLocked()
	s.mu.Unlock()
	for _, cc := range conns {
		cc.sever()
	}
	return len(conns)
}

// StopPings silences the live connections open now: they stay up, and stop
// pinging, until the client gives up on them. A connection opened later
// pings as usual.
func (s *Server) StopPings() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for cc := range s.conns {
		cc.silent = true
	}
}

// Subscribers is how many live connections hold a confirmed subscription
// now.
func (s *Server) Subscribers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for cc := range s.conns {
		if len(cc.subscriptions) > 0 {
			n++
		}
	}
	return n
}
