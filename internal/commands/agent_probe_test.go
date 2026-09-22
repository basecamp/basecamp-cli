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

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// setupAgentProbeApp stores a valid agent credential for account 555 against
// a server that answers the agent's person record and refuses everything
// else, /authorization.json included — which is how Basecamp answers an
// agent self-token, since it has no identity behind it. The returned
// function reports the paths the server was asked for.
func setupAgentProbeApp(t *testing.T) (*appctx.App, *bytes.Buffer, func() []string) {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("BASECAMP_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		// Only the agent's own token is answered, so a probe that sent
		// some other credential fails the same way a refused one would.
		if r.URL.Path == "/555/my/profile.json" && r.Header.Get("Authorization") == "Bearer bc_at_agent" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": 777, "name": "Triage Bot"})
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{AccountID: "555", BaseURL: srv.URL, ActiveProfile: "bot", Sources: map[string]string{}}
	authMgr := auth.NewManager(cfg, srv.Client())
	store := auth.NewStore(config.GlobalConfigDir())
	authMgr.SetStore(store)
	require.NoError(t, store.Save("profile:bot", &auth.Credentials{
		AccessToken:   "bc_at_agent",
		OAuthType:     "agent",
		ClientID:      "agent-client",
		ClientSecret:  "agent-secret",
		TokenEndpoint: srv.URL + "/oauth/tokens",
		ExpiresAt:     time.Now().Add(time.Hour).Unix(),
	}))

	buf := &bytes.Buffer{}
	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    basecamp.NewClient(&basecamp.Config{BaseURL: srv.URL}, authMgr, basecamp.WithMaxRetries(1)),
		Output: output.New(output.Options{Format: output.FormatJSON, Writer: buf}),
	}
	app.Flags.JSON = true
	return app, buf, func() []string { return paths }
}

// TestAuthStatusCheckAcceptsAValidAgentToken: --check asks the server
// whether it accepts the token the CLI would send. For an agent that is its
// person record, not the authorization document, which refuses every agent
// token and would report a working agent as rejected.
func TestAuthStatusCheckAcceptsAValidAgentToken(t *testing.T) {
	app, buf, paths := setupAgentProbeApp(t)

	cmd := NewAuthCmd()
	cmd.SetArgs([]string{"status", "--check"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope), buf.String())
	assert.Equal(t, true, envelope.Data["valid"], buf.String())
	assert.Equal(t, []string{"/555/my/profile.json"}, paths())
}

// TestAuthStatusCheckOnAnAgentWithoutAnAccountAsksForOne: an agent is probed
// in its account, so without one there is no request to make, and nothing
// is sent before that is said.
func TestAuthStatusCheckOnAnAgentWithoutAnAccountAsksForOne(t *testing.T) {
	app, _, paths := setupAgentProbeApp(t)
	app.Config.AccountID = ""
	// Expired, so producing a token would mint one at the token endpoint.
	creds, err := app.Auth.GetStore().Load("profile:bot")
	require.NoError(t, err)
	creds.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	require.NoError(t, app.Auth.GetStore().Save("profile:bot", creds))

	_, err = checkWithServer(context.Background(), app)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Account ID required")
	assert.Empty(t, paths())
}

// TestDoctorAPIConnectivityPassesForAValidAgentToken: the same probe in
// doctor, which otherwise fails a working agent as "Cannot connect".
func TestDoctorAPIConnectivityPassesForAValidAgentToken(t *testing.T) {
	app, _, paths := setupAgentProbeApp(t)

	check := checkAPIConnectivity(context.Background(), app, false)
	assert.Equal(t, "pass", check.Status, "%s: %s", check.Message, check.Hint)
	assert.Equal(t, []string{"/555/my/profile.json"}, paths())
}
