package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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

func setupAssignTestApp(t *testing.T) (*appctx.App, *bytes.Buffer) {
	t.Helper()
	return setupTodosTestApp(t)
}

func executeAssignCommand(cmd *cobra.Command, app *appctx.App, args ...string) error {
	cmd.SetArgs(args)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd.Execute()
}

func TestAssignRequiresID(t *testing.T) {
	app, _ := setupAssignTestApp(t)

	cmd := NewAssignCmd()
	err := executeAssignCommand(cmd, app)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg")
}

func TestUnassignRequiresID(t *testing.T) {
	app, _ := setupAssignTestApp(t)

	cmd := NewUnassignCmd()
	err := executeAssignCommand(cmd, app)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg")
}

func TestAssignCardAndStepMutuallyExclusive(t *testing.T) {
	app, _ := setupAssignTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewAssignCmd()
	err := executeAssignCommand(cmd, app, "456", "--card", "--step", "--to", "me")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Cannot use --card and --step together")
}

func TestUnassignCardAndStepMutuallyExclusive(t *testing.T) {
	app, _ := setupAssignTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewUnassignCmd()
	err := executeAssignCommand(cmd, app, "456", "--card", "--step", "--from", "me")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Cannot use --card and --step together")
}

func TestAssignRequiresProject(t *testing.T) {
	app, _ := setupAssignTestApp(t)
	// No project configured — should fail before reaching assignee check

	cmd := NewAssignCmd()
	err := executeAssignCommand(cmd, app, "456", "--to", "me")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "Project ID required", e.Message)
}

func TestUnassignRequiresProject(t *testing.T) {
	app, _ := setupAssignTestApp(t)

	cmd := NewUnassignCmd()
	err := executeAssignCommand(cmd, app, "456", "--from", "me")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "Project ID required", e.Message)
}

func TestAssignHasCardFlag(t *testing.T) {
	cmd := NewAssignCmd()
	flag := cmd.Flags().Lookup("card")
	require.NotNil(t, flag)
	assert.Equal(t, "false", flag.DefValue)
}

func TestAssignHasStepFlag(t *testing.T) {
	cmd := NewAssignCmd()
	flag := cmd.Flags().Lookup("step")
	require.NotNil(t, flag)
	assert.Equal(t, "false", flag.DefValue)
}

func TestUnassignHasCardFlag(t *testing.T) {
	cmd := NewUnassignCmd()
	flag := cmd.Flags().Lookup("card")
	require.NotNil(t, flag)
	assert.Equal(t, "false", flag.DefValue)
}

func TestUnassignHasStepFlag(t *testing.T) {
	cmd := NewUnassignCmd()
	flag := cmd.Flags().Lookup("step")
	require.NotNil(t, flag)
	assert.Equal(t, "false", flag.DefValue)
}

func TestAssignDefaultsTodoWithProjectConfig(t *testing.T) {
	app, _ := setupAssignTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewAssignCmd()
	err := executeAssignCommand(cmd, app, "456", "--to", "me")
	require.Error(t, err)

	// Should proceed past input validation and fail on network (not input validation)
	var e *output.Error
	if errors.As(err, &e) {
		assert.NotContains(t, e.Message, "Cannot use --card and --step")
		assert.NotContains(t, e.Message, "Person to assign is required")
	}
}

func TestAssignCardWithProjectConfig(t *testing.T) {
	app, _ := setupAssignTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewAssignCmd()
	err := executeAssignCommand(cmd, app, "456", "--card", "--to", "me")
	require.Error(t, err)

	// Should proceed past input validation and fail on network
	var e *output.Error
	if errors.As(err, &e) {
		assert.NotContains(t, e.Message, "Cannot use --card and --step")
		assert.NotContains(t, e.Message, "Person to assign is required")
	}
}

func TestAssignStepWithProjectConfig(t *testing.T) {
	app, _ := setupAssignTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewAssignCmd()
	err := executeAssignCommand(cmd, app, "456", "--step", "--to", "me")
	require.Error(t, err)

	// Should proceed past input validation and fail on network
	var e *output.Error
	if errors.As(err, &e) {
		assert.NotContains(t, e.Message, "Cannot use --card and --step")
		assert.NotContains(t, e.Message, "Person to assign is required")
	}
}

func TestAssignHelpMentionsCardAndStep(t *testing.T) {
	cmd := NewAssignCmd()
	assert.Contains(t, cmd.Long, "--card")
	assert.Contains(t, cmd.Long, "--step")
	assert.Contains(t, cmd.Long, "card step")
}

func TestUnassignHelpMentionsCardAndStep(t *testing.T) {
	cmd := NewUnassignCmd()
	assert.Contains(t, cmd.Long, "--card")
	assert.Contains(t, cmd.Long, "--step")
	assert.Contains(t, cmd.Long, "card step")
}

func TestExistingAssigneeIDs(t *testing.T) {
	ids := existingAssigneeIDs(nil)
	assert.Empty(t, ids)
}

func TestContainsID(t *testing.T) {
	assert.True(t, containsID([]int64{1, 2, 3}, 2))
	assert.False(t, containsID([]int64{1, 2, 3}, 4))
	assert.False(t, containsID(nil, 1))
}

func TestRemoveID(t *testing.T) {
	assert.Equal(t, []int64{1, 3}, removeID([]int64{1, 2, 3}, 2))
	assert.Equal(t, []int64{1, 2, 3}, removeID([]int64{1, 2, 3}, 4))
}

func TestFindAssigneeName(t *testing.T) {
	// Uses basecamp.Person from SDK, tested indirectly through the helper
	assert.Equal(t, "Unknown", findAssigneeName(nil, 1))
}

func TestNotFoundOrConvertReturnsTypedNotFound(t *testing.T) {
	tests := []struct {
		typeName string
		itemID   string
	}{
		{"to-do", "99999"},
		{"card", "88888"},
		{"step", "77777"},
	}

	for _, tt := range tests {
		t.Run(tt.typeName, func(t *testing.T) {
			sdkErr := basecamp.ErrNotFound(tt.typeName, tt.itemID)
			err := notFoundOrConvert(sdkErr, tt.typeName, tt.itemID)

			var e *output.Error
			require.True(t, errors.As(err, &e))
			assert.Equal(t, basecamp.CodeNotFound, e.Code)
			assert.Contains(t, e.Message, fmt.Sprintf("%s not found: %s", tt.typeName, tt.itemID))
		})
	}
}

func TestNotFoundOrConvertPassesThroughOtherErrors(t *testing.T) {
	sdkErr := basecamp.ErrForbidden("access denied")
	err := notFoundOrConvert(sdkErr, "to-do", "123")

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.NotEqual(t, basecamp.CodeNotFound, e.Code)
}

// assignGuardTransport serves project resolution but fatals on item-fetch endpoints.
// This proves the non-interactive guard short-circuits before any item lookup.
type assignGuardTransport struct {
	t *testing.T
}

func (tr *assignGuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	path := req.URL.Path
	if req.Method == "GET" && strings.Contains(path, "/projects.json") {
		body := `[{"id": 123, "name": "Test Project"}]`
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	// Any other request means the guard didn't fire — the command tried to
	// fetch a todo/card/step or resolve a person.
	return nil, fmt.Errorf("unexpected HTTP request: %s %s — guard should have short-circuited", req.Method, path)
}

// setupAssignGuardTestApp creates an app whose transport fatals on item-fetch calls.
func setupAssignGuardTestApp(t *testing.T) *appctx.App {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}

	transport := &assignGuardTransport{t: t}
	authMgr := auth.NewManager(cfg, nil)
	sdkClient := basecamp.NewClient(&basecamp.Config{}, &todosTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	nameResolver := names.NewResolver(sdkClient, authMgr, cfg.AccountID)

	return &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: &bytes.Buffer{},
		}),
	}
}

func TestAssignRequiresAssigneeNonInteractive(t *testing.T) {
	app := setupAssignGuardTestApp(t)

	cmd := NewAssignCmd()
	err := executeAssignCommand(cmd, app, "456", "-p", "123")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Person to assign is required")
	assert.Contains(t, e.Hint, "Use --to")
}

func TestUnassignRequiresAssigneeNonInteractive(t *testing.T) {
	app := setupAssignGuardTestApp(t)

	cmd := NewUnassignCmd()
	err := executeAssignCommand(cmd, app, "456", "-p", "123")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Person to unassign is required")
	assert.Contains(t, e.Hint, "Use --from")
}

func TestAssignCardRequiresAssigneeNonInteractive(t *testing.T) {
	app := setupAssignGuardTestApp(t)

	cmd := NewAssignCmd()
	err := executeAssignCommand(cmd, app, "456", "--card", "-p", "123")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Person to assign is required")
	assert.Contains(t, e.Hint, "Use --to")
}

func TestUnassignStepRequiresAssigneeNonInteractive(t *testing.T) {
	app := setupAssignGuardTestApp(t)

	cmd := NewUnassignCmd()
	err := executeAssignCommand(cmd, app, "456", "--step", "-p", "123")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Person to unassign is required")
	assert.Contains(t, e.Hint, "Use --from")
}
