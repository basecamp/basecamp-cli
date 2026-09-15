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
