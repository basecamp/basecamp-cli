package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// statusEnvelope is the JSON envelope `auth status` writes.
type statusEnvelope struct {
	OK          bool           `json:"ok"`
	Data        map[string]any `json:"data"`
	Summary     string         `json:"summary"`
	Notice      string         `json:"notice"`
	Breadcrumbs []struct {
		Action string `json:"action"`
		Cmd    string `json:"cmd"`
	} `json:"breadcrumbs"`
}

// runAuthStatus executes `auth status` on app and returns the parsed JSON
// envelope. Hints stay off: the login remedy must not depend on them.
func runAuthStatus(t *testing.T, app *appctx.App, buf *bytes.Buffer) statusEnvelope {
	t.Helper()
	app.Flags.Hints = false
	require.NoError(t, executeProfileCommand(newAuthStatusCmd(), app))
	var envelope statusEnvelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope), buf.String())
	return envelope
}

func statusTestConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		BaseURL:   "https://3.basecampapi.com/",
		AccountID: "999",
		CacheDir:  t.TempDir(),
		Sources:   map[string]string{},
	}
}

func TestAuthStatusReportsTheWholeCredential(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "")
	app, buf := setupProfileTestApp(t, statusTestConfig(t))
	expiresAt := time.Now().Add(42 * time.Minute).Truncate(time.Second)
	require.NoError(t, app.Auth.GetStore().Save("https://3.basecampapi.com", &auth.Credentials{
		AccessToken:   "tok",
		RefreshToken:  "ref",
		OAuthType:     "bc5",
		TokenEndpoint: "https://3.basecamp.com/oauth/tokens",
		Scope:         "full",
		UserID:        "12345",
		UserEmail:     "jeremy@example.com",
		Resource:      "urn:bc:account:999",
		ExpiresAt:     expiresAt.Unix(),
	}))

	envelope := runAuthStatus(t, app, buf)

	assert.True(t, envelope.OK)
	assert.Equal(t, "Logged in to https://3.basecampapi.com as jeremy@example.com (user 12345)", envelope.Summary)
	assert.Equal(t, true, envelope.Data["authenticated"])
	assert.Equal(t, "https://3.basecampapi.com", envelope.Data["base_url"])
	assert.Equal(t, "999", envelope.Data["account_id"])
	assert.Equal(t, "jeremy@example.com", envelope.Data["user_email"])
	assert.Equal(t, "12345", envelope.Data["user_id"])
	assert.Equal(t, "oauth", envelope.Data["source"])
	assert.Equal(t, "bc5", envelope.Data["oauth_type"])
	assert.Equal(t, "full", envelope.Data["scope"])
	assert.Equal(t, expiresAt.UTC().Format(time.RFC3339), envelope.Data["expires_at"])
	assert.Equal(t, false, envelope.Data["expired"])
	assert.Equal(t, true, envelope.Data["refreshable"])
	assert.Equal(t, "file", envelope.Data["storage"], "BASECAMP_NO_KEYRING puts the credential in a file")
	assert.NotContains(t, envelope.Data, "profile")
	assert.Empty(t, envelope.Notice, "a live credential needs no remedy")

	report, err := authStatusReport(app)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"Account: 999 · Access: full · Source: oauth (bc5)",
		"Token: expires in 41m, refreshes automatically · Storage: file",
	}, report.details)
	assert.Empty(t, report.hint)
}

func TestAuthStatusExpiredButRefreshable(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "")
	cfg := statusTestConfig(t)
	cfg.ActiveProfile = "work"
	app, buf := setupProfileTestApp(t, cfg)
	require.NoError(t, app.Auth.GetStore().Save("profile:work", &auth.Credentials{
		AccessToken:  "tok",
		RefreshToken: "ref",
		OAuthType:    "launchpad",
		Scope:        "read",
		ExpiresAt:    time.Now().Add(-time.Hour).Unix(),
	}))

	envelope := runAuthStatus(t, app, buf)

	assert.Equal(t, "Logged in to https://3.basecampapi.com", envelope.Summary)
	assert.Equal(t, true, envelope.Data["expired"])
	assert.Equal(t, true, envelope.Data["refreshable"])
	assert.Equal(t, "work", envelope.Data["profile"])
	assert.NotContains(t, envelope.Data, "scope", "Launchpad has no scopes")
	assert.Empty(t, envelope.Notice, "a refreshable token renews itself on the next command")

	report, err := authStatusReport(app)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"Profile: work · Account: 999 · Source: oauth (launchpad)",
		"Token: expired, will refresh on next use · Storage: file",
	}, report.details)
	assert.Empty(t, report.hint)
}

func TestAuthStatusExpiredImportedTokenNamesTheLogin(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "")
	cfg := statusTestConfig(t)
	cfg.ActiveProfile = "bot"
	app, buf := setupProfileTestApp(t, cfg)
	require.NoError(t, app.Auth.GetStore().Save("profile:bot", &auth.Credentials{
		AccessToken: "tok",
		OAuthType:   "bc5",
		Scope:       "full",
		Source:      auth.CredentialSourceToken,
		UserEmail:   "bot@example.com",
		ExpiresAt:   time.Now().Add(-time.Minute).Unix(),
	}))

	envelope := runAuthStatus(t, app, buf)

	assert.Equal(t, "token", envelope.Data["source"])
	assert.Equal(t, true, envelope.Data["expired"])
	assert.Equal(t, false, envelope.Data["refreshable"])
	assert.Equal(t, "Run: basecamp auth login -P bot", envelope.Notice)

	report, err := authStatusReport(app)
	require.NoError(t, err)
	assert.Equal(t, "Token: expired · Storage: file", report.details[1])
	assert.Equal(t, "Run: basecamp auth login -P bot", report.hint)
}

func TestAuthStatusNotLoggedIn(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "")
	cfg := statusTestConfig(t)
	cfg.ActiveProfile = "work"
	app, buf := setupProfileTestApp(t, cfg)

	envelope := runAuthStatus(t, app, buf)

	assert.True(t, envelope.OK, "scripts rely on exit 0 and authenticated:false")
	assert.Equal(t, false, envelope.Data["authenticated"])
	assert.Equal(t, "https://3.basecampapi.com", envelope.Data["base_url"])
	assert.Equal(t, "work", envelope.Data["profile"])
	assert.Equal(t, "Not logged in to https://3.basecampapi.com", envelope.Summary)
	assert.Equal(t, "Run: basecamp auth login -P work", envelope.Notice, "the remedy does not depend on --hints")
	assert.Empty(t, envelope.Breadcrumbs)
}

func TestAuthStatusEnvToken(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "bc_at_env")
	app, buf := setupProfileTestApp(t, statusTestConfig(t))

	envelope := runAuthStatus(t, app, buf)

	assert.Equal(t, true, envelope.Data["authenticated"])
	assert.Equal(t, "BASECAMP_TOKEN", envelope.Data["source"])
	assert.Equal(t, "env", envelope.Data["storage"])
	assert.Equal(t, false, envelope.Data["refreshable"])
	assert.Equal(t, "999", envelope.Data["account_id"])
	assert.Equal(t, "Logged in to https://3.basecampapi.com via BASECAMP_TOKEN", envelope.Summary)
	assert.NotContains(t, buf.String(), "bc_at_env", "the token itself is never printed")
}

// TestAuthStatusHumanOutput: a terminal gets the prose, not the envelope —
// the headline, the detail lines, and the remedy when there is one.
func TestAuthStatusHumanOutput(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "")
	cfg := statusTestConfig(t)
	cfg.ActiveProfile = "bot"
	app, _ := setupProfileTestApp(t, cfg)
	app.Output = output.New(output.Options{Format: output.FormatStyled, Writer: &bytes.Buffer{}})

	runHuman := func() string {
		cmd := newAuthStatusCmd()
		cmd.SetContext(appctx.WithApp(context.Background(), app))
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		require.NoError(t, cmd.Execute())
		return out.String()
	}

	assert.Equal(t, "Not logged in to https://3.basecampapi.com\n  Run: basecamp auth login -P bot\n", runHuman())

	require.NoError(t, app.Auth.GetStore().Save("profile:bot", &auth.Credentials{
		AccessToken:  "tok",
		RefreshToken: "ref",
		OAuthType:    "bc5",
		Scope:        "full",
		UserID:       "12345",
		UserEmail:    "jeremy@example.com",
		ExpiresAt:    time.Now().Add(3*time.Hour + 5*time.Minute).Unix(),
	}))
	assert.Equal(t, "Logged in to https://3.basecampapi.com as jeremy@example.com (user 12345)\n"+
		"  Profile: bot · Account: 999 · Access: full · Source: oauth (bc5)\n"+
		"  Token: expires in 3h 4m, refreshes automatically · Storage: file\n", runHuman())
}

func TestCoarseDuration(t *testing.T) {
	assert.Equal(t, "42s", coarseDuration(42*time.Second))
	assert.Equal(t, "42m", coarseDuration(42*time.Minute+30*time.Second))
	assert.Equal(t, "2h", coarseDuration(2*time.Hour))
	assert.Equal(t, "2h 5m", coarseDuration(2*time.Hour+5*time.Minute))
	assert.Equal(t, "3d", coarseDuration(80*time.Hour))
}

// checkedStatus runs `auth status --check` against the login identity
// server with the given stored access token and returns the envelope.
func checkedStatus(t *testing.T, storedToken string, authorizationStatus int) (statusEnvelope, error) {
	t.Helper()
	srv := startLoginIdentityServer(t, "live-tok")
	srv.authorizationStatus = authorizationStatus
	app, buf := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	require.NoError(t, app.Auth.GetStore().Save("profile:bot", &auth.Credentials{
		AccessToken: storedToken,
		OAuthType:   "bc5",
		Scope:       "full",
		Source:      auth.CredentialSourceToken,
		ExpiresAt:   time.Now().Add(time.Hour).Unix(),
	}))

	cmd := newAuthStatusCmd()
	cmd.SetArgs([]string{"--check"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	if err := cmd.Execute(); err != nil {
		return statusEnvelope{}, err
	}
	var envelope statusEnvelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope), buf.String())
	return envelope, nil
}

// TestAuthStatusCheckAcceptedToken: --check makes the authorization lookup
// with the stored token and reports the server's acceptance.
func TestAuthStatusCheckAcceptedToken(t *testing.T) {
	envelope, err := checkedStatus(t, "live-tok", 0)
	require.NoError(t, err)
	assert.Equal(t, true, envelope.Data["authenticated"])
	assert.Equal(t, true, envelope.Data["valid"])
	assert.Empty(t, envelope.Notice)
}

// TestAuthStatusCheckRejectedToken: a 401 is the "rejected" verdict, still
// exit 0, with the login named as the remedy.
func TestAuthStatusCheckRejectedToken(t *testing.T) {
	envelope, err := checkedStatus(t, "stale-tok", 0)
	require.NoError(t, err)
	assert.Equal(t, true, envelope.Data["authenticated"], "the credential is still stored")
	assert.Equal(t, false, envelope.Data["valid"])
	assert.Equal(t, "Run: basecamp auth login -P bot", envelope.Notice)
}

// TestAuthStatusCheckServerFault: a fault that is not a verdict on the token
// is returned as itself rather than reported as valid or rejected.
func TestAuthStatusCheckServerFault(t *testing.T) {
	_, err := checkedStatus(t, "live-tok", 503)
	require.Error(t, err)
	assert.NotEqual(t, output.CodeAuth, output.AsError(err).Code)
}

// TestAuthStatusWithoutCheckMakesNoRequest: the default is offline.
func TestAuthStatusWithoutCheckMakesNoRequest(t *testing.T) {
	srv := startLoginIdentityServer(t, "live-tok")
	app, buf := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	require.NoError(t, app.Auth.GetStore().Save("profile:bot", &auth.Credentials{
		AccessToken: "live-tok", OAuthType: "bc5", Scope: "full", ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}))

	envelope := runAuthStatus(t, app, buf)
	assert.Equal(t, true, envelope.Data["authenticated"])
	assert.NotContains(t, envelope.Data, "valid")
	assert.Empty(t, srv.seenPaths(), "no request without --check")
}

// TestAuthStatusCheckRejectedEnvToken: a login cannot replace what
// BASECAMP_TOKEN sends, so the remedy names the variable.
func TestAuthStatusCheckRejectedEnvToken(t *testing.T) {
	srv := startLoginIdentityServer(t, "bc_at_live")
	app, buf := loginTestApp(t, srv, &config.Config{})
	t.Setenv("BASECAMP_TOKEN", "bc_at_stale")

	cmd := newAuthStatusCmd()
	cmd.SetArgs([]string{"--check"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	require.NoError(t, cmd.Execute())

	var envelope statusEnvelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope), buf.String())
	assert.Equal(t, "BASECAMP_TOKEN", envelope.Data["source"])
	assert.Equal(t, false, envelope.Data["valid"])
	assert.Contains(t, envelope.Notice, "BASECAMP_TOKEN is set")
	assert.NotContains(t, envelope.Notice, "auth login")
}

// TestAuthStatusCheckReportsTheRefreshedCredential: the check's request
// refreshes an expired credential first, and the report describes the
// credential as it is stored afterwards, not the one the command started
// with.
func TestAuthStatusCheckReportsTheRefreshedCredential(t *testing.T) {
	srv := startLoginIdentityServer(t, "dev-tok")
	srv.srv.Config.Handler = deviceGrantThen(t, srv.srv.Config.Handler)
	app, buf := loginTestApp(t, srv, &config.Config{ActiveProfile: "bot"})
	require.NoError(t, app.Auth.GetStore().Save("profile:bot", &auth.Credentials{
		AccessToken:   "stale-tok",
		RefreshToken:  "stale-ref",
		OAuthType:     "bc5",
		TokenEndpoint: srv.srv.URL + "/oauth/tokens",
		Scope:         "full",
		ExpiresAt:     time.Now().Add(-time.Hour).Unix(),
	}))

	cmd := newAuthStatusCmd()
	cmd.SetArgs([]string{"--check"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	require.NoError(t, cmd.Execute())

	var envelope statusEnvelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope), buf.String())
	assert.Equal(t, true, envelope.Data["valid"])
	assert.Equal(t, false, envelope.Data["expired"], "the report is built after the refresh")
	assert.Empty(t, envelope.Notice)
	creds, err := app.Auth.GetStore().Load("profile:bot")
	require.NoError(t, err)
	assert.Equal(t, "dev-tok", creds.AccessToken)
}

// TestAuthStatusNonRefreshableTokenInsideTheRefreshWindow: a token with
// nothing to refresh with is refused by every command once it is inside
// the refresh window, so status calls it expired there too.
func TestAuthStatusNonRefreshableTokenInsideTheRefreshWindow(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "")
	cfg := statusTestConfig(t)
	cfg.ActiveProfile = "bot"
	app, buf := setupProfileTestApp(t, cfg)
	require.NoError(t, app.Auth.GetStore().Save("profile:bot", &auth.Credentials{
		AccessToken: "tok",
		OAuthType:   "bc5",
		Scope:       "full",
		Source:      auth.CredentialSourceToken,
		ExpiresAt:   time.Now().Add(2 * time.Minute).Unix(),
	}))

	envelope := runAuthStatus(t, app, buf)
	assert.Equal(t, true, envelope.Data["expired"])
	assert.Equal(t, "Run: basecamp auth login -P bot", envelope.Notice)

	report, err := authStatusReport(app)
	require.NoError(t, err)
	assert.Contains(t, report.details[1], "inside the 5m the CLI keeps clear of expiry")
}

// TestAuthStatusHumanOutputSanitizesValues: config and stored values reach
// the terminal only as single, control-free lines.
func TestAuthStatusHumanOutputSanitizesValues(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "")
	cfg := statusTestConfig(t)
	cfg.AccountID = "999\x1b[31m\nfake"
	app, _ := setupProfileTestApp(t, cfg)
	app.Output = output.New(output.Options{Format: output.FormatStyled, Writer: &bytes.Buffer{}})
	require.NoError(t, app.Auth.GetStore().Save("https://3.basecampapi.com", &auth.Credentials{
		AccessToken: "tok", OAuthType: "bc5", Scope: "full", UserEmail: "a@example.com\x1b[0m",
	}))

	cmd := newAuthStatusCmd()
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())
	assert.NotContains(t, out.String(), "\x1b")
	assert.Equal(t, 3, strings.Count(out.String(), "\n"), "no injected line breaks")
}
