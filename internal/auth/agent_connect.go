package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/version"
)

// Agent connections: the ceremony that hands this CLI an agent's OWN client
// id and secret, so nobody ever pastes one.
//
// It looks like the RFC 8628 device flow and is paced like it, but it is
// not one, and the difference is the whole point. A device login asks a
// person to lend their identity; this asks them to say WHICH AGENT this
// computer is, and hands back that agent's confidential client. Nothing
// here names an agent — client ids are never given out to be relayed — so
// the intake is clientless and the binding is made by the only party that
// can be trusted to make it: the signed-in operator, on Basecamp's own
// page.
//
// What comes back is the durable credential agent.go already knows how to
// keep and spend: a client id and secret that mint client_credentials
// self-tokens whose byline is the agent's. There is no refresh token and
// no authorization grant — the operator authorized the connection, they
// did not lend their identity — and a disconnect in Adminland kills the
// secret.
//
// The contract is bc3's doc/oauth/agent_connections.md. Everything below
// is built to it and covered against a local server; the production
// endpoints ship dark behind a pilot, so a live run needs the operator.

const (
	// agentConnectionsPath is where the intake is mounted on the
	// authorization server. The poll's endpoint is not derived: the intake
	// names it, and only the same origin is followed.
	agentConnectionsPath = "/oauth/agent_connections"

	// agentConnectIntakeOp and agentConnectPollOp name the two requests in
	// errors, so a failure says which half of the ceremony it came from.
	agentConnectIntakeOp = "requesting an agent connection"
	agentConnectPollOp   = "collecting the agent connection"

	// defaultAgentConnectLifetime and defaultAgentConnectInterval are what
	// the ceremony runs to when the intake reports neither. They are the
	// server's own documented values (a ten-minute code, RFC 8628's
	// five-second floor).
	defaultAgentConnectLifetime = 10 * time.Minute
	defaultAgentConnectInterval = 5 * time.Second

	// maxAgentConnectLifetime caps the wait a server can ask for. A code
	// lives ten minutes; a much longer one is not a lifetime to hold a
	// terminal open for, and an unbounded one overflows the conversion to
	// a Duration.
	maxAgentConnectLifetime = 30 * time.Minute

	// minAgentConnectInterval and maxAgentConnectInterval bound the poll
	// pace: never faster than the server's rate limit tolerates, never so
	// slow that an approval sits uncollected for minutes.
	minAgentConnectInterval = time.Second
	maxAgentConnectInterval = time.Minute

	// agentConnectSlowDownStep is RFC 8628 §3.5's answer to slow_down:
	// five seconds onto the interval, every time it is said.
	agentConnectSlowDownStep = 5 * time.Second

	// agentConnectTimeout bounds one request in the ceremony, matching the
	// mint's own round-trip timeout.
	agentConnectTimeout = 30 * time.Second

	// maxAgentConnectBytes bounds a response read. Both responses are a
	// few hundred bytes; this is the mint's ceiling.
	maxAgentConnectBytes int64 = 1 << 20

	// maxAgentConnectNameChars is the server's limit on the self-asserted
	// names, counted in characters as Ruby's String#length counts them, so
	// a name this CLI accepts is one the intake accepts.
	maxAgentConnectNameChars = 100
)

// AgentConnection is what the poll hands back once the operator has
// approved: the agent's own confidential client, the account it belongs
// to, and the scope the operator approved — which is what the client was
// provisioned with, not necessarily what was asked for.
type AgentConnection struct {
	ClientID     string
	ClientSecret string
	AccountID    string
	Scope        string
}

// AgentConnectResult is a completed connection: the credential it stored,
// and the two facts the caller needs that a login result does not carry —
// which account the operator connected, and which client is now this
// profile's.
type AgentConnectResult struct {
	LoginResult
	AccountID string
	ClientID  string
}

// AgentConnectOptions configures the connection ceremony.
type AgentConnectOptions struct {
	// DeviceName is where this connector runs, self-asserted and shown to
	// the operator on the approval page. Required, as the intake requires
	// it: an unnamed connection is one nobody can recognize later.
	DeviceName string

	// SoftwareName is the product this connector runs inside, shown as
	// "Connected to". Optional and self-asserted.
	SoftwareName string

	// Scope is the access to ask for ("read" or "full"); empty asks for
	// full, which the approval page offers to downgrade. What the operator
	// approves is what gets stored.
	Scope string

	// NoBrowser prints the link instead of opening it; Local forces a
	// launch the host heuristics would have skipped. Both mean what they
	// mean on a login, and are honored by the same code.
	NoBrowser bool
	Local     bool

	// BrowserLauncher opens the verification URL. Nil uses the system
	// browser, unless the flow is not launching one at all.
	BrowserLauncher func(url string) error

	// Logger receives the operator's half of the ceremony. Nil suppresses
	// it — which, since the ceremony is the operator reading a link and a
	// code, is only ever right for a test.
	Logger func(msg string)

	// Progress, when it is a terminal, carries the live wait line while
	// the poll runs, exactly as it does for a device login.
	Progress io.Writer

	// BeforeStore, when set, runs after the poll has handed over the
	// credential and the mint has proved it, and before anything is
	// written. A non-nil error aborts the connection and stores nothing.
	// It is where the caller commits the profile entry the credential
	// belongs to — which is why it receives the connection: the account
	// is the server's to name, not the caller's.
	BeforeStore func(conn *AgentConnection) error

	// sleep and now are the poll's clock. Test seams: the loop is paced by
	// the server's interval and bounded by the code's lifetime, and a test
	// should wait for neither.
	sleep func(ctx context.Context, d time.Duration) error
	now   func() time.Time
}

func (o *AgentConnectOptions) defaults() {
	if o.sleep == nil {
		o.sleep = sleepUnlessCanceled
	}
	if o.now == nil {
		o.now = time.Now
	}
}

// presentation is the login-flow view of these options: the same headless
// detection, the same browser announcement, the same live wait line. The
// ceremony's display half is a device login's in every respect that the
// person at the terminal can see, so it is not written twice.
func (o *AgentConnectOptions) presentation() *LoginOptions {
	view := &LoginOptions{
		NoBrowser:       o.NoBrowser,
		Local:           o.Local,
		Logger:          o.Logger,
		Progress:        o.Progress,
		BrowserLauncher: o.BrowserLauncher,
	}
	view.defaults()
	return view
}

// sleepUnlessCanceled waits out d, or returns as soon as ctx ends — a
// Ctrl-C during the interval must stop the ceremony, not be noticed five
// seconds later.
func sleepUnlessCanceled(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ConnectAgent runs the agent-connection handshake and stores what it is
// given as this profile's credential.
//
// Intake, then the operator's browser trip, then the poll — and on the
// first successful poll, the same path an agent login takes: one
// client_credentials mint to prove the credential is good, the caller's
// BeforeStore hook, and one write. Nothing is stored before the mint
// succeeds, so a connection the server would not honor leaves no profile
// behind.
func (m *Manager) ConnectAgent(ctx context.Context, opts AgentConnectOptions) (*AgentConnectResult, error) {
	deviceName, err := agentConnectName("device name", opts.DeviceName, true)
	if err != nil {
		return nil, err
	}
	softwareName, err := agentConnectName("software name", opts.SoftwareName, false)
	if err != nil {
		return nil, err
	}
	if opts.Scope != "" && opts.Scope != scopeRead && opts.Scope != scopeFull {
		return nil, output.ErrUsage("Invalid scope. Use 'read' or 'full'")
	}
	scope := opts.Scope
	if scope == "" {
		scope = scopeFull
	}
	opts.defaults()

	log := opts.Logger
	if log == nil {
		log = func(string) {}
	}

	disc, err := m.discoverOAuth(ctx, log)
	if err != nil {
		return nil, err
	}
	// Checked before the intake, not after: a server with no agent grant
	// cannot finish this ceremony, and finding that out after the operator
	// has approved something would be the worst moment to say so.
	if err := requireAgentAuthorizationServer(disc); err != nil {
		return nil, err
	}

	client, err := m.bc5Client()
	if err != nil {
		return nil, err
	}

	intake, err := m.openAgentConnection(ctx, client, disc, deviceName, softwareName, scope)
	if err != nil {
		return nil, err
	}

	wait := announceAgentConnection(opts.presentation(), intake, opts.now())
	conn, err := m.awaitAgentConnection(ctx, &opts, client, intake)
	wait.Stop()
	if err != nil {
		return nil, err
	}

	// The scope stored is the one the poll answered with: approval
	// provisions the client with what the operator approved, so a read
	// downgrade narrows the client itself and minting under the scope that
	// was ASKED for would be refused.
	result, err := m.adoptAgentGrantOrSayWhatWasLost(ctx, disc, conn, log, ClientCredentialsOptions{
		ClientID:     conn.ClientID,
		ClientSecret: conn.ClientSecret,
		Scope:        conn.Scope,
		Logger:       opts.Logger,
		BeforeStore: func(*LoginResult) error {
			if opts.BeforeStore == nil {
				return nil
			}
			return opts.BeforeStore(conn)
		},
	})
	if err != nil {
		return nil, err
	}

	return &AgentConnectResult{LoginResult: *result, AccountID: conn.AccountID, ClientID: conn.ClientID}, nil
}

// adoptAgentGrantOrSayWhatWasLost stores the handed-over credential, and
// says what a failure costs.
//
// The poll that answered burned the one-time code and MINTED THE AGENT'S
// SECRET — a rotation, for an agent something was already connected to —
// so what this holds is the only live copy and the server will not hand it
// over twice. A failure here (a refused mint, a profile that cannot be
// written, a Ctrl-C landing in the gap) therefore leaves the agent
// connected to nothing. Retrying this command starts a new ceremony, which
// is the right remedy; polling the spent code again is not, and neither is
// looking for the secret somewhere.
func (m *Manager) adoptAgentGrantOrSayWhatWasLost(ctx context.Context, disc *discovery, conn *AgentConnection, log func(string), opts ClientCredentialsOptions) (*LoginResult, error) {
	result, err := m.adoptAgentGrant(ctx, disc, opts, conn.Scope)
	if err != nil {
		log("warning: the connection was approved and the agent's client secret was handed over once, but keeping it failed. Disconnect the agent in Basecamp and connect again.")
		return nil, err
	}
	return result, nil
}

// agentIntake is the intake's answer, validated: what to show the
// operator, where to poll, and how to pace it.
type agentIntake struct {
	deviceCode      string
	userCode        string
	verificationURI string
	tokenURI        string
	lifetime        time.Duration
	interval        time.Duration
}

// openAgentConnection makes the anonymous intake request and validates
// everything the rest of the ceremony will act on.
func (m *Manager) openAgentConnection(ctx context.Context, client *http.Client, disc *discovery, deviceName, softwareName, scope string) (*agentIntake, error) {
	endpoint, err := agentConnectionsEndpoint(disc)
	if err != nil {
		return nil, err
	}

	form := url.Values{
		"device_name": {deviceName},
		"scope":       {scope},
	}
	if softwareName != "" {
		form.Set("software_name", softwareName)
	}

	answer, err := m.postAgentConnect(ctx, client, agentConnectIntakeOp, endpoint, form)
	if err != nil {
		return nil, err
	}
	if answer.status != http.StatusOK {
		return nil, agentConnectRefusal(agentConnectIntakeOp, answer)
	}

	var intake struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		TokenURI                string `json:"token_uri"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	if err := json.Unmarshal(answer.body, &intake); err != nil {
		return nil, output.ErrAPI(answer.status, agentConnectIntakeOp+": the response could not be parsed")
	}

	// The link is the one the operator's browser is sent to and the code
	// is printed to their terminal, so both are judged before either is
	// used: the URL by the policy every OAuth browser URL is held to, the
	// code by stripping control sequences and then requiring something
	// left to read.
	target := validVerificationURL(intake.VerificationURIComplete)
	if target == "" {
		target = validVerificationURL(intake.VerificationURI)
	}
	userCode := strings.TrimSpace(richtext.SanitizeSingleLine(intake.UserCode))
	if target == "" || userCode == "" || strings.TrimSpace(intake.DeviceCode) == "" {
		return nil, output.ErrAPI(answer.status, agentConnectIntakeOp+": the server returned a malformed agent connection")
	}

	tokenURI, err := agentConnectTokenURI(intake.TokenURI, endpoint)
	if err != nil {
		return nil, err
	}

	return &agentIntake{
		deviceCode:      intake.DeviceCode,
		userCode:        userCode,
		verificationURI: target,
		tokenURI:        tokenURI,
		lifetime:        agentConnectLifetime(intake.ExpiresIn),
		interval:        agentConnectInterval(intake.Interval),
	}, nil
}

// announceAgentConnection prints the operator's half of the ceremony and
// opens their browser, and starts the live wait line when the terminal can
// take one. Stop on the nil it returns otherwise is a no-op.
func announceAgentConnection(view *LoginOptions, intake *agentIntake, now time.Time) *approvalWait {
	// Link first, code second, each alone on its line so one click copies
	// one of them. The warning is RFC 8628 §5.4's remote-phishing defense:
	// a code someone else handed over connects THEIR computer.
	view.log("\nConnect this computer to a Basecamp agent\n")
	view.log("  1. Open this link on any device")
	view.log("     " + intake.verificationURI)
	view.log("  2. Check that the code shown there matches (expires in " + expiresIn(intake.lifetime) + ")")
	view.log("     " + intake.userCode)
	view.log("  3. Pick the agent this computer acts as, and approve it")
	view.log("")
	view.log("Only continue if you started this connection yourself. If a website or another")
	view.log("person gave you this code, press Ctrl-C now.")
	view.log("")
	view.announceBrowser(intake.verificationURI)

	if wait := startApprovalWait(view.Progress, now.Add(intake.lifetime)); wait != nil {
		return wait
	}
	view.log("Waiting for approval… (the code expires in " + expiresIn(intake.lifetime) + ")")
	return nil
}

// awaitAgentConnection polls until the operator approves, declines, or the
// code expires.
//
// The interval is the server's to choose and only ever grows: slow_down
// adds to it, a Retry-After sets a floor under it. The local deadline is
// the backstop for a server that answers authorization_pending forever —
// the code it issued is dead by then whatever it says.
func (m *Manager) awaitAgentConnection(ctx context.Context, opts *AgentConnectOptions, client *http.Client, intake *agentIntake) (*AgentConnection, error) {
	deadline := opts.now().Add(intake.lifetime)
	interval := intake.interval
	for {
		if err := opts.sleep(ctx, interval); err != nil {
			return nil, err
		}
		if !opts.now().Before(deadline) {
			return nil, agentConnectExpired()
		}
		conn, next, err := m.pollAgentConnection(ctx, client, intake, interval)
		if err != nil {
			return nil, err
		}
		if conn != nil {
			return conn, nil
		}
		interval = next
	}
}

// pollAgentConnection makes one poll. A connection answers it; a poll that
// is merely early answers with the interval to use next and no error.
func (m *Manager) pollAgentConnection(ctx context.Context, client *http.Client, intake *agentIntake, interval time.Duration) (*AgentConnection, time.Duration, error) {
	answer, err := m.postAgentConnect(ctx, client, agentConnectPollOp, intake.tokenURI,
		url.Values{"device_code": {intake.deviceCode}})
	if err != nil {
		return nil, 0, err
	}
	if answer.status == http.StatusOK {
		conn, parseErr := parseAgentConnection(answer)
		return conn, 0, parseErr
	}

	switch oauthErrorCode(answer.body) {
	case "authorization_pending":
		return nil, interval, nil
	case "slow_down":
		return nil, clampAgentConnectInterval(interval + agentConnectSlowDownStep), nil
	case "access_denied":
		return nil, 0, agentConnectFailure("The agent connection was declined",
			"Run the connect command again and approve it in the browser.")
	case "expired_token":
		return nil, 0, agentConnectExpired()
	case "invalid_grant":
		// The code was already spent, or the agent's account was canceled
		// or frozen between approval and this poll. Both are a connection
		// to start again, and neither says anything a second poll would
		// answer differently.
		return nil, 0, agentConnectFailure("The agent connection could not be collected (token error: invalid_grant)",
			"The code may already have been used, or the agent's account is no longer active. Run the connect command again.")
	}

	// Throttling and the server's own trouble are not verdicts on this
	// connection: back off and keep polling, which the code's lifetime
	// bounds anyway. Everything else is final.
	if answer.status == http.StatusTooManyRequests || answer.status >= 500 {
		return nil, agentConnectBackoff(answer, interval), nil
	}
	return nil, 0, agentConnectRefusal(agentConnectPollOp, answer)
}

// parseAgentConnection validates the four values the poll hands over. Each
// one is stored, and three of the four are written into a config file or a
// URL path later, so a malformed one is refused here rather than persisted.
func parseAgentConnection(answer *agentConnectAnswer) (*AgentConnection, error) {
	status := answer.status
	var handover struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		AccountID    string `json:"account_id"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(answer.body, &handover); err != nil {
		return nil, output.ErrAPI(status, agentConnectPollOp+": the response could not be parsed")
	}

	conn := &AgentConnection{
		ClientID:     handover.ClientID,
		ClientSecret: handover.ClientSecret,
		AccountID:    handover.AccountID,
		Scope:        handover.Scope,
	}
	switch {
	case conn.ClientID == "":
		return nil, output.ErrAPI(status, agentConnectPollOp+": the response carries no client_id")
	case conn.ClientSecret == "":
		return nil, output.ErrAPI(status, agentConnectPollOp+": the response carries no client_secret")
	case hasSpaceOrControl(conn.ClientID):
		return nil, output.ErrAPI(status, agentConnectPollOp+": the response carries a client_id with whitespace or control characters")
	}
	// The account id goes into the profile entry and every later request
	// path; the CLI's own account rule is digits only.
	if !isDigits(conn.AccountID) {
		return nil, output.ErrAPI(status, agentConnectPollOp+": the response carries an account_id that is not a number")
	}
	// The CLI can only represent read and full, and the scope is stored,
	// written into the profile entry, printed, and sent on every mint. A
	// response naming anything else is refused rather than persisted — the
	// judgment the mint already makes for a token response.
	if conn.Scope != scopeRead && conn.Scope != scopeFull {
		return nil, output.ErrAPI(status, agentConnectPollOp+": the operator approved a scope other than read or full, and only those can be stored")
	}
	return conn, nil
}

// agentConnectAnswer is one answered request in the ceremony, drained: the
// status and headers that classify it, and the body that was read under
// the same bound a token response is read under. The live response never
// leaves the function that closed it.
type agentConnectAnswer struct {
	status int
	header http.Header
	body   []byte
}

// failure classifies the answer the way the SDK classifies any other
// response — 429 a rate limit with its Retry-After, 5xx retryable, the
// rest final. statusFailure reads only the status and that header, both of
// which are kept here, so it is handed those rather than a response whose
// body is already spent.
func (a *agentConnectAnswer) failure(msg string) error {
	return statusFailure(msg, &http.Response{StatusCode: a.status, Header: a.header})
}

// postAgentConnect makes one request in the ceremony, to the rules the
// mint's own token request follows: a bounded read, a refused redirect,
// and a request that names its caller — the endpoints answer 400 to one
// that does not.
func (m *Manager) postAgentConnect(ctx context.Context, client *http.Client, op, endpoint string, form url.Values) (*agentConnectAnswer, error) {
	reqCtx, cancel := context.WithTimeout(ctx, agentConnectTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, wrapOAuthError(op, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", version.UserAgent())

	resp, err := client.Do(req)
	if err != nil {
		return nil, wrapOAuthError(op, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// The lane client hands a 3xx back rather than following it on a POST
	// like this one. Classify it before the body read, as the mint does.
	if isRedirect(resp.StatusCode) {
		return nil, output.ErrAPI(resp.StatusCode,
			fmt.Sprintf("%s: redirect %d on the agent connection endpoint is not followed", op, resp.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAgentConnectBytes+1))
	if err != nil {
		return nil, wrapOAuthError(op, err)
	}
	if int64(len(body)) > maxAgentConnectBytes {
		return nil, output.ErrAPI(resp.StatusCode,
			fmt.Sprintf("%s: response body exceeds %d bytes", op, maxAgentConnectBytes))
	}
	return &agentConnectAnswer{status: resp.StatusCode, header: resp.Header, body: body}, nil
}

// agentConnectRefusal renders a refused request: the RFC 6749 §5.2 error
// code when the body carries one this package knows, and the HTTP status
// otherwise. Nothing else from the body is repeated — the reason
// oauthErrorCodes gives — and the poll's body is answered on a request
// carrying a live device code.
func agentConnectRefusal(op string, answer *agentConnectAnswer) error {
	detail := fmt.Sprintf("the server answered HTTP %d", answer.status)
	if code := oauthErrorCode(answer.body); code != "" {
		detail = "token error: " + code
	}
	if answer.status == http.StatusServiceUnavailable {
		return agentConnectFailure(op+": "+detail,
			"This Basecamp is not issuing agent connections right now. Ask your Basecamp administrator, or sign in as a bot user instead: basecamp auth login --device-code.")
	}
	return answer.failure(op + ": " + detail)
}

// oauthErrorCode is the RFC 6749 §5.2 error code a body names, when it is
// one this package knows what to do with, and "" otherwise.
func oauthErrorCode(body []byte) string {
	var refusal struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &refusal) != nil || !oauthErrorCodes[refusal.Error] {
		return ""
	}
	return refusal.Error
}

// agentConnectBackoff is how long to wait after a throttled or failed
// poll: what the server asked for, or one slow_down step. It never
// shortens the interval — a server under pressure asking for less is not a
// reason to poll it faster.
func agentConnectBackoff(answer *agentConnectAnswer, interval time.Duration) time.Duration {
	next := interval + agentConnectSlowDownStep
	if seconds, err := strconv.Atoi(strings.TrimSpace(answer.header.Get("Retry-After"))); err == nil && seconds > 0 {
		if asked := reportedSeconds(seconds, maxAgentConnectInterval); asked > next {
			next = asked
		}
	}
	return clampAgentConnectInterval(next)
}

// agentConnectExpired is the one ending that is nobody's fault: the code
// was never approved in the ten minutes it was good for.
func agentConnectExpired() error {
	return agentConnectFailure("The agent connection code expired before it was approved",
		"Run the connect command again; the code it prints is good for ten minutes.")
}

// agentConnectFailure is an auth-class refusal carrying a remedy of its
// own, rather than the package default — every remedy here is "run the
// connect command again", not "log in".
func agentConnectFailure(msg, hint string) error {
	err := output.ErrAuth(msg)
	err.Hint = hint
	return err
}

// requireAgentAuthorizationServer refuses a discovery that fell back to
// Launchpad. Launchpad has no agent principals: an agent login would send
// a client secret there on the strength of a guess, and this ceremony
// would ask an operator to approve something that cannot exist.
func requireAgentAuthorizationServer(disc *discovery) error {
	if disc.oauthType == oauthTypeBC5 {
		return nil
	}
	return output.ErrUsageHint(
		"This server has no agent grant: OAuth discovery selected the Launchpad fallback",
		"Agent logins need Basecamp's own authorization server. Check BASECAMP_BASE_URL, or sign in as a bot user instead: basecamp auth login --device-code.")
}

// agentConnectionsEndpoint is the intake URL on the authorization server
// discovery selected. The ceremony is not advertised in the metadata
// document, so it is derived from the issuer — which is checked here like
// any other endpoint a request is about to be made to.
func agentConnectionsEndpoint(disc *discovery) (string, error) {
	issuer := disc.issuer
	if issuer == "" {
		issuer = disc.config.Issuer
	}
	if issuer == "" {
		return "", output.ErrAuth("the authorization server named no issuer, so the agent connection endpoint cannot be derived")
	}
	endpoint := strings.TrimRight(issuer, "/") + agentConnectionsPath
	if err := requireSecureOAuthEndpoint("agent connection endpoint", endpoint); err != nil {
		return "", err
	}
	return endpoint, nil
}

// agentConnectTokenURI validates the poll endpoint the intake named.
//
// The poll carries the device code and is answered with a client secret,
// and this value arrives in a response body rather than in the signed
// metadata document — so it is held to the endpoint policy AND to the
// origin the intake was made to. A server that wants the poll somewhere
// else can mount it on a different path; it cannot send the handover to a
// different host.
func agentConnectTokenURI(raw, endpoint string) (string, error) {
	if err := requireSecureOAuthEndpoint("agent connection token endpoint", raw); err != nil {
		return "", err
	}
	if !sameOrigin(raw, endpoint) {
		return "", output.ErrAPI(0, agentConnectIntakeOp+": the response named a token endpoint on another origin, which the poll does not follow")
	}
	return raw, nil
}

// sameOrigin reports whether two URLs share a scheme and host. Both have
// already parsed and passed the endpoint policy.
func sameOrigin(first, second string) bool {
	a, err := url.Parse(first)
	if err != nil {
		return false
	}
	b, err := url.Parse(second)
	if err != nil {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

// agentConnectLifetime is how long the printed code is good for: what the
// server reported, bounded, or the documented ten minutes when it reported
// nothing usable.
func agentConnectLifetime(expiresIn int) time.Duration {
	if expiresIn <= 0 {
		return defaultAgentConnectLifetime
	}
	return reportedSeconds(expiresIn, maxAgentConnectLifetime)
}

// agentConnectInterval is the pace the server asked for, bounded, or the
// RFC's five seconds when it asked for nothing.
func agentConnectInterval(interval int) time.Duration {
	if interval <= 0 {
		return defaultAgentConnectInterval
	}
	return clampAgentConnectInterval(reportedSeconds(interval, maxAgentConnectInterval))
}

// reportedSeconds is a count of seconds a server reported, as a duration,
// capped BEFORE the conversion: multiplying an unbounded one by a second
// overflows into a negative duration, which every comparison downstream
// would read as "already past" or "no wait at all".
func reportedSeconds(seconds int, ceiling time.Duration) time.Duration {
	if seconds > int(ceiling/time.Second) {
		return ceiling
	}
	return time.Duration(seconds) * time.Second
}

func clampAgentConnectInterval(d time.Duration) time.Duration {
	return min(max(d, minAgentConnectInterval), maxAgentConnectInterval)
}

// agentConnectName checks one of the two self-asserted names against the
// intake's own rule, so a name this CLI sends is one the intake takes.
// Control characters are refused on top of it: the value is displayed on
// the operator's approval page and kept on the connection.
func agentConnectName(label, name string, required bool) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		if required {
			return "", output.ErrUsageHint("An agent connection needs a "+label,
				"Pass --device-name <name>; it names this computer on the approval page.")
		}
		return "", nil
	}
	if utf8.RuneCountInString(name) > maxAgentConnectNameChars {
		return "", output.ErrUsage(fmt.Sprintf("The %s is longer than %d characters", label, maxAgentConnectNameChars))
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", output.ErrUsage("The " + label + " must not contain control characters")
	}
	return name, nil
}

// hasSpaceOrControl reports whether s carries whitespace or a control
// character — neither belongs in a value this CLI stores and echoes.
func hasSpaceOrControl(s string) bool {
	return strings.IndexFunc(s, func(c rune) bool { return unicode.IsSpace(c) || unicode.IsControl(c) }) >= 0
}

// isDigits reports whether s is a non-empty run of ASCII digits, the CLI's
// rule for an account id everywhere else.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
