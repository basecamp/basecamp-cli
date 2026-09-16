package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// The secret the mock server hands over. It is a literal nobody can mistake
// for a credential, and several tests assert it never reaches a transcript.
const fakeAgentSecret = "not-a-real-secret"

// connectAS is a mock Basecamp authorization server for the agent-connection
// ceremony: RFC 8414 metadata, the anonymous intake, the poll, and the token
// endpoint the credential is then minted against. Every form POST is
// recorded; every response is overridable per test.
type connectAS struct {
	srv *httptest.Server

	mu           sync.Mutex
	intakeForms  []url.Values
	pollForms    []url.Values
	tokenForms   []url.Values
	pollHeader   http.Header
	tokenURIHost string

	// intake renders the intake response. The default approves a
	// ten-minute code polled once a second.
	intake func() (status int, body string)
	// poll renders the nth (0-based) poll response. The default hands the
	// connection over immediately.
	poll func(call int) (status int, body string)
	// token renders the client_credentials mint.
	token func() (status int, body string)
}

func startConnectAS(t *testing.T) *connectAS {
	t.Helper()
	as := &connectAS{pollHeader: http.Header{}}

	record := func(into *[]url.Values, r *http.Request) {
		require.NoError(t, r.ParseForm())
		as.mu.Lock()
		defer as.mu.Unlock()
		*into = append(*into, r.PostForm)
	}
	answer := func(w http.ResponseWriter, status int, body string, header http.Header) {
		for name, values := range header {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"issuer": %q,
			"token_endpoint": %q,
			"device_authorization_endpoint": %q,
			"grant_types_supported": ["urn:ietf:params:oauth:grant-type:device_code", "client_credentials", "refresh_token"]
		}`, as.srv.URL, as.srv.URL+"/oauth/tokens", as.srv.URL+"/oauth/device_authorizations")
	})
	mux.HandleFunc("/oauth/agent_connections", func(w http.ResponseWriter, r *http.Request) {
		record(&as.intakeForms, r)
		status, body := as.intake()
		answer(w, status, body, nil)
	})
	mux.HandleFunc("/oauth/agent_connection_tokens", func(w http.ResponseWriter, r *http.Request) {
		as.mu.Lock()
		call := len(as.pollForms)
		as.mu.Unlock()
		record(&as.pollForms, r)
		status, body := as.poll(call)
		answer(w, status, body, as.pollHeader)
	})
	mux.HandleFunc("/oauth/tokens", func(w http.ResponseWriter, r *http.Request) {
		record(&as.tokenForms, r)
		status, body := as.token()
		answer(w, status, body, nil)
	})

	as.srv = httptest.NewServer(mux)
	t.Cleanup(as.srv.Close)

	as.intake = func() (int, string) {
		return http.StatusOK, fmt.Sprintf(
			`{"device_code":"dev-code-1","user_code":"WDJB-MJHT","verification_uri":%q,"verification_uri_complete":%q,"token_uri":%q,"expires_in":600,"interval":1}`,
			as.srv.URL+"/connect", as.srv.URL+"/connect?user_code=WDJB-MJHT", as.tokenURI())
	}
	as.poll = func(int) (int, string) { return http.StatusOK, connectionJSON(scopeFull) }
	as.token = func() (int, string) {
		return http.StatusOK, `{"access_token":"minted","token_type":"bearer","expires_in":3600,"resource":"urn:bc:agent:42","scope":"full"}`
	}
	return as
}

// tokenURI is where the intake tells the connector to poll: this server,
// unless a test points it somewhere else.
func (as *connectAS) tokenURI() string {
	if as.tokenURIHost != "" {
		return as.tokenURIHost + "/oauth/agent_connection_tokens"
	}
	return as.srv.URL + "/oauth/agent_connection_tokens"
}

func (as *connectAS) calls(which *[]url.Values) []url.Values {
	as.mu.Lock()
	defer as.mu.Unlock()
	return append([]url.Values(nil), *which...)
}

// connectionJSON is a successful poll: the agent's own client, its account,
// and the scope the operator approved.
func connectionJSON(scope string) string {
	return fmt.Sprintf(`{"client_id":"agent-client","client_secret":%q,"account_id":"999","scope":%q}`, fakeAgentSecret, scope)
}

func oauthErrorJSON(code string) (int, string) {
	return http.StatusBadRequest, fmt.Sprintf(`{"error":%q,"error_description":"%s happened"}`, code, code)
}

// testClock is the poll's clock, driven by the sleeps the flow itself asks
// for: a test waits for nothing and the code's lifetime still runs out.
type testClock struct {
	mu    sync.Mutex
	now   time.Time
	slept []time.Duration
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.slept = append(c.slept, d)
	c.now = c.now.Add(d)
	return nil
}

func (c *testClock) waits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.slept...)
}

// connectManager wires a manager whose discovery reaches as through the
// RFC 9728 resource hop, as a real base URL does.
func connectManager(t *testing.T, as *connectAS) *Manager {
	t.Helper()
	resource := startResourceServer(t, as.srv.URL)
	m := newDeviceTestManager(t, resource.URL)
	m.cfg.ActiveProfile = "agent"
	return m
}

// connectOptions are the options every test starts from: a named device, a
// collected transcript, no browser launch, and a clock the flow drives.
func connectOptions(cl *collectLogger, clock *testClock) AgentConnectOptions {
	return AgentConnectOptions{
		DeviceName:   "build-box",
		SoftwareName: "Some Editor",
		Logger:       cl.log,
		NoBrowser:    true,
		sleep:        clock.sleep,
		now:          clock.Now,
	}
}

// TestConnectAgentStoresTheApprovedClient: the whole ceremony, end to end.
// What the poll hands over is what the profile keeps, and what it keeps is
// what agent.go spends — the credential is loaded back and a token minted
// from it without another connection.
func TestConnectAgentStoresTheApprovedClient(t *testing.T) {
	as := startConnectAS(t)
	as.poll = func(call int) (int, string) {
		switch call {
		case 0:
			return oauthErrorJSON("authorization_pending")
		case 1:
			return oauthErrorJSON("slow_down")
		default:
			return http.StatusOK, connectionJSON(scopeFull)
		}
	}
	m := connectManager(t, as)
	cl := &collectLogger{}
	clock := newTestClock()

	var handed *AgentConnection
	opts := connectOptions(cl, clock)
	opts.BeforeStore = func(conn *AgentConnection) error {
		handed = conn
		return nil
	}
	result, err := m.ConnectAgent(context.Background(), opts)
	require.NoError(t, err)

	assert.Equal(t, "999", result.AccountID)
	assert.Equal(t, "agent-client", result.ClientID)
	assert.Equal(t, oauthTypeAgent, result.OAuthType)
	assert.Equal(t, scopeFull, result.Scope)
	require.NotNil(t, handed, "the profile is committed with the connection the operator approved")
	assert.Equal(t, "999", handed.AccountID)

	intake := as.calls(&as.intakeForms)
	require.Len(t, intake, 1, "the intake is made once")
	assert.Equal(t, "build-box", intake[0].Get("device_name"))
	assert.Equal(t, "Some Editor", intake[0].Get("software_name"))
	assert.Equal(t, scopeFull, intake[0].Get("scope"))

	polls := as.calls(&as.pollForms)
	require.Len(t, polls, 3)
	assert.Equal(t, "dev-code-1", polls[0].Get("device_code"))

	// The pace the server asked for, then slow_down's five seconds on top
	// of it — and never the other way.
	assert.Equal(t, []time.Duration{time.Second, time.Second, 6 * time.Second}, clock.waits())

	stored, err := m.store.Load("profile:agent")
	require.NoError(t, err)
	assert.Equal(t, oauthTypeAgent, stored.OAuthType)
	assert.Equal(t, "agent-client", stored.ClientID)
	assert.Equal(t, fakeAgentSecret, stored.ClientSecret)
	assert.Equal(t, scopeFull, stored.Scope)
	assert.Equal(t, as.srv.URL+"/oauth/tokens", stored.TokenEndpoint)
	assert.Equal(t, as.srv.URL, stored.Issuer)
	assert.Equal(t, "minted", stored.AccessToken)
	assert.Empty(t, stored.RefreshToken, "an agent is granted no refresh token")

	mints := as.calls(&as.tokenForms)
	require.Len(t, mints, 1, "the connection is proved by minting once")
	assert.Equal(t, "client_credentials", mints[0].Get("grant_type"))
	assert.Equal(t, "agent-client", mints[0].Get("client_id"))
	assert.Equal(t, fakeAgentSecret, mints[0].Get("client_secret"))
	assert.Equal(t, scopeFull, mints[0].Get("scope"))

	// The stored credential is the one every later command renews from.
	aged, err := m.store.Load("profile:agent")
	require.NoError(t, err)
	aged.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	aged.RenewAfter = 0
	require.NoError(t, m.store.Save("profile:agent", aged))
	token, err := m.AccessToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "minted", token)
	assert.Len(t, as.calls(&as.tokenForms), 2, "the stored client mints again on its own")

	assert.NotContains(t, cl.joined(), fakeAgentSecret, "the client secret must never be printed")
}

// TestConnectAgentPrintsTheLinkAndTheCode: the operator's half of the
// ceremony is a link, a code to compare, and the RFC 8628 §5.4 warning
// about a code somebody else handed over.
func TestConnectAgentPrintsTheLinkAndTheCode(t *testing.T) {
	as := startConnectAS(t)
	m := connectManager(t, as)
	cl := &collectLogger{}

	_, err := m.ConnectAgent(context.Background(), connectOptions(cl, newTestClock()))
	require.NoError(t, err)

	logs := cl.joined()
	assert.Contains(t, logs, as.srv.URL+"/connect?user_code=WDJB-MJHT")
	assert.Contains(t, logs, "WDJB-MJHT")
	assert.Contains(t, logs, "Only continue if you started this connection yourself")
	assert.Contains(t, logs, "Waiting for approval")
}

// TestConnectAgentOpensTheBrowserUnlessToldNotTo: the link is launched by
// default and only printed under --no-browser, which is the login flow's
// own rule because it is the login flow's own code.
func TestConnectAgentOpensTheBrowserUnlessToldNotTo(t *testing.T) {
	as := startConnectAS(t)
	m := connectManager(t, as)

	var launched []string
	cl := &collectLogger{}
	opts := connectOptions(cl, newTestClock())
	opts.NoBrowser = false
	opts.Local = true
	opts.BrowserLauncher = func(target string) error {
		launched = append(launched, target)
		return nil
	}
	_, err := m.ConnectAgent(context.Background(), opts)
	require.NoError(t, err)
	require.Equal(t, []string{as.srv.URL + "/connect?user_code=WDJB-MJHT"}, launched)

	headless := &collectLogger{}
	silent := connectOptions(headless, newTestClock())
	silent.BrowserLauncher = func(string) error {
		t.Fatal("--no-browser launched a browser")
		return nil
	}
	_, err = m.ConnectAgent(context.Background(), silent)
	require.NoError(t, err)
	assert.Contains(t, headless.joined(), "/connect?user_code=WDJB-MJHT", "the link is printed to be opened elsewhere")
}

// TestConnectAgentKeepsTheApprovedScope: the operator can downgrade the
// request to read on the approval page, which narrows the CLIENT. Storing
// what was asked for instead would make every mint ask for more than the
// client is allowed, and be refused.
func TestConnectAgentKeepsTheApprovedScope(t *testing.T) {
	as := startConnectAS(t)
	as.poll = func(int) (int, string) { return http.StatusOK, connectionJSON(scopeRead) }
	as.token = func() (int, string) {
		return http.StatusOK, `{"access_token":"minted","token_type":"bearer","expires_in":3600,"scope":"read"}`
	}
	m := connectManager(t, as)

	opts := connectOptions(&collectLogger{}, newTestClock())
	opts.Scope = scopeFull
	result, err := m.ConnectAgent(context.Background(), opts)
	require.NoError(t, err)
	assert.Equal(t, scopeRead, result.Scope)

	assert.Equal(t, scopeFull, as.calls(&as.intakeForms)[0].Get("scope"), "the request asked for full")
	assert.Equal(t, scopeRead, as.calls(&as.tokenForms)[0].Get("scope"), "the mint asks for what was approved")

	stored, err := m.store.Load("profile:agent")
	require.NoError(t, err)
	assert.Equal(t, scopeRead, stored.Scope)
}

// TestConnectAgentRefusesAScopeItCannotStore: the CLI can represent read
// and full. Anything else is refused before a credential is written, not
// persisted and then found unusable.
func TestConnectAgentRefusesAScopeItCannotStore(t *testing.T) {
	as := startConnectAS(t)
	as.poll = func(int) (int, string) { return http.StatusOK, connectionJSON("mcp") }
	m := connectManager(t, as)

	_, err := m.ConnectAgent(context.Background(), connectOptions(&collectLogger{}, newTestClock()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scope other than read or full")
	assert.Empty(t, as.calls(&as.tokenForms), "nothing was minted with a scope that cannot be stored")
	assertNoAgentCredential(t, m)
}

// TestConnectAgentReportsADecline: the operator can say no, and a refusal
// is not something to keep polling through.
func TestConnectAgentReportsADecline(t *testing.T) {
	as := startConnectAS(t)
	as.poll = func(int) (int, string) { return oauthErrorJSON("access_denied") }
	m := connectManager(t, as)

	_, err := m.ConnectAgent(context.Background(), connectOptions(&collectLogger{}, newTestClock()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "declined")
	assert.Len(t, as.calls(&as.pollForms), 1, "a decline ends the ceremony")
	assertNoAgentCredential(t, m)
}

// TestConnectAgentReportsAnExpiredCode: the server's own verdict.
func TestConnectAgentReportsAnExpiredCode(t *testing.T) {
	as := startConnectAS(t)
	as.poll = func(int) (int, string) { return oauthErrorJSON("expired_token") }
	m := connectManager(t, as)

	_, err := m.ConnectAgent(context.Background(), connectOptions(&collectLogger{}, newTestClock()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
	assertNoAgentCredential(t, m)
}

// TestConnectAgentStopsWhenTheCodeLifetimeRunsOut: a server that answers
// authorization_pending forever must not be polled forever. The code it
// issued is dead at ten minutes whatever it keeps saying.
func TestConnectAgentStopsWhenTheCodeLifetimeRunsOut(t *testing.T) {
	as := startConnectAS(t)
	as.intake = func() (int, string) {
		return http.StatusOK, fmt.Sprintf(
			`{"device_code":"dev-code-1","user_code":"WDJB-MJHT","verification_uri_complete":%q,"token_uri":%q,"expires_in":20,"interval":5}`,
			as.srv.URL+"/connect?user_code=WDJB-MJHT", as.tokenURI())
	}
	as.poll = func(int) (int, string) { return oauthErrorJSON("authorization_pending") }
	m := connectManager(t, as)

	_, err := m.ConnectAgent(context.Background(), connectOptions(&collectLogger{}, newTestClock()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
	assert.Len(t, as.calls(&as.pollForms), 3, "polled to the end of the code's life and no further")
	assertNoAgentCredential(t, m)
}

// TestConnectAgentReportsAnAgentThatIsNoLongerActive: an account canceled
// or frozen between approval and the poll answers invalid_grant, and the
// remedy is a new connection, not a retry.
func TestConnectAgentReportsAnAgentThatIsNoLongerActive(t *testing.T) {
	as := startConnectAS(t)
	as.poll = func(int) (int, string) { return oauthErrorJSON("invalid_grant") }
	m := connectManager(t, as)

	_, err := m.ConnectAgent(context.Background(), connectOptions(&collectLogger{}, newTestClock()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid_grant")
	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Contains(t, e.Hint, "no longer active")
	assert.Len(t, as.calls(&as.pollForms), 1)
	assertNoAgentCredential(t, m)
}

// TestConnectAgentBacksOffWhenThrottled: a rate limit is not a verdict on
// the connection. The poll waits out what the server asked for and carries
// on — an approval the operator has already given must not be dropped
// because the poll arrived a second early.
func TestConnectAgentBacksOffWhenThrottled(t *testing.T) {
	as := startConnectAS(t)
	as.pollHeader.Set("Retry-After", "30")
	as.poll = func(call int) (int, string) {
		if call == 0 {
			return http.StatusTooManyRequests, `{"error":"too_many_requests"}`
		}
		return http.StatusOK, connectionJSON(scopeFull)
	}
	m := connectManager(t, as)
	clock := newTestClock()

	result, err := m.ConnectAgent(context.Background(), connectOptions(&collectLogger{}, clock))
	require.NoError(t, err)
	assert.Equal(t, "999", result.AccountID)
	assert.Equal(t, []time.Duration{time.Second, 30 * time.Second}, clock.waits())
}

// TestConnectAgentRefusesATokenEndpointOnAnotherOrigin: the poll carries
// the device code and is answered with a client secret. A response body
// naming another host for it is not followed, and the handover never
// leaves the origin the intake was made to.
func TestConnectAgentRefusesATokenEndpointOnAnotherOrigin(t *testing.T) {
	elsewhere, calls := countingServer(t)
	as := startConnectAS(t)
	as.tokenURIHost = elsewhere.URL
	m := connectManager(t, as)

	_, err := m.ConnectAgent(context.Background(), connectOptions(&collectLogger{}, newTestClock()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "another origin")
	assert.Zero(t, *calls, "nothing was sent to the host the response named")
	assertNoAgentCredential(t, m)
}

// TestConnectAgentRefusesAMalformedIntake: a response missing the code or
// the link is refused before the operator is sent anywhere.
func TestConnectAgentRefusesAMalformedIntake(t *testing.T) {
	as := startConnectAS(t)
	as.intake = func() (int, string) {
		return http.StatusOK, fmt.Sprintf(`{"device_code":"dev-code-1","user_code":"  ","verification_uri_complete":%q,"token_uri":%q}`,
			as.srv.URL+"/connect", as.tokenURI())
	}
	m := connectManager(t, as)
	cl := &collectLogger{}

	_, err := m.ConnectAgent(context.Background(), connectOptions(cl, newTestClock()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed")
	assert.Empty(t, as.calls(&as.pollForms))
	assert.NotContains(t, cl.joined(), "Waiting for approval")
}

// TestConnectAgentReportsARefusedIntake: the ceremony ships dark behind a
// pilot, so "not issuing agent connections" is the answer an operator is
// most likely to meet, and it has to say what to do instead.
func TestConnectAgentReportsARefusedIntake(t *testing.T) {
	as := startConnectAS(t)
	as.intake = func() (int, string) { return http.StatusServiceUnavailable, `` }
	m := connectManager(t, as)

	_, err := m.ConnectAgent(context.Background(), connectOptions(&collectLogger{}, newTestClock()))
	require.Error(t, err)
	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Contains(t, e.Hint, "not issuing agent connections")
	assert.Empty(t, as.calls(&as.pollForms))
}

// TestConnectAgentStoresNothingWhenTheProfileCannotBeCommitted: the caller
// commits the profile entry between the mint and the write, and a refusal
// there must leave no credential behind — an orphaned client secret under
// a profile nothing registered is exactly what that hook exists to prevent.
func TestConnectAgentStoresNothingWhenTheProfileCannotBeCommitted(t *testing.T) {
	as := startConnectAS(t)
	m := connectManager(t, as)

	cl := &collectLogger{}
	opts := connectOptions(cl, newTestClock())
	opts.BeforeStore = func(*AgentConnection) error { return output.ErrUsage("the profile is bound to another account") }
	_, err := m.ConnectAgent(context.Background(), opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bound to another account")
	assertNoAgentCredential(t, m)
	// The poll burned the code and rotated the agent's secret, so this
	// failure leaves the agent connected to nothing. Saying "nothing was
	// stored" alone would send the operator looking for a credential that
	// no longer exists anywhere.
	assert.Contains(t, cl.joined(), "Disconnect the agent in Basecamp and connect again")
}

// TestConnectAgentRefusesTheLaunchpadFallback: Launchpad has no agent
// principals. Finding that out AFTER an operator has approved something
// would be the worst moment, so the intake is never even made.
func TestConnectAgentRefusesTheLaunchpadFallback(t *testing.T) {
	as := startConnectAS(t)
	// No protected-resource metadata at the base URL: the soft fallback.
	bare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) }))
	defer bare.Close()
	lp, lpCalls := countingServer(t)
	t.Setenv("BASECAMP_LAUNCHPAD_URL", lp.URL)

	m := newDeviceTestManager(t, bare.URL)
	m.cfg.ActiveProfile = "agent"

	_, err := m.ConnectAgent(context.Background(), connectOptions(&collectLogger{}, newTestClock()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Launchpad fallback")
	assert.Empty(t, as.calls(&as.intakeForms))
	assert.Zero(t, *lpCalls, "no ceremony was started against a server that has none")
}

// TestConnectAgentRefusesNamesTheIntakeWouldRefuse: the self-asserted names
// are checked here to the intake's own rule, so a refusal costs a round
// trip nobody needs — and a control sequence never reaches the approval
// page the operator reads.
func TestConnectAgentRefusesNamesTheIntakeWouldRefuse(t *testing.T) {
	as := startConnectAS(t)
	m := connectManager(t, as)

	missing := connectOptions(&collectLogger{}, newTestClock())
	missing.DeviceName = "  "
	_, err := m.ConnectAgent(context.Background(), missing)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "device name")

	long := connectOptions(&collectLogger{}, newTestClock())
	long.SoftwareName = strings.Repeat("é", maxAgentConnectNameChars+1)
	_, err = m.ConnectAgent(context.Background(), long)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "software name")

	// One character shorter is the limit itself, counted as the server
	// counts it: characters, not bytes.
	edge := connectOptions(&collectLogger{}, newTestClock())
	edge.SoftwareName = strings.Repeat("é", maxAgentConnectNameChars)
	_, err = m.ConnectAgent(context.Background(), edge)
	require.NoError(t, err)

	control := connectOptions(&collectLogger{}, newTestClock())
	control.DeviceName = "build\x1b[2Jbox"
	_, err = m.ConnectAgent(context.Background(), control)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "control characters")

	assert.Len(t, as.calls(&as.intakeForms), 1, "only the name within the limit was sent")
}

// TestConnectAgentStopsOnCancellation: Ctrl-C during the wait ends the
// ceremony there, and nothing is stored.
func TestConnectAgentStopsOnCancellation(t *testing.T) {
	as := startConnectAS(t)
	as.poll = func(int) (int, string) { return oauthErrorJSON("authorization_pending") }
	m := connectManager(t, as)

	ctx, cancel := context.WithCancel(context.Background())
	clock := newTestClock()
	opts := connectOptions(&collectLogger{}, clock)
	opts.sleep = func(sleepCtx context.Context, d time.Duration) error {
		cancel()
		return clock.sleep(sleepCtx, d)
	}
	_, err := m.ConnectAgent(ctx, opts)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, as.calls(&as.pollForms))
	assertNoAgentCredential(t, m)
}

// TestAgentConnectIntervalIsBounded: the pace is the server's to choose
// within reason — never so fast it trips the rate limit that exists to
// stop exactly this, never so slow an approval sits uncollected.
func TestAgentConnectIntervalIsBounded(t *testing.T) {
	assert.Equal(t, defaultAgentConnectInterval, agentConnectInterval(0))
	assert.Equal(t, defaultAgentConnectInterval, agentConnectInterval(-30))
	assert.Equal(t, 7*time.Second, agentConnectInterval(7))
	assert.Equal(t, maxAgentConnectInterval, agentConnectInterval(3600))
	assert.Equal(t, minAgentConnectInterval, clampAgentConnectInterval(0))
}

// TestAgentConnectLifetimeIsBounded: the printed code's life, and the
// terminal it holds open, is not the server's to set without limit.
func TestAgentConnectLifetimeIsBounded(t *testing.T) {
	assert.Equal(t, defaultAgentConnectLifetime, agentConnectLifetime(0))
	assert.Equal(t, defaultAgentConnectLifetime, agentConnectLifetime(-1))
	assert.Equal(t, 90*time.Second, agentConnectLifetime(90))
	assert.Equal(t, maxAgentConnectLifetime, agentConnectLifetime(1<<40))
}

func assertNoAgentCredential(t *testing.T, m *Manager) {
	t.Helper()
	_, err := m.store.Load("profile:agent")
	assert.ErrorIs(t, err, ErrNoCredential, "a connection that did not complete stores nothing")
}
