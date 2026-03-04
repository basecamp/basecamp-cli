package commands

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// TestScheduleCreateHasSubscribeFlags tests that schedule create has --subscribe and --no-subscribe flags.
func TestScheduleCreateHasSubscribeFlags(t *testing.T) {
	cmd := NewScheduleCmd()

	createCmd, _, err := cmd.Find([]string{"create"})
	require.NoError(t, err)

	flag := createCmd.Flags().Lookup("subscribe")
	require.NotNil(t, flag, "expected --subscribe flag on schedule create")

	flag = createCmd.Flags().Lookup("no-subscribe")
	require.NotNil(t, flag, "expected --no-subscribe flag on schedule create")
}

// TestScheduleCreateSubscribeEmptyIsError tests that --subscribe "" is rejected on schedule create.
func TestScheduleCreateSubscribeEmptyIsError(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewScheduleCmd()

	err := executeMessagesCommand(cmd, app, "create", "Standup",
		"--starts-at", "2026-03-04T09:00:00Z",
		"--ends-at", "2026-03-04T09:30:00Z",
		"--subscribe", "")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "at least one person")
}

// TestScheduleCreateSubscribeMutualExclusion tests that --subscribe and --no-subscribe are mutually exclusive.
func TestScheduleCreateSubscribeMutualExclusion(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewScheduleCmd()

	err := executeMessagesCommand(cmd, app, "create", "Standup",
		"--starts-at", "2026-03-04T09:00:00Z",
		"--ends-at", "2026-03-04T09:30:00Z",
		"--subscribe", "me", "--no-subscribe")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "mutually exclusive")
}
