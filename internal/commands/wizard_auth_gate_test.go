package commands

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// TestWizardAuthRefusesRatherThanLoginWhenTheStoreCannotBeRead: setup's
// authentication step falls through to an interactive login when it finds
// no credential, so "I could not read the store" must not reach it as
// "there is no credential" — a person sent through that login would
// replace whatever was already stored, an agent's credential included.
func TestWizardAuthRefusesRatherThanLoginWhenTheStoreCannotBeRead(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("BASECAMP_TOKEN", "")
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	// A directory where credentials.json belongs: present, and unreadable
	// as anything. Not "no credential stored".
	require.NoError(t, os.MkdirAll(filepath.Join(config.GlobalConfigDir(), "credentials.json"), 0o700))

	// Any request at all would mean a login was started.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("setup reached the network (%s) instead of reporting the unreadable store", r.URL.Path)
	}))
	defer srv.Close()

	cfg := &config.Config{BaseURL: srv.URL, Sources: map[string]string{}}
	authMgr := auth.NewManager(cfg, srv.Client())
	authMgr.SetStore(auth.NewStore(config.GlobalConfigDir()))
	app := &appctx.App{Config: cfg, Auth: authMgr}

	cmd := &cobra.Command{}
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})

	_, err := wizardAuth(cmd, app, tui.NewStyles(), false)
	require.Error(t, err, "setup treated an unreadable store as an invitation to log in")
	assert.NotContains(t, err.Error(), "Not authenticated")
}

// TestWizardAuthRefusesRatherThanReplaceABrokenAgent: setup falls through
// to an interactive login when it finds no credential, and an agent
// credential that holds nothing usable is stored, not absent. Reading it as
// absent is how someone signing in to fix their setup replaces the agent
// with themselves.
func TestWizardAuthRefusesRatherThanReplaceABrokenAgent(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("BASECAMP_TOKEN", "")
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("setup reached the network (%s) instead of refusing", r.URL.Path)
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

	app := &appctx.App{Config: cfg, Auth: authMgr}
	cmd := &cobra.Command{}
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})

	_, err := wizardAuth(cmd, app, tui.NewStyles(), false)
	require.Error(t, err, "setup offered to log in over a stored agent credential")
	assert.Contains(t, output.AsError(err).Hint, "--with-client-credentials")
}
