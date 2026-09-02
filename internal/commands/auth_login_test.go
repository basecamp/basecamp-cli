package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/stdinarg"
)

// TestAuthLoginDeviceCodeForcesRemoteMode is the regression test for the
// --device-code → Remote flag mapping. Discovery falls back to Launchpad
// (pointed at a 404 test server), where remote mode is observable: it prints
// the paste-callback instructions instead of opening a browser and listening
// on the loopback callback port.
func TestAuthLoginDeviceCodeForcesRemoteMode(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	// Pin SSH auto-detection off so only the flag can select remote mode.
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_CLIENT", "")
	t.Setenv("SSH_TTY", "")

	// No protected-resource metadata (404) → Launchpad fallback, pointed at
	// this server. The token endpoint is never reached.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv("BASECAMP_LAUNCHPAD_URL", srv.URL)

	// Remote mode reads the pasted callback URL from os.Stdin. Swap in an
	// immediate EOF so the prompt fails fast instead of blocking.
	devNull, err := os.Open(os.DevNull)
	require.NoError(t, err)
	defer devNull.Close()
	origStdin := os.Stdin
	os.Stdin = devNull
	t.Cleanup(func() { os.Stdin = origStdin })

	cfg := &config.Config{BaseURL: srv.URL}
	authMgr := auth.NewManager(cfg, srv.Client())
	authMgr.SetStore(auth.NewStore(tmpDir))
	app := &appctx.App{Config: cfg, Auth: authMgr}

	// Safety net: if the mapping regresses to local mode, the loopback
	// listener would otherwise wait out its full five-minute timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := NewAuthCmd()
	cmd.SetArgs([]string{"login", "--device-code"})
	cmd.SetContext(appctx.WithApp(ctx, app))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	err = cmd.Execute()
	require.Error(t, err, "EOF on the paste prompt must abort the login")
	assert.Contains(t, err.Error(), "no input received")

	output := out.String()
	assert.Contains(t, output, "Remote Authentication",
		"--device-code must select the remote paste-callback flow")
	assert.Contains(t, output, "Paste the callback URL")
	assert.NotContains(t, output, "Opening browser",
		"remote mode must not attempt a browser launch")
}

// loginIdentityServer is a mock resource server for the post-login identity
// check: /my/profile.json and /authorization.json answer for exactly one
// bearer token and record what they were sent.
type loginIdentityServer struct {
	srv *httptest.Server

	mu      sync.Mutex
	bearers []string
	// identityID is what /authorization.json reports; scope is optional.
	identityID int64
	scope      string
	// authorizationStatus overrides the /authorization.json status when non-zero.
	authorizationStatus int
}

func startLoginIdentityServer(t *testing.T, wantToken string) *loginIdentityServer {
	t.Helper()
	s := &loginIdentityServer{identityID: 28142355}
	mux := http.NewServeMux()
	record := func(r *http.Request) bool {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		s.mu.Lock()
		s.bearers = append(s.bearers, bearer)
		s.mu.Unlock()
		return bearer == wantToken
	}
	mux.HandleFunc("/my/profile.json", func(w http.ResponseWriter, r *http.Request) {
		if !record(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":51177542,"name":"Clawdito","email_address":"clawdito@example.com"}`)
	})
	mux.HandleFunc("/authorization.json", func(w http.ResponseWriter, r *http.Request) {
		if !record(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if s.authorizationStatus != 0 {
			w.WriteHeader(s.authorizationStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		scope := ""
		if s.scope != "" {
			scope = fmt.Sprintf(`,"scope":%q`, s.scope)
		}
		fmt.Fprintf(w, `{"identity":{"id":%d,"email_address":"clawdito@example.com"},"accounts":[{"id":999,"name":"Acme","href":"%s/999","product":"bc3"}]%s,"expires_at":"2036-01-01T00:00:00Z"}`, s.identityID, s.srv.URL, scope)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *loginIdentityServer) seenBearers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bearers...)
}

// managerTokenProvider feeds the SDK client from the auth manager, as the
// production authAdapter does, so the identity check runs as whatever the
// login just stored.
type managerTokenProvider struct{ mgr *auth.Manager }

func (p *managerTokenProvider) AccessToken(ctx context.Context) (string, error) {
	return p.mgr.AccessToken(ctx)
}

// loginTestApp is an App wired for --with-token tests: file credential
// store under a temp XDG_CONFIG_HOME, SDK client pointed at the identity
// server, JSON output captured in buf.
func loginTestApp(t *testing.T, srv *loginIdentityServer, cfg *config.Config) (*appctx.App, *bytes.Buffer) {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("BASECAMP_TOKEN", "")
	t.Setenv("BASECAMP_OAUTH_ISSUER", "")
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg.BaseURL = srv.srv.URL
	if cfg.Sources == nil {
		cfg.Sources = map[string]string{}
	}
	authMgr := auth.NewManager(cfg, srv.srv.Client())
	authMgr.SetStore(auth.NewStore(config.GlobalConfigDir()))
	sdkClient := basecamp.NewClient(&basecamp.Config{BaseURL: srv.srv.URL}, &managerTokenProvider{mgr: authMgr},
		basecamp.WithTransport(srv.srv.Client().Transport),
		basecamp.WithMaxRetries(1),
	)
	buf := &bytes.Buffer{}
	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Output: output.New(output.Options{Format: output.FormatJSON, Writer: buf}),
	}
	return app, buf
}

// runLogin executes `auth login` with stdin set to in and returns the error
// and everything the command wrote to stdout/stderr.
func runLogin(t *testing.T, app *appctx.App, in io.Reader, args ...string) (string, error) {
	t.Helper()
	cmd := NewAuthCmd()
	cmd.SetArgs(append([]string{"login"}, args...))
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetIn(in)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.Execute()
	return out.String(), err
}

func readGlobalConfig(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(config.GlobalConfigDir(), "config.json"))
	if os.IsNotExist(err) {
		return map[string]any{}
	}
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(data, &cfg))
	return cfg
}

func TestAuthLoginWithTokenCreatesProfileAndVerifiesIdentity(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	app.Flags.Account = "999"

	// A trailing newline is what `op read` and `echo` deliver; it must not
	// reach the Authorization header.
	out, err := runLogin(t, app, strings.NewReader("bc_at_secret\n"), "--with-token")
	require.NoError(t, err, out)

	assert.Contains(t, out, "Logged in as Clawdito <clawdito@example.com> (identity 28142355, person 51177542)")
	assert.Contains(t, out, `Created profile "bot" for account 999 (default)`)
	assert.NotContains(t, out, "bc_at_secret", "the token must never be echoed")
	for _, bearer := range srv.seenBearers() {
		assert.Equal(t, "bc_at_secret", bearer)
	}

	creds, err := app.Auth.GetStore().Load("profile:bot")
	require.NoError(t, err)
	assert.Equal(t, "bc_at_secret", creds.AccessToken)
	assert.Zero(t, creds.ExpiresAt, "a personal access token is stored as non-expiring")
	assert.Empty(t, creds.RefreshToken)
	assert.Equal(t, "bc5", creds.OAuthType)
	assert.Equal(t, "full", creds.Scope)
	assert.Equal(t, "51177542", creds.UserID)
	assert.Equal(t, "clawdito@example.com", creds.UserEmail)

	// Non-expiring: AccessToken serves it without attempting a refresh
	// (there is no refresh token or token endpoint to attempt one with).
	tok, err := app.Auth.AccessToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "bc_at_secret", tok)

	cfgFile := readGlobalConfig(t)
	profiles := cfgFile["profiles"].(map[string]any)
	bot := profiles["bot"].(map[string]any)
	assert.Equal(t, "999", bot["account_id"])
	assert.Equal(t, srv.srv.URL, bot["base_url"])
	assert.Equal(t, "full", bot["scope"])
	assert.Equal(t, "bot", cfgFile["default_profile"], "the first profile becomes the default")
	assert.Equal(t, "999", app.Config.Profiles["bot"].AccountID, "the in-memory config learns the profile too")
}

func TestAuthLoginWithTokenJSONEnvelope(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	app, buf := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	app.Flags.Account = "999"
	app.Flags.JSON = true

	out, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token", "--expect-identity", "28142355")
	require.NoError(t, err, out)
	assert.Empty(t, out, "machine mode writes the envelope only")

	var envelope struct {
		OK   bool           `json:"ok"`
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope), buf.String())
	assert.True(t, envelope.OK)
	data := envelope.Data
	assert.Equal(t, "bot", data["profile"])
	assert.Equal(t, "999", data["account_id"])
	assert.Equal(t, "token", data["source"])
	assert.Equal(t, "bc5", data["oauth_type"])
	assert.Equal(t, "full", data["scope"])
	assert.Contains(t, data, "expires_at")
	assert.Nil(t, data["expires_at"])
	assert.Equal(t, true, data["profile_created"])
	assert.Equal(t, true, data["default"])
	assert.Equal(t, float64(28142355), data["identity"].(map[string]any)["id"])
	assert.Equal(t, "clawdito@example.com", data["identity"].(map[string]any)["email"])
	assert.Equal(t, float64(51177542), data["person"].(map[string]any)["id"])
	assert.Equal(t, "Clawdito", data["person"].(map[string]any)["name"])
	assert.NotContains(t, buf.String(), "bc_at_secret")
}

func TestAuthLoginWithTokenExpectIdentityMismatchLeavesNothingBehind(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	app.Flags.Account = "999"

	out, err := runLogin(t, app, strings.NewReader("bc_at_secret\n"), "--with-token", "--expect-identity", "1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Authenticated as Clawdito <clawdito@example.com> (identity 28142355, person 51177542), not identity 1")
	assert.NotContains(t, err.Error()+out, "bc_at_secret")

	var outErr *output.Error
	require.ErrorAs(t, err, &outErr)
	assert.Equal(t, output.CodeAuth, outErr.Code)

	_, loadErr := app.Auth.GetStore().Load("profile:bot")
	assert.Error(t, loadErr, "the rejected credential must be deleted")
	assert.NotContains(t, readGlobalConfig(t), "profiles", "no profile entry may be registered for a rejected credential")
	assert.NotContains(t, app.Config.Profiles, "bot")
}

func TestAuthLoginWithTokenMismatchRestoresPreviousCredentials(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_new")
	cfg := &config.Config{
		ActiveProfile: "bot",
		Profiles:      map[string]*config.ProfileConfig{"bot": {AccountID: "999"}},
	}
	app, _ := loginTestApp(t, srv, cfg)
	prev := &auth.Credentials{AccessToken: "bc_at_old", RefreshToken: "old-refresh", OAuthType: "bc5", Scope: "full", ExpiresAt: 4102444800}
	require.NoError(t, app.Auth.GetStore().Save("profile:bot", prev))

	_, err := runLogin(t, app, strings.NewReader("bc_at_new"), "--with-token", "--expect-identity", "1")
	require.Error(t, err)

	creds, loadErr := app.Auth.GetStore().Load("profile:bot")
	require.NoError(t, loadErr, "the profile's previous credential must survive a rejected import")
	assert.Equal(t, prev, creds)
}

func TestAuthLoginWithTokenFailsClosedWhenIdentityUnverifiable(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	srv.authorizationStatus = http.StatusUnauthorized
	app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	app.Flags.Account = "999"

	_, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token", "--expect-identity", "28142355")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Could not verify the identity")
	_, loadErr := app.Auth.GetStore().Load("profile:bot")
	assert.Error(t, loadErr)

	// Without an expectation the identity endpoint is informational: the
	// person from /my/profile.json is enough to keep the credential.
	app, _ = loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	app.Flags.Account = "999"
	out, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token")
	require.NoError(t, err, out)
	assert.Contains(t, out, "Logged in as Clawdito <clawdito@example.com> (person 51177542)")
}

func TestAuthLoginWithTokenRejectedTokenIsNotKept(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	app.Flags.Account = "999"

	_, err := runLogin(t, app, strings.NewReader("bc_at_wrong"), "--with-token")
	require.Error(t, err)
	_, loadErr := app.Auth.GetStore().Load("profile:bot")
	assert.Error(t, loadErr, "a token the server rejects must not be stored")
	assert.NotContains(t, readGlobalConfig(t), "profiles")
}

func TestAuthLoginWithTokenScopeReportedByServerWins(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	srv.scope = "read"
	app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	app.Flags.Account = "999"

	out, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token")
	require.NoError(t, err, out)
	creds, err := app.Auth.GetStore().Load("profile:bot")
	require.NoError(t, err)
	assert.Equal(t, "read", creds.Scope)
	assert.Equal(t, "51177542", creds.UserID, "identity survives the scope correction")
	assert.Equal(t, "read", app.Config.Profiles["bot"].Scope)
}

func TestAuthLoginWithTokenRequiresAccountToCreateProfile(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})

	_, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `Profile "bot" does not exist`)
	assert.Contains(t, err.Error(), "--account")
	assert.Empty(t, srv.seenBearers(), "nothing may be sent before the profile question is settled")
	_, loadErr := app.Auth.GetStore().Load("profile:bot")
	assert.Error(t, loadErr)
}

func TestAuthLoginWithTokenRequiresProfile(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	app, _ := loginTestApp(t, srv, &config.Config{})
	app.Flags.Account = "999"

	_, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "named profile")
	assert.Contains(t, err.Error(), "--profile")
	assert.Empty(t, srv.seenBearers())
}

func TestAuthLoginWithTokenRejectsAccountMismatchOnExistingProfile(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	cfg := &config.Config{
		ActiveProfile: "bot",
		Profiles:      map[string]*config.ProfileConfig{"bot": {AccountID: "111"}},
	}
	app, _ := loginTestApp(t, srv, cfg)
	app.Flags.Account = "222"

	_, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `Profile "bot" is bound to account 111, not 222`)
	assert.Empty(t, srv.seenBearers())
}

func TestAuthLoginWithTokenExistingProfileKeepsItsEntry(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	cfg := &config.Config{
		ActiveProfile: "bot",
		Profiles:      map[string]*config.ProfileConfig{"bot": {AccountID: "999", ProjectID: "42"}},
	}
	app, _ := loginTestApp(t, srv, cfg)
	require.NoError(t, os.MkdirAll(config.GlobalConfigDir(), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(config.GlobalConfigDir(), "config.json"),
		[]byte(`{"profiles":{"bot":{"base_url":"https://3.basecampapi.com","account_id":"999","project_id":"42"}},"default_profile":"human"}`), 0o600))

	out, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token")
	require.NoError(t, err, out)
	assert.NotContains(t, out, "Created profile")

	cfgFile := readGlobalConfig(t)
	bot := cfgFile["profiles"].(map[string]any)["bot"].(map[string]any)
	assert.Equal(t, "42", bot["project_id"], "importing into an existing profile must not rewrite its entry")
	assert.Equal(t, "human", cfgFile["default_profile"])
}

func TestAuthLoginWithTokenRejectsBadStdin(t *testing.T) {
	for name, tc := range map[string]struct {
		in   string
		want string
	}{
		"empty":        {"", "No token on stdin"},
		"whitespace":   {"  \n\t", "No token on stdin"},
		"two lines":    {"bc_at_one\nbc_at_two\n", "single line"},
		"inner space":  {"bc_at one", "single line"},
		"control char": {"bc_at\x1bone", "single line"},
		"oversized":    {strings.Repeat("x", maxTokenBytes+1), "longer than"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := startLoginIdentityServer(t, "bc_at_secret")
			app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
			app.Flags.Account = "999"

			_, err := runLogin(t, app, strings.NewReader(tc.in), "--with-token")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.NotContains(t, err.Error(), "bc_at_", "the rejected input must not be echoed")
			assert.Empty(t, srv.seenBearers())
		})
	}
}

func TestAuthLoginWithTokenRefusesTerminalStdin(t *testing.T) {
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

	srv := startLoginIdentityServer(t, "bc_at_secret")
	app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	app.Flags.Account = "999"

	_, err = runLogin(t, app, pty, "--with-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stdin is a terminal")
	assert.Contains(t, err.Error(), `op read "op://<vault>/<item>/credential" | basecamp auth login --with-token -P <profile> --account <id>`)
	assert.Empty(t, srv.seenBearers())
}

func TestAuthLoginWithTokenRefusesEnvToken(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	app.Flags.Account = "999"
	t.Setenv("BASECAMP_TOKEN", "env-token")

	_, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BASECAMP_TOKEN is set")
	assert.Empty(t, srv.seenBearers())
}

func TestAuthLoginWithTokenRejectsNonNumericExpectIdentity(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	app.Flags.Account = "999"

	_, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token", "--expect-identity", "clawdito")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "numeric identity ID")
	assert.Empty(t, srv.seenBearers())
}

func TestAuthLoginWithTokenIsExclusiveWithInteractiveFlags(t *testing.T) {
	for _, flag := range []string{"--device-code", "--remote", "--local", "--no-browser", "--login-hint=x@y"} {
		t.Run(flag, func(t *testing.T) {
			srv := startLoginIdentityServer(t, "bc_at_secret")
			app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
			_, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token", flag)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "with-token")
		})
	}
}

// TestAuthLoginRefusesMachineOutputForInteractiveFlows covers the machine-mode
// half of #669: a browser or device login under --json/--agent used to print
// prose and block on approval. It now refuses before touching the network.
func TestAuthLoginRefusesMachineOutputForInteractiveFlows(t *testing.T) {
	for name, set := range map[string]func(*appctx.App){
		"json":     func(a *appctx.App) { a.Flags.JSON = true },
		"agent":    func(a *appctx.App) { a.Flags.Agent = true },
		"quiet":    func(a *appctx.App) { a.Flags.Quiet = true },
		"ids-only": func(a *appctx.App) { a.Flags.IDsOnly = true },
		"count":    func(a *appctx.App) { a.Flags.Count = true },
	} {
		t.Run(name, func(t *testing.T) {
			srv := startLoginIdentityServer(t, "bc_at_secret")
			app, _ := loginTestApp(t, srv, &config.Config{})
			set(app)
			_, err := runLogin(t, app, strings.NewReader(""), "--device-code")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "machine output mode")
			assert.Contains(t, err.Error(), "--with-token")
			assert.Empty(t, srv.seenBearers())
		})
	}
}

func TestAuthLoginUnknownProfileNeedsCreateOrToken(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "ghost"})

	_, err := runLogin(t, app, strings.NewReader(""), "--device-code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `Profile "ghost" does not exist`)
	assert.Contains(t, err.Error(), "basecamp profile create ghost")
	assert.Contains(t, err.Error(), "--with-token -P ghost")
}

func TestAuthLoginExpectIdentityRefusesEnvToken(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	app, _ := loginTestApp(t, srv, &config.Config{})
	t.Setenv("BASECAMP_TOKEN", "env-token")

	_, err := runLogin(t, app, strings.NewReader(""), "--device-code", "--expect-identity", "1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BASECAMP_TOKEN is set")
}
