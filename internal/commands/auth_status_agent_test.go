package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		Notice string `json:"notice"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope), buf.String())
	assert.Contains(t, envelope.Notice, "--with-client-credentials")
	assert.Contains(t, envelope.Notice, "--client-id agent-client")
	assert.NotContains(t, envelope.Notice, "Run: basecamp auth login -P")
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
