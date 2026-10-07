//go:build unix

package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeWorker writes a program that answers a preflight as a worker would:
// its version, a help listing flags, and a login status.
func fakeWorker(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "worker")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o700))
	return path
}

const fakeWorkerAnswers = `
case "$1" in
  --version) echo "2.1.283 (Claude Code)" ;;
  --help) echo "Usage: worker [options]"; echo "  -p, --print"; echo "  --allowed-tools <tools...>"; echo "  --tools <tools...>" ;;
  auth) if [ -n "$LOGGED_IN" ]; then echo '{"loggedIn":true,"email":"someone@example.com"}'; else echo '{"loggedIn":false}'; exit 1; fi ;;
esac
`

func testProbe(binary string, env ...string) WorkerProbe {
	return WorkerProbe{
		Product:  "Claude Code",
		Binary:   binary,
		Env:      append([]string{"PATH=/usr/bin:/bin"}, env...),
		Help:     []string{"--help"},
		Flags:    []string{"-p", "--tools", "--allowed-tools"},
		Update:   "Update Claude Code: claude update",
		Login:    []string{"auth", "status", "--json"},
		LoggedIn: func(r ProbeResult) (bool, bool) { return testLoggedIn(r) },
		LoginFix: "run `claude` and log in",
	}
}

func testLoggedIn(r ProbeResult) (bool, bool) {
	var s struct {
		LoggedIn *bool `json:"loggedIn"`
	}
	if json.Unmarshal([]byte(r.Stdout), &s) != nil || s.LoggedIn == nil {
		return false, false
	}
	return *s.LoggedIn, true
}

func TestPreflightPassesAWorkerThatStartsKnowsItsFlagsAndIsLoggedIn(t *testing.T) {
	p := testProbe(fakeWorker(t, fakeWorkerAnswers), "LOGGED_IN=1").Check(context.Background())
	_, failed := p.Failed()
	assert.False(t, failed)
	assert.Equal(t, "2.1.283", p.Version)
	require.Len(t, p.Checks, 3)
	for _, c := range p.Checks {
		assert.Equal(t, PreflightPass, c.Status, c.Name)
		assert.NotContains(t, c.Message, "someone@example.com", "nothing the login answer says about the person is repeated")
	}
}

func TestPreflightRunsTheWorkerWithTheSessionsEnvironmentOnly(t *testing.T) {
	t.Setenv("LOGGED_IN", "1")
	p := testProbe(fakeWorker(t, fakeWorkerAnswers)).Check(context.Background())
	c, failed := p.Failed()
	require.True(t, failed, "the connector's own environment is not the worker's")
	assert.Equal(t, PreflightLogin, c.Name)
}

func TestPreflightSaysWhyAWorkerCannotStart(t *testing.T) {
	cases := map[string]struct {
		binary  string
		message string
		hint    string
	}{
		"not installed": {
			binary:  "basecamp-connect-no-such-worker",
			message: "Claude Code isn't installed here: basecamp-connect-no-such-worker is not on the PATH the connector starts with",
			hint:    "Install Claude Code, or put basecamp-connect-no-such-worker on the PATH the connector starts with.",
		},
		"a launcher whose target is gone": {
			binary:  fakeWorker(t, `exec "$HOME/.local/share/mise/installs/claude/latest/claude" "$@"`),
			message: "The %s on your PATH is a launcher that couldn't find Claude Code",
		},
		"a worker that fails": {
			binary:  fakeWorker(t, `echo "config is broken" >&2; exit 3`),
			message: "Claude Code couldn't start: `%s --version` exited with status 3 (config is broken)",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := testProbe(tc.binary, "HOME="+t.TempDir()).Check(context.Background())
			require.Len(t, p.Checks, 1, "nothing else is asked of a worker that did not start")
			c, failed := p.Failed()
			require.True(t, failed)
			assert.Equal(t, PreflightStarts, c.Name)
			assert.Contains(t, c.Message, sprintfIf(tc.message, tc.binary))
			if tc.hint != "" {
				assert.Equal(t, tc.hint, c.Hint)
			}
			assert.Empty(t, p.Version)
		})
	}
}

func TestPreflightNamesTheVersionTooOldForTheConnector(t *testing.T) {
	probe := testProbe(fakeWorker(t, fakeWorkerAnswers), "LOGGED_IN=1")
	probe.Flags = append(probe.Flags, "--permission-prompts", "--strict-mcp-config")
	c, failed := probe.Check(context.Background()).Failed()
	require.True(t, failed)
	assert.Equal(t, PreflightFlags, c.Name)
	assert.Equal(t, "Claude Code 2.1.283 is too old for the connector — update it", c.Message)
	assert.Equal(t, "It doesn't know --permission-prompts, --strict-mcp-config. Update Claude Code: claude update", c.Hint)
}

func TestPreflightSaysAWorkerIsLoggedOut(t *testing.T) {
	c, failed := testProbe(fakeWorker(t, fakeWorkerAnswers)).Check(context.Background()).Failed()
	require.True(t, failed)
	assert.Equal(t, PreflightLogin, c.Name)
	assert.Equal(t, "Claude Code is logged out on this computer — run `claude` and log in", c.Message)

	probe := testProbe(fakeWorker(t, fakeWorkerAnswers))
	probe.LoginWarns = true
	p := probe.Check(context.Background())
	_, failed = p.Failed()
	assert.False(t, failed, "a login the worker can only guess at never stops anything")
	assert.Equal(t, PreflightWarn, p.Checks[2].Status)
}

func TestPreflightCannotTellALoginItCannotRead(t *testing.T) {
	probe := testProbe(fakeWorker(t, `case "$1" in --version) echo 1.0.0 ;; --help) echo "-p --tools --allowed-tools" ;; *) echo "unknown command" >&2; exit 2 ;; esac`))
	p := probe.Check(context.Background())
	_, failed := p.Failed()
	assert.False(t, failed)
	assert.Equal(t, PreflightWarn, p.Checks[2].Status)
	assert.Contains(t, p.Checks[2].Message, "couldn't tell whether Claude Code is logged in")
}

func TestMissingFlagsMatchesWholeFlagsOnly(t *testing.T) {
	help := "  -p, --print\n  --allowed-tools <tools...>\n  --mcp-config <configs...>\n  -c, --config <key=value>"
	assert.Equal(t, []string{"--tools", "--mcp"}, MissingFlags(help, []string{"-p", "--tools", "--allowed-tools", "--mcp", "--mcp-config", "-c"}))
}

func TestFlagsOfSkipsArguments(t *testing.T) {
	assert.Equal(t, []string{"--json", "-c", "--disable"},
		FlagsOf([]string{"exec", "--json", "-c", "a=1", "-c", "b=2", "--disable", "x", "-"}))
}

func sprintfIf(format, arg string) string {
	if strings.Contains(format, "%s") {
		return fmt.Sprintf(format, arg)
	}
	return format
}

// A probe that times out ends its whole process tree: a launcher that started
// the real worker as a child and waited must not leave that child running
// (Codex on #794).
func TestRunProbeEndsTheLaunchersChildrenWhenItTimesOut(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	launcher := filepath.Join(dir, "claude")
	require.NoError(t, os.WriteFile(launcher, []byte("#!/bin/sh\nsleep 60 &\necho $! > "+pidFile+"\nwait\n"), 0o700))

	// Allow the shell time to start before timing out the waiting launcher.
	// macOS process startup can itself exceed 300 ms under the full test suite.
	r := RunProbe(context.Background(), Command{Path: launcher, Dir: dir}, 5*time.Second)
	require.True(t, r.TimedOut)
	raw, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	assert.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, 5*time.Second, 20*time.Millisecond, "the launcher's child is gone")
}

// A program that is on PATH but can't be run — its interpreter is gone — is
// not reported as missing from PATH (Codex on #794).
func TestRunProbeTellsAMissingInterpreterFromAMissingProgram(t *testing.T) {
	dir := t.TempDir()
	launcher := filepath.Join(dir, "claude")
	require.NoError(t, os.WriteFile(launcher, []byte("#!/nonexistent/interpreter\n"), 0o700))

	r := RunProbe(context.Background(), Command{Path: launcher, Dir: dir}, 5*time.Second)
	assert.False(t, r.NotFound, "the program is there")
	require.Error(t, r.Err)

	missing := RunProbe(context.Background(), Command{Path: filepath.Join(dir, "absent"), Dir: dir}, 5*time.Second)
	assert.True(t, missing.NotFound)
}

// A launcher that leaves a child behind and exits on its own still has that
// child ended when the probe returns (Codex on #794).
func TestRunProbeEndsChildrenALauncherLeftWhenItExited(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	launcher := filepath.Join(dir, "claude")
	require.NoError(t, os.WriteFile(launcher, []byte("#!/bin/sh\nsleep 60 &\necho $! > "+pidFile+"\necho 2.1.0\n"), 0o700))

	r := RunProbe(context.Background(), Command{Path: launcher, Dir: dir}, 10*time.Second)
	require.False(t, r.TimedOut)
	raw, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	assert.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, 5*time.Second, 20*time.Millisecond, "the child is gone")
}
