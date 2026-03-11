package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCommentShortcutAcceptsInFlag tests that the top-level 'comment' shortcut
// accepts --in, matching the 'comments' group. Previously, 'comment' was built
// directly from newCommentsCreateCmd() and did not inherit the persistent flags
// registered on NewCommentsCmd().
func TestCommentShortcutAcceptsInFlag(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewCommentCmd()

	// --in should be accepted (not "unknown flag"). The command will proceed
	// to RunE and hit an API/network error, which is fine — we're testing
	// flag acceptance, not API behavior.
	err := executeCommand(cmd, app, "--in", "456", "789", "hello")

	// If there's an error, it must NOT be "unknown flag"
	require.NotNil(t, err)
	assert.NotContains(t, err.Error(), "unknown flag")
	assert.NotContains(t, err.Error(), "unknown shorthand")
}

// TestCommentShortcutAcceptsProjectFlag tests the -p shorthand works too.
func TestCommentShortcutAcceptsProjectFlag(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewCommentCmd()

	err := executeCommand(cmd, app, "-p", "456", "789", "hello")

	require.NotNil(t, err)
	assert.NotContains(t, err.Error(), "unknown flag")
	assert.NotContains(t, err.Error(), "unknown shorthand")
}

// TestCommentsGroupAcceptsInFlag tests the 'comments' group accepts --in.
func TestCommentsGroupAcceptsInFlag(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewCommentsCmd()

	err := executeCommand(cmd, app, "list", "--in", "456", "789")

	// Should not be "unknown flag" or "unknown shorthand"
	require.NotNil(t, err)
	assert.NotContains(t, err.Error(), "unknown flag")
	assert.NotContains(t, err.Error(), "unknown shorthand")
}
