package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectOmarchy(t *testing.T) {
	t.Run("OMARCHY_PATH", func(t *testing.T) {
		t.Setenv("OMARCHY_PATH", t.TempDir())
		assert.True(t, detectOmarchy())
	})

	t.Run("state directory", func(t *testing.T) {
		t.Setenv("OMARCHY_PATH", "")
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".local", "state", "omarchy"), 0o755))
		assert.True(t, detectOmarchy())
	})

	t.Run("not detected", func(t *testing.T) {
		t.Setenv("OMARCHY_PATH", "")
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		assert.False(t, detectOmarchy())
	})
}

func TestEnsureOmarchyPluginInstallsMissingPlugin(t *testing.T) {
	var calls []string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if len(calls) == 1 {
			return `[{"id":"37signals.hey","enabled":true}]`, nil
		}
		return "installed", nil
	}

	outcome := ensureOmarchyPlugin(context.Background(), run)
	assert.Equal(t, "installed", outcome.Status)
	assert.Equal(t, []string{
		"plugin list --json",
		"plugin add " + omarchyBasecampPluginSource + " --enable --yes",
	}, calls)
}

func TestEnsureOmarchyPluginUpdatesInstalledPlugin(t *testing.T) {
	var calls []string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if len(calls) == 1 {
			return `[{"id":"37signals.basecamp","enabled":false}]`, nil
		}
		return "updated", nil
	}

	outcome := ensureOmarchyPlugin(context.Background(), run)
	assert.Equal(t, "updated", outcome.Status)
	assert.Equal(t, []string{
		"plugin list --json",
		"plugin update " + omarchyBasecampPluginID + " --yes",
	}, calls)
}

func TestEnsureOmarchyPluginReportsRemediation(t *testing.T) {
	t.Run("unexpected list", func(t *testing.T) {
		outcome := ensureOmarchyPlugin(context.Background(), func(_ context.Context, _ ...string) (string, error) {
			return `{"plugins":[]}`, nil
		})
		assert.True(t, outcome.failed())
		assert.Equal(t, "omarchy plugin list --json", outcome.Manual)
	})

	t.Run("update failure", func(t *testing.T) {
		calls := 0
		outcome := ensureOmarchyPlugin(context.Background(), func(_ context.Context, _ ...string) (string, error) {
			calls++
			if calls == 1 {
				return `[{"id":"37signals.basecamp","enabled":true}]`, nil
			}
			return "failed", errors.New("exit status 1")
		})
		assert.True(t, outcome.failed())
		assert.Equal(t, "omarchy plugin update "+omarchyBasecampPluginID, outcome.Manual)
	})
}
