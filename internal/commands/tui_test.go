//go:build dev

package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
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

func TestPrintDevNotice(t *testing.T) {
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })
	version.Version = "0.1.0-test"

	t.Run("prints once then silences", func(t *testing.T) {
		dir := t.TempDir()
		sentinel := filepath.Join(dir, "dev-tui-0.1.0-test")

		// First call creates the sentinel
		printDevNotice(dir)
		_, err := os.Stat(sentinel)
		assert.NoError(t, err, "sentinel file should exist after first call")

		// Second call is a no-op (sentinel exists)
		printDevNotice(dir)
		content, _ := os.ReadFile(sentinel)
		assert.Equal(t, "0.1.0-test", string(content))
	})

	t.Run("skips when cacheDir is empty", func(t *testing.T) {
		// Should not panic or write to cwd
		printDevNotice("")
	})

	t.Run("resurfaces on version change", func(t *testing.T) {
		dir := t.TempDir()

		printDevNotice(dir)
		_, err := os.Stat(filepath.Join(dir, "dev-tui-0.1.0-test"))
		require.NoError(t, err)

		version.Version = "0.2.0-test"
		printDevNotice(dir)
		_, err = os.Stat(filepath.Join(dir, "dev-tui-0.2.0-test"))
		assert.NoError(t, err, "new version should create a new sentinel")
	})
}

func TestTUIExperimentalGate(t *testing.T) {
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })
	version.Version = "0.1.0-test"

	t.Run("blocked before side effects when experimental.tui is off", func(t *testing.T) {
		cacheDir := t.TempDir()
		cfg := config.Default()
		cfg.CacheDir = cacheDir
		// No experimental.tui set
		app := appctx.NewApp(cfg)

		cmd := NewTUICmd()
		ctx := appctx.WithApp(context.Background(), app)
		cmd.SetContext(ctx)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})

		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), `experimental feature "tui" is not enabled`)

		// No dev-tui sentinel should exist — gate must run before printDevNotice
		matches, _ := filepath.Glob(filepath.Join(cacheDir, "dev-tui-*"))
		assert.Empty(t, matches, "sentinel file must not be created when experimental gate blocks")
	})

	t.Run("passes gate when experimental.tui is true", func(t *testing.T) {
		cacheDir := t.TempDir()
		cfg := config.Default()
		cfg.CacheDir = cacheDir
		cfg.AccountID = "12345"
		cfg.Experimental = map[string]bool{"tui": true}
		app := appctx.NewApp(cfg)

		cmd := NewTUICmd()
		ctx := appctx.WithApp(context.Background(), app)
		cmd.SetContext(ctx)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})

		// PreRunE should succeed (experimental gate passes, ensureAccount succeeds
		// with numeric AccountID). RunE will fail because there's no real workspace,
		// but PreRunE is what we're testing.
		err := cmd.PreRunE(cmd, nil)
		require.NoError(t, err)

		// dev-tui sentinel should exist — printDevNotice ran after gate passed
		matches, _ := filepath.Glob(filepath.Join(cacheDir, "dev-tui-*"))
		assert.NotEmpty(t, matches, "sentinel file must be created when experimental gate passes")
	})
}
