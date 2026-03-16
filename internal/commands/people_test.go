package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/names"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// noNetworkTransport is an http.RoundTripper that fails immediately.
// Used in tests to prevent real network calls without waiting for timeouts.
type peopleNoNetworkTransport struct{}

func (peopleNoNetworkTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network disabled in tests")
}

// peopleTestTokenProvider is a mock token provider for tests.
type peopleTestTokenProvider struct{}

func (t *peopleTestTokenProvider) AccessToken(_ context.Context) (string, error) {
	return "test-token", nil
}

// setupPeopleTestApp creates a minimal test app context for people tests.
// By default, sets up an unauthenticated state (no credentials stored).
func setupPeopleTestApp(t *testing.T) (*appctx.App, *bytes.Buffer) {
	t.Helper()

	// Disable keyring access during tests
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
	}

	// Create auth manager without any stored credentials
	authMgr := auth.NewManager(cfg, nil)

	sdkCfg := &basecamp.Config{}
	sdkClient := basecamp.NewClient(sdkCfg, &peopleTestTokenProvider{},
		basecamp.WithTransport(peopleNoNetworkTransport{}),
		basecamp.WithMaxRetries(0), // Disable retries for instant failure
	)
	nameResolver := names.NewResolver(sdkClient, authMgr, cfg.AccountID)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
		Flags: appctx.GlobalFlags{Hints: true},
	}
	return app, buf
}

// executePeopleCommand executes a cobra command with the given args.
func executePeopleCommand(cmd *cobra.Command, app *appctx.App, args ...string) error {
	cmd.SetArgs(args)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)

	// Suppress output during tests
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	return cmd.Execute()
}

// TestMeRequiresAuth tests that basecamp me returns auth error when not authenticated.
func TestMeRequiresAuth(t *testing.T) {
	app, _ := setupPeopleTestApp(t)

	// Ensure no authentication - no env token, no stored credentials
	t.Setenv("BASECAMP_TOKEN", "")

	cmd := NewMeCmd()

	err := executePeopleCommand(cmd, app)
	require.Error(t, err)

	// Should be auth required error
	var e *output.Error
	if assert.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err) {
		assert.Equal(t, output.CodeAuth, e.Code)
		assert.Contains(t, e.Message, "Not authenticated", "expected 'Not authenticated', got %q", e.Message)
	}
}

// setupAuthenticatedTestApp creates a test app with credentials stored for Launchpad OAuth.
// It also starts a mock Launchpad server (cleaned up via t.Cleanup) and returns the test app and its output buffer.
func setupAuthenticatedTestApp(t *testing.T, accountID string, launchpadResponse *basecamp.AuthorizationInfo) (*appctx.App, *bytes.Buffer) {
	t.Helper()

	// Start mock Launchpad server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Expect requests to /authorization.json
		assert.Equal(t, "/authorization.json", r.URL.Path, "unexpected path")
		if r.URL.Path != "/authorization.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(launchpadResponse)
	}))
	t.Cleanup(server.Close)

	// Override Launchpad URL to use mock server (base URL, not full path)
	t.Setenv("BASECAMP_LAUNCHPAD_URL", server.URL)

	// Disable keyring access during tests
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	// Create temp directory for credentials
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	// Create credentials directory and file
	credsDir := filepath.Join(tmpDir, "basecamp")
	require.NoError(t, os.MkdirAll(credsDir, 0700), "failed to create creds dir")

	// Write mock credentials to file
	origin := "https://3.basecampapi.com"
	creds := map[string]any{
		origin: map[string]any{
			"access_token":   "test-token",
			"refresh_token":  "test-refresh",
			"expires_at":     9999999999,
			"oauth_type":     "launchpad",
			"token_endpoint": "https://launchpad.37signals.com/authorization/token",
		},
	}
	credsData, _ := json.Marshal(creds)
	credsPath := filepath.Join(credsDir, "credentials.json")
	require.NoError(t, os.WriteFile(credsPath, credsData, 0600), "failed to write creds")

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: accountID,
		BaseURL:   "https://3.basecampapi.com",
	}

	// Create auth manager
	authMgr := auth.NewManager(cfg, nil)

	sdkCfg := &basecamp.Config{}
	// Use default transport to allow HTTP requests to the mock server
	sdkClient := basecamp.NewClient(sdkCfg, &peopleTestTokenProvider{},
		basecamp.WithMaxRetries(0),
	)
	nameResolver := names.NewResolver(sdkClient, authMgr, cfg.AccountID)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
		Flags: appctx.GlobalFlags{Hints: true},
	}
	return app, buf
}

// TestMeWithLaunchpadNoAccountConfigured tests that basecamp me works via Launchpad
// even when no account is configured, showing available accounts with setup breadcrumbs.
func TestMeWithLaunchpadNoAccountConfigured(t *testing.T) {
	launchpadResponse := &basecamp.AuthorizationInfo{
		Identity: basecamp.Identity{
			ID:           12345,
			FirstName:    "Test",
			LastName:     "User",
			EmailAddress: "test@example.com",
		},
		Accounts: []basecamp.AuthorizedAccount{
			{Product: "bc3", ID: 111, Name: "Acme Corp", HREF: "https://3.basecampapi.com/111", AppHREF: "https://3.basecamp.com/111"},
			{Product: "bc3", ID: 222, Name: "Test Inc", HREF: "https://3.basecampapi.com/222", AppHREF: "https://3.basecamp.com/222"},
			{Product: "bcx", ID: 333, Name: "Classic Account", HREF: "https://basecamp.com/333", AppHREF: "https://basecamp.com/333"}, // Should be filtered
		},
	}

	// No account configured (empty string)
	app, buf := setupAuthenticatedTestApp(t, "", launchpadResponse)

	cmd := NewMeCmd()
	err := executePeopleCommand(cmd, app)
	require.NoError(t, err)

	// Parse JSON output
	var result struct {
		Data struct {
			Identity struct {
				ID           int64  `json:"id"`
				FirstName    string `json:"first_name"`
				LastName     string `json:"last_name"`
				EmailAddress string `json:"email_address"`
			} `json:"identity"`
			Accounts []struct {
				ID      int64  `json:"id"`
				Name    string `json:"name"`
				Current bool   `json:"current"`
			} `json:"accounts"`
		} `json:"data"`
		Breadcrumbs []struct {
			Action string `json:"action"`
			Cmd    string `json:"cmd"`
		} `json:"breadcrumbs"`
	}

	require.NoError(t, json.Unmarshal(buf.Bytes(), &result), "failed to parse output: %s", buf.String())

	// Verify identity
	assert.Equal(t, int64(12345), result.Data.Identity.ID)
	assert.Equal(t, "test@example.com", result.Data.Identity.EmailAddress)

	// Verify only bc3 accounts are shown (filtered out bcx)
	assert.Equal(t, 2, len(result.Data.Accounts), "expected 2 bc3 accounts")

	// Verify no account is marked as current
	for _, acct := range result.Data.Accounts {
		assert.False(t, acct.Current, "expected no account marked as current, but %d (%s) is marked current", acct.ID, acct.Name)
	}

	// Verify breadcrumbs suggest account setup
	foundSetup := false
	for _, bc := range result.Breadcrumbs {
		if bc.Action == "setup" && strings.Contains(bc.Cmd, "basecamp config set account") {
			foundSetup = true
			break
		}
	}
	assert.True(t, foundSetup, "expected breadcrumbs to suggest account setup, got: %+v", result.Breadcrumbs)
}

// TestMeWithAccountConfigured tests that basecamp me shows the current account marker
// when an account is already configured.
func TestMeWithAccountConfigured(t *testing.T) {
	launchpadResponse := &basecamp.AuthorizationInfo{
		Identity: basecamp.Identity{
			ID:           12345,
			FirstName:    "Test",
			LastName:     "User",
			EmailAddress: "test@example.com",
		},
		Accounts: []basecamp.AuthorizedAccount{
			{Product: "bc3", ID: 111, Name: "Acme Corp", HREF: "https://3.basecampapi.com/111", AppHREF: "https://3.basecamp.com/111"},
			{Product: "bc3", ID: 222, Name: "Test Inc", HREF: "https://3.basecampapi.com/222", AppHREF: "https://3.basecamp.com/222"},
		},
	}

	// Account 222 is configured
	app, buf := setupAuthenticatedTestApp(t, "222", launchpadResponse)

	cmd := NewMeCmd()
	err := executePeopleCommand(cmd, app)
	require.NoError(t, err)

	// Parse JSON output
	var result struct {
		Data struct {
			Accounts []struct {
				ID      int64  `json:"id"`
				Name    string `json:"name"`
				Current bool   `json:"current"`
			} `json:"accounts"`
		} `json:"data"`
		Breadcrumbs []struct {
			Action string `json:"action"`
			Cmd    string `json:"cmd"`
		} `json:"breadcrumbs"`
	}

	require.NoError(t, json.Unmarshal(buf.Bytes(), &result), "failed to parse output: %s", buf.String())

	// Verify account 222 is marked as current
	foundCurrent := false
	for _, acct := range result.Data.Accounts {
		if acct.ID == 222 {
			assert.True(t, acct.Current, "expected account 222 to be marked as current")
			foundCurrent = true
		} else {
			assert.False(t, acct.Current, "expected only account 222 to be marked as current, but %d is also marked", acct.ID)
		}
	}
	assert.True(t, foundCurrent, "account 222 not found in output")

	// Verify breadcrumbs show next steps (not setup)
	foundSetup := false
	foundProjects := false
	for _, bc := range result.Breadcrumbs {
		if bc.Action == "setup" {
			foundSetup = true
		}
		if bc.Action == "projects" {
			foundProjects = true
		}
	}
	assert.False(t, foundSetup, "expected no setup breadcrumb when account is configured")
	assert.True(t, foundProjects, "expected projects breadcrumb when account is configured")
}

// setupBC3TokenTestApp creates a test app that uses BASECAMP_TOKEN with
// a bc_at_ prefix. The mock server is used as BaseURL (BC3 path) rather
// than Launchpad. No stored credentials are written.
func setupBC3TokenTestApp(t *testing.T, accountID string, bc3Response *basecamp.AuthorizationInfo) (*appctx.App, *bytes.Buffer) {
	t.Helper()

	// Start mock BC3 server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/authorization.json", r.URL.Path, "unexpected path")
		if r.URL.Path != "/authorization.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(bc3Response)
	}))
	t.Cleanup(server.Close)

	// BASECAMP_TOKEN with bc_at_ prefix → should route to BC3 URL
	t.Setenv("BASECAMP_TOKEN", "bc_at_test_token_123")
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	// Temp config dir with no stored credentials
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "basecamp"), 0700))

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: accountID,
		BaseURL:   server.URL, // BC3-direct URL
	}

	authMgr := auth.NewManager(cfg, nil)

	sdkCfg := &basecamp.Config{BaseURL: server.URL}
	sdkClient := basecamp.NewClient(sdkCfg, &peopleTestTokenProvider{},
		basecamp.WithMaxRetries(0),
	)
	nameResolver := names.NewResolver(sdkClient, authMgr, cfg.AccountID)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
		Flags: appctx.GlobalFlags{Hints: true},
	}
	return app, buf
}

// TestMeWithBC3Token tests that basecamp me routes to the BC3 authorization
// endpoint when BASECAMP_TOKEN has a bc_at_ prefix and no stored credentials exist.
func TestMeWithBC3Token(t *testing.T) {
	bc3Response := &basecamp.AuthorizationInfo{
		Identity: basecamp.Identity{
			ID:           42,
			FirstName:    "Token",
			LastName:     "User",
			EmailAddress: "token@example.com",
		},
		Accounts: []basecamp.AuthorizedAccount{
			{Product: "bc3", ID: 555, Name: "Token Corp", HREF: "https://3.basecampapi.com/555", AppHREF: "https://3.basecamp.com/555"},
		},
	}

	app, buf := setupBC3TokenTestApp(t, "555", bc3Response)

	cmd := NewMeCmd()
	err := executePeopleCommand(cmd, app)
	require.NoError(t, err)

	var result struct {
		Data struct {
			Identity struct {
				EmailAddress string `json:"email_address"`
			} `json:"identity"`
			Accounts []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"accounts"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &result), "failed to parse output: %s", buf.String())

	assert.Equal(t, "token@example.com", result.Data.Identity.EmailAddress)
	require.Len(t, result.Data.Accounts, 1)
	assert.Equal(t, int64(555), result.Data.Accounts[0].ID)
}

// TestMeWithBC3TokenOverridingStaleLaunchpadCreds is the exact scenario from
// issue #268: BASECAMP_TOKEN=bc_at_... is set, but stale stored credentials
// with oauth_type=launchpad still exist on disk. The endpoint must follow
// the token, not the stored type.
func TestMeWithBC3TokenOverridingStaleLaunchpadCreds(t *testing.T) {
	bc3Response := &basecamp.AuthorizationInfo{
		Identity: basecamp.Identity{
			ID:           99,
			FirstName:    "Mixed",
			LastName:     "State",
			EmailAddress: "mixed@example.com",
		},
		Accounts: []basecamp.AuthorizedAccount{
			{Product: "bc3", ID: 777, Name: "Mixed Corp"},
		},
	}

	// Start mock BC3 server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/authorization.json", r.URL.Path, "unexpected path")
		if r.URL.Path != "/authorization.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(bc3Response)
	}))
	t.Cleanup(server.Close)

	// bc_at_ token → should route to BC3, not Launchpad
	t.Setenv("BASECAMP_TOKEN", "bc_at_mixed_state_token")
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	// Write stale launchpad credentials that would cause the old bug
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	credsDir := filepath.Join(tmpDir, "basecamp")
	require.NoError(t, os.MkdirAll(credsDir, 0700))

	staleCreds := map[string]any{
		server.URL: map[string]any{
			"access_token":   "stale-lp-token",
			"refresh_token":  "stale-refresh",
			"expires_at":     9999999999,
			"oauth_type":     "launchpad",
			"token_endpoint": "https://launchpad.37signals.com/authorization/token",
		},
	}
	credsData, _ := json.Marshal(staleCreds)
	require.NoError(t, os.WriteFile(filepath.Join(credsDir, "credentials.json"), credsData, 0600))

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "777",
		BaseURL:   server.URL,
	}

	authMgr := auth.NewManager(cfg, nil)

	sdkCfg := &basecamp.Config{BaseURL: server.URL}
	sdkClient := basecamp.NewClient(sdkCfg, &peopleTestTokenProvider{},
		basecamp.WithMaxRetries(0),
	)
	nameResolver := names.NewResolver(sdkClient, authMgr, cfg.AccountID)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
		Flags: appctx.GlobalFlags{Hints: true},
	}

	cmd := NewMeCmd()
	err := executePeopleCommand(cmd, app)
	require.NoError(t, err, "basecamp me should succeed despite stale launchpad creds")

	var result struct {
		Data struct {
			Identity struct {
				EmailAddress string `json:"email_address"`
			} `json:"identity"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &result), "failed to parse output: %s", buf.String())
	assert.Equal(t, "mixed@example.com", result.Data.Identity.EmailAddress)
}

// setupPeopleMockServer creates a mock server that routes people endpoints.
// It serves distinct payloads for account-wide vs project-scoped list,
// and handles the UpdateProjectAccess (grant/revoke) endpoint.
func setupPeopleMockServer(t *testing.T, accountID string, projectID int64) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		projectsPath := fmt.Sprintf("/%s/projects.json", accountID)
		projectPeoplePath := fmt.Sprintf("/%s/projects/%d/people.json", accountID, projectID)
		accountPeoplePath := fmt.Sprintf("/%s/people.json", accountID)
		accessPath := fmt.Sprintf("/%s/projects/%d/people/users.json", accountID, projectID)

		switch {
		case r.URL.Path == projectsPath && r.Method == http.MethodGet:
			// Projects list — used by name resolver
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": projectID, "name": "Test Project"},
			})
		case r.URL.Path == accountPeoplePath && r.Method == http.MethodGet:
			// Account-wide people list — also used by name resolver for person IDs
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1001, "name": "Alice Test", "email_address": "alice@example.com"},
				{"id": 2001, "name": "Account Bob", "title": "PM", "employee": true, "admin": true, "email_address": "bob@example.com"},
				{"id": 2002, "name": "Account Carol", "title": "Design", "employee": true, "admin": false, "email_address": "carol@example.com"},
			})
		case r.URL.Path == projectPeoplePath && r.Method == http.MethodGet:
			// Project-scoped people list — return a distinct set
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1001, "name": "Project Alice", "title": "Dev", "employee": true, "admin": false, "email_address": "alice@example.com"},
			})
		case r.URL.Path == accessPath && r.Method == http.MethodPut:
			// UpdateProjectAccess — echo back granted/revoked
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, fmt.Sprintf("bad request body: %v", err), http.StatusBadRequest)
				return
			}
			resp := map[string]any{"granted": []any{}, "revoked": []any{}}
			if ids, ok := req["grant"].([]any); ok {
				for _, id := range ids {
					resp["granted"] = append(resp["granted"].([]any), map[string]any{
						"id": id, "name": fmt.Sprintf("Person %v", id),
					})
				}
			}
			if ids, ok := req["revoke"].([]any); ok {
				for _, id := range ids {
					resp["revoked"] = append(resp["revoked"].([]any), map[string]any{
						"id": id, "name": fmt.Sprintf("Person %v", id),
					})
				}
			}
			json.NewEncoder(w).Encode(resp)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// setupPeopleMockApp creates a test app wired to the mock people server.
func setupPeopleMockApp(t *testing.T, server *httptest.Server) (*appctx.App, *bytes.Buffer) {
	t.Helper()

	t.Setenv("BASECAMP_NO_KEYRING", "1")

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
		CacheDir:  t.TempDir(),
	}

	sdkClient := basecamp.NewClient(
		&basecamp.Config{BaseURL: server.URL},
		&peopleTestTokenProvider{},
	)

	app := &appctx.App{
		Config: cfg,
		SDK:    sdkClient,
		Names:  names.NewResolver(sdkClient, nil, "99999"),
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
		Flags: appctx.GlobalFlags{Hints: true},
	}
	return app, buf
}

// TestPeopleListIn verifies that --in takes the project-scoped path and
// returns project-specific people, not the account-wide list.
func TestPeopleListIn(t *testing.T) {
	server := setupPeopleMockServer(t, "99999", 55555)
	app, buf := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "list", "--in", "55555")
	require.NoError(t, err)

	var result struct {
		Data []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &result), "output: %s", buf.String())

	// Should return the project-scoped person (Alice), not the account-wide set
	require.Len(t, result.Data, 1)
	assert.Equal(t, int64(1001), result.Data[0].ID)
	assert.Equal(t, "Project Alice", result.Data[0].Name)
}

// TestPeopleListWithoutIn returns account-wide list as a control.
func TestPeopleListWithoutIn(t *testing.T) {
	server := setupPeopleMockServer(t, "99999", 55555)
	app, buf := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "list")
	require.NoError(t, err)

	var result struct {
		Data []struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &result), "output: %s", buf.String())

	// Should return the account-wide people (Alice + Bob + Carol)
	assert.Len(t, result.Data, 3)
}

// TestPeopleAddIn verifies that --in is accepted and routed to the
// correct project access endpoint (grant succeeds).
func TestPeopleAddIn(t *testing.T) {
	server := setupPeopleMockServer(t, "99999", 55555)
	app, buf := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "add", "--in", "55555", "1001")
	require.NoError(t, err)

	var result struct {
		Data struct {
			Granted []struct {
				ID int64 `json:"id"`
			} `json:"granted"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &result), "output: %s", buf.String())
	require.Len(t, result.Data.Granted, 1)
	assert.Equal(t, int64(1001), result.Data.Granted[0].ID)
}

// TestPeopleRemoveIn verifies that --in is accepted and routed to the
// correct project access endpoint (revoke succeeds).
func TestPeopleRemoveIn(t *testing.T) {
	server := setupPeopleMockServer(t, "99999", 55555)
	app, buf := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "remove", "--in", "55555", "1001")
	require.NoError(t, err)

	var result struct {
		Data struct {
			Revoked []struct {
				ID int64 `json:"id"`
			} `json:"revoked"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &result), "output: %s", buf.String())
	require.Len(t, result.Data.Revoked, 1)
	assert.Equal(t, int64(1001), result.Data.Revoked[0].ID)
}

// TestPeopleAddNoProject verifies that omitting --project/--in returns a usage error.
func TestPeopleAddNoProject(t *testing.T) {
	app, _ := setupPeopleTestApp(t)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "add", "1001")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Contains(t, e.Message, "--project (or --in) is required")
}

// TestPeopleRemoveNoProject verifies that omitting --project/--in returns a usage error.
func TestPeopleRemoveNoProject(t *testing.T) {
	app, _ := setupPeopleTestApp(t)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "remove", "1001")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Contains(t, e.Message, "--project (or --in) is required")
}
