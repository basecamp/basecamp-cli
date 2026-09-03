package harness

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestCodexHomeAliases(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	tests := []struct {
		configured string
		want       string
	}{
		{"", filepath.Join(home, ".codex")},
		{"~", home},
		{"~/", home},
		{"~\\", home},
		{"~/custom", filepath.Join(home, "custom")},
	}
	for _, test := range tests {
		t.Run(strconv.Quote(test.configured), func(t *testing.T) {
			t.Setenv("CODEX_HOME", test.configured)
			got, err := CodexHome()
			require.NoError(t, err)
			assert.Equal(t, filepath.Clean(test.want), got)
		})
	}
}

func TestCodexHomePreservesWhitespaceInOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	configured := "  codex home  "
	t.Setenv("CODEX_HOME", configured)
	cwd, err := os.Getwd()
	require.NoError(t, err)

	got, err := CodexHome()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(cwd, configured), got)
}

func TestCodexHomeResolvesRelativeOverrideFromWorkingDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", "project-local-codex")
	cwd, err := os.Getwd()
	require.NoError(t, err)

	got, err := CodexHome()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(cwd, "project-local-codex"), got)
}

func TestCodexHomeDoesNotRequireHomeForSelfContainedOverride(t *testing.T) {
	t.Setenv("HOME", "")
	absolute := filepath.Join(t.TempDir(), "codex")
	t.Setenv("CODEX_HOME", absolute)
	got, err := CodexHome()
	require.NoError(t, err)
	assert.Equal(t, absolute, got)

	t.Setenv("CODEX_HOME", "project-local-codex")
	cwd, err := os.Getwd()
	require.NoError(t, err)
	got, err = CodexHome()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(cwd, "project-local-codex"), got)
}

func TestDetectCodexBinary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stubCodexLookPath(t, "/usr/local/bin/codex", nil)

	assert.True(t, DetectCodex())
	assert.Equal(t, "/usr/local/bin/codex", FindCodexBinary())
}

func TestDetectCodexCustomHomeWithoutBinary(t *testing.T) {
	home := t.TempDir()
	customHome := filepath.Join(t.TempDir(), "custom-codex")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", customHome)
	stubCodexLookPath(t, "", exec.ErrNotFound)

	assert.False(t, DetectCodex())
	require.NoError(t, os.MkdirAll(customHome, 0o755))
	assert.True(t, DetectCodex())
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

func TestCheckCodexPluginVersionSkipsMissingPlugin(t *testing.T) {
	stubCodexList(t, `{"installed":[],"available":[]}`, nil)

	check := CheckCodexPluginVersion()

	assert.Equal(t, "skip", check.Status)
	assert.Equal(t, "Skipped (plugin not installed)", check.Message)
	assert.Empty(t, check.Hint)
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
	resetRegistry()
	defer resetRegistry()

	stubCodexList(t, codexListFixture("0.7.2", true, true), nil)
	RegisterAgent(AgentInfo{
		Name:   "Codex",
		ID:     "codex",
		Detect: DetectCodex,
		Checks: func() []*StatusCheck { return []*StatusCheck{CheckCodexPlugin()} },
	})

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

// TestRunCodexCommandOutlivingGrandchild pins the deadline that ten minutes of
// a hung `basecamp doctor` proved was not being enforced.
//
// The stub above replaces runCodexCommand, so nothing else here exercises the
// real one. This does. It stands in for the shape codex actually ships as on
// some machines — a wrapper script that backgrounds a longer-lived process —
// where canceling the context kills the wrapper but the grandchild keeps the
// inherited stdout pipe open. Without cmd.WaitDelay, Wait blocks on that pipe
// for as long as the grandchild lives, and the query timeout means nothing.
func TestRunCodexCommandOutlivingGrandchild(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}

	// The grandchild has to outlive the deadline by a wide margin, or the test
	// passes on the sleep ending rather than on WaitDelay working. That makes
	// it our job to reap it: WaitDelay closes the inherited pipe, it does not
	// kill the process, which is reparented to init and would otherwise sit
	// there for two minutes accumulating one orphan per `bin/ci`.
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	script := "sleep 120 & echo $! > " + pidFile + "; exit 0"

	t.Cleanup(func() {
		raw, readErr := os.ReadFile(pidFile) //nolint:gosec // G304: path is this test's own TempDir
		if readErr != nil {
			return
		}
		pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
		if convErr != nil {
			return
		}
		if proc, findErr := os.FindProcess(pid); findErr == nil {
			_ = proc.Kill()
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		_, _ = runCodexCommand(ctx, sh, "-c", script)
	}()

	select {
	case <-done:
		// The call must return on its own deadline, not the grandchild's.
		assert.Less(t, time.Since(start), 30*time.Second,
			"runCodexCommand blocked on a pipe held open by a surviving grandchild")
	case <-time.After(30 * time.Second):
		t.Fatal("runCodexCommand did not return: WaitDelay is not bounding Wait")
	}
}
