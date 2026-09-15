package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/stdinarg"
)

// agentAS is a mock Basecamp authorization server for the agent login: the
// token endpoint a pinned issuer derives, recording every form it is sent.
type agentAS struct {
	srv *httptest.Server

	mu    sync.Mutex
	forms []url.Values

	// token renders the token response; the default mints one self-token.
	token func() (status int, body string)
}

func startAgentAS(t *testing.T) *agentAS {
	t.Helper()
	as := &agentAS{}
	as.token = func() (int, string) {
		return http.StatusOK, `{"access_token":"minted","token_type":"bearer","expires_in":3600,"resource":"urn:bc:agent:42","scope":"full"}`
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/tokens", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		as.mu.Lock()
		as.forms = append(as.forms, r.PostForm)
		as.mu.Unlock()
		status, body := as.token()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	as.srv = httptest.NewServer(mux)
	t.Cleanup(as.srv.Close)
	return as
}

func (as *agentAS) calls() []url.Values {
	as.mu.Lock()
	defer as.mu.Unlock()
	return append([]url.Values(nil), as.forms...)
}

// agentLoginApp is an App wired for --with-client-credentials tests: a file
// credential store under a temp XDG_CONFIG_HOME and a pinned issuer, so
// discovery reaches the mock authorization server without a resource hop.
func agentLoginApp(t *testing.T, as *agentAS, cfg *config.Config) (*appctx.App, *bytes.Buffer) {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("BASECAMP_TOKEN", "")
	t.Setenv("BASECAMP_OAUTH_ISSUER", as.srv.URL)
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg.BaseURL = as.srv.URL
	if cfg.Sources == nil {
		cfg.Sources = map[string]string{}
	}
	authMgr := auth.NewManager(cfg, as.srv.Client())
	authMgr.SetStore(auth.NewStore(config.GlobalConfigDir()))
	buf := &bytes.Buffer{}
	return &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		Output: output.New(output.Options{Format: output.FormatJSON, Writer: buf}),
	}, buf
}

func TestAuthLoginWithClientCredentialsCreatesAnAgentProfile(t *testing.T) {
	as := startAgentAS(t)
	app, _ := agentLoginApp(t, as, &config.Config{ActiveProfile: "clawdito"})
	withAccount(app, "999", "flag")

	// A trailing newline is what `op read` delivers; it must not reach the
	// token request.
	out, err := runLogin(t, app, strings.NewReader("agent-secret\n"),
		"--with-client-credentials", "--client-id", "agent-client")
	require.NoError(t, err, out)

	assert.Contains(t, out, `Authenticated profile "clawdito" as an agent`)
	assert.Contains(t, out, "no refresh token")
	assert.Contains(t, out, `Created profile "clawdito" for account 999 (default)`)
	assert.NotContains(t, out, "agent-secret", "the client secret must never be echoed")

	calls := as.calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "client_credentials", calls[0].Get("grant_type"))
	assert.Equal(t, "agent-client", calls[0].Get("client_id"))
	assert.Equal(t, "agent-secret", calls[0].Get("client_secret"))

	creds, err := app.Auth.GetStore().Load("profile:clawdito")
	require.NoError(t, err)
	assert.Equal(t, "agent", creds.OAuthType)
	assert.Equal(t, "minted", creds.AccessToken)
	assert.Empty(t, creds.RefreshToken, "an agent is granted no refresh token")
	assert.Equal(t, "agent-client", creds.ClientID)
	assert.Equal(t, "agent-secret", creds.ClientSecret)
	assert.Equal(t, "urn:bc:agent:42", creds.Resource)

	cfgFile := readGlobalConfig(t)
	profiles := cfgFile["profiles"].(map[string]any)
	entry := profiles["clawdito"].(map[string]any)
	assert.Equal(t, "999", entry["account_id"])
	assert.Equal(t, as.srv.URL, entry["base_url"])
	assert.Equal(t, "full", entry["scope"])
}

func TestAuthLoginWithClientCredentialsJSONEnvelope(t *testing.T) {
	as := startAgentAS(t)
	app, buf := agentLoginApp(t, as, &config.Config{ActiveProfile: "clawdito"})
	withAccount(app, "999", "flag")
	app.Flags.JSON = true

	out, err := runLogin(t, app, strings.NewReader("agent-secret"),
		"--with-client-credentials", "--client-id", "agent-client")
	require.NoError(t, err, out)
	assert.Empty(t, out, "machine mode writes the envelope only")

	var envelope struct {
		OK   bool           `json:"ok"`
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope), buf.String())
	assert.True(t, envelope.OK)
	assert.Equal(t, "clawdito", envelope.Data["profile"])
	assert.Equal(t, "999", envelope.Data["account_id"])
	assert.Equal(t, "client_credentials", envelope.Data["source"])
	assert.Equal(t, "agent", envelope.Data["oauth_type"])
	assert.Equal(t, "full", envelope.Data["scope"])
	assert.Equal(t, "agent-client", envelope.Data["client_id"])
	assert.Equal(t, true, envelope.Data["profile_created"])
	assert.NotContains(t, buf.String(), "agent-secret", "the client secret must never reach the envelope")
}

// TestAuthLoginWithClientCredentialsStoresNothingWhenRefused: the mint is
// what proves the client, so a refusal must leave neither a credential nor
// a profile entry behind.
func TestAuthLoginWithClientCredentialsStoresNothingWhenRefused(t *testing.T) {
	as := startAgentAS(t)
	as.token = func() (int, string) {
		return http.StatusUnauthorized, `{"error":"invalid_client","error_description":"unknown client"}`
	}
	app, _ := agentLoginApp(t, as, &config.Config{ActiveProfile: "clawdito"})
	withAccount(app, "999", "flag")

	_, err := runLogin(t, app, strings.NewReader("wrong-secret"),
		"--with-client-credentials", "--client-id", "agent-client")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid_client")
	assertNothingStored(t, app, "clawdito")
}

func TestAuthLoginWithClientCredentialsNeedsAClientID(t *testing.T) {
	as := startAgentAS(t)
	app, _ := agentLoginApp(t, as, &config.Config{ActiveProfile: "clawdito"})
	withAccount(app, "999", "flag")

	_, err := runLogin(t, app, strings.NewReader("agent-secret"), "--with-client-credentials")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--client-id")
	assert.Empty(t, as.calls(), "the secret was sent without a client to send it for")
}

func TestAuthLoginWithClientCredentialsNeedsAProfile(t *testing.T) {
	as := startAgentAS(t)
	app, _ := agentLoginApp(t, as, &config.Config{})

	_, err := runLogin(t, app, strings.NewReader("agent-secret"),
		"--with-client-credentials", "--client-id", "agent-client")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "named profile")
	assert.Empty(t, as.calls())
}

func TestAuthLoginWithClientCredentialsNeedsAnAccountToCreateAProfile(t *testing.T) {
	as := startAgentAS(t)
	app, _ := agentLoginApp(t, as, &config.Config{ActiveProfile: "clawdito"})

	_, err := runLogin(t, app, strings.NewReader("agent-secret"),
		"--with-client-credentials", "--client-id", "agent-client")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--account")
	assert.Empty(t, as.calls())
}

func TestAuthLoginWithClientCredentialsRefusesEnvToken(t *testing.T) {
	as := startAgentAS(t)
	app, _ := agentLoginApp(t, as, &config.Config{ActiveProfile: "clawdito"})
	withAccount(app, "999", "flag")
	t.Setenv("BASECAMP_TOKEN", "env-token")

	_, err := runLogin(t, app, strings.NewReader("agent-secret"),
		"--with-client-credentials", "--client-id", "agent-client")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BASECAMP_TOKEN is set")
	assert.Empty(t, as.calls())
}

func TestAuthLoginWithClientCredentialsRefusesTerminalStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no /dev/ptmx on Windows")
	}
	pty, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("open /dev/ptmx: %v", err)
	}
	defer pty.Close()
	if !stdinarg.IsTerminal(pty) {
		t.Skip("this environment's pty is not a terminal")
	}

	as := startAgentAS(t)
	app, _ := agentLoginApp(t, as, &config.Config{ActiveProfile: "clawdito"})
	withAccount(app, "999", "flag")

	_, err = runLogin(t, app, pty, "--with-client-credentials", "--client-id", "agent-client")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stdin is a terminal")
	assert.Empty(t, as.calls())
}

func TestAuthLoginWithClientCredentialsRejectsBadStdin(t *testing.T) {
	for name, in := range map[string]string{
		"empty":       "",
		"newline":     "\n",
		"two lines":   "secret\nmore\n",
		"inner space": "sec ret\n",
	} {
		t.Run(name, func(t *testing.T) {
			as := startAgentAS(t)
			app, _ := agentLoginApp(t, as, &config.Config{ActiveProfile: "clawdito"})
			withAccount(app, "999", "flag")

			_, err := runLogin(t, app, strings.NewReader(in),
				"--with-client-credentials", "--client-id", "agent-client")
			require.Error(t, err)
			assert.Empty(t, as.calls())
		})
	}
}

func TestAuthLoginWithClientCredentialsIsExclusiveWithTheOtherLogins(t *testing.T) {
	for _, flag := range []string{"--with-token", "--device-code", "--remote", "--local", "--no-browser", "--login-hint=x@y"} {
		t.Run(flag, func(t *testing.T) {
			as := startAgentAS(t)
			app, _ := agentLoginApp(t, as, &config.Config{ActiveProfile: "clawdito"})
			_, err := runLogin(t, app, strings.NewReader("agent-secret"),
				"--with-client-credentials", "--client-id", "agent-client", flag)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "none of the others can be")
			assert.Empty(t, as.calls())
		})
	}
}

// TestAuthLoginWithClientCredentialsRefusesAnIdentityExpectation: an agent
// is not a person, so there is no identity for --expect-identity to check
// and the flag must be refused rather than quietly ignored.
func TestAuthLoginWithClientCredentialsRefusesAnIdentityExpectation(t *testing.T) {
	as := startAgentAS(t)
	app, _ := agentLoginApp(t, as, &config.Config{ActiveProfile: "clawdito"})
	withAccount(app, "999", "flag")

	_, err := runLogin(t, app, strings.NewReader("agent-secret"),
		"--with-client-credentials", "--client-id", "agent-client", "--expect-identity", "12345")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a person")
	assert.Empty(t, as.calls())
}
