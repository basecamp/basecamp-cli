package auth

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
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
// owns two sub-transports: the lane's address-policed direct transport
// (surfguard dial-time enforcement; Proxy nil by construction), and — only
// when the operator set BASECAMP_OAUTH_USE_PROXY=1 — a proxying clone of
// http.DefaultTransport.
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
	guarded http.RoundTripper
	proxied http.RoundTripper
	resolve func(*url.URL) (*url.URL, error)
	optOut  bool
	warnf   func(format string, args ...any)

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
		return t.guarded.RoundTrip(req)
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
		return t.guarded.RoundTrip(req)
	}
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
		m.bc5Lane.client, m.bc5Lane.err = m.buildLaneClient(m.cfg.BaseURL, "base URL")
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
		m.lpLane.client, m.lpLane.err = m.buildLaneClient(lpURL, "Launchpad URL")
	})
	return m.lpLane.client, m.lpLane.err
}

func (m *Manager) buildLaneClient(anchorURL, label string) (*http.Client, error) {
	policy, err := oauthEndpointPolicy(anchorURL, label)
	if err != nil {
		return nil, err
	}
	st := m.proxyState()
	tr := &oauthTransport{
		guarded: policy.RoundTripper(),
		resolve: st.resolve,
		optOut:  st.optOut,
		warnf:   m.warnf,
		warned:  map[string]bool{},
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
