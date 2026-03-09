package commands

import (
	"bytes"
	"context"
	"errors"
	"net/http"
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

// quickstartNoNetworkTransport prevents real network calls in tests.
type quickstartNoNetworkTransport struct{}

func (quickstartNoNetworkTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network disabled in tests")
}

// quickstartTestTokenProvider is a mock token provider for tests.
type quickstartTestTokenProvider struct{}

func (t *quickstartTestTokenProvider) AccessToken(_ context.Context) (string, error) {
	return "test-token", nil
}

// setupQuickstartTestApp creates a minimal test app context for quickstart tests.
func setupQuickstartTestApp(t *testing.T, accountID, projectID string) (*appctx.App, *bytes.Buffer) {
	t.Helper()

	t.Setenv("BASECAMP_NO_KEYRING", "1")

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: accountID,
		ProjectID: projectID,
	}

	authMgr := auth.NewManager(cfg, nil)

	sdkCfg := &basecamp.Config{}
	sdkClient := basecamp.NewClient(sdkCfg, &quickstartTestTokenProvider{},
		basecamp.WithTransport(quickstartNoNetworkTransport{}),
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
	}
	return app, buf
}

// executeQuickstartCommand executes a cobra command with the given args.
func executeQuickstartCommand(cmd *cobra.Command, app *appctx.App, args ...string) error {
	cmd.SetArgs(args)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd.Execute()
}

// TestQuickstartWithProjectButNoAccount verifies quickstart doesn't panic
// when project_id is set but account_id is missing.
func TestQuickstartWithProjectButNoAccount(t *testing.T) {
	// Project ID set, but account ID empty
	app, _ := setupQuickstartTestApp(t, "", "12345")

	cmd := NewQuickStartCmd()
	err := executeQuickstartCommand(cmd, app)
	require.NoError(t, err, "quickstart should not error when account is missing")
}

// TestQuickstartWithProjectAndInvalidAccount verifies quickstart doesn't panic
// when project_id is set but account_id is invalid (non-numeric).
func TestQuickstartWithProjectAndInvalidAccount(t *testing.T) {
	// Project ID set, but account ID is invalid (non-numeric)
	app, _ := setupQuickstartTestApp(t, "not-a-number", "12345")

	cmd := NewQuickStartCmd()
	err := executeQuickstartCommand(cmd, app)
	require.NoError(t, err, "quickstart should not error when account is invalid")
}

// TestQuickstartWithNoConfig verifies quickstart works with minimal config.
func TestQuickstartWithNoConfig(t *testing.T) {
	// No account, no project
	app, _ := setupQuickstartTestApp(t, "", "")

	cmd := NewQuickStartCmd()
	err := executeQuickstartCommand(cmd, app)
	require.NoError(t, err, "quickstart should not error with empty config")
}

func TestRunQuickStartDefaultWithConfigFormatJSON(t *testing.T) {
	// Config-driven format=json should route to quickstart (not help).
	// Exercises the IsMachineOutput() check in RunQuickStartDefault.
	app, buf := setupQuickstartTestApp(t, "", "")
	app.Config.Format = "json"

	cmd := &cobra.Command{Use: "basecamp", RunE: RunQuickStartDefault}
	err := executeQuickstartCommand(cmd, app)
	require.NoError(t, err)

	assert.Contains(t, buf.String(), `"version"`)
}

func TestRunQuickStartDefaultWithConfigFormatQuiet(t *testing.T) {
	app, buf := setupQuickstartTestApp(t, "", "")
	app.Config.Format = "quiet"

	cmd := &cobra.Command{Use: "basecamp", RunE: RunQuickStartDefault}
	err := executeQuickstartCommand(cmd, app)
	require.NoError(t, err)

	assert.Contains(t, buf.String(), `"version"`)
}

func TestRunQuickStartDefaultIncludesBreadcrumbsWhenHintsEnabled(t *testing.T) {
	// When hints are enabled (via resolvePreferences from config), quickstart
	// output should include breadcrumbs. This verifies preferences are applied
	// before quickstart runs.
	app, buf := setupQuickstartTestApp(t, "", "")
	app.Flags.Hints = true

	cmd := &cobra.Command{Use: "basecamp", RunE: RunQuickStartDefault}
	err := executeQuickstartCommand(cmd, app)
	require.NoError(t, err)

	assert.Contains(t, buf.String(), `"breadcrumbs"`)
}

func TestRunQuickStartDefaultSuppressesBreadcrumbsWithoutHints(t *testing.T) {
	// Default (hints=false) should suppress breadcrumbs.
	app, buf := setupQuickstartTestApp(t, "", "")

	cmd := &cobra.Command{Use: "basecamp", RunE: RunQuickStartDefault}
	err := executeQuickstartCommand(cmd, app)
	require.NoError(t, err)

	assert.NotContains(t, buf.String(), `"breadcrumbs"`)
}
