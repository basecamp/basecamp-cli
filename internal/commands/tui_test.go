package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
)

func TestViewFactory_UnknownTarget_ReturnsHome(t *testing.T) {
	session := workspace.NewTestSessionWithHub()

	// The previous code panicked on unknown ViewTarget values.
	// After the fix, it returns a Home view as a safe fallback.
	v := viewFactory(workspace.ViewTarget(9999), session, workspace.Scope{})
	require.NotNil(t, v, "unknown target must return a non-nil view")
	assert.Equal(t, "Home", v.Title(), "unknown target must fall back to Home view")
}
