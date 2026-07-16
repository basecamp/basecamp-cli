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
	mux.HandleFunc("/oauth/device", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		as.mu.Lock()
		as.deviceForms = append(as.deviceForms, r.PostForm)
		as.mu.Unlock()
		status, body := as.deviceAuth()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		as.mu.Lock()
		call := len(as.tokenForms)
		as.tokenForms = append(as.tokenForms, r.PostForm)
		as.mu.Unlock()
		status, body := as.token(call)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	})

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

	t.Run("unset scope omitted and defaults to read", func(t *testing.T) {
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

		calls := as.deviceCalls()
		require.Len(t, calls, 1)
		_, hasScope := calls[0]["scope"]
		assert.False(t, hasScope, "no scope requested means no scope parameter")
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
