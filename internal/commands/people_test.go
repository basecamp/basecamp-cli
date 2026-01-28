package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/auth"
	"github.com/basecamp/bcq/internal/config"
	"github.com/basecamp/bcq/internal/names"
	"github.com/basecamp/bcq/internal/output"
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
	t.Setenv("BCQ_NO_KEYRING", "1")

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
	}

	// Create auth manager without any stored credentials
	authMgr := auth.NewManager(cfg, nil)

	sdkCfg := &basecamp.Config{
		AccountID: cfg.AccountID,
	}
	sdkClient := basecamp.NewClient(sdkCfg, &peopleTestTokenProvider{},
		basecamp.WithTransport(peopleNoNetworkTransport{}),
		basecamp.WithMaxRetries(0), // Disable retries for instant failure
	)
	nameResolver := names.NewResolver(sdkClient, authMgr)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
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

// TestMeRequiresAuth tests that bcq me returns auth error when not authenticated.
func TestMeRequiresAuth(t *testing.T) {
	app, _ := setupPeopleTestApp(t)

	// Ensure no authentication - no env token, no stored credentials
	t.Setenv("BASECAMP_TOKEN", "")

	cmd := NewMeCmd()

	err := executePeopleCommand(cmd, app)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Should be auth required error
	if e, ok := err.(*output.Error); ok {
		if e.Code != output.CodeAuth {
			t.Errorf("expected code %q, got %q", output.CodeAuth, e.Code)
		}
		if !strings.Contains(e.Message, "Not authenticated") {
			t.Errorf("expected 'Not authenticated', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}

// setupAuthenticatedTestApp creates a test app with credentials stored for Launchpad OAuth.
// It also starts a mock Launchpad server and returns a cleanup function.
func setupAuthenticatedTestApp(t *testing.T, accountID string, launchpadResponse *basecamp.AuthorizationInfo) (*appctx.App, *bytes.Buffer) {
	t.Helper()

	// Start mock Launchpad server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(launchpadResponse)
	}))
	t.Cleanup(server.Close)

	// Override Launchpad URL to use mock server
	t.Setenv("BCQ_LAUNCHPAD_URL", server.URL)

	// Disable keyring access during tests
	t.Setenv("BCQ_NO_KEYRING", "1")

	// Create temp directory for credentials
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	// Create credentials directory and file
	credsDir := filepath.Join(tmpDir, "basecamp")
	if err := os.MkdirAll(credsDir, 0700); err != nil {
		t.Fatalf("failed to create creds dir: %v", err)
	}

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
	if err := os.WriteFile(credsPath, credsData, 0600); err != nil {
		t.Fatalf("failed to write creds: %v", err)
	}

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: accountID,
		BaseURL:   "https://3.basecampapi.com",
	}

	// Create auth manager
	authMgr := auth.NewManager(cfg, nil)

	sdkCfg := &basecamp.Config{
		AccountID: cfg.AccountID,
	}
	// Use default transport to allow HTTP requests to the mock server
	sdkClient := basecamp.NewClient(sdkCfg, &peopleTestTokenProvider{},
		basecamp.WithMaxRetries(0),
	)
	nameResolver := names.NewResolver(sdkClient, authMgr)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
	}
	return app, buf
}

// TestMeWithLaunchpadNoAccountConfigured tests that bcq me works via Launchpad
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
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

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

	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse output: %v\nOutput: %s", err, buf.String())
	}

	// Verify identity
	if result.Data.Identity.ID != 12345 {
		t.Errorf("expected identity ID 12345, got %d", result.Data.Identity.ID)
	}
	if result.Data.Identity.EmailAddress != "test@example.com" {
		t.Errorf("expected email test@example.com, got %s", result.Data.Identity.EmailAddress)
	}

	// Verify only bc3 accounts are shown (filtered out bcx)
	if len(result.Data.Accounts) != 2 {
		t.Errorf("expected 2 bc3 accounts, got %d", len(result.Data.Accounts))
	}

	// Verify no account is marked as current
	for _, acct := range result.Data.Accounts {
		if acct.Current {
			t.Errorf("expected no account marked as current, but %d (%s) is marked current", acct.ID, acct.Name)
		}
	}

	// Verify breadcrumbs suggest account setup
	foundSetup := false
	for _, bc := range result.Breadcrumbs {
		if bc.Action == "setup" && strings.Contains(bc.Cmd, "bcq config set account") {
			foundSetup = true
			break
		}
	}
	if !foundSetup {
		t.Errorf("expected breadcrumbs to suggest account setup, got: %+v", result.Breadcrumbs)
	}
}

// TestMeWithAccountConfigured tests that bcq me shows the current account marker
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
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

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

	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse output: %v\nOutput: %s", err, buf.String())
	}

	// Verify account 222 is marked as current
	foundCurrent := false
	for _, acct := range result.Data.Accounts {
		if acct.ID == 222 {
			if !acct.Current {
				t.Errorf("expected account 222 to be marked as current")
			}
			foundCurrent = true
		} else if acct.Current {
			t.Errorf("expected only account 222 to be marked as current, but %d is also marked", acct.ID)
		}
	}
	if !foundCurrent {
		t.Errorf("account 222 not found in output")
	}

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
	if foundSetup {
		t.Errorf("expected no setup breadcrumb when account is configured")
	}
	if !foundProjects {
		t.Errorf("expected projects breadcrumb when account is configured")
	}
}
