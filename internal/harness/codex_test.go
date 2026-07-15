package harness

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/version"
)

func TestDetectCodexDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stubCodexLookPath(t, "", exec.ErrNotFound)

	assert.False(t, DetectCodex())
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".codex"), 0o755))
	assert.True(t, DetectCodex())
}

func TestDetectCodexBinary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stubCodexLookPath(t, "/usr/local/bin/codex", nil)

	assert.True(t, DetectCodex())
	assert.Equal(t, "/usr/local/bin/codex", FindCodexBinary())
}

func TestCheckCodexPluginMissingBinary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubCodexLookPath(t, "", exec.ErrNotFound)

	check := CheckCodexPlugin()

	assert.Equal(t, "fail", check.Status)
	assert.Contains(t, check.Message, "Codex executable")
	assert.Contains(t, check.Hint, "basecamp setup codex")
}

func TestCheckCodexPluginMissing(t *testing.T) {
	stubCodexList(t, `{"installed":[],"available":[{"pluginId":"basecamp@37signals","name":"basecamp","marketplaceName":"37signals","version":"0.7.2","installed":false,"enabled":false}]}`, nil)

	check := CheckCodexPlugin()

	assert.Equal(t, "fail", check.Status)
	assert.Equal(t, "Plugin not installed", check.Message)
	assert.Equal(t, "Run: basecamp setup codex", check.Hint)
}

func TestCheckCodexPluginDisabled(t *testing.T) {
	stubCodexList(t, codexListFixture("0.7.2", true, false), nil)

	check := CheckCodexPlugin()

	assert.Equal(t, "fail", check.Status)
	assert.Equal(t, "Installed but disabled", check.Message)
	assert.Contains(t, check.Hint, "basecamp setup codex")
}

func TestCheckCodexPluginInstalledAndEnabled(t *testing.T) {
	stubCodexList(t, codexListFixture("0.7.2", true, true), nil)

	check := CheckCodexPlugin()

	assert.Equal(t, "pass", check.Status)
	assert.Equal(t, "Installed and enabled", check.Message)
}

func TestCheckCodexPluginMalformedJSON(t *testing.T) {
	stubCodexList(t, `not json`, nil)

	check := CheckCodexPlugin()

	assert.Equal(t, "fail", check.Status)
	assert.Contains(t, check.Message, "Cannot parse")
	assert.Contains(t, check.Hint, "basecamp setup codex")
}

func TestCheckCodexPluginCommandFailure(t *testing.T) {
	stubCodexList(t, "", errors.New("exit status 1"))

	check := CheckCodexPlugin()

	assert.Equal(t, "fail", check.Status)
	assert.Contains(t, check.Message, "Cannot query")
	assert.Contains(t, check.Hint, "codex plugin list --available --json")
}

func TestCheckCodexPluginVersionMatching(t *testing.T) {
	original := version.Version
	version.Version = "0.7.2"
	t.Cleanup(func() { version.Version = original })
	stubCodexList(t, codexListFixture("0.7.2", true, true), nil)

	check := CheckCodexPluginVersion()

	assert.Equal(t, "pass", check.Status)
	assert.Equal(t, "Up to date (0.7.2)", check.Message)
}

func TestCheckCodexPluginVersionMismatch(t *testing.T) {
	original := version.Version
	version.Version = "0.8.0"
	t.Cleanup(func() { version.Version = original })
	stubCodexList(t, codexListFixture("0.7.2", true, true), nil)

	check := CheckCodexPluginVersion()

	assert.Equal(t, "warn", check.Status)
	assert.Contains(t, check.Message, "plugin 0.7.2, CLI 0.8.0")
	assert.Equal(t, "Run: basecamp setup codex", check.Hint)
}

func TestCheckCodexPluginUsesSupportedJSONCommand(t *testing.T) {
	stubCodexLookPath(t, "/usr/local/bin/codex", nil)
	var gotPath string
	var gotArgs []string
	original := runCodexCommand
	runCodexCommand = func(_ context.Context, path string, args ...string) ([]byte, error) {
		gotPath = path
		gotArgs = append([]string(nil), args...)
		return []byte(codexListFixture("0.7.2", true, true)), nil
	}
	t.Cleanup(func() { runCodexCommand = original })

	check := CheckCodexPlugin()

	assert.Equal(t, "pass", check.Status)
	assert.Equal(t, "/usr/local/bin/codex", gotPath)
	assert.Equal(t, []string{"plugin", "list", "--available", "--json"}, gotArgs)
}

func TestCodexAgentInfoWiring(t *testing.T) {
	stubCodexList(t, codexListFixture("0.7.2", true, true), nil)

	found := FindAgent("codex")
	require.NotNil(t, found)
	assert.Equal(t, "Codex", found.Name)
	assert.NotNil(t, found.Detect)
	assert.NotNil(t, found.Checks)
	checks := found.Checks()
	require.Len(t, checks, 1)
	assert.Equal(t, "Codex Plugin", checks[0].Name)
}

func stubCodexLookPath(t *testing.T, path string, err error) {
	t.Helper()
	original := codexLookPath
	codexLookPath = func(name string) (string, error) {
		assert.Equal(t, "codex", name)
		return path, err
	}
	t.Cleanup(func() { codexLookPath = original })
}

func stubCodexList(t *testing.T, output string, commandErr error) {
	t.Helper()
	stubCodexLookPath(t, "/usr/local/bin/codex", nil)
	original := runCodexCommand
	runCodexCommand = func(_ context.Context, path string, args ...string) ([]byte, error) {
		assert.Equal(t, "/usr/local/bin/codex", path)
		assert.Equal(t, []string{"plugin", "list", "--available", "--json"}, args)
		return []byte(output), commandErr
	}
	t.Cleanup(func() { runCodexCommand = original })
}

func codexListFixture(pluginVersion string, installed, enabled bool) string {
	return `{"installed":[{"pluginId":"basecamp@37signals","name":"basecamp","marketplaceName":"37signals","version":"` + pluginVersion + `","installed":` + boolJSON(installed) + `,"enabled":` + boolJSON(enabled) + `}],"available":[]}`
}

func boolJSON(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
