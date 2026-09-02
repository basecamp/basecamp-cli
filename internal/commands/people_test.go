package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	sdkCfg := &basecamp.Config{BaseURL: "https://3.basecampapi.com"}
	sdkClient := basecamp.NewClient(sdkCfg, &peopleTestTokenProvider{},
		basecamp.WithTransport(peopleNoNetworkTransport{}),
		basecamp.WithMaxRetries(1), // Disable retries for instant failure
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

	sdkCfg := &basecamp.Config{BaseURL: "https://3.basecampapi.com"}
	// Use default transport to allow HTTP requests to the mock server
	sdkClient := basecamp.NewClient(sdkCfg, &peopleTestTokenProvider{},
		basecamp.WithMaxRetries(1),
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
		basecamp.WithMaxRetries(1),
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
		basecamp.WithMaxRetries(1),
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

func TestPeopleListIncludesEmailAddress(t *testing.T) {
	server := setupPeopleMockServer(t, "99999", 55555)
	app, buf := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "list")
	require.NoError(t, err)

	var result struct {
		Data []struct {
			ID           int64  `json:"id"`
			EmailAddress string `json:"email_address"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &result), "output: %s", buf.String())

	emailsByID := make(map[int64]string, len(result.Data))
	for _, person := range result.Data {
		emailsByID[person.ID] = person.EmailAddress
	}
	assert.Equal(t, "alice@example.com", emailsByID[1001])
	assert.Equal(t, "bob@example.com", emailsByID[2001])
	assert.Equal(t, "carol@example.com", emailsByID[2002])
}

func TestPeopleListInIncludesEmailAddress(t *testing.T) {
	server := setupPeopleMockServer(t, "99999", 55555)
	app, buf := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "list", "--in", "55555")
	require.NoError(t, err)

	var result struct {
		Data []struct {
			EmailAddress string `json:"email_address"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &result), "output: %s", buf.String())
	require.Len(t, result.Data, 1)
	assert.Equal(t, "alice@example.com", result.Data[0].EmailAddress)
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

// profileCapture records what the mock server saw so tests can assert on the
// exact request the command built.
type profileCapture struct {
	putSeen   bool
	putBody   map[string]any
	oooMethod string
	oooPath   string
	oooBody   map[string]any
}

// setupProfileMockServer routes the profile PUT/GET and out-of-office
// POST/DELETE endpoints, recording each mutating request into capture.
func setupProfileMockServer(t *testing.T, accountID string, capture *profileCapture) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		profilePath := fmt.Sprintf("/%s/my/profile.json", accountID)

		switch {
		case r.URL.Path == profilePath && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(map[string]any{
				"id": 12345, "name": "Test User", "title": "Engineer", "bio": "current bio",
			})
		case r.URL.Path == profilePath && r.Method == http.MethodPut:
			capture.putSeen = true
			body, _ := io.ReadAll(r.Body)
			capture.putBody = map[string]any{}
			if err := json.Unmarshal(body, &capture.putBody); err != nil {
				http.Error(w, fmt.Sprintf("bad body: %v", err), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/out_of_office.json") && r.Method == http.MethodPost:
			capture.oooMethod = http.MethodPost
			capture.oooPath = r.URL.Path
			body, _ := io.ReadAll(r.Body)
			capture.oooBody = map[string]any{}
			_ = json.Unmarshal(body, &capture.oooBody)
			json.NewEncoder(w).Encode(map[string]any{
				"enabled": true, "start_date": "2026-09-14", "end_date": "2026-09-18",
			})
		case strings.HasSuffix(r.URL.Path, "/out_of_office.json") && r.Method == http.MethodDelete:
			capture.oooMethod = http.MethodDelete
			capture.oooPath = r.URL.Path
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// TestPeopleUpdateSetsFields verifies that set flags land in the PUT body and
// that the updated profile is read back and returned.
func TestPeopleUpdateSetsFields(t *testing.T) {
	capture := &profileCapture{}
	server := setupProfileMockServer(t, "99999", capture)
	app, buf := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "update", "me", "--bio", "Building the CLI", "--title", "Staff Engineer")
	require.NoError(t, err)

	require.True(t, capture.putSeen, "expected a PUT to /my/profile.json")
	assert.Equal(t, "Building the CLI", capture.putBody["bio"])
	assert.Equal(t, "Staff Engineer", capture.putBody["title"])
	_, hasName := capture.putBody["name"]
	assert.False(t, hasName, "name should be omitted when its flag is not set")

	var result struct {
		Data struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &result), "output: %s", buf.String())
	assert.Equal(t, int64(12345), result.Data.ID)
	assert.Equal(t, "Test User", result.Data.Name)
}

// TestPeopleUpdateClearsField verifies that an explicit empty value clears the
// field: the key is present in the body with an empty string, not omitted.
func TestPeopleUpdateClearsField(t *testing.T) {
	capture := &profileCapture{}
	server := setupProfileMockServer(t, "99999", capture)
	app, _ := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "update", "me", "--bio", "")
	require.NoError(t, err)

	require.True(t, capture.putSeen)
	bio, ok := capture.putBody["bio"]
	require.True(t, ok, "bio key must be present to clear it, got body: %v", capture.putBody)
	assert.Equal(t, "", bio)
}

// TestPeopleUpdateOmitsUnsetFields verifies that unset flags never appear in
// the request body.
func TestPeopleUpdateOmitsUnsetFields(t *testing.T) {
	capture := &profileCapture{}
	server := setupProfileMockServer(t, "99999", capture)
	app, _ := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "update", "me", "--title", "PM")
	require.NoError(t, err)

	require.True(t, capture.putSeen)
	assert.Equal(t, "PM", capture.putBody["title"])
	assert.Len(t, capture.putBody, 1, "only the title key should be present, got: %v", capture.putBody)
}

// TestPeopleUpdateDefaultsToMe verifies the bare "me" target is optional.
func TestPeopleUpdateDefaultsToMe(t *testing.T) {
	capture := &profileCapture{}
	server := setupProfileMockServer(t, "99999", capture)
	app, _ := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "update", "--bio", "no explicit target")
	require.NoError(t, err)
	assert.True(t, capture.putSeen)
}

// TestPeopleUpdateNoFields returns a usage error and never hits the server.
func TestPeopleUpdateNoFields(t *testing.T) {
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")
	capture := &profileCapture{}
	server := setupProfileMockServer(t, "99999", capture)
	app, _ := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "update", "me")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.False(t, capture.putSeen, "no request should be sent when nothing changes")
}

// TestPeopleUpdateRejectsOtherTarget refuses a non-"me" target before any call.
func TestPeopleUpdateRejectsOtherTarget(t *testing.T) {
	capture := &profileCapture{}
	server := setupProfileMockServer(t, "99999", capture)
	app, _ := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "update", "1001", "--bio", "x")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Contains(t, e.Message, `Only "me" is a valid target`)
	assert.False(t, capture.putSeen)
}

// TestPeopleUpdateInvalidFirstWeekDay rejects an unknown enum value.
func TestPeopleUpdateInvalidFirstWeekDay(t *testing.T) {
	capture := &profileCapture{}
	server := setupProfileMockServer(t, "99999", capture)
	app, _ := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "update", "me", "--first-week-day", "Frday")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.False(t, capture.putSeen)
}

// TestPeopleUpdateFirstWeekDay sends a valid enum through to the body.
func TestPeopleUpdateFirstWeekDay(t *testing.T) {
	capture := &profileCapture{}
	server := setupProfileMockServer(t, "99999", capture)
	app, _ := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "update", "me", "--first-week-day", "Monday")
	require.NoError(t, err)
	require.True(t, capture.putSeen)
	assert.Equal(t, "Monday", capture.putBody["first_week_day"])
}

// TestPeopleUpdateInvalidTimeFormat rejects an unknown time-format value.
func TestPeopleUpdateInvalidTimeFormat(t *testing.T) {
	capture := &profileCapture{}
	server := setupProfileMockServer(t, "99999", capture)
	app, _ := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "update", "me", "--time-format", "military")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.False(t, capture.putSeen)
}

// TestPeopleUpdateTimeFormat sends a valid time-format through to the body.
func TestPeopleUpdateTimeFormat(t *testing.T) {
	capture := &profileCapture{}
	server := setupProfileMockServer(t, "99999", capture)
	app, _ := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "update", "me", "--time-format", "twenty_four_hour")
	require.NoError(t, err)
	require.True(t, capture.putSeen)
	assert.Equal(t, "twenty_four_hour", capture.putBody["time_format"])
}

// TestPeopleOutOfOfficeEnable posts resolved dates to the OOO endpoint.
func TestPeopleOutOfOfficeEnable(t *testing.T) {
	capture := &profileCapture{}
	server := setupProfileMockServer(t, "99999", capture)
	app, buf := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "out-of-office", "me", "--start", "2026-09-14", "--end", "2026-09-18")
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, capture.oooMethod)
	assert.Equal(t, "/99999/people/12345/out_of_office.json", capture.oooPath)
	payload, ok := capture.oooBody["out_of_office"].(map[string]any)
	require.True(t, ok, "expected out_of_office payload, got: %v", capture.oooBody)
	assert.Equal(t, "2026-09-14", payload["start_date"])
	assert.Equal(t, "2026-09-18", payload["end_date"])

	var result struct {
		Data struct {
			Enabled bool `json:"enabled"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &result), "output: %s", buf.String())
	assert.True(t, result.Data.Enabled)
}

// TestPeopleOutOfOfficeClear deletes the OOO for the current user.
func TestPeopleOutOfOfficeClear(t *testing.T) {
	capture := &profileCapture{}
	server := setupProfileMockServer(t, "99999", capture)
	app, _ := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "out-of-office", "me", "--clear")
	require.NoError(t, err)

	assert.Equal(t, http.MethodDelete, capture.oooMethod)
	assert.Equal(t, "/99999/people/12345/out_of_office.json", capture.oooPath)
}

// TestPeopleOutOfOfficeConflict rejects --clear with dates before any call.
func TestPeopleOutOfOfficeConflict(t *testing.T) {
	capture := &profileCapture{}
	server := setupProfileMockServer(t, "99999", capture)
	app, _ := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "out-of-office", "me", "--clear", "--start", "2026-09-14")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Empty(t, capture.oooMethod, "no OOO request should be sent on a conflicting invocation")
}

// TestPeopleOutOfOfficeRequiresBothDates rejects a half-specified range.
func TestPeopleOutOfOfficeRequiresBothDates(t *testing.T) {
	capture := &profileCapture{}
	server := setupProfileMockServer(t, "99999", capture)
	app, _ := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "out-of-office", "me", "--start", "2026-09-14")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Empty(t, capture.oooMethod)
}

// TestPeopleOutOfOfficeEmptyStartRejected verifies an explicit empty --start is
// a malformed date, not a silently-dropped omission.
func TestPeopleOutOfOfficeEmptyStartRejected(t *testing.T) {
	capture := &profileCapture{}
	server := setupProfileMockServer(t, "99999", capture)
	app, _ := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "out-of-office", "me", "--start", "", "--end", "2026-09-18")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Empty(t, capture.oooMethod)
}

// TestPeopleOutOfOfficeClearWithEmptyStartConflict verifies that an explicitly
// passed empty --start still conflicts with --clear.
func TestPeopleOutOfOfficeClearWithEmptyStartConflict(t *testing.T) {
	capture := &profileCapture{}
	server := setupProfileMockServer(t, "99999", capture)
	app, _ := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "out-of-office", "me", "--clear", "--start", "")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Contains(t, e.Message, "--clear cannot be combined")
	assert.Empty(t, capture.oooMethod)
}

// TestPeopleOutOfOfficeImpossibleDate rejects a syntactically-shaped but
// non-calendar date before any request.
func TestPeopleOutOfOfficeImpossibleDate(t *testing.T) {
	capture := &profileCapture{}
	server := setupProfileMockServer(t, "99999", capture)
	app, _ := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "out-of-office", "me", "--start", "2026-13-45", "--end", "2026-09-18")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Empty(t, capture.oooMethod)
}

// TestPeopleOutOfOfficeInvertedRange rejects an end that precedes the start.
func TestPeopleOutOfOfficeInvertedRange(t *testing.T) {
	capture := &profileCapture{}
	server := setupProfileMockServer(t, "99999", capture)
	app, _ := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "out-of-office", "me", "--start", "2026-09-18", "--end", "2026-09-14")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Contains(t, e.Message, "precedes start")
	assert.Empty(t, capture.oooMethod)
}

// TestPeopleUpdateReadBackFailureStillSucceeds verifies a failed profile
// read-back keeps the update reported as successful with a diagnostic.
func TestPeopleUpdateReadBackFailureStillSucceeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/99999/my/profile.json" && r.Method == http.MethodPut {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
			return
		}
		// Any read-back GET fails with a non-retryable client error.
		http.Error(w, "boom", http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)
	app, buf := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "update", "me", "--bio", "x")
	require.NoError(t, err, "output: %s", buf.String())

	var result struct {
		OK   bool `json:"ok"`
		Data struct {
			Updated bool `json:"updated"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &result), "output: %s", buf.String())
	assert.True(t, result.OK)
	assert.True(t, result.Data.Updated)
}

// TestPeopleOutOfOfficeNaturalLanguageDates resolves relative dates before
// sending them.
func TestPeopleOutOfOfficeNaturalLanguageDates(t *testing.T) {
	capture := &profileCapture{}
	server := setupProfileMockServer(t, "99999", capture)
	app, _ := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "out-of-office", "me", "--start", "today", "--end", "tomorrow")
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, capture.oooMethod)
	payload, ok := capture.oooBody["out_of_office"].(map[string]any)
	require.True(t, ok)
	// Resolved to concrete YYYY-MM-DD dates, not the literal words.
	assert.Regexp(t, `^\d{4}-\d{2}-\d{2}$`, payload["start_date"])
	assert.Regexp(t, `^\d{4}-\d{2}-\d{2}$`, payload["end_date"])
}

// TestPeopleOutOfOfficeNoArgs returns a usage error with no flags.
func TestPeopleOutOfOfficeNoArgs(t *testing.T) {
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")
	capture := &profileCapture{}
	server := setupProfileMockServer(t, "99999", capture)
	app, _ := setupPeopleMockApp(t, server)

	cmd := NewPeopleCmd()
	err := executePeopleCommand(cmd, app, "out-of-office", "me")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, output.CodeUsage, e.Code)
	assert.Empty(t, capture.oooMethod)
}
