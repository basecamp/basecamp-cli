package commands

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// TestTodolistgroupsPositionAcceptsPosAlias tests that --pos works as an alias for --position.
func TestTodolistgroupsPositionAcceptsPosAlias(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := newTodolistgroupsPositionCmd()

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

// TestTodolistgroupsPositionRequiresPosition tests the RunE guard when neither flag is given.
func TestTodolistgroupsPositionRequiresPosition(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := newTodolistgroupsPositionCmd()

	err := executeCommand(cmd, app, "456")
	require.NotNil(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "--position is required (1-based)", e.Message)
}
