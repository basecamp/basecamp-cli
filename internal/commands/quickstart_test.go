package commands

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/bcq/internal/appctx"
	"github.com/basecamp/bcq/internal/auth"
	"github.com/basecamp/bcq/internal/config"
	"github.com/basecamp/bcq/internal/names"
	"github.com/basecamp/bcq/internal/output"
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

	t.Setenv("BCQ_NO_KEYRING", "1")

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
	if err != nil {
		t.Fatalf("quickstart should not error when account is missing: %v", err)
	}
}

// TestQuickstartWithProjectAndInvalidAccount verifies quickstart doesn't panic
// when project_id is set but account_id is invalid (non-numeric).
func TestQuickstartWithProjectAndInvalidAccount(t *testing.T) {
	// Project ID set, but account ID is invalid (non-numeric)
	app, _ := setupQuickstartTestApp(t, "not-a-number", "12345")

	cmd := NewQuickStartCmd()
	err := executeQuickstartCommand(cmd, app)
	if err != nil {
		t.Fatalf("quickstart should not error when account is invalid: %v", err)
	}
}

// TestQuickstartWithNoConfig verifies quickstart works with minimal config.
func TestQuickstartWithNoConfig(t *testing.T) {
	// No account, no project
	app, _ := setupQuickstartTestApp(t, "", "")

	cmd := NewQuickStartCmd()
	err := executeQuickstartCommand(cmd, app)
	if err != nil {
		t.Fatalf("quickstart should not error with empty config: %v", err)
	}
}
