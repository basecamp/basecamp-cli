package fakebasecamp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Reporter is the part of testing.TB the fake needs: where to report a
// request it could not make sense of, and where to register its own Close.
type Reporter interface {
	Helper()
	Errorf(format string, args ...any)
	Cleanup(func())
}

// Route names one endpoint the fake serves, as the request pattern it is
// mounted on.
type Route string

// The routes the fake serves.
const (
	RouteASMetadata            Route = "GET /.well-known/oauth-authorization-server"
	RouteResourceMetadata      Route = "GET /.well-known/oauth-protected-resource"
	RouteAgentConnections      Route = "POST /oauth/agent_connections"
	RouteAgentConnectionTokens Route = "POST /oauth/agent_connection_tokens" //nolint:gosec // G101: a route, not a credential
	RouteToken                 Route = "POST /oauth/tokens"                  //nolint:gosec // G101: a route, not a credential
	RouteAuthorization         Route = "GET /authorization.json"

	RouteProfile       Route = "GET /{account}/my/profile.json"
	RoutePerson        Route = "GET /{account}/people/{id}"
	RouteProjects      Route = "GET /{account}/projects.json"
	RouteProject       Route = "GET /{account}/projects/{id}"
	RouteProjectPeople Route = "GET /{account}/projects/{id}/people.json"

	RouteStreamTicket Route = "POST /{account}/events/stream_ticket.json"
	RouteEvents       Route = "GET /{account}/events.json"
	RouteCable        Route = "GET /cable"

	RouteComment         Route = "GET /{account}/comments/{id}"
	RouteMessage         Route = "GET /{account}/messages/{id}"
	RouteTodo            Route = "GET /{account}/todos/{id}"
	RouteCard            Route = "GET /{account}/card_tables/cards/{id}"
	RouteCampfires       Route = "GET /{account}/chats.json"
	RouteChatLine        Route = "GET /{account}/chats/{campfire}/lines/{id}"
	RouteSubscription    Route = "GET /{account}/recordings/{id}/subscription.json"
	RouteRecordingEvents Route = "GET /{account}/recordings/{id}/events.json"
)

// RecordingReads are the typed reads a recording summary is made from, one
// per event type the connector admits.
var RecordingReads = []Route{RouteComment, RouteMessage, RouteTodo, RouteCard, RouteChatLine}

// Request is one request the fake received.
type Request struct {
	Method string
	Path   string
	Query  url.Values
	// Form is a form-encoded body, parsed; nil for any other.
	Form url.Values
	// Route is the route it reached; empty for a request no route serves.
	Route Route
	// Caller is the person its bearer token authenticates as, zero for none;
	// Agent says the caller is an Agent principal.
	Caller int64
	Agent  bool
	// Status is what it was answered, zero while it is unanswered: held at
	// a gate, or being answered now.
	Status int
}

// Option configures a Server.
type Option func(*options)

type options struct {
	pageSize     int
	pingInterval time.Duration
	listener     net.Listener
}

// WithPageSize sets how many poll-lane rows a page walks. The default is
// 100.
func WithPageSize(n int) Option { return func(o *options) { o.pageSize = n } }

// WithPingInterval sets how often the cable pings. The default is Action
// Cable's three seconds, which is what eventfeed's staleness rule is built
// around.
func WithPingInterval(d time.Duration) Option { return func(o *options) { o.pingInterval = d } }

// WithListener serves on l instead of a fresh loopback port.
func WithListener(l net.Listener) Option { return func(o *options) { o.listener = l } }

// Server is the fake Basecamp.
type Server struct {
	r    Reporter
	srv  *httptest.Server
	opts options

	// done is closed by Close: it releases every held request and stops
	// every cable goroutine.
	done chan struct{}
	// cables counts the goroutines serving cable connections, which
	// httptest does not wait for once a connection is hijacked.
	cables sync.WaitGroup

	mu sync.Mutex
	// changed is closed, and replaced, on every change Await can observe.
	changed chan struct{}
	closed  bool
	world   *World
	log     []Request
	faults  []*Injected
	gates   []*Gate
	hooks   []*hook
	// events is every event emitted, in id order, on either lane.
	events  []*fedEvent
	conns   map[*cableConn]struct{}
	tickets map[string]int64
	// devices are the agent connection codes issued and not yet spent.
	devices map[string]bool
	// serial numbers every token, ticket and code the fake issues.
	serial int
}

// Start serves world on a loopback port until the Reporter's cleanup.
func Start(r Reporter, world *World, opts ...Option) *Server {
	r.Helper()
	o := options{pageSize: 100, pingInterval: 3 * time.Second}
	for _, opt := range opts {
		opt(&o)
	}
	if err := world.check(); err != nil {
		r.Errorf("fakebasecamp: the world is inconsistent: %v", err)
	}
	s := &Server{
		r:       r,
		opts:    o,
		done:    make(chan struct{}),
		changed: make(chan struct{}),
		world:   world,
		conns:   map[*cableConn]struct{}{},
		tickets: map[string]int64{},
		devices: map[string]bool{},
	}
	s.srv = httptest.NewUnstartedServer(s.routes())
	if o.listener != nil {
		_ = s.srv.Listener.Close()
		s.srv.Listener = o.listener
	}
	s.srv.Start()
	r.Cleanup(s.Close)
	return s
}

// URL is the fake's origin: the base URL, the OAuth issuer, and the cable's
// host.
func (s *Server) URL() string { return s.srv.URL }

// Close stops the fake: every cable connection is severed, every held
// request answered, and every goroutine the fake started has returned when
// it does. It is safe to call more than once.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.done)
	conns := s.detachConnsLocked()
	s.notifyLocked()
	s.mu.Unlock()
	for _, c := range conns {
		c.sever()
	}
	s.srv.Close()
	s.cables.Wait()
}

// Update changes the world under the fake's lock. fn must not call the
// Server.
func (s *Server) Update(fn func(w *World)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.world)
	if err := s.world.check(); err != nil {
		s.r.Errorf("fakebasecamp: the world is inconsistent after an update: %v", err)
	}
	s.notifyLocked()
}

// View reads the world under the fake's lock. fn must not keep what it is
// given, nor call the Server.
func (s *Server) View(fn func(w *World)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.world)
}

// Requests is every request received so far, in arrival order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.log)
}

// Count is how many requests reached any of routes, answered or not.
func (s *Server) Count(routes ...Route) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.log {
		if slices.Contains(routes, r.Route) {
			n++
		}
	}
	return n
}

// Await returns once cond holds, or with ctx's error when ctx ends first.
// cond is called without the fake's lock, so it may call the Server; it is
// called again after every change the fake makes, never on a timer.
func (s *Server) Await(ctx context.Context, cond func() bool) error {
	for {
		s.mu.Lock()
		changed := s.changed
		s.mu.Unlock()
		if cond() {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// notifyLocked wakes every Await.
func (s *Server) notifyLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// nextSerialLocked numbers the next thing the fake issues.
func (s *Server) nextSerialLocked() int {
	s.serial++
	return s.serial
}

// Fault answers matching requests in place of their route. Its answer can
// be any status: a 200 with a body of its own is a fault too.
type Fault struct {
	// Routes are the routes it answers.
	Routes []Route
	// When, when set, narrows it to the requests it accepts. It is called
	// under the fake's lock and must not call the Server.
	When func(Request) bool
	// Times is how many requests it answers before it lapses; zero answers
	// every one until it is removed.
	Times int

	Status int
	Header http.Header
	Body   string
}

// Injected is a fault in force.
type Injected struct {
	s     *Server
	fault Fault
	hits  int
}

// Inject puts a fault in force. The fault injected last is the one that
// answers a request more than one would match.
func (s *Server) Inject(f Fault) *Injected {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := &Injected{s: s, fault: f}
	s.faults = append(s.faults, in)
	return in
}

// Remove takes the fault out of force.
func (i *Injected) Remove() {
	i.s.mu.Lock()
	defer i.s.mu.Unlock()
	i.s.faults = slices.DeleteFunc(i.s.faults, func(f *Injected) bool { return f == i })
}

// Hits is how many requests the fault has answered.
func (i *Injected) Hits() int {
	i.s.mu.Lock()
	defer i.s.mu.Unlock()
	return i.hits
}

// faultLocked claims the fault that answers r, if one does.
func (s *Server) faultLocked(r Request) (answer, bool) {
	for idx := len(s.faults) - 1; idx >= 0; idx-- {
		in := s.faults[idx]
		f := in.fault
		if !slices.Contains(f.Routes, r.Route) || (f.When != nil && !f.When(r)) {
			continue
		}
		in.hits++
		if f.Times > 0 && in.hits >= f.Times {
			s.faults = slices.Delete(s.faults, idx, idx+1)
		}
		return answer{status: f.Status, header: f.Header.Clone(), body: []byte(f.Body)}, true
	}
	return answer{}, false
}

// TokenRateLimited is the token endpoint answering 429, asking for
// retryAfter seconds; anything under one asks for one.
func TokenRateLimited(retryAfter int) Fault {
	retryAfter = max(retryAfter, 1)
	return Fault{
		Routes: []Route{RouteToken},
		Status: http.StatusTooManyRequests,
		Header: http.Header{"Retry-After": {strconv.Itoa(retryAfter)}},
	}
}

// TokenUnavailable is the token endpoint answering 503.
func TokenUnavailable() Fault {
	return Fault{Routes: []Route{RouteToken}, Status: http.StatusServiceUnavailable}
}

// TokenInvalidClient is the token endpoint refusing the client, as Basecamp
// does for an agent disconnected, or connected on another computer since.
func TokenInvalidClient() Fault {
	return Fault{
		Routes: []Route{RouteToken},
		Status: http.StatusUnauthorized,
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   `{"error":"invalid_client"}`,
	}
}

// ServerError is routes answering 500, times times.
func ServerError(times int, routes ...Route) Fault {
	return Fault{Routes: routes, Times: times, Status: http.StatusInternalServerError}
}

// Refused is routes answering status, with no body, until removed.
func Refused(status int, routes ...Route) Fault {
	return Fault{Routes: routes, Status: status}
}

// ByAgents narrows a fault to requests an Agent makes.
func ByAgents(r Request) bool { return r.Agent }

// Gate holds requests open.
type Gate struct {
	s       *Server
	routes  []Route
	release chan struct{}
	// waiting and released are guarded by the Server's lock.
	waiting  int
	released bool
}

// Gate holds every request to routes open, unanswered, until Release.
func (s *Server) Gate(routes ...Route) *Gate {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := &Gate{s: s, routes: slices.Clone(routes), release: make(chan struct{})}
	s.gates = append(s.gates, g)
	return g
}

// Waiting is how many requests the gate is holding now.
func (g *Gate) Waiting() int {
	g.s.mu.Lock()
	defer g.s.mu.Unlock()
	return g.waiting
}

// Release lets every held request through, and lets later ones pass.
func (g *Gate) Release() {
	g.s.mu.Lock()
	defer g.s.mu.Unlock()
	if g.released {
		return
	}
	g.released = true
	close(g.release)
	g.s.gates = slices.DeleteFunc(g.s.gates, func(o *Gate) bool { return o == g })
	g.s.notifyLocked()
}

type hook struct {
	route Route
	fn    func(Request)
}

// Before runs fn as each request reaches route, before it is answered and
// outside the fake's lock, so fn may call the Server. The returned function
// stops it.
func (s *Server) Before(route Route, fn func(Request)) (remove func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := &hook{route: route, fn: fn}
	s.hooks = append(s.hooks, h)
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.hooks = slices.DeleteFunc(s.hooks, func(o *hook) bool { return o == h })
	}
}

// answer is a response worked out under the lock and written outside it.
type answer struct {
	status int
	header http.Header
	body   []byte
}

// call is one request on its way through a route.
type call struct {
	s     *Server
	req   *http.Request
	route Route
	index int
	// caller is who the bearer authenticates as when the handler runs.
	caller int64
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	handle := func(route Route, h func(c *call) answer) {
		mux.HandleFunc(string(route), func(w http.ResponseWriter, req *http.Request) {
			c, ok := s.begin(route, w, req)
			if !ok {
				return
			}
			s.mu.Lock()
			c.caller, _ = s.callerLocked(req)
			a := h(c)
			s.mu.Unlock()
			s.finish(c, w, a)
		})
	}
	handle(RouteASMetadata, (*call).asMetadata)
	handle(RouteResourceMetadata, (*call).resourceMetadata)
	handle(RouteAgentConnections, (*call).agentConnections)
	handle(RouteAgentConnectionTokens, (*call).agentConnectionTokens)
	handle(RouteToken, (*call).token)
	handle(RouteAuthorization, (*call).authorization)
	handle(RouteProfile, account((*call).profile))
	handle(RoutePerson, account((*call).person))
	handle(RouteProjects, account((*call).projects))
	handle(RouteProject, account((*call).project))
	handle(RouteProjectPeople, account((*call).projectPeople))
	handle(RouteStreamTicket, account((*call).streamTicket))
	handle(RouteEvents, account((*call).events))
	handle(RouteComment, account(recordingRead("Comment")))
	handle(RouteMessage, account(recordingRead("Message")))
	handle(RouteTodo, account(recordingRead("Todo")))
	handle(RouteCard, account(recordingRead("Kanban::Card")))
	handle(RouteCampfires, account((*call).campfires))
	handle(RouteChatLine, account((*call).chatLine))
	handle(RouteSubscription, account((*call).subscription))
	handle(RouteRecordingEvents, account((*call).recordingEvents))
	mux.HandleFunc(string(RouteCable), s.serveCable)
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		c, ok := s.begin("", w, req)
		if !ok {
			return
		}
		s.r.Errorf("fakebasecamp: no route serves %s %s", req.Method, req.URL.Path)
		s.finish(c, w, answer{status: http.StatusNotFound})
	})
	return mux
}

// begin logs a request and takes it through the hooks, the gates and the
// faults. It reports false when one of them answered it.
func (s *Server) begin(route Route, w http.ResponseWriter, req *http.Request) (*call, bool) {
	var form url.Values
	if req.Method == http.MethodPost && strings.HasPrefix(req.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		if err := req.ParseForm(); err == nil {
			form = req.PostForm
		}
	}
	s.mu.Lock()
	caller, _ := s.callerLocked(req)
	c := &call{s: s, req: req, route: route, index: len(s.log)}
	r := Request{
		Method: req.Method,
		Path:   req.URL.Path,
		Query:  req.URL.Query(),
		Form:   form,
		Route:  route,
		Caller: caller,
		Agent:  s.world.isAgent(caller),
	}
	s.log = append(s.log, r)
	var hooks []func(Request)
	for _, h := range s.hooks {
		if h.route == route {
			hooks = append(hooks, h.fn)
		}
	}
	s.notifyLocked()
	s.mu.Unlock()

	for _, fn := range hooks {
		fn(r)
	}
	if !s.hold(req.Context(), c) {
		s.finish(c, w, answer{status: http.StatusServiceUnavailable})
		return nil, false
	}
	s.mu.Lock()
	a, faulted := s.faultLocked(r)
	s.mu.Unlock()
	if faulted {
		s.finish(c, w, a)
		return nil, false
	}
	return c, true
}

// hold waits at the first gate that holds the request's route. It reports
// false when the fake closed, or the client left, before the gate opened.
func (s *Server) hold(ctx context.Context, c *call) bool {
	s.mu.Lock()
	var gate *Gate
	for _, g := range s.gates {
		if slices.Contains(g.routes, c.route) {
			gate = g
			break
		}
	}
	if gate == nil {
		s.mu.Unlock()
		return true
	}
	gate.waiting++
	s.notifyLocked()
	s.mu.Unlock()

	released := false
	select {
	case <-gate.release:
		released = true
	case <-s.done:
	case <-ctx.Done():
	}
	s.mu.Lock()
	gate.waiting--
	s.notifyLocked()
	s.mu.Unlock()
	return released
}

// finish writes an answer and logs its status.
func (s *Server) finish(c *call, w http.ResponseWriter, a answer) {
	for name, values := range a.header {
		for _, v := range values {
			w.Header().Add(name, v)
		}
	}
	if len(a.body) > 0 && w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	status := a.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(a.body)
	s.setStatus(c.index, status)
}

func (s *Server) setStatus(index, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log[index].Status = status
	s.notifyLocked()
}

// callerLocked is who the request's bearer token authenticates as, and
// whether it carried one the fake issued.
func (s *Server) callerLocked(req *http.Request) (int64, bool) {
	bearer, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return 0, false
	}
	t, ok := s.world.Tokens[bearer]
	if !ok {
		return 0, false
	}
	return t.PersonID, true
}

// account guards a route mounted under an account: the account has to be
// the fake's, and the caller someone the fake issued a token to.
func account(h func(c *call) answer) func(c *call) answer {
	return func(c *call) answer {
		if c.req.PathValue("account") != strconv.FormatInt(c.s.world.Account.ID, 10) {
			return answer{status: http.StatusNotFound}
		}
		if _, ok := c.s.callerLocked(c.req); !ok {
			return answer{status: http.StatusUnauthorized}
		}
		return h(c)
	}
}

// pathID is a numeric path value; ok is false when it is not one.
func (c *call) pathID(name string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSuffix(c.req.PathValue(name), ".json"), 10, 64)
	return id, err == nil && id > 0
}

// jsonAnswer renders v as a JSON answer.
func (c *call) jsonAnswer(status int, v any) answer {
	body, err := json.Marshal(v)
	if err != nil {
		c.s.r.Errorf("fakebasecamp: rendering %s: %v", c.route, err)
		return answer{status: http.StatusInternalServerError}
	}
	return answer{status: status, body: body}
}

// oauthError is an RFC 6749 error answer.
func oauthError(status int, code string) answer {
	return answer{status: status, body: []byte(fmt.Sprintf(`{"error":%q}`, code))}
}

// base is the fake's origin, as the client reached it.
func (c *call) base() string { return c.s.srv.URL }

// accountURL is a URL under the account.
func (c *call) accountURL(format string, args ...any) string {
	return fmt.Sprintf("%s/%d/", c.base(), c.s.world.Account.ID) + fmt.Sprintf(format, args...)
}
