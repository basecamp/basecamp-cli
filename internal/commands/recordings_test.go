package commands

import (
	"bytes"
	"context"
	"errors"
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

func setupRecordingsTestApp(t *testing.T) (*appctx.App, *bytes.Buffer) {
	t.Helper()

	t.Setenv("BASECAMP_NO_KEYRING", "1")

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
	}

	authMgr := auth.NewManager(cfg, nil)
	sdkCfg := &basecamp.Config{}
	sdkClient := basecamp.NewClient(sdkCfg, &todosTestTokenProvider{},
		basecamp.WithTransport(todosNoNetworkTransport{}),
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

func executeRecordingsCommand(cmd *cobra.Command, app *appctx.App, args ...string) error {
	cmd.SetArgs(args)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd.Execute()
}

// TestRecordingsAssigneeRedirect tests that recordings todos --assignee redirects to reports assigned.
func TestRecordingsAssigneeRedirect(t *testing.T) {
	app, _ := setupRecordingsTestApp(t)

	cmd := NewRecordingsCmd()
	err := executeRecordingsCommand(cmd, app, "todos", "--assignee", "me")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "does not support --assignee")
	assert.Contains(t, e.Hint, "reports assigned")
}

// TestRecordingsAssigneeRedirectWithTypeFlag tests --type flag form.
func TestRecordingsAssigneeRedirectWithTypeFlag(t *testing.T) {
	app, _ := setupRecordingsTestApp(t)

	cmd := NewRecordingsCmd()
	err := executeRecordingsCommand(cmd, app, "--type", "todos", "--assignee", "me")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "does not support --assignee")
	assert.Contains(t, e.Hint, "reports assigned")
}

// TestRecordingsAssigneeWithProjectRedirects tests that --assignee with --in suggests todos command.
func TestRecordingsAssigneeWithProjectRedirects(t *testing.T) {
	app, _ := setupRecordingsTestApp(t)

	cmd := NewRecordingsCmd()
	err := executeRecordingsCommand(cmd, app, "todos", "--assignee", "me", "--in", "myproject")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "does not support --assignee")
	assert.Contains(t, e.Hint, `basecamp todos --assignee "me" --in "myproject" --json`)
}

// TestRecordingsAssigneeNonMeRedirects tests that --assignee with a specific person preserves the name.
func TestRecordingsAssigneeNonMeRedirects(t *testing.T) {
	app, _ := setupRecordingsTestApp(t)

	cmd := NewRecordingsCmd()
	err := executeRecordingsCommand(cmd, app, "todos", "--assignee", "Jane Doe")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Hint, `reports assigned "Jane Doe" --json`)
}

// TestRecordingsAssigneeMeCaseInsensitive tests that --assignee ME is treated as "me" (reports assigned, no person arg).
func TestRecordingsAssigneeMeCaseInsensitive(t *testing.T) {
	app, _ := setupRecordingsTestApp(t)

	cmd := NewRecordingsCmd()
	err := executeRecordingsCommand(cmd, app, "todos", "--assignee", "ME")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Hint, "reports assigned --json")
	assert.NotContains(t, e.Hint, `"ME"`, "ME should be treated as the default alias, not a person name")
}

// TestRecordingsListAssigneeRedirect tests that recordings list todos --assignee redirects to reports assigned.
func TestRecordingsListAssigneeRedirect(t *testing.T) {
	app, _ := setupRecordingsTestApp(t)

	cmd := NewRecordingsCmd()
	err := executeRecordingsCommand(cmd, app, "list", "todos", "--assignee", "me")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "does not support --assignee")
	assert.Contains(t, e.Hint, "reports assigned")
}

// TestRecordingsListAssigneeRedirectWithTypeFlag tests list subcommand with --type flag form.
func TestRecordingsListAssigneeRedirectWithTypeFlag(t *testing.T) {
	app, _ := setupRecordingsTestApp(t)

	cmd := NewRecordingsCmd()
	err := executeRecordingsCommand(cmd, app, "list", "--type", "todos", "--assignee", "me")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "does not support --assignee")
	assert.Contains(t, e.Hint, "reports assigned")
}

// TestRecordingsListAssigneeWithProjectRedirects tests list with --assignee and --in suggests todos command.
func TestRecordingsListAssigneeWithProjectRedirects(t *testing.T) {
	app, _ := setupRecordingsTestApp(t)

	cmd := NewRecordingsCmd()
	err := executeRecordingsCommand(cmd, app, "list", "todos", "--assignee", "me", "--in", "myproject")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "does not support --assignee")
	assert.Contains(t, e.Hint, `basecamp todos --assignee "me" --in "myproject" --json`)
}

// TestRecordingsWithoutAssigneeStillWorks tests that recordings works normally without --assignee.
func TestRecordingsWithoutAssigneeStillWorks(t *testing.T) {
	app, _ := setupRecordingsTestApp(t)

	cmd := NewRecordingsCmd()
	err := executeRecordingsCommand(cmd, app, "todos")

	// Should not get an assignee redirect error — it will fail on account/network instead
	if err != nil {
		var e *output.Error
		if errors.As(err, &e) {
			assert.NotContains(t, e.Message, "does not support --assignee",
				"should not get assignee redirect when --assignee not used")
		}
	}
}

// TestRecordingsAssigneeFlagIsHidden tests that --assignee is hidden on the root recordings command.
func TestRecordingsAssigneeFlagIsHidden(t *testing.T) {
	cmd := NewRecordingsCmd()

	flag := cmd.Flags().Lookup("assignee")
	require.NotNil(t, flag, "expected --assignee flag to exist")
	assert.True(t, flag.Hidden, "expected --assignee flag to be hidden")
}

// TestRecordingsListAssigneeFlagIsHidden tests that --assignee is hidden on the list subcommand.
func TestRecordingsListAssigneeFlagIsHidden(t *testing.T) {
	cmd := NewRecordingsCmd()

	listCmd, _, err := cmd.Find([]string{"list"})
	require.NoError(t, err, "expected list subcommand to exist")

	flag := listCmd.Flags().Lookup("assignee")
	require.NotNil(t, flag, "expected --assignee flag to exist on list subcommand")
	assert.True(t, flag.Hidden, "expected --assignee flag to be hidden on list subcommand")
}
