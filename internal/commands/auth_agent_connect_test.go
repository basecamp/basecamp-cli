package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

	// pollStatus, when non-zero, is the poll's status in place of 200: a
	// refusal, rendered from connection.
	pollStatus int
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
		if as.pollStatus != 0 {
			w.WriteHeader(as.pollStatus)
		}
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

// runAgentConnectJSON runs the ceremony under --json with stdout and stderr
// kept apart, as a program driving it would read them. stdout is a
// lockedBuffer so the mock server's handlers can read it mid-ceremony.
func runAgentConnectJSON(t *testing.T, app *appctx.App, stdout *lockedBuffer, args ...string) (string, error) {
	t.Helper()
	app.Flags.JSON = true
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: stdout})
	cmd := NewAuthCmd()
	cmd.SetArgs(append([]string{"agent", "connect", "--no-browser"}, args...))
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	var stderr bytes.Buffer
	cmd.SetOut(stdout)
	cmd.SetErr(&stderr)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.Execute()
	return stderr.String(), err
}

// jsonValues reads stdout as a stream of JSON values, the way a program
// driving the ceremony would: the verification line, then the envelope,
// however that is laid out.
func jsonValues(t *testing.T, stdout string) []map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	var values []map[string]any
	for dec.More() {
		var v map[string]any
		require.NoError(t, dec.Decode(&v), "stdout is JSON values and nothing else: %q", stdout)
		values = append(values, v)
	}
	return values
}

// TestAuthAgentConnectJSONWritesTheVerificationThenTheResult: under --json
// the ceremony is two lines of data on stdout. The first carries what the
// operator needs and is written while the ceremony is still waiting on
// them; the last is the result. The operator's words go to stderr. Neither
// stream carries the client secret or the device code.
func TestAuthAgentConnectJSONWritesTheVerificationThenTheResult(t *testing.T) {
	as := startConnectAS(t)
	app := connectApp(t, as, &config.Config{ActiveProfile: "agent"})
	stdout := &lockedBuffer{}

	approve := as.connection
	var atPoll string
	as.connection = func() string {
		atPoll = stdout.String()
		return approve()
	}

	stderr, err := runAgentConnectJSON(t, app, stdout, "--device-name", "build-box")
	require.NoError(t, err, stderr)

	require.NotEmpty(t, atPoll, "the verification line is written before the operator approves, not after")
	require.True(t, strings.HasSuffix(atPoll, "}\n") && strings.Count(atPoll, "\n") == 1,
		"the verification is one line, newline-terminated, so a reader can act on it at once: %q", atPoll)
	verification := jsonValues(t, atPoll)
	require.Len(t, verification, 1)
	assert.Equal(t, "verification", verification[0]["type"])
	assert.Equal(t, as.srv.URL+"/connect?user_code=WDJB-MJHT", verification[0]["verification_uri"])
	assert.Equal(t, "WDJB-MJHT", verification[0]["user_code"])
	assert.NotEmpty(t, verification[0]["expires_at"])
	expiresIn, ok := verification[0]["expires_in"].(float64)
	require.True(t, ok)
	assert.InDelta(t, 600, expiresIn, 5)

	lines := jsonValues(t, stdout.String())
	require.Len(t, lines, 2, "the verification line, then the result")
	data, ok := lines[1]["data"].(map[string]any)
	require.True(t, ok, stdout.String())
	assert.Equal(t, "agent", data["profile"])
	assert.Equal(t, "999", data["account_id"])
	assert.Equal(t, "agent", data["oauth_type"])
	assert.Equal(t, "agent_connection", data["source"])
	assert.Equal(t, "full", data["scope"])
	assert.Equal(t, "agent-client", data["client_id"])
	assert.Equal(t, true, data["profile_created"])
	assert.Equal(t, true, data["default"])
	assert.Equal(t, `Connected profile "agent" to a Basecamp agent`, lines[1]["summary"])

	assert.Contains(t, stderr, "WDJB-MJHT", "a person at the terminal still sees the code")
	assert.NotContains(t, stderr, "Connected profile", "the result is data, not a second copy in prose")
	for name, stream := range map[string]string{"stdout": stdout.String(), "stderr": stderr} {
		assert.NotContains(t, stream, fakeConnectSecret, "%s carries the client secret", name)
		assert.NotContains(t, stream, "dev-code-1", "%s carries the device code, the poll's bearer", name)
	}

	creds, err := app.Auth.GetStore().Load("profile:agent")
	require.NoError(t, err)
	assert.Equal(t, fakeConnectSecret, creds.ClientSecret, "the credential is stored exactly as without --json")
}

// TestAuthAgentConnectJSONOnADeclineWritesOnlyTheVerification: a declined
// connection ends with an error, which the CLI renders as the last line,
// and nothing is stored. The verification line is already out by then.
func TestAuthAgentConnectJSONOnADeclineWritesOnlyTheVerification(t *testing.T) {
	as := startConnectAS(t)
	as.pollStatus = 400
	as.connection = func() string { return `{"error":"access_denied"}` }
	app := connectApp(t, as, &config.Config{ActiveProfile: "agent"})
	stdout := &lockedBuffer{}

	_, err := runAgentConnectJSON(t, app, stdout, "--device-name", "build-box")
	require.Error(t, err)

	lines := jsonValues(t, stdout.String())
	require.Len(t, lines, 1, "no result envelope from the command itself; the error is the CLI's to render")
	assert.Equal(t, "verification", lines[0]["type"])
	assert.Empty(t, as.mints(), "a declined connection mints nothing")
	assertNothingStored(t, app, "agent")
}

// TestAuthAgentConnectRefusesOutputModesThatCannotCarryIt: --jq filters one
// envelope and this writes two lines; --quiet, --ids-only and --count would
// throw the verification line away. Each is refused before the server is
// asked for anything.
func TestAuthAgentConnectRefusesOutputModesThatCannotCarryIt(t *testing.T) {
	for name, set := range map[string]func(*appctx.App){
		"jq":       func(a *appctx.App) { a.Flags.JQFilter = ".data" },
		"quiet":    func(a *appctx.App) { a.Flags.Quiet = true },
		"ids-only": func(a *appctx.App) { a.Flags.IDsOnly = true },
		"count":    func(a *appctx.App) { a.Flags.Count = true },
	} {
		t.Run(name, func(t *testing.T) {
			as := startConnectAS(t)
			app := connectApp(t, as, &config.Config{ActiveProfile: "agent"})
			set(app)

			_, err := runAgentConnect(t, app)
			require.Error(t, err)
			assert.Equal(t, output.CodeUsage, output.AsError(err).Code)
			assert.Empty(t, as.mints())
		})
	}
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
