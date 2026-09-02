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

// loginIdentityServer is a mock resource server for the pre-store identity
// check: /authorization.json and the account-scoped /{account}/my/profile.json
// answer for exactly one bearer token and record what they were sent.
type loginIdentityServer struct {
	srv *httptest.Server

	mu      sync.Mutex
	bearers []string
	paths   []string
	// identityID is what /authorization.json reports; scope is optional;
	// accounts are the authorized account IDs (999 by default).
	identityID int64
	scope      string
	accounts   []int64
	// authorizationStatus overrides the /authorization.json status when non-zero.
	authorizationStatus int
	// personName lets a test plant hostile content in the person record.
	personName string
}

func startLoginIdentityServer(t *testing.T, wantToken string) *loginIdentityServer {
	t.Helper()
	s := &loginIdentityServer{identityID: 28142355, accounts: []int64{999}, personName: "Clawdito"}
	mux := http.NewServeMux()
	record := func(r *http.Request) bool {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		s.mu.Lock()
		s.bearers = append(s.bearers, bearer)
		s.paths = append(s.paths, r.URL.Path)
		s.mu.Unlock()
		return bearer == wantToken
	}
	mux.HandleFunc("/999/my/profile.json", func(w http.ResponseWriter, r *http.Request) {
		if !record(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"id": 51177542, "name": s.personName, "email_address": "clawdito@example.com",
		}))
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
		accounts := make([]map[string]any, 0, len(s.accounts))
		for _, id := range s.accounts {
			accounts = append(accounts, map[string]any{"id": id, "name": "Acme", "href": fmt.Sprintf("%s/%d", s.srv.URL, id), "product": "bc3"})
		}
		body := map[string]any{
			"identity":   map[string]any{"id": s.identityID, "first_name": "Claw", "last_name": "Dito", "email_address": "identity@example.com"},
			"accounts":   accounts,
			"expires_at": "2036-01-01T00:00:00Z",
		}
		if s.scope != "" {
			body["scope"] = s.scope
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(body))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		http.NotFound(w, r)
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *loginIdentityServer) seenBearers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bearers...)
}

func (s *loginIdentityServer) seenPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...)
}

// managerTokenProvider feeds the app's SDK client from the auth manager, as
// the production authAdapter does. The login never verifies through it —
// verification rides a client bound to the candidate token — so a test that
// sees it used has found a regression.
type managerTokenProvider struct{ mgr *auth.Manager }

func (p *managerTokenProvider) AccessToken(ctx context.Context) (string, error) {
	return p.mgr.AccessToken(ctx)
}

// loginTestApp is an App wired for --with-token tests: file credential
// store under a temp XDG_CONFIG_HOME, SDK client (and SDKClientFor) pointed
// at the identity server, JSON output captured in buf.
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
	sdkOptions := []basecamp.ClientOption{
		basecamp.WithTransport(srv.srv.Client().Transport),
		basecamp.WithMaxRetries(1),
	}
	sdkClient := basecamp.NewClient(&basecamp.Config{BaseURL: srv.srv.URL}, &managerTokenProvider{mgr: authMgr}, sdkOptions...)
	buf := &bytes.Buffer{}
	app := &appctx.App{
		Config:     cfg,
		Auth:       authMgr,
		SDK:        sdkClient,
		SDKOptions: sdkOptions,
		Output:     output.New(output.Options{Format: output.FormatJSON, Writer: buf}),
	}
	return app, buf
}

// withAccount sets the effective account the way the root pre-run would
// from the given source ("flag", "env", "profile", "global").
func withAccount(app *appctx.App, id, source string) {
	app.Config.AccountID = id
	app.Config.Sources["account_id"] = source
	if source == "flag" {
		app.Flags.Account = id
	}
}

// runLogin executes `auth login` with stdin set to in and returns everything
// the command wrote to stdout/stderr, and the error.
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

func assertNothingStored(t *testing.T, app *appctx.App, name string) {
	t.Helper()
	_, loadErr := app.Auth.GetStore().Load("profile:" + name)
	assert.Error(t, loadErr, "no credential may be stored for a rejected token")
	assert.NotContains(t, readGlobalConfig(t), "profiles", "no profile entry may be registered for a rejected token")
	assert.NotContains(t, app.Config.Profiles, name)
}

func TestAuthLoginWithTokenCreatesProfileAndVerifiesIdentity(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	withAccount(app, "999", "flag")

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
	assert.Equal(t, []string{"/authorization.json", "/999/my/profile.json"}, srv.seenPaths(),
		"the person lookup is account-scoped, after the authorization document")

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
	withAccount(app, "999", "flag")
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
	assert.Equal(t, "identity@example.com", data["identity"].(map[string]any)["email"], "the identity's own email, not the person's")
	assert.Equal(t, float64(51177542), data["person"].(map[string]any)["id"])
	assert.Equal(t, "Clawdito", data["person"].(map[string]any)["name"])
	assert.Equal(t, "clawdito@example.com", data["person"].(map[string]any)["email"])
	assert.NotContains(t, buf.String(), "bc_at_secret")
}

func TestAuthLoginWithTokenExpectIdentityMismatchStoresNothing(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	withAccount(app, "999", "flag")

	out, err := runLogin(t, app, strings.NewReader("bc_at_secret\n"), "--with-token", "--expect-identity", "1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Authenticated as Claw Dito <identity@example.com> (identity 28142355), not identity 1")
	assert.NotContains(t, err.Error()+out, "bc_at_secret")

	var outErr *output.Error
	require.ErrorAs(t, err, &outErr)
	assert.Equal(t, output.CodeAuth, outErr.Code)

	assertNothingStored(t, app, "bot")
	assert.Equal(t, []string{"/authorization.json"}, srv.seenPaths(), "a rejected identity is not looked up any further")
}

func TestAuthLoginWithTokenExpectIdentityAcceptsNumericSpellings(t *testing.T) {
	for _, spelling := range []string{"28142355", "028142355", "+28142355"} {
		t.Run(spelling, func(t *testing.T) {
			srv := startLoginIdentityServer(t, "bc_at_secret")
			app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
			withAccount(app, "999", "flag")
			out, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token", "--expect-identity", spelling)
			require.NoError(t, err, out)
		})
	}
}

func TestAuthLoginWithTokenMismatchLeavesPreviousCredentialUntouched(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_new")
	cfg := &config.Config{
		ActiveProfile: "bot",
		Profiles:      map[string]*config.ProfileConfig{"bot": {AccountID: "999"}},
	}
	app, _ := loginTestApp(t, srv, cfg)
	withAccount(app, "999", "profile")
	prev := &auth.Credentials{AccessToken: "bc_at_old", RefreshToken: "old-refresh", OAuthType: "bc5", Scope: "full", ExpiresAt: 4102444800}
	require.NoError(t, app.Auth.GetStore().Save("profile:bot", prev))

	_, err := runLogin(t, app, strings.NewReader("bc_at_new"), "--with-token", "--expect-identity", "1")
	require.Error(t, err)

	creds, loadErr := app.Auth.GetStore().Load("profile:bot")
	require.NoError(t, loadErr)
	assert.Equal(t, prev, creds, "the previous credential is never touched by a rejected import")
	for _, bearer := range srv.seenBearers() {
		assert.Equal(t, "bc_at_new", bearer, "verification must never run as the stored credential")
	}
}

func TestAuthLoginWithTokenFailsClosedWhenUnverifiable(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	srv.authorizationStatus = http.StatusUnauthorized
	app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	withAccount(app, "999", "flag")

	_, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Could not verify the new credential")
	assertNothingStored(t, app, "bot")
}

func TestAuthLoginWithTokenRejectedTokenIsNotStored(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	withAccount(app, "999", "flag")

	_, err := runLogin(t, app, strings.NewReader("bc_at_wrong"), "--with-token")
	require.Error(t, err)
	assertNothingStored(t, app, "bot")
}

func TestAuthLoginWithTokenRequiresAccessToTheAccount(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	srv.accounts = []int64{111, 222}
	app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	withAccount(app, "999", "flag")

	_, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot access account 999 (authorized: 111, 222)")
	assertNothingStored(t, app, "bot")
	assert.NotContains(t, srv.seenPaths(), "/999/my/profile.json")
}

func TestAuthLoginWithTokenRejectsPersonLookupFailure(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	srv.accounts = []int64{999, 1000}
	app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	withAccount(app, "1000", "flag")

	_, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token")
	require.Error(t, err, "the authorization document lists 1000 but the account-scoped lookup 404s")
	assert.Contains(t, err.Error(), "on account 1000")
	assertNothingStored(t, app, "bot")
}

func TestAuthLoginWithTokenScopeReportedByServerWins(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	srv.scope = "read"
	app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	withAccount(app, "999", "flag")

	out, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token")
	require.NoError(t, err, out)
	creds, err := app.Auth.GetStore().Load("profile:bot")
	require.NoError(t, err)
	assert.Equal(t, "read", creds.Scope)
	assert.Equal(t, "51177542", creds.UserID)
	assert.Equal(t, "read", app.Config.Profiles["bot"].Scope)
}

func TestAuthLoginWithTokenRejectsUnknownReportedScope(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	srv.scope = "admin\x1b[31m"
	app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	withAccount(app, "999", "flag")

	_, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only read or full can be stored")
	assert.NotContains(t, err.Error(), "\x1b")
	assertNothingStored(t, app, "bot")
}

func TestAuthLoginWithTokenRejectsInvalidScopeBeforeReadingStdin(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	withAccount(app, "999", "flag")

	in := strings.NewReader("bc_at_secret")
	_, err := runLogin(t, app, in, "--with-token", "--scope", "admin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Invalid scope")
	assert.Equal(t, 12, in.Len(), "stdin must not be consumed by a usage error")
	assert.Empty(t, srv.seenBearers())
}

func TestAuthLoginWithTokenRequiresAccountToCreateProfile(t *testing.T) {
	t.Run("no account anywhere", func(t *testing.T) {
		srv := startLoginIdentityServer(t, "bc_at_secret")
		app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})

		_, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `Profile "bot" does not exist`)
		assert.Contains(t, err.Error(), "--account")
		assert.Empty(t, srv.seenBearers(), "nothing may be sent before the profile question is settled")
		assertNothingStored(t, app, "bot")
	})

	t.Run("a config-file account does not count", func(t *testing.T) {
		srv := startLoginIdentityServer(t, "bc_at_secret")
		app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
		withAccount(app, "999", "global")

		_, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--account")
		assert.Empty(t, srv.seenBearers())
	})

	t.Run("BASECAMP_ACCOUNT_ID counts", func(t *testing.T) {
		srv := startLoginIdentityServer(t, "bc_at_secret")
		app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
		withAccount(app, "999", "env")

		out, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token")
		require.NoError(t, err, out)
		assert.Equal(t, "999", app.Config.Profiles["bot"].AccountID)
	})
}

func TestAuthLoginWithTokenRejectsNonNumericAccount(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	withAccount(app, "abc", "flag")

	_, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `Invalid account ID "abc"`)
	assert.Empty(t, srv.seenBearers())
	assertNothingStored(t, app, "bot")
}

func TestAuthLoginWithTokenRequiresProfile(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	app, _ := loginTestApp(t, srv, &config.Config{})
	withAccount(app, "999", "flag")

	_, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "named profile")
	assert.Contains(t, err.Error(), "--profile")
	assert.Empty(t, srv.seenBearers())
}

func TestAuthLoginWithTokenRejectsAccountMismatchOnExistingProfile(t *testing.T) {
	for _, source := range []string{"flag", "env"} {
		t.Run(source, func(t *testing.T) {
			srv := startLoginIdentityServer(t, "bc_at_secret")
			cfg := &config.Config{
				ActiveProfile: "bot",
				Profiles:      map[string]*config.ProfileConfig{"bot": {AccountID: "111"}},
			}
			app, _ := loginTestApp(t, srv, cfg)
			// The root pre-run re-applies flag and env over the profile
			// binding, so the effective account is the override.
			withAccount(app, "222", source)

			_, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token")
			require.Error(t, err)
			assert.Contains(t, err.Error(), `Profile "bot" is bound to account 111, not 222`)
			assert.Empty(t, srv.seenBearers())
		})
	}
}

func TestAuthLoginWithTokenExistingProfileKeepsItsEntry(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	cfg := &config.Config{
		ActiveProfile:  "bot",
		DefaultProfile: "bot",
		Profiles:       map[string]*config.ProfileConfig{"bot": {AccountID: "999", ProjectID: "42"}},
	}
	app, buf := loginTestApp(t, srv, cfg)
	withAccount(app, "999", "profile")
	app.Flags.JSON = true
	require.NoError(t, os.MkdirAll(config.GlobalConfigDir(), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(config.GlobalConfigDir(), "config.json"),
		[]byte(`{"profiles":{"bot":{"base_url":"https://3.basecampapi.com","account_id":"999","project_id":"42"}},"default_profile":"bot"}`), 0o600))

	out, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token")
	require.NoError(t, err, out)

	cfgFile := readGlobalConfig(t)
	bot := cfgFile["profiles"].(map[string]any)["bot"].(map[string]any)
	assert.Equal(t, "42", bot["project_id"], "importing into an existing profile must not rewrite its entry")
	assert.Equal(t, "bot", cfgFile["default_profile"])

	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	assert.Equal(t, false, envelope.Data["profile_created"])
	assert.Equal(t, true, envelope.Data["default"], "an existing default profile reports default")
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
			withAccount(app, "999", "flag")

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
	withAccount(app, "999", "flag")

	_, err = runLogin(t, app, pty, "--with-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stdin is a terminal")
	assert.Contains(t, err.Error(), `op read "op://<vault>/<item>/credential" | basecamp auth login --with-token -P <profile> --account <id>`)
	assert.Empty(t, srv.seenBearers())
}

func TestAuthLoginWithTokenRefusesEnvToken(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	withAccount(app, "999", "flag")
	t.Setenv("BASECAMP_TOKEN", "env-token")

	_, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BASECAMP_TOKEN is set")
	assert.Empty(t, srv.seenBearers())
}

func TestAuthLoginRejectsNonNumericExpectIdentity(t *testing.T) {
	for _, bad := range []string{"clawdito", "0", "-5", ""} {
		if bad == "" {
			continue
		}
		t.Run(bad, func(t *testing.T) {
			srv := startLoginIdentityServer(t, "bc_at_secret")
			app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
			withAccount(app, "999", "flag")

			_, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token", "--expect-identity", bad)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "numeric identity ID")
			assert.Empty(t, srv.seenBearers())
		})
	}
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

// TestAuthLoginSanitizesServerSuppliedName: the person's name is server
// data headed for a one-line terminal sink; the JSON field keeps it verbatim.
func TestAuthLoginSanitizesServerSuppliedName(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_secret")
	srv.personName = "Claw\x1b]8;;https://evil.example\x07dito\r\nEvil"
	app, buf := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	withAccount(app, "999", "flag")

	out, err := runLogin(t, app, strings.NewReader("bc_at_secret"), "--with-token")
	require.NoError(t, err, out)
	assert.Contains(t, out, "Logged in as Clawdito Evil <clawdito@example.com>")
	assert.NotContains(t, out, "\x1b")
	assert.NotContains(t, out, "evil.example")

	app2, buf2 := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	withAccount(app2, "999", "flag")
	app2.Flags.JSON = true
	_, err = runLogin(t, app2, strings.NewReader("bc_at_secret"), "--with-token")
	require.NoError(t, err)
	_ = buf
	assert.Contains(t, buf2.String(), `evil.example`, "structured output carries the field verbatim")
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

// TestAuthLoginDeviceFlowExpectIdentityStoresNothingOnMismatch: the OAuth
// flows verify through the same pre-store hook. A device login whose token
// belongs to someone else never reaches the credential store, and the
// success line is never printed.
func TestAuthLoginDeviceFlowExpectIdentityStoresNothingOnMismatch(t *testing.T) {
	srv := startLoginIdentityServer(t, "dev-tok")
	// A pinned issuer served by the same mux: the device grant hands out
	// dev-tok, which the identity server then answers for.
	srv.srv.Config.Handler = deviceGrantThen(t, srv.srv.Config.Handler)
	app, _ := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot", Profiles: map[string]*config.ProfileConfig{"bot": {AccountID: "999"}}})
	withAccount(app, "999", "profile")
	t.Setenv("BASECAMP_OAUTH_ISSUER", srv.srv.URL)
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_CLIENT", "")
	t.Setenv("SSH_TTY", "")

	out, err := runLogin(t, app, strings.NewReader(""), "--device-code", "--expect-identity", "1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not identity 1")
	assert.NotContains(t, out, "Authentication successful", "no success line before the check")
	_, loadErr := app.Auth.GetStore().Load("profile:bot")
	assert.Error(t, loadErr, "a device login that fails --expect-identity stores nothing")

	// The same grant with the right expectation is stored and labeled.
	out, err = runLogin(t, app, strings.NewReader(""), "--device-code", "--expect-identity", "28142355")
	require.NoError(t, err, out)
	assert.Contains(t, out, "Authentication successful")
	assert.Contains(t, out, "Logged in as: Clawdito <clawdito@example.com> (identity 28142355, person 51177542)")
	creds, err := app.Auth.GetStore().Load("profile:bot")
	require.NoError(t, err)
	assert.Equal(t, "dev-tok", creds.AccessToken)
	assert.Equal(t, "51177542", creds.UserID)
}

// deviceGrantThen wraps a handler with a BC5 device grant that issues
// dev-tok immediately, at the paths a pinned issuer derives.
func deviceGrantThen(t *testing.T, next http.Handler) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/device_authorizations":
			fmt.Fprintf(w, `{"device_code":"dc","user_code":"ABCD-EFGH","verification_uri":%q,"expires_in":600,"interval":1}`, "http://"+r.Host+"/verify")
		case "/oauth/tokens":
			fmt.Fprint(w, `{"access_token":"dev-tok","refresh_token":"dev-ref","token_type":"bearer","expires_in":3600,"scope":"full"}`)
		default:
			next.ServeHTTP(w, r)
		}
	})
}
