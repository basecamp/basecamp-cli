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
		if e.Message != "Not authenticated" {
			t.Errorf("expected 'Not authenticated', got %q", e.Message)
		}
	} else {
		t.Errorf("expected *output.Error, got %T: %v", err, err)
	}
}
