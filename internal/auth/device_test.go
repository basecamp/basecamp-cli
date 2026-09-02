package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/oauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// deviceAS is a mock BC5 authorization server: RFC 8414 metadata advertising
// the device grant, a device-authorization endpoint, and a token endpoint.
// Response hooks are overridable per test; every form POST is recorded.
type deviceAS struct {
	srv *httptest.Server

	mu          sync.Mutex
	deviceForms []url.Values
	tokenForms  []url.Values

	// metadata renders the AS metadata JSON. Defaults to a device-capable doc
	// with issuer = the server's own origin.
	metadata func() string
	// deviceAuth renders the device-authorization response.
	deviceAuth func() (status int, body string)
	// token renders the nth (0-based) token poll response.
	token func(call int) (status int, body string)
}

func startDeviceAS(t *testing.T) *deviceAS {
	t.Helper()
	as := &deviceAS{}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, as.metadata())
	})
	deviceHandler := func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		as.mu.Lock()
		as.deviceForms = append(as.deviceForms, r.PostForm)
		as.mu.Unlock()
		status, body := as.deviceAuth()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}
	tokenHandler := func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		as.mu.Lock()
		call := len(as.tokenForms)
		as.tokenForms = append(as.tokenForms, r.PostForm)
		as.mu.Unlock()
		status, body := as.token(call)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}
	mux.HandleFunc("/oauth/device", deviceHandler)
	mux.HandleFunc("/oauth/token", tokenHandler)
	// The paths Basecamp mounts, which a pinned BASECAMP_OAUTH_ISSUER derives
	// without reading metadata.
	mux.HandleFunc("/oauth/device_authorizations", deviceHandler)
	mux.HandleFunc("/oauth/tokens", tokenHandler)

	as.srv = httptest.NewServer(mux)
	t.Cleanup(as.srv.Close)

	as.metadata = func() string {
		return fmt.Sprintf(`{
			"issuer": %q,
			"token_endpoint": %q,
			"device_authorization_endpoint": %q,
			"grant_types_supported": ["urn:ietf:params:oauth:grant-type:device_code", "refresh_token"]
		}`, as.srv.URL, as.srv.URL+"/oauth/token", as.srv.URL+"/oauth/device")
	}
	as.deviceAuth = func() (int, string) {
		return http.StatusOK, fmt.Sprintf(
			`{"device_code":"dev-code-1","user_code":"ABCD-EFGH","verification_uri":%q,"verification_uri_complete":%q,"expires_in":600,"interval":1}`,
			as.srv.URL+"/verify", as.srv.URL+"/verify?user_code=ABCD-EFGH")
	}
	as.token = func(int) (int, string) {
		return http.StatusOK, `{"access_token":"dev-tok","refresh_token":"dev-ref","token_type":"bearer","expires_in":3600,"scope":"read"}`
	}
	return as
}

func (as *deviceAS) deviceCalls() []url.Values {
	as.mu.Lock()
	defer as.mu.Unlock()
	return append([]url.Values(nil), as.deviceForms...)
}

func (as *deviceAS) tokenCalls() []url.Values {
	as.mu.Lock()
	defer as.mu.Unlock()
	return append([]url.Values(nil), as.tokenForms...)
}

// startResourceServer starts a mock protected resource advertising the given
// authorization servers via RFC 9728 metadata.
func startResourceServer(t *testing.T, asURLs ...string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, _ *http.Request) {
		servers, err := json.Marshal(asURLs)
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"resource": %q, "authorization_servers": %s}`, srv.URL, servers)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// countingServer records how many requests it receives and 404s all of them.
func countingServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	var mu sync.Mutex
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		count++
		mu.Unlock()
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &count
}

func newDeviceTestManager(t *testing.T, baseURL string) *Manager {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	// Pin SSH auto-detection off so LoginOptions.defaults() is deterministic.
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_CLIENT", "")
	t.Setenv("SSH_TTY", "")
	// A pinned issuer in the developer's environment would bypass the
	// discovery every test here exercises.
	t.Setenv("BASECAMP_OAUTH_ISSUER", "")
	cfg := &config.Config{BaseURL: baseURL}
	m := NewManager(cfg, http.DefaultClient)
	m.store = newTestStore(t, tmpDir)
	return m
}

// collectLogger is a mutex-guarded log sink for device-flow tests.
type collectLogger struct {
	mu   sync.Mutex
	logs []string
}

func (c *collectLogger) log(msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, msg)
}

func (c *collectLogger) joined() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.logs, "\n")
}

// instantSleep makes the SDK polling loop tick without real delays.
func instantSleep() oauth.DeviceOption {
	return oauth.WithDeviceSleep(func(context.Context, time.Duration) error { return nil })
}

func TestLoginDevice_HappyPath(t *testing.T) {
	as := startDeviceAS(t)
	resource := startResourceServer(t, as.srv.URL)
	m := newDeviceTestManager(t, resource.URL)

	cl := &collectLogger{}
	var launched []string
	result, err := m.Login(context.Background(), LoginOptions{
		Logger:          cl.log,
		BrowserLauncher: func(u string) error { launched = append(launched, u); return nil },
		deviceOptions:   []oauth.DeviceOption{instantSleep()},
	})
	require.NoError(t, err)

	assert.Equal(t, &LoginResult{OAuthType: "bc5", Scope: "read"}, result)

	// The public client authenticates without a secret.
	deviceCalls := as.deviceCalls()
	require.Len(t, deviceCalls, 1)
	assert.Equal(t, "basecamp-cli", deviceCalls[0].Get("client_id"))
	_, hasSecret := deviceCalls[0]["client_secret"]
	assert.False(t, hasSecret)

	tokenCalls := as.tokenCalls()
	require.NotEmpty(t, tokenCalls)
	assert.Equal(t, "urn:ietf:params:oauth:grant-type:device_code", tokenCalls[0].Get("grant_type"))
	assert.Equal(t, "dev-code-1", tokenCalls[0].Get("device_code"))
	assert.Equal(t, "basecamp-cli", tokenCalls[0].Get("client_id"))

	// Browser launched with the validated code-embedding URI.
	require.Len(t, launched, 1)
	assert.Equal(t, as.srv.URL+"/verify?user_code=ABCD-EFGH", launched[0])

	// Code and URI shown to the user.
	logs := cl.joined()
	assert.Contains(t, logs, "ABCD-EFGH")
	assert.Contains(t, logs, as.srv.URL+"/verify?user_code=ABCD-EFGH")
	assert.Contains(t, logs, "Waiting for approval")
	assert.Contains(t, logs, "(device flow)")

	// Stored credentials carry the bc5 identity.
	creds, err := m.store.Load(config.NormalizeBaseURL(resource.URL))
	require.NoError(t, err)
	assert.Equal(t, "dev-tok", creds.AccessToken)
	assert.Equal(t, "dev-ref", creds.RefreshToken)
	assert.Equal(t, "bc5", creds.OAuthType)
	assert.Equal(t, as.srv.URL+"/oauth/token", creds.TokenEndpoint)
	assert.Equal(t, "read", creds.Scope)
	assert.Positive(t, creds.ExpiresAt)
}

// TestLoginDevice_FlagMatrix covers the browser column of the flag matrix at
// the auth layer: Remote (what --remote/--device-code map to) and NoBrowser
// print the code and URI without launching; the default launches.
func TestLoginDevice_FlagMatrix(t *testing.T) {
	for name, opts := range map[string]LoginOptions{
		"remote":     {Remote: true},
		"no-browser": {NoBrowser: true},
	} {
		t.Run(name+" does not launch", func(t *testing.T) {
			as := startDeviceAS(t)
			resource := startResourceServer(t, as.srv.URL)
			m := newDeviceTestManager(t, resource.URL)

			cl := &collectLogger{}
			launched := 0
			opts.Logger = cl.log
			opts.BrowserLauncher = func(string) error { launched++; return nil }
			opts.deviceOptions = []oauth.DeviceOption{instantSleep()}

			_, err := m.Login(context.Background(), opts)
			require.NoError(t, err)

			assert.Zero(t, launched, "headless mode must not launch a browser")
			logs := cl.joined()
			assert.Contains(t, logs, "ABCD-EFGH")
			assert.Contains(t, logs, as.srv.URL+"/verify?user_code=ABCD-EFGH")
		})
	}

	t.Run("default launches", func(t *testing.T) {
		as := startDeviceAS(t)
		resource := startResourceServer(t, as.srv.URL)
		m := newDeviceTestManager(t, resource.URL)

		launched := 0
		_, err := m.Login(context.Background(), LoginOptions{
			BrowserLauncher: func(string) error { launched++; return nil },
			deviceOptions:   []oauth.DeviceOption{instantSleep()},
		})
		require.NoError(t, err)
		assert.Equal(t, 1, launched)
	})
}

func TestLoginDevice_InvalidURICompleteFallsBackToPlainURI(t *testing.T) {
	for _, evil := range []string{"file:///etc/passwd", "https://user@evil.example/verify", "https://evil.example:70000/verify"} {
		t.Run(evil, func(t *testing.T) {
			as := startDeviceAS(t)
			as.deviceAuth = func() (int, string) {
				return http.StatusOK, fmt.Sprintf(
					`{"device_code":"dc","user_code":"ABCD-EFGH","verification_uri":%q,"verification_uri_complete":%q,"expires_in":600,"interval":1}`,
					as.srv.URL+"/verify", evil)
			}
			resource := startResourceServer(t, as.srv.URL)
			m := newDeviceTestManager(t, resource.URL)

			cl := &collectLogger{}
			var launched []string
			_, err := m.Login(context.Background(), LoginOptions{
				Logger:          cl.log,
				BrowserLauncher: func(u string) error { launched = append(launched, u); return nil },
				deviceOptions:   []oauth.DeviceOption{instantSleep()},
			})
			require.NoError(t, err)

			require.Len(t, launched, 1, "must fall back to the plain verification URI")
			assert.Equal(t, as.srv.URL+"/verify", launched[0])
			assert.NotContains(t, cl.joined(), evil, "invalid URI must never be shown as a target")
		})
	}
}

func TestLoginDevice_MalformedDisplayDataAbortsBeforePolling(t *testing.T) {
	cases := map[string]string{
		"both URIs invalid": fmt.Sprintf(
			`{"device_code":"dc","user_code":"ABCD-EFGH","verification_uri":%q,"verification_uri_complete":%q,"expires_in":600,"interval":1}`,
			"file:///etc/passwd", "javascript:alert(1)"),
		"user code sanitizes to empty": `{"device_code":"dc","user_code":"\u001b[31m","verification_uri":"URI","expires_in":600,"interval":1}`,
		"user code is whitespace only": `{"device_code":"dc","user_code":"   ","verification_uri":"URI","expires_in":600,"interval":1}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			as := startDeviceAS(t)
			resolved := strings.Replace(body, `"URI"`, fmt.Sprintf("%q", as.srv.URL+"/verify"), 1)
			as.deviceAuth = func() (int, string) { return http.StatusOK, resolved }
			resource := startResourceServer(t, as.srv.URL)
			m := newDeviceTestManager(t, resource.URL)

			launched := 0
			_, err := m.Login(context.Background(), LoginOptions{
				Logger:          func(string) {},
				BrowserLauncher: func(string) error { launched++; return nil },
				deviceOptions:   []oauth.DeviceOption{instantSleep()},
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "malformed device authorization")

			var outErr *output.Error
			require.ErrorAs(t, err, &outErr)
			assert.Equal(t, output.CodeAPI, outErr.Code)

			assert.Zero(t, launched, "nothing may be launched on malformed display data")
			assert.Empty(t, as.tokenCalls(), "the abort must land before any token poll")
		})
	}
}

func TestLoginDevice_SanitizesDisplayedCodeAndURI(t *testing.T) {
	as := startDeviceAS(t)
	// OSC hyperlink + CSI + CRLF smuggled into the user code.
	as.deviceAuth = func() (int, string) {
		return http.StatusOK, fmt.Sprintf(
			`{"device_code":"dc","user_code":"AB\u001b]8;;https://evil.example\u0007CD\r\nEF\u001b[31m","verification_uri":%q,"expires_in":600,"interval":1}`,
			as.srv.URL+"/verify")
	}
	resource := startResourceServer(t, as.srv.URL)
	m := newDeviceTestManager(t, resource.URL)

	cl := &collectLogger{}
	var launched []string
	_, err := m.Login(context.Background(), LoginOptions{
		Logger:          cl.log,
		BrowserLauncher: func(u string) error { launched = append(launched, u); return nil },
		deviceOptions:   []oauth.DeviceOption{instantSleep()},
	})
	require.NoError(t, err)

	logs := cl.joined()
	assert.Contains(t, logs, "ABCD EF", "controls stripped, CRLF collapsed to a space")
	assert.NotContains(t, logs, "\x1b", "no escape sequences may reach the logger")
	assert.NotContains(t, logs, "\r")
	assert.NotContains(t, logs, "evil.example")

	// The raw validated URL still goes to the launcher (no URIComplete here,
	// so the plain verification URI is the target).
	require.Len(t, launched, 1)
	assert.Equal(t, as.srv.URL+"/verify", launched[0])
}

func TestLoginDevice_BrowserLaunchFailureContinues(t *testing.T) {
	as := startDeviceAS(t)
	resource := startResourceServer(t, as.srv.URL)
	m := newDeviceTestManager(t, resource.URL)

	cl := &collectLogger{}
	result, err := m.Login(context.Background(), LoginOptions{
		Logger:          cl.log,
		BrowserLauncher: func(string) error { return fmt.Errorf("no display") },
		deviceOptions:   []oauth.DeviceOption{instantSleep()},
	})
	require.NoError(t, err, "launch failure must not abort the flow")
	assert.Equal(t, "bc5", result.OAuthType)
	assert.Contains(t, cl.joined(), "Couldn't open browser")
}

func TestLoginDevice_ScopeWiring(t *testing.T) {
	t.Run("explicit scope sent and used as fallback", func(t *testing.T) {
		as := startDeviceAS(t)
		as.token = func(int) (int, string) {
			// No scope in the token response — opts.Scope is the fallback.
			return http.StatusOK, `{"access_token":"tok","refresh_token":"ref","token_type":"bearer"}`
		}
		resource := startResourceServer(t, as.srv.URL)
		m := newDeviceTestManager(t, resource.URL)

		result, err := m.Login(context.Background(), LoginOptions{
			Scope:         "full",
			Remote:        true,
			Logger:        func(string) {},
			deviceOptions: []oauth.DeviceOption{instantSleep()},
		})
		require.NoError(t, err)

		calls := as.deviceCalls()
		require.Len(t, calls, 1)
		assert.Equal(t, "full", calls[0].Get("scope"))
		assert.Equal(t, "full", result.Scope)
	})

	t.Run("unset scope requests full explicitly", func(t *testing.T) {
		as := startDeviceAS(t)
		as.token = func(int) (int, string) {
			return http.StatusOK, `{"access_token":"tok","refresh_token":"ref","token_type":"bearer"}`
		}
		resource := startResourceServer(t, as.srv.URL)
		m := newDeviceTestManager(t, resource.URL)

		result, err := m.Login(context.Background(), LoginOptions{
			Remote:        true,
			Logger:        func(string) {},
			deviceOptions: []oauth.DeviceOption{instantSleep()},
		})
		require.NoError(t, err)

		// Omitting scope would let the server pick its least-privilege
		// default (read), silently making every write fail — Launchpad
		// logins have always been read-write.
		calls := as.deviceCalls()
		require.Len(t, calls, 1)
		assert.Equal(t, "full", calls[0].Get("scope"), "an unqualified login asks for full access")
		assert.Equal(t, "full", result.Scope)
	})

	t.Run("explicit read is honored", func(t *testing.T) {
		as := startDeviceAS(t)
		as.token = func(int) (int, string) {
			return http.StatusOK, `{"access_token":"tok","refresh_token":"ref","token_type":"bearer"}`
		}
		resource := startResourceServer(t, as.srv.URL)
		m := newDeviceTestManager(t, resource.URL)

		result, err := m.Login(context.Background(), LoginOptions{
			Scope:         "read",
			Remote:        true,
			Logger:        func(string) {},
			deviceOptions: []oauth.DeviceOption{instantSleep()},
		})
		require.NoError(t, err)

		calls := as.deviceCalls()
		require.Len(t, calls, 1)
		assert.Equal(t, "read", calls[0].Get("scope"), "--scope read is how a caller asks for less")
		assert.Equal(t, "read", result.Scope)
	})

	t.Run("server-granted scope wins", func(t *testing.T) {
		as := startDeviceAS(t)
		as.token = func(int) (int, string) {
			return http.StatusOK, `{"access_token":"tok","refresh_token":"ref","token_type":"bearer","scope":"read"}`
		}
		resource := startResourceServer(t, as.srv.URL)
		m := newDeviceTestManager(t, resource.URL)

		result, err := m.Login(context.Background(), LoginOptions{
			Scope:         "full",
			Remote:        true,
			Logger:        func(string) {},
			deviceOptions: []oauth.DeviceOption{instantSleep()},
		})
		require.NoError(t, err)
		assert.Equal(t, "read", result.Scope, "the server's granted scope is authoritative")

		creds, err := m.store.Load(config.NormalizeBaseURL(resource.URL))
		require.NoError(t, err)
		assert.Equal(t, "read", creds.Scope)
	})
}

func TestDiscoverOAuth_ResourceDiscoveryFailedFallsBackWithWarning(t *testing.T) {
	// No protected-resource metadata at all → resource_discovery_failed.
	srv, _ := countingServer(t)
	m := newDeviceTestManager(t, srv.URL)

	cl := &collectLogger{}
	disc, err := m.discoverOAuth(context.Background(), cl.log)
	require.NoError(t, err)

	assert.Equal(t, "launchpad", disc.oauthType)
	require.NotNil(t, disc.config.AuthorizationEndpoint)
	assert.Equal(t, "https://launchpad.37signals.com/authorization/new", *disc.config.AuthorizationEndpoint)
	assert.Equal(t, "https://launchpad.37signals.com/authorization/token", disc.config.TokenEndpoint)
	assert.Contains(t, cl.joined(), "warning: OAuth discovery failed")
}

func TestDiscoverOAuth_NoASAdvertisedFallsBackQuietly(t *testing.T) {
	// Valid resource metadata that advertises no non-Launchpad issuer.
	resource := startResourceServer(t, "https://launchpad.37signals.com")
	m := newDeviceTestManager(t, resource.URL)

	cl := &collectLogger{}
	disc, err := m.discoverOAuth(context.Background(), cl.log)
	require.NoError(t, err)

	assert.Equal(t, "launchpad", disc.oauthType)
	assert.NotContains(t, cl.joined(), "warning:", "no_as_advertised is a quiet fallback")
}

func TestDiscoverOAuth_HardErrorsNeverFallBack(t *testing.T) {
	tests := map[string]struct {
		setup    func(t *testing.T) string // returns base URL
		sentinel error
	}{
		"ambiguous issuers": {
			setup: func(t *testing.T) string {
				return startResourceServer(t, "https://as1.example.com", "https://as2.example.com").URL
			},
			sentinel: oauth.ErrAmbiguousIssuers,
		},
		"AS metadata fetch fails": {
			setup: func(t *testing.T) string {
				as := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					http.Error(w, "boom", http.StatusInternalServerError)
				}))
				t.Cleanup(as.Close)
				return startResourceServer(t, as.URL).URL
			},
			sentinel: oauth.ErrASFetchFailed,
		},
		"issuer mismatch": {
			setup: func(t *testing.T) string {
				as := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"issuer":"https://impostor.example.com","token_endpoint":"https://impostor.example.com/token"}`)
				}))
				t.Cleanup(as.Close)
				return startResourceServer(t, as.URL).URL
			},
			sentinel: oauth.ErrIssuerMismatch,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			baseURL := tt.setup(t)

			// A poisoned Launchpad recorder proves no fallback attempt is made.
			launchpad, hits := countingServer(t)
			t.Setenv("BASECAMP_LAUNCHPAD_URL", launchpad.URL)

			m := newDeviceTestManager(t, baseURL)
			_, err := m.Login(context.Background(), LoginOptions{
				Remote:        true,
				Logger:        func(string) {},
				deviceOptions: []oauth.DeviceOption{instantSleep()},
			})
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.sentinel)
			assert.Zero(t, *hits, "a hard discovery error must never reach Launchpad")
		})
	}
}

func TestLoginDevice_CapabilityUnavailable(t *testing.T) {
	as := startDeviceAS(t)
	// Committed BC5 issuer without device capability: no device endpoint,
	// no device grant.
	as.metadata = func() string {
		return fmt.Sprintf(`{
			"issuer": %q,
			"token_endpoint": %q,
			"grant_types_supported": ["refresh_token"]
		}`, as.srv.URL, as.srv.URL+"/oauth/token")
	}
	resource := startResourceServer(t, as.srv.URL)

	launchpad, hits := countingServer(t)
	t.Setenv("BASECAMP_LAUNCHPAD_URL", launchpad.URL)

	m := newDeviceTestManager(t, resource.URL)
	_, err := m.Login(context.Background(), LoginOptions{
		Remote:        true,
		Logger:        func(string) {},
		deviceOptions: []oauth.DeviceOption{instantSleep()},
	})
	require.Error(t, err)

	var dfe *oauth.DeviceFlowError
	require.ErrorAs(t, err, &dfe)
	assert.Equal(t, oauth.DeviceFlowUnavailable, dfe.Reason)
	assert.Zero(t, *hits, "capability failure on a selected issuer must not fall back")
	assert.Empty(t, as.deviceCalls(), "no device request may be made without the capability")
}

func TestLoginDevice_AccessDenied(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(int) (int, string) {
		return http.StatusBadRequest, `{"error":"access_denied"}`
	}
	resource := startResourceServer(t, as.srv.URL)
	m := newDeviceTestManager(t, resource.URL)

	_, err := m.Login(context.Background(), LoginOptions{
		Remote:        true,
		Logger:        func(string) {},
		deviceOptions: []oauth.DeviceOption{instantSleep()},
	})
	require.Error(t, err)

	var dfe *oauth.DeviceFlowError
	require.ErrorAs(t, err, &dfe)
	assert.Equal(t, oauth.DeviceFlowAccessDenied, dfe.Reason)
}

func TestLoginDevice_AuthorizationPendingThenSuccess(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(call int) (int, string) {
		if call == 0 {
			return http.StatusBadRequest, `{"error":"authorization_pending"}`
		}
		return http.StatusOK, `{"access_token":"tok","refresh_token":"ref","token_type":"bearer","scope":"read"}`
	}
	resource := startResourceServer(t, as.srv.URL)
	m := newDeviceTestManager(t, resource.URL)

	result, err := m.Login(context.Background(), LoginOptions{
		Remote:        true,
		Logger:        func(string) {},
		deviceOptions: []oauth.DeviceOption{instantSleep()},
	})
	require.NoError(t, err)
	assert.Equal(t, "bc5", result.OAuthType)
	assert.Len(t, as.tokenCalls(), 2, "one pending poll, then success")
}

func TestLoginDevice_CancellationSurvivesErrorChain(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(int) (int, string) {
		return http.StatusBadRequest, `{"error":"authorization_pending"}`
	}
	resource := startResourceServer(t, as.srv.URL)
	m := newDeviceTestManager(t, resource.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel the parent context from the sleep seam after the first poll cycle.
	polls := 0
	sleepThenCancel := oauth.WithDeviceSleep(func(context.Context, time.Duration) error {
		polls++
		if polls > 1 {
			cancel()
		}
		return nil
	})

	_, err := m.Login(ctx, LoginOptions{
		Remote:        true,
		Logger:        func(string) {},
		deviceOptions: []oauth.DeviceOption{sleepThenCancel},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled, "parent-context cancellation must survive the error chain")
}

func TestLoginDevice_RejectsPoisonedEndpointsBeforePOST(t *testing.T) {
	tests := map[string]struct {
		metadata func(as *deviceAS) string
		wantMsg  string
	}{
		"poisoned token endpoint": {
			metadata: func(as *deviceAS) string {
				return fmt.Sprintf(`{
					"issuer": %q,
					"token_endpoint": "https://user@evil.example/token",
					"device_authorization_endpoint": %q,
					"grant_types_supported": ["urn:ietf:params:oauth:grant-type:device_code"]
				}`, as.srv.URL, as.srv.URL+"/oauth/device")
			},
			wantMsg: "invalid token endpoint",
		},
		"poisoned device authorization endpoint": {
			metadata: func(as *deviceAS) string {
				return fmt.Sprintf(`{
					"issuer": %q,
					"token_endpoint": %q,
					"device_authorization_endpoint": "https://user@evil.example/device",
					"grant_types_supported": ["urn:ietf:params:oauth:grant-type:device_code"]
				}`, as.srv.URL, as.srv.URL+"/oauth/token")
			},
			wantMsg: "invalid device authorization endpoint",
		},
		"token endpoint with out-of-range port": {
			metadata: func(as *deviceAS) string {
				return fmt.Sprintf(`{
					"issuer": %q,
					"token_endpoint": "https://evil.example:70000/token",
					"device_authorization_endpoint": %q,
					"grant_types_supported": ["urn:ietf:params:oauth:grant-type:device_code"]
				}`, as.srv.URL, as.srv.URL+"/oauth/device")
			},
			wantMsg: "invalid token endpoint",
		},
		"device authorization endpoint with out-of-range port": {
			metadata: func(as *deviceAS) string {
				return fmt.Sprintf(`{
					"issuer": %q,
					"token_endpoint": %q,
					"device_authorization_endpoint": "https://evil.example:70000/device",
					"grant_types_supported": ["urn:ietf:params:oauth:grant-type:device_code"]
				}`, as.srv.URL, as.srv.URL+"/oauth/token")
			},
			wantMsg: "invalid device authorization endpoint",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			as := startDeviceAS(t)
			as.metadata = func() string { return tt.metadata(as) }
			resource := startResourceServer(t, as.srv.URL)
			m := newDeviceTestManager(t, resource.URL)

			_, err := m.Login(context.Background(), LoginOptions{
				Remote:        true,
				Logger:        func(string) {},
				deviceOptions: []oauth.DeviceOption{instantSleep()},
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantMsg)
			assert.Empty(t, as.deviceCalls(), "hardening must reject before any POST")
			assert.Empty(t, as.tokenCalls())
		})
	}
}

func TestRefreshLocked_BC5PublicClient(t *testing.T) {
	var mu sync.Mutex
	var capturedForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		mu.Lock()
		capturedForm = r.PostForm
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"new-tok","refresh_token":"new-ref","expires_in":3600}`)
	}))
	defer srv.Close()

	m := &Manager{
		cfg:        config.Default(),
		httpClient: srv.Client(),
		store:      newTestStore(t, t.TempDir()),
	}

	creds := &Credentials{
		AccessToken:   "old-tok",
		RefreshToken:  "old-ref",
		OAuthType:     "bc5",
		TokenEndpoint: srv.URL + "/oauth/token",
		Scope:         "read",
		ExpiresAt:     time.Now().Add(-1 * time.Hour).Unix(),
	}
	require.NoError(t, m.store.Save("test", creds))

	require.NoError(t, m.refreshLocked(context.Background(), "test", creds))

	mu.Lock()
	form := capturedForm
	mu.Unlock()
	assert.Equal(t, "refresh_token", form.Get("grant_type"), "bc5 refresh uses the standard format")
	assert.Equal(t, "basecamp-cli", form.Get("client_id"))
	_, hasSecret := form["client_secret"]
	assert.False(t, hasSecret, "the public client must not send a client_secret key")

	reloaded, err := m.store.Load("test")
	require.NoError(t, err)
	assert.Equal(t, "new-tok", reloaded.AccessToken)
}

func TestRefreshLocked_LegacyBC3RequiresReauth(t *testing.T) {
	transport := &recordingTransport{}
	m := &Manager{
		cfg:        config.Default(),
		httpClient: &http.Client{Transport: transport},
		store:      newTestStore(t, t.TempDir()),
	}

	creds := &Credentials{
		AccessToken:   "old-tok",
		RefreshToken:  "old-ref",
		OAuthType:     "bc3",
		TokenEndpoint: "https://example.com/token",
	}

	err := m.refreshLocked(context.Background(), "test", creds)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "re-authenticate")
	assert.False(t, transport.attempted.Load(), "legacy bc3 refresh must fail without any network request")
}

func TestResourceOrigin(t *testing.T) {
	valid := map[string]string{
		"https://3.basecampapi.com":            "https://3.basecampapi.com",
		"https://3.basecampapi.com/":           "https://3.basecampapi.com",
		"https://3.basecampapi.com/api/v1":     "https://3.basecampapi.com",
		"https://host.example.com:8443/deep/p": "https://host.example.com:8443",
		"http://3.basecamp.localhost:3001":     "http://3.basecamp.localhost:3001",
		"http://127.0.0.1:3001/path":           "http://127.0.0.1:3001",
		// Canonicalization: the SDK binds the protected-resource identifier to
		// this string code-point exact, so equivalent spellings must reduce to
		// the canonical origin — else discovery silently soft-falls back to
		// Launchpad.
		"HTTPS://3.BasecampAPI.com":        "https://3.basecampapi.com",
		"https://3.basecampapi.com:443":    "https://3.basecampapi.com",
		"https://3.basecampapi.com:0443/x": "https://3.basecampapi.com",
		"https://Host.Example.com:08443":   "https://host.example.com:8443",
		"http://LocalHost:80/path":         "http://localhost",
		"http://[::1]:3001/path":           "http://[::1]:3001",
	}
	for input, want := range valid {
		t.Run("strips "+input, func(t *testing.T) {
			got, err := resourceOrigin(input)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}

	invalid := []string{
		"file:///etc/passwd",
		"https://user@host.example.com",
		"https://host.example.com?q=1",
		"https://host.example.com?",      // bare query: ForceQuery, empty RawQuery
		"https://host.example.com/path?", // bare query after a path
		"https://host.example.com/#frag",
		"https:opaque",
		"https://",
		"http://evil.example.com", // http off localhost
		"https://host.example.com:0",
		"https://host.example.com:70000",
		"://bad",
		"",
	}
	for _, input := range invalid {
		t.Run("rejects "+input, func(t *testing.T) {
			_, err := resourceOrigin(input)
			require.Error(t, err)

			var outErr *output.Error
			require.ErrorAs(t, err, &outErr)
			assert.Equal(t, output.CodeUsage, outErr.Code)
			if input != "" {
				assert.NotContains(t, err.Error(), input,
					"validation failures must not echo the raw URL")
			}
		})
	}
}

// TestLoginDevice_ResourceEchoEndToEnd proves the RFC 8707 multi-account
// contract end to end: a device login stores the token response's resource
// indicator, a later refresh echoes it as the resource form parameter, and the
// rotated credentials still carry it when the refresh response omits it —
// without any of which a BC5 device login dies at its first token expiry.
func TestLoginDevice_ResourceEchoEndToEnd(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(call int) (int, string) {
		if call == 0 {
			// The device-grant response binds the token to an account.
			return http.StatusOK, `{"access_token":"dev-tok","refresh_token":"dev-ref","token_type":"bearer","expires_in":3600,"scope":"read","resource":"urn:bc:account:42"}`
		}
		// The refresh response rotates tokens but OMITS resource — the stored
		// binding must survive the rotation.
		return http.StatusOK, `{"access_token":"dev-tok-2","refresh_token":"dev-ref-2","token_type":"bearer","expires_in":3600}`
	}
	resource := startResourceServer(t, as.srv.URL)
	m := newDeviceTestManager(t, resource.URL)

	_, err := m.Login(context.Background(), LoginOptions{
		Logger:        (&collectLogger{}).log,
		NoBrowser:     true,
		deviceOptions: []oauth.DeviceOption{instantSleep()},
	})
	require.NoError(t, err)

	origin := config.NormalizeBaseURL(resource.URL)
	creds, err := m.store.Load(origin)
	require.NoError(t, err)
	assert.Equal(t, "urn:bc:account:42", creds.Resource, "device login must persist the resource binding")

	// Simulate expiry so the next refresh is forced.
	creds.ExpiresAt = 1
	require.NoError(t, m.store.Save(origin, creds))

	require.NoError(t, m.Refresh(context.Background()))

	tokenCalls := as.tokenCalls()
	require.Len(t, tokenCalls, 2)
	refreshForm := tokenCalls[1]
	assert.Equal(t, "refresh_token", refreshForm.Get("grant_type"))
	assert.Equal(t, "urn:bc:account:42", refreshForm.Get("resource"), "refresh must echo the stored resource")
	assert.Equal(t, "basecamp-cli", refreshForm.Get("client_id"))
	_, hasSecret := refreshForm["client_secret"]
	assert.False(t, hasSecret, "the public client sends no secret")

	rotated, err := m.store.Load(origin)
	require.NoError(t, err)
	assert.Equal(t, "dev-tok-2", rotated.AccessToken)
	assert.Equal(t, "dev-ref-2", rotated.RefreshToken)
	assert.Equal(t, "urn:bc:account:42", rotated.Resource, "an omitted resource must preserve the stored binding")
}

// TestAccountID covers the account a BC5 token is bound to, read back from its
// RFC 8707 resource indicator. This is what lets a device login address its
// account without /authorization.json, which BC3 serves only on the API host.
func TestAccountID(t *testing.T) {
	tests := []struct {
		name     string
		creds    Credentials
		expected string
	}{
		{
			name:     "account URN yields the account ID",
			creds:    Credentials{OAuthType: oauthTypeBC5, Resource: "urn:bc:account:2914079"},
			expected: "2914079",
		},
		{
			name:     "no resource indicator",
			creds:    Credentials{OAuthType: oauthTypeBC5},
			expected: "",
		},
		{
			name: "a service origin names no account",
			// RFC 9728 metadata publishes the serving origin; BC3 treats that
			// form as "no account restriction", not as an account.
			creds:    Credentials{OAuthType: oauthTypeBC5, Resource: "https://3.basecampapi.com"},
			expected: "",
		},
		{
			name:     "empty account segment",
			creds:    Credentials{OAuthType: oauthTypeBC5, Resource: "urn:bc:account:"},
			expected: "",
		},
		{
			name: "non-numeric segment is refused",
			// The indicator is server-controlled and feeds URL construction.
			creds:    Credentials{OAuthType: oauthTypeBC5, Resource: "urn:bc:account:../../evil"},
			expected: "",
		},
		{
			name:     "launchpad credentials carry no binding",
			creds:    Credentials{OAuthType: oauthTypeLaunchpad},
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newDeviceTestManager(t, "https://example.com")
			require.NoError(t, m.store.Save(m.credentialKey(), &tc.creds))
			assert.Equal(t, tc.expected, m.AccountID())
		})
	}
}

// TestEnvTokenOverridesStoredAccountBinding pins BASECAMP_TOKEN precedence for
// AccountID. Requests carry the environment token (AccessToken short-circuits
// on it), so answering from a stored BC5 binding would silently address an
// account that token may have nothing to do with.
func TestEnvTokenOverridesStoredAccountBinding(t *testing.T) {
	stale := &Credentials{
		AccessToken: "stored-tok",
		OAuthType:   oauthTypeBC5,
		Scope:       scopeRead,
		Resource:    "urn:bc:account:2914079",
	}

	t.Run("stored binding stands without an env token", func(t *testing.T) {
		m := newDeviceTestManager(t, "https://example.com")
		require.NoError(t, m.store.Save(m.credentialKey(), stale))
		t.Setenv("BASECAMP_TOKEN", "") // don't inherit an ambient token

		assert.Equal(t, "2914079", m.AccountID())
	})

	t.Run("env token suppresses the stored binding", func(t *testing.T) {
		m := newDeviceTestManager(t, "https://example.com")
		require.NoError(t, m.store.Save(m.credentialKey(), stale))
		t.Setenv("BASECAMP_TOKEN", "bc_at_from_environment")

		assert.Empty(t, m.AccountID(), "the env token's account is not the stored one")
	})
}

// TestDiscoverOAuth_PinnedIssuerSkipsDiscovery: BASECAMP_OAUTH_ISSUER names
// the authorization server outright. No discovery request is made — the
// resource here 404s everything and counts — and the device flow runs
// against the endpoints Basecamp mounts under the issuer.
func TestDiscoverOAuth_PinnedIssuerSkipsDiscovery(t *testing.T) {
	as := startDeviceAS(t)
	resource, discoveryHits := countingServer(t)
	m := newDeviceTestManager(t, resource.URL)
	t.Setenv("BASECAMP_OAUTH_ISSUER", as.srv.URL+"/")

	cl := &collectLogger{}
	result, err := m.Login(context.Background(), LoginOptions{
		Remote:        true,
		Logger:        cl.log,
		deviceOptions: []oauth.DeviceOption{instantSleep()},
	})
	require.NoError(t, err)
	assert.Equal(t, &LoginResult{OAuthType: "bc5", Scope: "read"}, result)

	assert.Zero(t, *discoveryHits, "a pinned issuer must not consult resource metadata")
	require.Len(t, as.deviceCalls(), 1)
	assert.Equal(t, "basecamp-cli", as.deviceCalls()[0].Get("client_id"))
	assert.NotEmpty(t, as.tokenCalls())

	logs := cl.joined()
	assert.Contains(t, logs, "pinned by BASECAMP_OAUTH_ISSUER")
	assert.Contains(t, logs, "Authenticating via "+as.srv.URL+" (device flow")

	// The trailing slash is trimmed: the stored token endpoint is the exact
	// mount, which refresh will POST to later.
	creds, err := m.store.Load(config.NormalizeBaseURL(resource.URL))
	require.NoError(t, err)
	assert.Equal(t, as.srv.URL+"/oauth/tokens", creds.TokenEndpoint)
	assert.Equal(t, "bc5", creds.OAuthType)
}

// TestDiscoverOAuth_PinnedIssuerIsCheckedLikeAnEndpoint: the override is
// operator environment, but it names where credentials get POSTed, so it
// passes the same checks a discovery document would — and never falls back
// to Launchpad.
func TestDiscoverOAuth_PinnedIssuerIsCheckedLikeAnEndpoint(t *testing.T) {
	for name, issuer := range map[string]string{
		"plain http off loopback": "http://as.example",
		"userinfo":                "https://user@as.example",
		"no host":                 "https://",
		"bad port":                "https://as.example:70000",
		"file scheme":             "file:///etc/passwd",
		"query string":            "https://as.example/?token=hunter2",
		"fragment":                "https://as.example/#frag",
		"path":                    "https://as.example/some/path",
		"opaque":                  "https:as.example",
	} {
		t.Run(name, func(t *testing.T) {
			resource, discoveryHits := countingServer(t)
			m := newDeviceTestManager(t, resource.URL)
			t.Setenv("BASECAMP_OAUTH_ISSUER", issuer)

			_, err := m.Login(context.Background(), LoginOptions{Remote: true, Logger: func(string) {}})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "BASECAMP_OAUTH_ISSUER")
			assert.NotContains(t, err.Error(), "as.example", "the rejected value is never echoed")
			assert.NotContains(t, err.Error(), "hunter2")
			var outErr *output.Error
			require.ErrorAs(t, err, &outErr)
			assert.Equal(t, output.CodeAuth, outErr.Code)
			assert.Zero(t, *discoveryHits, "a rejected override must not fall back to discovery or Launchpad")
		})
	}
}

// TestLoginDevice_LoginHintAnnounced: until the SDK bump that carries
// login_hint on the wire, the hint is told to the user rather than sent.
func TestLoginDevice_LoginHintAnnounced(t *testing.T) {
	as := startDeviceAS(t)
	resource := startResourceServer(t, as.srv.URL)
	m := newDeviceTestManager(t, resource.URL)

	cl := &collectLogger{}
	_, err := m.Login(context.Background(), LoginOptions{
		Remote:        true,
		LoginHint:     "bot@example.com",
		Logger:        cl.log,
		deviceOptions: []oauth.DeviceOption{instantSleep()},
	})
	require.NoError(t, err)
	assert.Contains(t, cl.joined(), "Sign in as bot@example.com")
	require.Len(t, as.deviceCalls(), 1)
}

func TestImportToken(t *testing.T) {
	m := newDeviceTestManager(t, "https://3.basecampapi.com")
	m.cfg.ActiveProfile = "bot"

	require.NoError(t, m.ImportToken("bc_at_secret", "full", "51177542", "bot@example.com", time.Time{}))

	creds, err := m.store.Load("profile:bot")
	require.NoError(t, err)
	assert.Equal(t, &Credentials{AccessToken: "bc_at_secret", OAuthType: "bc5", Scope: "full", UserID: "51177542", UserEmail: "bot@example.com", Source: "token"}, creds)
	assert.Zero(t, creds.ExpiresAt)

	// Non-expiring: served as-is, with no refresh attempted (there is no
	// refresh token or endpoint to attempt one with, and no HTTP client that
	// could reach one).
	tok, err := m.AccessToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "bc_at_secret", tok)
	assert.True(t, m.IsAuthenticated())
	assert.Equal(t, "bc5", m.GetOAuthType())

	err = m.Refresh(context.Background())
	require.Error(t, err, "an explicit refresh has nothing to refresh with")
	assert.Contains(t, err.Error(), "No refresh token")

	err = m.ImportToken("bc_at_secret", "admin", "", "", time.Time{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Invalid scope")

	// A reported expiry is kept, and near it the token is refused rather
	// than served: there is no refresh token to renew it with.
	require.NoError(t, m.ImportToken("bc_at_short", "full", "", "", time.Now().Add(time.Minute)))
	creds, err = m.store.Load("profile:bot")
	require.NoError(t, err)
	assert.Positive(t, creds.ExpiresAt)
	_, err = m.AccessToken(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "No refresh token")
}

// TestLoginDevice_VerifyRunsBeforeStore: a Verify hook sees the freshly
// issued token before anything is written, and its error aborts the login
// with nothing stored.
func TestLoginDevice_VerifyRunsBeforeStore(t *testing.T) {
	as := startDeviceAS(t)
	resource := startResourceServer(t, as.srv.URL)
	m := newDeviceTestManager(t, resource.URL)
	credKey := config.NormalizeBaseURL(resource.URL)

	var seenToken, seenType string
	_, err := m.Login(context.Background(), LoginOptions{
		Remote:        true,
		Logger:        func(string) {},
		deviceOptions: []oauth.DeviceOption{instantSleep()},
		Verify: func(_ context.Context, token, oauthType string) error {
			seenToken, seenType = token, oauthType
			_, loadErr := m.store.Load(credKey)
			assert.Error(t, loadErr, "the store must still be empty while verifying")
			return output.ErrAuth("not you")
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not you")
	assert.Equal(t, "dev-tok", seenToken)
	assert.Equal(t, "bc5", seenType)
	_, loadErr := m.store.Load(credKey)
	assert.Error(t, loadErr, "a rejected token is never stored")

	result, err := m.Login(context.Background(), LoginOptions{
		Remote:        true,
		Logger:        func(string) {},
		deviceOptions: []oauth.DeviceOption{instantSleep()},
		Verify:        func(context.Context, string, string) error { return nil },
	})
	require.NoError(t, err)
	assert.Equal(t, "bc5", result.OAuthType)
	creds, err := m.store.Load(credKey)
	require.NoError(t, err)
	assert.Equal(t, "dev-tok", creds.AccessToken)
}

func TestLoginDevice_LoginHintIsSanitizedForTheTerminal(t *testing.T) {
	as := startDeviceAS(t)
	resource := startResourceServer(t, as.srv.URL)
	m := newDeviceTestManager(t, resource.URL)

	cl := &collectLogger{}
	_, err := m.Login(context.Background(), LoginOptions{
		Remote:        true,
		LoginHint:     "bot@example.com\x1b]8;;https://evil.example\x07\r\nEvil",
		Logger:        cl.log,
		deviceOptions: []oauth.DeviceOption{instantSleep()},
	})
	require.NoError(t, err)
	logs := cl.joined()
	assert.Contains(t, logs, "Sign in as bot@example.com Evil")
	assert.NotContains(t, logs, "\x1b")
	assert.NotContains(t, logs, "evil.example")
}

func TestDiscoverOAuth_PinnedIssuerIsSanitizedForTheTerminal(t *testing.T) {
	resource, _ := countingServer(t)
	m := newDeviceTestManager(t, resource.URL)
	// Only an origin is accepted now, so a control character can only ride
	// in the host — where url.Parse or the endpoint check refuses it. Either
	// way the terminal never sees it: refused values are not echoed, and an
	// accepted one is announced through the sanitizer.
	t.Setenv("BASECAMP_OAUTH_ISSUER", "http://127.0.0.1\u0085:3001")

	cl := &collectLogger{}
	_, err := m.Login(context.Background(), LoginOptions{Remote: true, Logger: cl.log, deviceOptions: []oauth.DeviceOption{instantSleep()}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BASECAMP_OAUTH_ISSUER")
	assert.NotContains(t, err.Error(), "\u0085")
	assert.NotContains(t, cl.joined(), "\u0085")
}
