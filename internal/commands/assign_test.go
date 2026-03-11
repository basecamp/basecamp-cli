package commands

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
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
