package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/oauth"
	surfguard "github.com/basecamp/surfguard/go"
	"golang.org/x/net/http/httpproxy"

	"github.com/basecamp/basecamp-cli/internal/hostutil"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// oauthUseProxyEnv opts OAuth traffic out of the SSRF address policy for
// requests the environment's proxy configuration actually routes to a proxy.
// Only the exact value "1" enables it. Set-but-empty is treated as unset,
// silently: env-scrubbing tooling (direnv, t.Setenv) writes "" and cannot
// be told apart from intent. Any other non-empty value is refused with a
// warning rather than guessed at.
const oauthUseProxyEnv = "BASECAMP_OAUTH_USE_PROXY"

// oauthAllowPrivateEnv admits private network addresses (RFC 1918 and IPv6
// unique local) on the BC5 OAuth lane, for a base URL served inside a private
// network: a beta preview's API host resolves to the previews load balancer,
// reachable only on the VPN. It is read like oauthUseProxyEnv: only "1"
// enables it, "" is unset, and anything else is refused with a warning.
//
// It is the operator vouching for the network of the base URL they
// configured, as a local base URL vouches for loopback, so it applies to that
// lane alone: never to the Launchpad lane, and never to a production base URL
// (firstPartyOAuthHosts), whose discovery documents must not be able to steer
// the CLI to an internal address because a variable was left set. Loopback,
// link-local, cloud metadata and CGNAT space stay refused.
const oauthAllowPrivateEnv = "BASECAMP_OAUTH_ALLOW_PRIVATE"

// privateNetworkPrefixes is what oauthAllowPrivateEnv admits.
var privateNetworkPrefixes = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("fc00::/7"),
}

// privateNetworkPolicy judges an address inside privateNetworkPrefixes once
// the opt-in is on. fd00:ec2::254 is EC2's IPv6 instance metadata service,
// which sits inside the unique local range and stays refused.
var privateNetworkPolicy = surfguard.Policy{}.AllowAllPorts().
	Allow(privateNetworkPrefixes...).
	Deny(netip.MustParsePrefix("fd00:ec2::254/128"))

// privateNetworkAdmits reports whether the opt-in admits addr: it is in
// privateNetworkPrefixes and privateNetworkPolicy does not refuse it.
func privateNetworkAdmits(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, p := range privateNetworkPrefixes {
		if p.Contains(addr) {
			return !privateNetworkPolicy.Blocked(addr)
		}
	}
	return false
}

// privateNetworkTransport is an opted-in lane's direct transport. The lane's
// own policy judges every address, as it does without the opt-in; only an
// address it refuses that privateNetworkAdmits is let through, so everything
// else the IANA special-use tables refuse (NAT64, AMT, AS112 and the rest)
// stays refused. Each dial is judged at connect time, which is what defeats
// DNS rebinding.
func privateNetworkTransport(policy surfguard.Policy) http.RoundTripper {
	t := policy.Transport()
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, ControlContext: privateNetworkControl(policy)}
	t.DialContext = dialer.DialContext
	return privateNetworkRoundTripper{policy: policy, next: t}
}

// privateNetworkControl is the opted-in lane's dial check: the lane's policy,
// and where it refuses, privateNetworkPolicy for an address
// privateNetworkAdmits.
func privateNetworkControl(policy surfguard.Policy) func(context.Context, string, string, syscall.RawConn) error {
	return func(ctx context.Context, network, address string, c syscall.RawConn) error {
		err := policy.ControlContext(ctx, network, address, c)
		if err == nil {
			return nil
		}
		ap, perr := netip.ParseAddrPort(address)
		if perr != nil || !privateNetworkAdmits(ap.Addr()) {
			return err
		}
		return privateNetworkPolicy.ControlContext(ctx, network, address, c)
	}
}

// privateNetworkRoundTripper refuses a request whose URL the lane's policy
// finds malformed (scheme or host) before the transport dials, as
// surfguard's own RoundTripper does. Its verdict on the address is left to
// the dial.
type privateNetworkRoundTripper struct {
	policy surfguard.Policy
	next   *http.Transport
}

func (r privateNetworkRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, &surfguard.Violation{Reason: surfguard.ReasonMalformedHost}
	}
	// surfguard's dialer rewrites a legacy numeric host (0x0a1405e5,
	// 012.024.5.229) to the address it means; a plain net.Dialer would send
	// it to DNS instead. No OAuth endpoint is named that way, so refuse it.
	if isLegacyNumericHost(req.URL.Hostname()) {
		return nil, &surfguard.Violation{Host: req.URL.Hostname(), Reason: surfguard.ReasonMalformedHost}
	}
	var v *surfguard.Violation
	if err := r.policy.CheckURL(req.Context(), req.URL.String()); errors.As(err, &v) &&
		(v.Reason == surfguard.ReasonMalformedHost || v.Reason == surfguard.ReasonScheme) {
		return nil, err
	}
	return r.next.RoundTrip(req)
}

// isLegacyNumericHost reports whether host is an IPv4 address in a spelling
// other than dotted decimal: every dot-separated part a decimal, octal or 0x
// hex number. A DNS name's last label is never all digits, and a canonical
// literal parses as an address.
func isLegacyNumericHost(host string) bool {
	if host == "" || strings.Contains(host, ":") {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	for _, part := range strings.Split(strings.ToLower(host), ".") {
		digits := strings.TrimPrefix(part, "0x")
		if digits == "" && part != "0x" {
			return false
		}
		for _, c := range digits {
			if (c < '0' || c > '9') && (part == digits || c < 'a' || c > 'f') {
				return false
			}
		}
	}
	return true
}

// CloseIdleConnections forwards to the transport, as http.Client expects.
func (r privateNetworkRoundTripper) CloseIdleConnections() { r.next.CloseIdleConnections() }

// oauthClientTimeout bounds each OAuth HTTP request on the lane clients —
// the same 30s the pre-lane appctx client carried.
const oauthClientTimeout = 30 * time.Second

// checkAuthClientRedirect is the CheckRedirect guard for the Manager's OAuth
// lane clients (discovery, device flow, token exchange and refresh). Refuse to
// follow redirects for non-idempotent requests: RFC 6749 token endpoints don't
// legitimately 3xx-redirect POSTs, and because the exchange/refresh requests
// set GetBody, Go would replay the auth code / refresh_token to the redirect
// target (only the initial endpoint is origin-validated). Idempotent GET/HEAD
// requests (e.g. OAuth discovery) carry no credential body, so they may follow
// redirects normally — blocking those would needlessly fail discovery and
// force the Launchpad fallback. Still cap the hop count so a looping endpoint
// fails fast instead of spinning until the client timeout.
func checkAuthClientRedirect(_ *http.Request, via []*http.Request) error {
	if len(via) > 0 && via[0].Method != http.MethodGet && via[0].Method != http.MethodHead {
		return http.ErrUseLastResponse
	}
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	return nil
}

// oauthEndpointPolicy derives the address policy for one OAuth egress lane
// from that lane's anchor URL — the operator-configured base of the flows the
// lane carries, not any URL a server response named. Loopback is admitted
// only when the anchor itself is local: a developer pointing the CLI at
// http://3.basecamp.localhost:3001 has trusted that space explicitly, and the
// admission must not leak to the other lane (a localhost Launchpad override
// must not let production BC5 metadata name a loopback token endpoint).
//
// The label names the anchor in errors without echoing its value: anchor URLs
// can arrive via environment overrides, and parse failures can reproduce
// their input.
func oauthEndpointPolicy(anchorURL, label string) (surfguard.Policy, error) {
	u, err := url.Parse(anchorURL)
	if err != nil || u.Opaque != "" || !u.IsAbs() || u.Hostname() == "" {
		return surfguard.Policy{}, output.ErrAuth(fmt.Sprintf("invalid %s: must be an absolute URL with a hostname", label))
	}
	policy := oauth.DefaultIssuerPolicy()
	// IsLocalhost matches a host[:port] string case-sensitively; DNS names
	// are case-insensitive, so lowercase first — the same normalization
	// resourceOrigin and isSecureEndpointURL apply.
	if hostutil.IsLocalhost(strings.ToLower(u.Host)) {
		policy = policy.AllowLoopback()
	}
	return policy, nil
}

// privateOptIn reports whether oauthAllowPrivateEnv is "1". Read once per
// Manager, so a malformed value warns once.
func (m *Manager) privateOptIn() bool {
	m.allowPrivateOnce.Do(func() {
		switch v := os.Getenv(oauthAllowPrivateEnv); v {
		case "":
		case "1":
			m.allowPrivate = true
		default:
			m.warnf("warning: %s=%q is not understood (only \"1\" enables it); OAuth requests keep the SSRF address policy", oauthAllowPrivateEnv, v)
		}
	})
	return m.allowPrivate
}

// isFirstPartyHost reports whether host names one of firstPartyOAuthHosts,
// in any case and with or without the root's trailing dot.
func isFirstPartyHost(host string) bool {
	return firstPartyOAuthHosts[strings.TrimSuffix(strings.ToLower(host), ".")]
}

// allowPrivateFor reports whether a BC5 lane anchored on baseURL admits
// private addresses: the opt-in is on and baseURL is not a production host.
func (m *Manager) allowPrivateFor(baseURL string) bool {
	if !m.privateOptIn() {
		return false
	}
	if u, err := url.Parse(baseURL); err != nil || isFirstPartyHost(u.Hostname()) {
		m.warnf("warning: %s=1 is ignored for a production base URL; OAuth requests keep the SSRF address policy", oauthAllowPrivateEnv)
		return false
	}
	return true
}

// privateBaseURLHint explains a failed discovery whose base URL resolves only
// to addresses the opt-in would admit, naming it. It returns "" when the
// opt-in is already on, or the host is a production one, unresolvable, or
// resolves anywhere else (public, loopback, metadata).
func (m *Manager) privateBaseURLHint(ctx context.Context, origin string) string {
	if m.httpClient != nil || m.allowPrivateFor(origin) {
		return ""
	}
	u, err := url.Parse(origin)
	if err != nil || isFirstPartyHost(u.Hostname()) {
		return ""
	}
	addrs := []netip.Addr{}
	if a, err := netip.ParseAddr(u.Hostname()); err == nil {
		addrs = append(addrs, a)
	} else {
		// A diagnostic, after discovery already failed: never let a stalled
		// resolver hold up the fallback.
		lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if addrs, err = net.DefaultResolver.LookupNetIP(lookupCtx, "ip", u.Hostname()); err != nil {
			return ""
		}
	}
	if len(addrs) == 0 {
		return ""
	}
	for _, a := range addrs {
		if !privateNetworkAdmits(a) {
			return ""
		}
	}
	return fmt.Sprintf("the base URL resolves to a private network address, which OAuth refuses by default; if you trust it (a beta preview on the VPN), set %s=1", oauthAllowPrivateEnv)
}

// firstPartyOAuthHosts are the hosts 37signals operates that the OAuth flows
// reach: the BC5 API and its issuer under both the original and the current
// domains (credentials stored before the move name
// https://3.basecamp.com/oauth/tokens), and Launchpad. The set is fixed at
// build time. Neither a configured base URL nor anything a discovery document
// names can add to it, which is what keeps firstPartyOAuthPolicy from ever
// reaching an endpoint a remote peer chose.
var firstPartyOAuthHosts = map[string]bool{
	"3.basecampapi.com":       true,
	"api.basecamp.com":        true,
	"3.basecamp.com":          true,
	"app.basecamp.com":        true,
	"launchpad.37signals.com": true,
}

// fakeIPRange is 198.18.0.0/15, the RFC 2544 benchmarking block. Fake-IP DNS
// (mihomo/Clash Meta and sing-box in fake-ip mode, and the egress of hosted
// agent machines such as Grok's) answers every name from it and carries the
// connection on to the real host, so on those networks app.basecamp.com
// resolves here.
var fakeIPRange = netip.MustParsePrefix("198.18.0.0/15")

// firstPartyOAuthPolicy is the address policy for requests to
// firstPartyOAuthHosts: surfguard's default deny tables (private, loopback,
// link-local and cloud metadata, CGNAT, multicast) with the fake-IP range
// admitted, on port 443 only. It leaves out IANASpecialUse because surfguard's
// Allow cannot override that table. Leaving it out also admits globally
// reachable anycast services (AS112, AMT), and NAT64 addresses whose embedded
// IPv4 is itself admitted, which is how an IPv6-only network with DNS64
// reaches a public host. TLS still verifies the certificate
// against the first-party name, so a connection that lands anywhere else fails
// its handshake before a credential is sent.
func firstPartyOAuthPolicy() surfguard.Policy {
	return surfguard.Policy{}.Allow(fakeIPRange).AllowPorts(443)
}

// firstPartyOAuthPolicy is the Manager's first-party policy: the
// firstPartyPolicy field when a test set one.
func (m *Manager) firstPartyOAuthPolicy() surfguard.Policy {
	if m.firstPartyPolicy != nil {
		return m.firstPartyPolicy()
	}
	return firstPartyOAuthPolicy()
}

// isFirstPartyOAuthURL reports whether u is an HTTPS request on the default
// port to one of firstPartyOAuthHosts. Plain HTTP (no certificate vouches for
// the answer), an explicit non-default port, and near-miss spellings such as a
// trailing dot all keep the lane's own policy.
func isFirstPartyOAuthURL(u *url.URL) bool {
	if u.Scheme != "https" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	return firstPartyOAuthHosts[strings.ToLower(u.Hostname())]
}

// proxyEnvState is the Manager's one construction-time snapshot of the proxy
// environment: the resolver both lanes route and warn against, and whether
// the operator opted OAuth traffic out of address enforcement when a proxy
// applies. Snapshotting once keeps routing and warnings from ever
// disagreeing — http.ProxyFromEnvironment caches process-globally on its own
// schedule and could diverge from a second FromEnvironment read.
type proxyEnvState struct {
	resolve func(*url.URL) (*url.URL, error)
	optOut  bool
}

func (m *Manager) proxyState() *proxyEnvState {
	m.proxyOnce.Do(func() {
		st := &proxyEnvState{resolve: httpproxy.FromEnvironment().ProxyFunc()}
		// Set-but-empty means unset, matching how httpproxy reads its own
		// variables: "" is what env-scrubbing tooling writes, not a request.
		switch v := os.Getenv(oauthUseProxyEnv); v {
		case "":
		case "1":
			st.optOut = true
		default:
			// A malformed opt-out is OFF, loudly: silently honoring "yes"
			// would disable enforcement on a value nobody defined, and
			// silently ignoring it would leave the operator believing they
			// opted out.
			m.warnf("warning: %s=%q is not understood (only \"1\" enables it); OAuth requests keep the SSRF address policy", oauthUseProxyEnv, v)
		}
		m.proxyEnv = st
	})
	return m.proxyEnv
}

// warnf routes transport-policy warnings to the Manager's Warnf seam, or
// stderr by default — these fire inside RoundTrip, where no command logger
// is in scope.
//
// The rendered message is sanitized as a whole before it reaches either
// sink: the endpoint host and path come from OAuth discovery documents, the
// proxy host from the environment, and url.Parse admits UTF-8 C1 controls
// (U+009B CSI and friends) in a host verbatim. Scrubbing once here covers
// every field the warnings interpolate, including ones added later.
func (m *Manager) warnf(format string, args ...any) {
	msg := richtext.SanitizeSingleLine(fmt.Sprintf(format, args...))
	if m.Warnf != nil {
		m.Warnf("%s", msg)
		return
	}
	fmt.Fprintln(os.Stderr, msg)
}

// oauthTransport is the per-lane egress RoundTripper for OAuth traffic. It
// owns two address-policed direct transports (surfguard dial-time
// enforcement; Proxy nil by construction): the lane's own, and one under
// firstPartyOAuthPolicy for firstPartyOAuthHosts. Only when the operator set
// BASECAMP_OAUTH_USE_PROXY=1 does it also own a proxying clone of
// http.DefaultTransport. Which direct transport carries a request is decided
// from that request's URL, so every redirect hop is judged afresh, and a
// lane's anchor never lends the first-party allowance to an endpoint a
// discovery document named.
//
// Routing is per-request and fail-closed:
//
//   - the snapshot proxy resolver errors → the request is refused before
//     either sub-transport runs. A malformed HTTP_PROXY must not degrade
//     into egress the operator asked to route elsewhere;
//   - opt-out set and the resolver names a proxy for this URL → the proxied
//     sub-transport carries it, with address enforcement off for exactly
//     that request (the proxy owns the connection); the downgrade is logged;
//   - everything else → the guarded direct transport. A NO_PROXY exclusion
//     or absent proxy config therefore stays enforced even in opt-out mode —
//     there is no path to unguarded DIRECT egress.
//
// In the default (protected) mode the guarded transport applies
// unconditionally; the resolver is consulted only to warn — deduplicated per
// endpoint — that a configured proxy is being ignored for OAuth traffic.
// Consulting it per actual request URL means the discovered issuer, device,
// polling, and persisted refresh endpoints are each evaluated, not just the
// lane's anchor.
//
// The transports live for the Manager's (process) lifetime and are never
// rebuilt per call; idle connections are bounded by each sub-transport's
// IdleConnTimeout, so a process-lifetime CLI Manager needs no explicit Close.
type oauthTransport struct {
	guarded    http.RoundTripper
	firstParty http.RoundTripper
	proxied    http.RoundTripper
	resolve    func(*url.URL) (*url.URL, error)
	optOut     bool
	warnf      func(format string, args ...any)

	mu     sync.Mutex
	warned map[string]bool
}

func (t *oauthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	proxyURL, err := t.resolve(req.URL)
	if err != nil {
		return nil, fmt.Errorf("refusing OAuth request: proxy configuration error: %w", err)
	}
	switch {
	case proxyURL == nil:
		return t.direct(req.URL).RoundTrip(req)
	case t.optOut:
		// Deduplicated per endpoint: the device poll re-POSTs the same URL
		// every few seconds, and one downgrade notice per endpoint records
		// the decision without drowning the terminal.
		t.warnOnce(req.URL, "warning: OAuth request to %s routed through proxy %s WITHOUT the SSRF address policy (%s=1)",
			redactedEndpoint(req.URL), redactedProxy(proxyURL), oauthUseProxyEnv)
		return t.proxied.RoundTrip(req)
	default:
		t.warnOnce(req.URL, "warning: ignoring proxy %s for OAuth request to %s: the SSRF address policy requires direct egress; set %s=1 to route OAuth through the proxy without address enforcement",
			redactedProxy(proxyURL), redactedEndpoint(req.URL), oauthUseProxyEnv)
		return t.direct(req.URL).RoundTrip(req)
	}
}

// direct picks the address-policed transport for a request that is not going
// through a proxy.
func (t *oauthTransport) direct(u *url.URL) http.RoundTripper {
	if isFirstPartyOAuthURL(u) {
		return t.firstParty
	}
	return t.guarded
}

func (t *oauthTransport) warnOnce(u *url.URL, format string, args ...any) {
	key := redactedEndpoint(u)
	t.mu.Lock()
	seen := t.warned[key]
	t.warned[key] = true
	t.mu.Unlock()
	if !seen {
		t.warnf(format, args...)
	}
}

// redactedEndpoint renders a request URL for warnings and dedupe keys without
// its query or userinfo — endpoint paths are diagnostic, query strings can
// carry parameters that don't belong in a terminal. The path is rendered in
// its percent-encoded form: endpoints come from OAuth discovery documents,
// and url.Parse decodes escapes into Path, so a hostile document could
// otherwise put terminal control sequences or a newline into the warning.
func redactedEndpoint(u *url.URL) string {
	return u.Scheme + "://" + u.Host + u.EscapedPath()
}

// redactedProxy renders a proxy URL as scheme and host only. Proxy URLs come
// from HTTP(S)_PROXY, where credentials may be encoded as a bare username or
// as query parameters — neither of which url.URL.Redacted masks.
func redactedProxy(u *url.URL) string {
	return u.Scheme + "://" + u.Host
}

// proxiedTransport clones http.DefaultTransport (never mutating the global)
// and pins its routing to the SAME construction-time snapshot resolver the
// wrapper consults — http.ProxyFromEnvironment's process-global cache could
// diverge from the snapshot and recreate exactly the unguarded direct
// fallback the wrapper exists to prevent.
func proxiedTransport(resolve func(*url.URL) (*url.URL, error)) *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		// http.DefaultTransport is *http.Transport on every supported
		// runtime; a replaced global is a programming error, not a request
		// failure to limp past.
		panic("http.DefaultTransport is not a *http.Transport")
	}
	t := base.Clone()
	t.Proxy = func(r *http.Request) (*url.URL, error) { return resolve(r.URL) }
	return t
}

// oauthLane caches one per-provenance egress client. Lazy and
// error-returning: a malformed anchor (a bad BASECAMP_LAUNCHPAD_URL above
// all) surfaces at the OAuth operation that needed the lane, not at Manager
// construction, so it cannot break unrelated commands.
type oauthLane struct {
	once   sync.Once
	client *http.Client
	err    error
}

// bc5Client returns the egress client for BC5-provenance OAuth traffic:
// resource-first discovery (both hops), the device flow's authorization and
// polling POSTs, and refreshes of bc5-typed credentials. Its policy derives
// from cfg.BaseURL, so loopback is admitted exactly when the operator
// configured a local Basecamp.
func (m *Manager) bc5Client() (*http.Client, error) {
	if m.httpClient != nil {
		return m.httpClient, nil
	}
	m.bc5Lane.once.Do(func() {
		m.bc5Lane.client, m.bc5Lane.err = m.buildLaneClient(m.cfg.BaseURL, "base URL", m.allowPrivateFor(m.cfg.BaseURL))
	})
	return m.bc5Lane.client, m.bc5Lane.err
}

// launchpadClient returns the egress client for Launchpad-provenance OAuth
// traffic: the web-flow code exchange and refreshes of launchpad-typed
// credentials. Its policy derives from launchpadURL() (environment override
// included), independently of the BC5 lane.
func (m *Manager) launchpadClient() (*http.Client, error) {
	if m.httpClient != nil {
		return m.httpClient, nil
	}
	m.lpLane.once.Do(func() {
		lpURL, err := m.launchpadURL()
		if err != nil {
			m.lpLane.err = err
			return
		}
		m.lpLane.client, m.lpLane.err = m.buildLaneClient(lpURL, "Launchpad URL", false)
	})
	return m.lpLane.client, m.lpLane.err
}

func (m *Manager) buildLaneClient(anchorURL, label string, allowPrivate bool) (*http.Client, error) {
	policy, err := oauthEndpointPolicy(anchorURL, label)
	if err != nil {
		return nil, err
	}
	guarded := policy.RoundTripper()
	if allowPrivate {
		guarded = privateNetworkTransport(policy)
	}
	st := m.proxyState()
	tr := &oauthTransport{
		guarded:    guarded,
		firstParty: m.firstPartyOAuthPolicy().RoundTripper(),
		resolve:    st.resolve,
		optOut:     st.optOut,
		warnf:      m.warnf,
		warned:     map[string]bool{},
	}
	if st.optOut {
		tr.proxied = proxiedTransport(st.resolve)
	}
	return &http.Client{
		Timeout:       oauthClientTimeout,
		CheckRedirect: checkAuthClientRedirect,
		Transport:     tr,
	}, nil
}

// wrapOAuthError maps an SDK token-request failure into the CLI taxonomy
// without flattening it to a string: the SDK's code, HTTP status, and
// retryability survive (a refused redirect keeps its 3xx; a policy refusal
// stays matchable via errors.Is(err, surfguard.ErrBlocked)), and the original
// error stays on the cause chain for errors.As/errors.Is across the CLI
// boundary.
func wrapOAuthError(op string, err error) error {
	e := *output.AsError(err)
	e.Message = op + ": " + e.Message
	e.Cause = err
	return &e
}
