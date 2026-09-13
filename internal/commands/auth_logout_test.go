package commands

import (
	"bytes"
	"context"
	"encoding/json"
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

// revocationServer is a BC5 authorization server reduced to what a logout
// touches: RFC 8414 metadata naming an RFC 7009 revocation endpoint, which
// records every form it is sent.
type revocationServer struct {
	srv *httptest.Server

	mu    sync.Mutex
	forms []url.Values
	// metadataStatus overrides the metadata response status when non-zero.
	metadataStatus int
}

func startRevocationServer(t *testing.T) *revocationServer {
	t.Helper()
	s := &revocationServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		if s.metadataStatus != 0 {
			w.WriteHeader(s.metadataStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"issuer": %q, "token_endpoint": %q, "revocation_endpoint": %q}`,
			s.srv.URL, s.srv.URL+"/oauth/tokens", s.srv.URL+"/oauth/revocations")
	})
	mux.HandleFunc("/oauth/revocations", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		s.mu.Lock()
		s.forms = append(s.forms, r.PostForm)
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *revocationServer) revoked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	tokens := make([]string, 0, len(s.forms))
	for _, f := range s.forms {
		tokens = append(tokens, f.Get("token"))
	}
	return tokens
}

// newLogoutTestApp wires an app whose credential store holds creds (when
// non-nil) for the base URL s serves, rendering in the given format.
func newLogoutTestApp(t *testing.T, s *revocationServer, format output.Format, creds *auth.Credentials) (*appctx.App, *bytes.Buffer) {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("BASECAMP_TOKEN", "")
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg := &config.Config{BaseURL: s.srv.URL, Sources: map[string]string{}}
	authMgr := auth.NewManager(cfg, s.srv.Client())
	authMgr.SetStore(auth.NewStore(tmpDir))
	if creds != nil {
		require.NoError(t, authMgr.GetStore().Save(authMgr.CredentialKey(), creds))
	}

	buf := &bytes.Buffer{}
	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		Output: output.New(output.Options{Format: format, Writer: buf}),
		Flags:  appctx.GlobalFlags{JSON: format == output.FormatJSON},
	}
	return app, buf
}

func runLogout(t *testing.T, app *appctx.App) error {
	t.Helper()
	return runAuthSubcommand(t, app, "logout")
}

func runAuthSubcommand(t *testing.T, app *appctx.App, sub string) error {
	t.Helper()
	cmd := NewAuthCmd()
	cmd.SetArgs([]string{sub})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	return cmd.Execute()
}

func bc5LogoutCredentials(s *revocationServer) *auth.Credentials {
	return &auth.Credentials{
		AccessToken:   "at-1",
		RefreshToken:  "rt-1",
		OAuthType:     "bc5",
		TokenEndpoint: s.srv.URL + "/oauth/tokens",
		Issuer:        s.srv.URL,
		Scope:         "full",
	}
}

func decodeLogoutJSON(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope), buf.String())
	return envelope.Data
}

func TestAuthLogoutRevokesThenForgets(t *testing.T) {
	s := startRevocationServer(t)
	app, buf := newLogoutTestApp(t, s, output.FormatJSON, bc5LogoutCredentials(s))

	require.NoError(t, runLogout(t, app))
	assert.Equal(t, []string{"rt-1", "at-1"}, s.revoked(), "refresh token first, then the access token")
	data := decodeLogoutJSON(t, buf)
	assert.Equal(t, "logged_out", data["status"])
	assert.Equal(t, true, data["revoked"])
	assert.NotContains(t, data, "reason")
	assert.False(t, app.Auth.IsAuthenticated())
}

func TestAuthLogoutHumanCopy(t *testing.T) {
	t.Run("revoked", func(t *testing.T) {
		s := startRevocationServer(t)
		app, buf := newLogoutTestApp(t, s, output.FormatStyled, bc5LogoutCredentials(s))
		require.NoError(t, runLogout(t, app))
		assert.Contains(t, buf.String(), "Logged out (token revoked)")
	})

	t.Run("revocation failed", func(t *testing.T) {
		s := startRevocationServer(t)
		s.metadataStatus = http.StatusNotFound
		app, buf := newLogoutTestApp(t, s, output.FormatStyled, bc5LogoutCredentials(s))
		require.NoError(t, runLogout(t, app), "a failed revocation is not a failed logout")
		assert.Contains(t, buf.String(), "Logged out locally; could not revoke the token server-side: authorization server metadata returned HTTP 404 — it expires within the hour")
		assert.Empty(t, s.revoked())
		assert.False(t, app.Auth.IsAuthenticated(), "the local copy goes regardless")
	})

	t.Run("launchpad", func(t *testing.T) {
		s := startRevocationServer(t)
		creds := &auth.Credentials{AccessToken: "lp-at", RefreshToken: "lp-rt", OAuthType: "launchpad", TokenEndpoint: "https://launchpad.37signals.com/authorization/token"}
		app, buf := newLogoutTestApp(t, s, output.FormatStyled, creds)
		require.NoError(t, runLogout(t, app))
		assert.Contains(t, buf.String(), "Logged out (Launchpad tokens cannot be revoked from the CLI)")
		assert.Empty(t, s.revoked())
		assert.False(t, app.Auth.IsAuthenticated())
	})

	t.Run("imported token", func(t *testing.T) {
		s := startRevocationServer(t)
		creds := &auth.Credentials{AccessToken: "pat-1", OAuthType: "bc5", Scope: "full", Source: auth.CredentialSourceToken}
		app, buf := newLogoutTestApp(t, s, output.FormatStyled, creds)
		require.NoError(t, runLogout(t, app))
		assert.Contains(t, buf.String(), "Logged out (forgot the imported token; it stays valid until revoked in Basecamp)")
		assert.Empty(t, s.revoked(), "an imported token is the operator's, not the CLI's, to revoke")
		assert.False(t, app.Auth.IsAuthenticated())
	})

	t.Run("not logged in", func(t *testing.T) {
		s := startRevocationServer(t)
		app, buf := newLogoutTestApp(t, s, output.FormatStyled, nil)
		require.NoError(t, runLogout(t, app), "nothing to log out of is not an error")
		assert.Contains(t, buf.String(), "Not logged in")
		assert.Empty(t, s.revoked())
	})
}

func TestAuthRevokeRevokesThenForgets(t *testing.T) {
	s := startRevocationServer(t)
	app, buf := newLogoutTestApp(t, s, output.FormatJSON, bc5LogoutCredentials(s))

	require.NoError(t, runAuthSubcommand(t, app, "revoke"))
	assert.Equal(t, []string{"rt-1", "at-1"}, s.revoked())
	data := decodeLogoutJSON(t, buf)
	assert.Equal(t, "revoked", data["status"])
	assert.Equal(t, true, data["revoked"])
	assert.False(t, app.Auth.IsAuthenticated(), "a revoked credential is removed")

	buf.Reset()
	require.NoError(t, runAuthSubcommand(t, app, "revoke"), "nothing stored is not an error")
	assert.Equal(t, "not_logged_in", decodeLogoutJSON(t, buf)["status"])
}

func TestAuthRevokeHumanCopy(t *testing.T) {
	s := startRevocationServer(t)
	app, buf := newLogoutTestApp(t, s, output.FormatStyled, bc5LogoutCredentials(s))

	require.NoError(t, runAuthSubcommand(t, app, "revoke"))
	assert.Contains(t, buf.String(), "Revoked the token with the server and removed the credential")
}

// Where logout forgets regardless, revoke keeps a credential it could not
// revoke: the operator asked for the token to be dead, and forgetting it
// would make that impossible from here.
func TestAuthRevokeKeepsTheCredentialWhenItCannotRevoke(t *testing.T) {
	s := startRevocationServer(t)
	s.metadataStatus = http.StatusNotFound
	app, _ := newLogoutTestApp(t, s, output.FormatJSON, bc5LogoutCredentials(s))

	err := runAuthSubcommand(t, app, "revoke")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not revoke the token server-side: authorization server metadata returned HTTP 404")
	assert.Contains(t, output.AsError(err).Hint, "basecamp auth revoke")
	assert.Empty(t, s.revoked())
	assert.True(t, app.Auth.IsAuthenticated(), "the credential stays for a retry")
}

func TestAuthRevokeRefusesWhatItCannotRevoke(t *testing.T) {
	t.Run("launchpad", func(t *testing.T) {
		s := startRevocationServer(t)
		creds := &auth.Credentials{AccessToken: "lp-at", RefreshToken: "lp-rt", OAuthType: "launchpad"}
		app, _ := newLogoutTestApp(t, s, output.FormatJSON, creds)
		err := runAuthSubcommand(t, app, "revoke")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Launchpad tokens cannot be revoked from the CLI")
		assert.Contains(t, output.AsError(err).Hint, "basecamp auth logout")
		assert.True(t, app.Auth.IsAuthenticated())
	})

	t.Run("imported token", func(t *testing.T) {
		s := startRevocationServer(t)
		creds := &auth.Credentials{AccessToken: "pat-1", OAuthType: "bc5", Scope: "full", Source: auth.CredentialSourceToken}
		app, _ := newLogoutTestApp(t, s, output.FormatJSON, creds)
		err := runAuthSubcommand(t, app, "revoke")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "revoke it in Basecamp")
		assert.Empty(t, s.revoked())
		assert.True(t, app.Auth.IsAuthenticated())
	})
}

func TestAuthLogoutJSONCarriesTheReason(t *testing.T) {
	t.Run("revocation failed", func(t *testing.T) {
		s := startRevocationServer(t)
		s.metadataStatus = http.StatusNotFound
		app, buf := newLogoutTestApp(t, s, output.FormatJSON, bc5LogoutCredentials(s))
		require.NoError(t, runLogout(t, app))
		data := decodeLogoutJSON(t, buf)
		assert.Equal(t, "logged_out", data["status"])
		assert.Equal(t, false, data["revoked"])
		assert.Equal(t, "authorization server metadata returned HTTP 404", data["reason"])
	})

	t.Run("imported token", func(t *testing.T) {
		s := startRevocationServer(t)
		creds := &auth.Credentials{AccessToken: "pat-1", OAuthType: "bc5", Scope: "full", Source: auth.CredentialSourceToken}
		app, buf := newLogoutTestApp(t, s, output.FormatJSON, creds)
		require.NoError(t, runLogout(t, app))
		data := decodeLogoutJSON(t, buf)
		assert.Equal(t, "logged_out", data["status"])
		assert.Equal(t, false, data["revoked"])
		assert.Equal(t, "imported_token", data["reason"])
	})

	t.Run("not logged in", func(t *testing.T) {
		s := startRevocationServer(t)
		app, buf := newLogoutTestApp(t, s, output.FormatJSON, nil)
		require.NoError(t, runLogout(t, app))
		data := decodeLogoutJSON(t, buf)
		assert.Equal(t, "not_logged_in", data["status"])
		assert.Equal(t, false, data["revoked"])
	})
}
