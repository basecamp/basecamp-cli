package commands

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// The secret the mock server hands over: a literal nobody can mistake for a
// credential, which the transcript assertions then look for.
const fakeConnectSecret = "not-a-real-secret"

// connectAS is a mock Basecamp authorization server for the connection
// ceremony, mounted on the paths a pinned issuer derives: the anonymous
// intake, the poll, and the token endpoint the credential is minted
// against.
type connectAS struct {
	srv *httptest.Server

	mu         sync.Mutex
	pollForms  []url.Values
	tokenForms []url.Values

	// connection renders the successful poll; the default approves a
	// full-scope agent in account 999.
	connection func() string
}

func startConnectAS(t *testing.T) *connectAS {
	t.Helper()
	as := &connectAS{}
	as.connection = func() string {
		return fmt.Sprintf(`{"client_id":"agent-client","client_secret":%q,"account_id":"999","scope":"full"}`, fakeConnectSecret)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/agent_connections", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"device_code":"dev-code-1","user_code":"WDJB-MJHT","verification_uri_complete":%q,"token_uri":%q,"expires_in":600,"interval":1}`,
			as.srv.URL+"/connect?user_code=WDJB-MJHT", as.srv.URL+"/oauth/agent_connection_tokens")
	})
	mux.HandleFunc("/oauth/agent_connection_tokens", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		as.mu.Lock()
		as.pollForms = append(as.pollForms, r.PostForm)
		as.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, as.connection())
	})
	mux.HandleFunc("/oauth/tokens", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		as.mu.Lock()
		as.tokenForms = append(as.tokenForms, r.PostForm)
		as.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"minted","token_type":"bearer","expires_in":3600,"resource":"urn:bc:agent:42","scope":"full"}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })

	as.srv = httptest.NewServer(mux)
	t.Cleanup(as.srv.Close)
	return as
}

func (as *connectAS) mints() []url.Values {
	as.mu.Lock()
	defer as.mu.Unlock()
	return append([]url.Values(nil), as.tokenForms...)
}

// connectApp is an App wired for the connection ceremony: a file credential
// store under a temp XDG_CONFIG_HOME and a pinned issuer, so discovery
// reaches the mock server without a resource hop.
func connectApp(t *testing.T, as *connectAS, cfg *config.Config) *appctx.App {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("BASECAMP_TOKEN", "")
	t.Setenv("BASECAMP_NONINTERACTIVE", "")
	t.Setenv("BASECAMP_OAUTH_ISSUER", as.srv.URL)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg.BaseURL = as.srv.URL
	if cfg.Sources == nil {
		cfg.Sources = map[string]string{}
	}
	authMgr := auth.NewManager(cfg, as.srv.Client())
	authMgr.SetStore(auth.NewStore(config.GlobalConfigDir()))
	return &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		Output: output.New(output.Options{Format: output.FormatStyled, Writer: &bytes.Buffer{}}),
	}
}

func runAgentConnect(t *testing.T, app *appctx.App, args ...string) (string, error) {
	t.Helper()
	cmd := NewAuthCmd()
	cmd.SetArgs(append([]string{"agent", "connect", "--no-browser"}, args...))
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.Execute()
	return out.String(), err
}

// TestAuthAgentConnectCreatesTheProfileFromTheApprovedAccount: the account
// is the server's to name — the operator picks an agent, and the agent
// belongs to whichever account it belongs to — so the profile entry is
// written from the connection rather than from a flag.
func TestAuthAgentConnectCreatesTheProfileFromTheApprovedAccount(t *testing.T) {
	as := startConnectAS(t)
	app := connectApp(t, as, &config.Config{ActiveProfile: "agent"})

	out, err := runAgentConnect(t, app, "--device-name", "build-box")
	require.NoError(t, err, out)

	assert.Contains(t, out, "WDJB-MJHT", "the operator compares the code on the approval page")
	assert.Contains(t, out, `Connected profile "agent" to a Basecamp agent`)
	assert.Contains(t, out, "Account: 999")
	assert.Contains(t, out, `Created profile "agent" for account 999 (default)`)
	assert.NotContains(t, out, fakeConnectSecret, "the client secret must never be echoed")

	mints := as.mints()
	require.Len(t, mints, 1)
	assert.Equal(t, "client_credentials", mints[0].Get("grant_type"))
	assert.Equal(t, fakeConnectSecret, mints[0].Get("client_secret"))

	creds, err := app.Auth.GetStore().Load("profile:agent")
	require.NoError(t, err)
	assert.Equal(t, "agent", creds.OAuthType)
	assert.Equal(t, "agent-client", creds.ClientID)
	assert.Equal(t, fakeConnectSecret, creds.ClientSecret)
	assert.Equal(t, "full", creds.Scope)
	assert.Empty(t, creds.RefreshToken)

	entry := readGlobalConfig(t)["profiles"].(map[string]any)["agent"].(map[string]any)
	assert.Equal(t, "999", entry["account_id"])
	assert.Equal(t, as.srv.URL, entry["base_url"])
	assert.Equal(t, "full", entry["scope"])
}

// TestAuthAgentConnectRefusesAnAgentInAnotherAccount: --account is an
// assertion, not a request. An agent in a different account is the wrong
// agent, and storing its credential under this profile would leave every
// later command addressing an account the credential cannot reach.
func TestAuthAgentConnectRefusesAnAgentInAnotherAccount(t *testing.T) {
	as := startConnectAS(t)
	app := connectApp(t, as, &config.Config{ActiveProfile: "agent"})
	withAccount(app, "123", "flag")

	_, err := runAgentConnect(t, app, "--device-name", "build-box")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "account 999")
	assertNothingStored(t, app, "agent")
	assert.Empty(t, readGlobalConfig(t)["profiles"], "no profile entry was left behind")
}

// TestAuthAgentConnectRefusesAProfileBoundElsewhere: a profile already
// bound to an account is not the place for an agent from another one.
func TestAuthAgentConnectRefusesAProfileBoundElsewhere(t *testing.T) {
	as := startConnectAS(t)
	app := connectApp(t, as, &config.Config{
		ActiveProfile: "agent",
		Profiles:      map[string]*config.ProfileConfig{"agent": {AccountID: "123"}},
	})

	_, err := runAgentConnect(t, app, "--device-name", "build-box")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bound to account 123")
	_, loadErr := app.Auth.GetStore().Load("profile:agent")
	assert.Error(t, loadErr, "the credential of an agent from another account is not stored")
}

// TestAuthAgentConnectNeedsAProfile: the credential is stored under a named
// profile, and nothing is asked of the server until there is one.
func TestAuthAgentConnectNeedsAProfile(t *testing.T) {
	as := startConnectAS(t)
	app := connectApp(t, as, &config.Config{})

	_, err := runAgentConnect(t, app)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "named profile")
	assert.Empty(t, as.mints())
}

// TestAuthAgentConnectRefusesMachineOutput: the link, the code and the wait
// line go to stdout, which an envelope also owns.
func TestAuthAgentConnectRefusesMachineOutput(t *testing.T) {
	as := startConnectAS(t)
	app := connectApp(t, as, &config.Config{ActiveProfile: "agent"})
	app.Flags.JSON = true

	_, err := runAgentConnect(t, app)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "machine output mode")
	assert.Empty(t, as.mints())
}

// TestAuthAgentConnectRefusesNonInteractive: approving a connection needs a
// person signed in to Basecamp, and the environment has said there is none.
func TestAuthAgentConnectRefusesNonInteractive(t *testing.T) {
	as := startConnectAS(t)
	app := connectApp(t, as, &config.Config{ActiveProfile: "agent"})
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")

	_, err := runAgentConnect(t, app)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BASECAMP_NONINTERACTIVE")
	assert.Empty(t, as.mints())
}

// TestAuthAgentConnectRefusesAShadowingEnvToken: every request, the mint
// included, would carry the environment token instead of the credential
// being connected.
func TestAuthAgentConnectRefusesAShadowingEnvToken(t *testing.T) {
	as := startConnectAS(t)
	app := connectApp(t, as, &config.Config{ActiveProfile: "agent"})
	t.Setenv("BASECAMP_TOKEN", "bc_at_something")

	_, err := runAgentConnect(t, app)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BASECAMP_TOKEN")
	assert.Empty(t, as.mints())
}

// TestAuthAgentConnectNamesThisHostByDefault: the approval page shows the
// operator which computer is asking, so the device name defaults to this
// host's own rather than to something generic.
func TestAuthAgentConnectNamesThisHostByDefault(t *testing.T) {
	assert.Equal(t, "build-box", connectDeviceName("build-box"))
	assert.NotEmpty(t, connectDeviceName(""), "a host with a name uses it")
}
