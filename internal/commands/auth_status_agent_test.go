package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// TestAuthStatusOnABrokenAgentOffersTheAgentLogin: `auth status` is where
// someone goes when an agent profile has stopped working, and the command
// it names is the one they will run. Naming the interactive login there
// would have them sign in as themselves over the agent's credential.
//
// This runs in a fresh process against a credential the CLI cannot renew
// (its client secret is gone), which is the case where the report has to
// say what to do and nothing else has looked at the credential yet.
func TestAuthStatusOnABrokenAgentOffersTheAgentLogin(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("BASECAMP_TOKEN", "")
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	cfg := &config.Config{BaseURL: srv.URL, ActiveProfile: "clawdito", Sources: map[string]string{}}
	authMgr := auth.NewManager(cfg, srv.Client())
	store := auth.NewStore(config.GlobalConfigDir())
	authMgr.SetStore(store)
	require.NoError(t, store.Save("profile:clawdito", &auth.Credentials{
		AccessToken:   "spent",
		OAuthType:     "agent",
		ClientID:      "agent-client",
		TokenEndpoint: srv.URL + "/oauth/tokens",
		// No client secret: nothing can mint, so the report must say what
		// to do about it.
		ExpiresAt: time.Now().Add(-time.Minute).Unix(),
	}))

	buf := &bytes.Buffer{}
	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		Output: output.New(output.Options{Format: output.FormatJSON, Writer: buf}),
	}
	app.Flags.JSON = true

	cmd := NewAuthCmd()
	cmd.SetArgs([]string{"status"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	var envelope struct {
		Notice string         `json:"notice"`
		Data   map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope), buf.String())
	assert.Contains(t, envelope.Notice, "--with-client-credentials")
	assert.Contains(t, envelope.Notice, "--client-id agent-client")
	assert.NotContains(t, envelope.Notice, "Run: basecamp auth login -P")
	assert.NotContains(t, envelope.Data, "renewal_refused", "a secret missing locally is not a hold the token endpoint set")
}

// agentStatusReport is `auth status --json` for the clawdito profile after
// one renewal of creds was answered by the token endpoint with status,
// header and body: the hold that answer left, as the report shows it.
type agentStatusReport struct {
	Notice string `json:"notice"`
	Data   struct {
		Authenticated  bool                    `json:"authenticated"`
		Refreshable    bool                    `json:"refreshable"`
		Expired        bool                    `json:"expired"`
		RenewalRefused *auth.RenewalHoldStatus `json:"renewal_refused"`
	} `json:"data"`
	raw string
}

func agentStatusAfterRefusal(t *testing.T, creds *auth.Credentials, status int, header http.Header, body string) agentStatusReport {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("BASECAMP_TOKEN", "")
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		for k, v := range header {
			w.Header()[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{BaseURL: srv.URL, ActiveProfile: "clawdito", Sources: map[string]string{}}
	authMgr := auth.NewManager(cfg, srv.Client())
	store := auth.NewStore(config.GlobalConfigDir())
	authMgr.SetStore(store)
	creds.OAuthType = "agent"
	creds.ClientID = "agent-client"
	creds.ClientSecret = "rotated-away"
	creds.TokenEndpoint = srv.URL + "/oauth/tokens"
	if creds.ExpiresAt == 0 {
		creds.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	}
	require.NoError(t, store.Save("profile:clawdito", creds))

	// The refusal that is remembered.
	_, err := authMgr.AccessToken(context.Background())
	require.Error(t, err)
	require.EqualValues(t, 1, calls.Load())

	buf := &bytes.Buffer{}
	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		Output: output.New(output.Options{Format: output.FormatJSON, Writer: buf}),
	}
	app.Flags.JSON = true

	cmd := NewAuthCmd()
	cmd.SetArgs([]string{"status"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())
	assert.EqualValues(t, 1, calls.Load(), "the report asked the token endpoint")

	report := agentStatusReport{raw: buf.String()}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &report), buf.String())
	assert.NotContains(t, report.raw, "rotated-away")
	return report
}

// TestAuthStatusSaysARefusalIsRemembered: an agent whose secret the token
// endpoint refused holds that verdict, and the next command will not ask
// again. The report is where someone looks to find out why the agent went
// quiet, so it says so — and what replaces the secret.
func TestAuthStatusSaysARefusalIsRemembered(t *testing.T) {
	report := agentStatusAfterRefusal(t, &auth.Credentials{AccessToken: "spent"},
		http.StatusUnauthorized, nil, `{"error":"invalid_client"}`)

	hold := report.Data.RenewalRefused
	require.NotNil(t, hold)
	assert.Equal(t, "refused", hold.Kind)
	assert.Equal(t, "token error: invalid_client", hold.Detail)
	assert.Empty(t, hold.Until, "invalid_client never ends with this secret")
	assert.Contains(t, hold.Message, "the refusal is remembered")
	assert.False(t, report.Data.Refreshable)
	assert.Contains(t, report.Notice, "--with-client-credentials")
}

// TestAuthStatusNamesAPermanentHoldWhileTheTokenIsLive: the report says
// what a renewal sent now would meet. A stored invalid_client hold means
// the next renewal is refused, so the token line says so while the token
// still has well over the refresh window left, not only once it expires.
func TestAuthStatusNamesAPermanentHoldWhileTheTokenIsLive(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("BASECAMP_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{BaseURL: srv.URL, ActiveProfile: "clawdito", Sources: map[string]string{}}
	authMgr := auth.NewManager(cfg, srv.Client())
	store := auth.NewStore(config.GlobalConfigDir())
	authMgr.SetStore(store)
	require.NoError(t, store.Save("profile:clawdito", &auth.Credentials{
		AccessToken:   "spent",
		OAuthType:     "agent",
		ClientID:      "agent-client",
		ClientSecret:  "rotated-away",
		TokenEndpoint: srv.URL + "/oauth/tokens",
		ExpiresAt:     time.Now().Add(-time.Minute).Unix(),
	}))
	_, err := authMgr.AccessToken(context.Background())
	require.Error(t, err)

	// A token that is good for another hour, beside the stored refusal.
	creds, err := store.Load("profile:clawdito")
	require.NoError(t, err)
	require.NotNil(t, creds.RenewalHold)
	creds.AccessToken = "still-good"
	creds.ExpiresAt = time.Now().Add(time.Hour).Unix()
	require.NoError(t, store.Save("profile:clawdito", creds))

	app := &appctx.App{Config: cfg, Auth: authMgr}
	report, err := authStatusReport(context.Background(), app)
	require.NoError(t, err)
	joined := strings.Join(report.details, "\n")
	assert.Contains(t, joined, "renewal would be refused: token error: invalid_client; log in with a new secret")
	assert.NotContains(t, joined, "refreshes automatically")
	assert.Contains(t, report.hint, "--with-client-credentials")
}

// TestAuthStatusReportsATimeLimitedHoldAsRefreshable: a hold that ends on
// its own — a 429 until its Retry-After, a refusal rechecked hourly — is
// what a renewal sent now would meet, and nothing more. The credential is
// refreshable, the hold and its deadline are reported, and the advice is to
// wait, not to replace the secret.
func TestAuthStatusReportsATimeLimitedHoldAsRefreshable(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		header http.Header
		body   string
		kind   string
		hint   string
	}{
		"a rate limit":  {http.StatusTooManyRequests, http.Header{"Retry-After": {"300"}}, `{}`, "rate_limited", "Try again in"},
		"invalid_grant": {http.StatusBadRequest, nil, `{"error":"invalid_grant"}`, "refused", "Wait until"},
		"a bare 401":    {http.StatusUnauthorized, nil, `nothing useful`, "refused", "Wait until"},
	} {
		t.Run(name, func(t *testing.T) {
			// Inside the refresh window, so a renewal is due and is held.
			report := agentStatusAfterRefusal(t, &auth.Credentials{
				AccessToken: "still-good",
				ExpiresAt:   time.Now().Add(2 * time.Minute).Unix(),
			}, c.status, c.header, c.body)

			hold := report.Data.RenewalRefused
			require.NotNil(t, hold)
			assert.Equal(t, c.kind, hold.Kind)
			assert.NotEmpty(t, hold.Until)
			assert.True(t, report.Data.Refreshable, "a hold that ends on its own was reported as unrenewable")
			assert.False(t, report.Data.Expired)
			assert.Contains(t, report.Notice, c.hint)
			assert.NotContains(t, report.raw, "--with-client-credentials", "a hold that ends on its own is not a reason to replace the secret")
		})
	}
}

// TestAuthStatusReportsAHoldWithoutAnAccessToken: the stored hold is
// reported whether or not a token is stored beside it.
func TestAuthStatusReportsAHoldWithoutAnAccessToken(t *testing.T) {
	report := agentStatusAfterRefusal(t, &auth.Credentials{},
		http.StatusUnauthorized, nil, `{"error":"invalid_client"}`)

	assert.False(t, report.Data.Authenticated)
	require.NotNil(t, report.Data.RenewalRefused)
	assert.Equal(t, "refused", report.Data.RenewalRefused.Kind)
}

// TestDoctorOffersTheAgentLoginForABrokenAgent: doctor is the diagnostic
// people (and agents) read for exactly the command to run, in its check
// hints and its breadcrumbs. Naming the interactive login there would have
// them sign in as themselves over the agent's credential.
func TestDoctorOffersTheAgentLoginForABrokenAgent(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("BASECAMP_TOKEN", "")
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	cfg := &config.Config{BaseURL: srv.URL, ActiveProfile: "clawdito", Sources: map[string]string{}}
	authMgr := auth.NewManager(cfg, srv.Client())
	store := auth.NewStore(config.GlobalConfigDir())
	authMgr.SetStore(store)
	require.NoError(t, store.Save("profile:clawdito", &auth.Credentials{
		AccessToken:   "spent",
		OAuthType:     "agent",
		ClientID:      "agent-client",
		TokenEndpoint: srv.URL + "/oauth/tokens",
		// Nothing to mint with, so the check fails and has to say what
		// to do about it.
		ExpiresAt: time.Now().Add(-time.Minute).Unix(),
	}))

	app := &appctx.App{Config: cfg, Auth: authMgr}

	check := checkAuthentication(context.Background(), app, false)
	assert.Equal(t, "fail", check.Status)
	assert.Contains(t, check.Hint, "--with-client-credentials")
	assert.NotContains(t, check.Hint, "Run: basecamp auth login -P")

	crumbs := buildDoctorBreadcrumbs([]Check{{Name: "Credentials", Status: "fail"}}, app.Auth.LoginCommand())
	require.NotEmpty(t, crumbs)
	assert.Contains(t, crumbs[0].Cmd, "--with-client-credentials")
	assert.Contains(t, crumbs[0].Cmd, "--client-id agent-client")

	// An agent credential with nothing usable in it is still an agent's.
	// Doctor reports "no credentials found" for it, and must not answer
	// that with the login that would replace the identity.
	require.NoError(t, store.Save("profile:clawdito", &auth.Credentials{
		OAuthType:     "agent",
		ClientID:      "agent-client",
		ClientSecret:  "agent-secret",
		TokenEndpoint: srv.URL + "/oauth/tokens",
	}))
	credentials := checkCredentials(app, false)
	assert.Equal(t, "fail", credentials.Status)
	assert.Contains(t, credentials.Hint, "--with-client-credentials")
	assert.Contains(t, app.Auth.LoginCommand(), "--client-id agent-client")
}

// TestAuthStatusNamesTheKindEvenWhenItCannotAuthenticate: a credential can
// be stored and still authenticate nobody — one holding no access token.
// Reporting only "not logged in" there hides WHICH credential is broken,
// and a reader deciding how to recover from `oauth_type` would find it
// absent and reach for the interactive login: the way an agent gets
// replaced by a person.
func TestAuthStatusNamesTheKindEvenWhenItCannotAuthenticate(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("BASECAMP_TOKEN", "")
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	cfg := &config.Config{BaseURL: srv.URL, ActiveProfile: "clawdito", Sources: map[string]string{}}
	authMgr := auth.NewManager(cfg, srv.Client())
	store := auth.NewStore(config.GlobalConfigDir())
	authMgr.SetStore(store)
	require.NoError(t, store.Save("profile:clawdito", &auth.Credentials{
		OAuthType:     "agent",
		ClientID:      "agent-client",
		ClientSecret:  "agent-secret",
		TokenEndpoint: srv.URL + "/oauth/tokens",
	}))

	buf := &bytes.Buffer{}
	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		Output: output.New(output.Options{Format: output.FormatJSON, Writer: buf}),
	}
	app.Flags.JSON = true

	cmd := NewAuthCmd()
	cmd.SetArgs([]string{"status"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	var envelope struct {
		Notice string         `json:"notice"`
		Data   map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope), buf.String())
	assert.Equal(t, false, envelope.Data["authenticated"])
	assert.Equal(t, "agent", envelope.Data["oauth_type"], "the report hid which credential was broken")
	assert.Contains(t, envelope.Notice, "--with-client-credentials")
}

// TestAuthStatusClaimsNothingWhenTheStoreCannotBeRead: "not logged in" is a
// statement about the credential, and a store that could not be read makes
// none. Saying it anyway would be worse than wrong here, because the rules
// people follow to recover decide from oauth_type — absent, they reach for
// the interactive login, which for an agent profile replaces the identity.
func TestAuthStatusClaimsNothingWhenTheStoreCannotBeRead(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("BASECAMP_TOKEN", "")
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	require.NoError(t, os.MkdirAll(config.GlobalConfigDir(), 0o700))
	// A directory where credentials.json belongs: present, unreadable as
	// anything, and not "no credential stored".
	require.NoError(t, os.MkdirAll(filepath.Join(config.GlobalConfigDir(), "credentials.json"), 0o700))

	cfg := &config.Config{BaseURL: "https://3.basecampapi.com", ActiveProfile: "clawdito", Sources: map[string]string{}}
	authMgr := auth.NewManager(cfg, http.DefaultClient)
	authMgr.SetStore(auth.NewStore(config.GlobalConfigDir()))
	app := &appctx.App{Config: cfg, Auth: authMgr}

	_, err := authStatusReport(context.Background(), app)
	require.Error(t, err, "status reported a verdict on a credential it could not read")
}
