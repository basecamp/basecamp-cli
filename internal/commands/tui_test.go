package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/version"
)

func TestViewFactory_UnknownTarget_ReturnsHome(t *testing.T) {
	session := workspace.NewTestSessionWithHub()

	// The previous code panicked on unknown ViewTarget values.
	// After the fix, it returns a Home view as a safe fallback.
	v := viewFactory(workspace.ViewTarget(9999), session, workspace.Scope{})
	require.NotNil(t, v, "unknown target must return a non-nil view")
	assert.Equal(t, "Home", v.Title(), "unknown target must fall back to Home view")
}

func TestPrintExperimentalNotice(t *testing.T) {
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })
	version.Version = "0.1.0-test"

	t.Run("prints once then silences", func(t *testing.T) {
		dir := t.TempDir()
		sentinel := filepath.Join(dir, "experimental-tui-0.1.0-test")

		// First call creates the sentinel
		printExperimentalNotice(dir)
		_, err := os.Stat(sentinel)
		assert.NoError(t, err, "sentinel file should exist after first call")

		// Second call is a no-op (sentinel exists)
		printExperimentalNotice(dir)
		content, _ := os.ReadFile(sentinel)
		assert.Equal(t, "0.1.0-test", string(content))
	})

	t.Run("skips when cacheDir is empty", func(t *testing.T) {
		// Should not panic or write to cwd
		printExperimentalNotice("")
	})

	t.Run("resurfaces on version change", func(t *testing.T) {
		dir := t.TempDir()

		printExperimentalNotice(dir)
		_, err := os.Stat(filepath.Join(dir, "experimental-tui-0.1.0-test"))
		require.NoError(t, err)

		version.Version = "0.2.0-test"
		printExperimentalNotice(dir)
		_, err = os.Stat(filepath.Join(dir, "experimental-tui-0.2.0-test"))
		assert.NoError(t, err, "new version should create a new sentinel")
	})
}
