package commands

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// TestToolsCreateAcceptsCloneAlias tests that --clone works as an alias for --source.
// Previously, MarkFlagRequired("source") caused Cobra to reject --clone before RunE ran.
func TestToolsCreateAcceptsCloneAlias(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newToolsCreateCmd(&project)

	// --clone should reach the RunE guard, not fail with "required flag not set"
	err := executeCommand(cmd, app, "--clone", "999", "My Tool")

	// Expect an API/network error — NOT "required flag" and NOT the RunE usage guard
	require.NotNil(t, err)
	var e *output.Error
	if errors.As(err, &e) {
		assert.NotEqual(t, "--source or --clone is required (ID of tool to clone)", e.Message)
	}
	assert.NotContains(t, err.Error(), "required flag")
}

// TestToolsCreateRequiresSourceOrClone tests that omitting both --source and --clone
// produces a usage error from the RunE guard.
func TestToolsCreateRequiresSourceOrClone(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newToolsCreateCmd(&project)

	err := executeCommand(cmd, app, "My Tool")
	require.NotNil(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "--source or --clone is required (ID of tool to clone)", e.Message)
}

// TestToolsRepositionAcceptsPosAlias tests that --pos works as an alias for --position.
func TestToolsRepositionAcceptsPosAlias(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newToolsRepositionCmd(&project)

	// --pos should reach the RunE and proceed past the position guard
	err := executeCommand(cmd, app, "456", "--pos", "2")

	// Expect an API/network error — NOT "required flag" and NOT the RunE usage guard
	require.NotNil(t, err)
	assert.NotContains(t, err.Error(), "required flag")
	var e *output.Error
	if errors.As(err, &e) {
		assert.NotEqual(t, "--position is required (1-based)", e.Message)
	}
}

// TestToolsRepositionRequiresPosition tests the RunE guard when neither flag is given.
func TestToolsRepositionRequiresPosition(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newToolsRepositionCmd(&project)

	err := executeCommand(cmd, app, "456")
	require.NotNil(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "--position is required (1-based)", e.Message)
}

// TestToolsUpdateRejectsLongTitle verifies that tool rename rejects titles over 64 characters.
func TestToolsUpdateRejectsLongTitle(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newToolsUpdateCmd(&project)

	longTitle := strings.Repeat("x", 65)
	err := executeCommand(cmd, app, "456", longTitle)
	require.NotNil(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Tool name too long")
}

// TestToolsUpdateAcceptsMaxTitle verifies that a 64-character title passes validation.
func TestToolsUpdateAcceptsMaxTitle(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newToolsUpdateCmd(&project)

	maxTitle := strings.Repeat("x", 64)
	err := executeCommand(cmd, app, "456", maxTitle)
	require.NotNil(t, err) // fails at network, not validation

	var e *output.Error
	if errors.As(err, &e) {
		assert.NotContains(t, e.Message, "too long")
	}
}

// TestToolsCreateRejectsLongTitle verifies that tool create rejects titles over 64 characters.
func TestToolsCreateRejectsLongTitle(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newToolsCreateCmd(&project)

	longTitle := strings.Repeat("x", 65)
	err := executeCommand(cmd, app, "--source", "999", longTitle)
	require.NotNil(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Tool name too long")
}

// TestToolsCreateAcceptsMaxTitle verifies that a 64-character title passes create validation.
func TestToolsCreateAcceptsMaxTitle(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	project := ""
	cmd := newToolsCreateCmd(&project)

	maxTitle := strings.Repeat("x", 64)
	err := executeCommand(cmd, app, "--source", "999", maxTitle)
	require.NotNil(t, err) // fails at network, not validation

	var e *output.Error
	if errors.As(err, &e) {
		assert.NotContains(t, e.Message, "too long")
	}
}
