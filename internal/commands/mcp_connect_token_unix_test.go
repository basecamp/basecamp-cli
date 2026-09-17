//go:build unix

package commands

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tokenPipe hands the token over the way the connector does: the read end of
// a pipe the child inherits, the write end written and closed. It returns the
// descriptor number to pass, which is the command's to close.
func tokenPipe(t *testing.T, token string) int {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, err = w.WriteString(token)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	// A descriptor of its own, so the test's *os.File never closes the one
	// the command is handed.
	fd, err := syscall.Dup(int(r.Fd()))
	require.NoError(t, err)
	require.NoError(t, r.Close())
	// The command closes it once it reads the token; a case that never gets
	// that far leaves it to this.
	t.Cleanup(func() { _ = syscall.Close(fd) })
	return fd
}

func fdOpen(fd int) bool {
	_, err := fcntlGetFD(fd)
	return err == nil
}

// fdIdentity is what a descriptor refers to. A closed number is reused by the
// next open, so "is fd N still the pipe" is asked of the file, not the number.
func fdIdentity(t *testing.T, fd int) (dev, ino uint64, open bool) {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return 0, 0, false
	}
	return uint64(st.Dev), uint64(st.Ino), true //nolint:unconvert // Dev's width differs by platform
}

// The token never exists where anything else can read it: not at a path, not
// in argv, not in the server's environment, and not on the descriptor it came
// in on once startup is over.
func TestMCPCommandTokenLeavesNoTrace(t *testing.T) {
	app, dir, grant, _ := connectMCPApp(t, "999", unusedUpstream(t).URL)
	fd := tokenPipe(t, grant.Token+"\n")
	pipeDev, pipeIno, _ := fdIdentity(t, fd)
	args := []string{"--connect-state", dir, "--connect-token-fd", strconv.Itoa(fd)}

	session := runMCPCommandWithApp(t, app, args...)
	assert.Contains(t, toolNames(t, session), "basecamp_connect", "the token came through the descriptor")

	for _, arg := range args {
		assert.NotContains(t, arg, grant.Token, "argv")
	}
	for _, kv := range os.Environ() {
		assert.NotContains(t, kv, grant.Token, "the server's environment")
	}
	if dev, ino, open := fdIdentity(t, fd); open {
		assert.False(t, dev == pipeDev && ino == pipeIno, "the descriptor the token came in on is closed once it is read")
	}
	stateHome := os.Getenv("XDG_STATE_HOME")
	require.NoError(t, filepath.WalkDir(stateHome, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		assert.False(t, bytes.Contains(data, []byte(grant.Token)), "no file holds the token: %s", path)
		return nil
	}))
}

// The environment is not a way in: a token left there is refused, and taken
// out, so no one is led to hand it over that way.
func TestMCPCommandRefusesATokenInTheEnvironment(t *testing.T) {
	app, dir, grant, _ := connectMCPApp(t, "999", "https://3.basecampapi.com")
	t.Setenv("BASECAMP_CONNECT_TASK_TOKEN", grant.Token)
	fd := tokenPipe(t, grant.Token)

	err := executeMCPCommand(t, app, "--connect-state", dir, "--connect-token-fd", strconv.Itoa(fd))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--connect-token-fd")
	assert.Empty(t, os.Getenv("BASECAMP_CONNECT_TASK_TOKEN"))
}

// A descriptor that is not a pipe or a socket is refused and left alone: a
// file would be the token at a path, and a wrong number could be one the
// process already uses.
func TestMCPCommandLeavesADescriptorThatIsNotAPipeAlone(t *testing.T) {
	app, dir, grant, _ := connectMCPApp(t, "999", "https://3.basecampapi.com")
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte(grant.Token), 0o600))
	file, err := os.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })

	err = executeMCPCommand(t, app, "--connect-state", dir, "--connect-token-fd", strconv.Itoa(int(file.Fd())))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a pipe or a socket")
	assert.True(t, fdOpen(int(file.Fd())), "a descriptor that is not the token's is not closed")
}

// Not only in connect mode: any server started with a token in the
// environment takes it out and refuses to start.
func TestMCPCommandRefusesATokenInTheEnvironmentWithoutConnectState(t *testing.T) {
	app, _, grant, _ := connectMCPApp(t, "999", "https://3.basecampapi.com")
	t.Setenv("BASECAMP_CONNECT_TASK_TOKEN", grant.Token)

	err := executeMCPCommand(t, app)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BASECAMP_CONNECT_TASK_TOKEN")
	assert.Empty(t, os.Getenv("BASECAMP_CONNECT_TASK_TOKEN"))
}

func TestMCPCommandRefusesABadTokenDescriptor(t *testing.T) {
	app, dir, _, _ := connectMCPApp(t, "999", "https://3.basecampapi.com")
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no descriptor":               {[]string{"--connect-state", dir}, "--connect-token-fd"},
		"stdin is the MCP wire":       {[]string{"--connect-state", dir, "--connect-token-fd", "0"}, "3 or above"},
		"stdout":                      {[]string{"--connect-state", dir, "--connect-token-fd", "1"}, "3 or above"},
		"not open":                    {[]string{"--connect-state", dir, "--connect-token-fd", "987"}, "it is not open"},
		"descriptor alone":            {[]string{"--connect-token-fd", "5"}, "--connect-state"},
		"a negative descriptor alone": {[]string{"--connect-token-fd", "-1"}, "--connect-state"},
		"empty":                       {[]string{"--connect-state", dir, "--connect-token-fd", strconv.Itoa(tokenPipe(t, "  \n"))}, "empty"},
		"too long":                    {[]string{"--connect-state", dir, "--connect-token-fd", strconv.Itoa(tokenPipe(t, strings.Repeat("x", maxTaskTokenBytes+1)))}, "not a task token"},
	} {
		t.Run(name, func(t *testing.T) {
			err := executeMCPCommand(t, app, tc.args...)
			require.Error(t, err)
			assert.True(t, strings.Contains(err.Error(), tc.want), "%q does not say %q", err.Error(), tc.want)
		})
	}
}

// heldPipe is a token pipe whose write end the test keeps open, as a write
// end leaked into some other process would be.
func heldPipe(t *testing.T, written string) int {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	_, err = w.WriteString(written)
	require.NoError(t, err)
	fd, err := syscall.Dup(int(r.Fd()))
	require.NoError(t, err)
	require.NoError(t, r.Close())
	t.Cleanup(func() { _ = syscall.Close(fd) })
	return fd
}

// A write end left open somewhere does not hang startup: the token ends at its
// newline, and a token that never arrives is a refusal within the timeout.
func TestMCPCommandDoesNotWaitOnAWriteEndLeftOpen(t *testing.T) {
	app, dir, grant, _ := connectMCPApp(t, "999", unusedUpstream(t).URL)

	session := runMCPCommandWithApp(t, app, "--connect-state", dir, "--connect-token-fd", strconv.Itoa(heldPipe(t, grant.Token+"\n")))
	assert.Contains(t, toolNames(t, session), "basecamp_connect", "the newline ends the token")

	previous := taskTokenReadTimeout
	taskTokenReadTimeout = 200 * time.Millisecond
	t.Cleanup(func() { taskTokenReadTimeout = previous })
	started := time.Now()
	err := executeMCPCommand(t, app, "--connect-state", dir, "--connect-token-fd", strconv.Itoa(heldPipe(t, grant.Token)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no task token arrived")
	assert.Less(t, time.Since(started), 5*time.Second)
}

// A descriptor number no descriptor could have is refused where every other
// bad one is: at the read, by asking the operating system about it.
func TestABadDescriptorNumberIsRefusedAtTheRead(t *testing.T) {
	_, err := readTaskToken(99999999)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not open")
}

// And the command reads a whitespace state directory as absent as well, so it
// refuses the descriptor rather than reporting a token that was never read.
func TestABlankStateDirectoryIsNoStateDirectory(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "test-token")
	app := setupMCPTestApp(t, "999", "https://3.basecampapi.com")
	fd := tokenPipe(t, "token\n")
	dev, ino, _ := fdIdentity(t, fd)

	err := executeMCPCommand(t, app, "--connect-state", "   ", "--connect-token-fd", strconv.Itoa(fd))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--connect-token-fd is only for a server started with --connect-state")
	nowDev, nowIno, open := fdIdentity(t, fd)
	assert.True(t, open && nowDev == dev && nowIno == ino, "and the descriptor was not touched")
}

// Startup keeps the token descriptor out of anything the process starts, and
// reads nothing: the command reads it once cobra has accepted the invocation,
// so a one-shot pipe is never drained for a run that never serves.
func TestPrepareConnectTokenMarksTheDescriptorCloseOnExec(t *testing.T) {
	fd := tokenPipe(t, "a-task-token\n")
	before, err := fcntlGetFD(fd)
	require.NoError(t, err)
	require.Zero(t, before&unix.FD_CLOEXEC, "inherited descriptors arrive without it")

	PrepareConnectToken([]string{"mcp", "--connect-state", "/x", "--connect-token-fd", strconv.Itoa(fd)})

	after, err := fcntlGetFD(fd)
	require.NoError(t, err)
	assert.NotZero(t, after&unix.FD_CLOEXEC, "no child of this process inherits it")
	assert.True(t, fdOpen(fd), "and it is still there for the command to read")
}

func TestPrepareConnectTokenLooksOnlyWhereItShould(t *testing.T) {
	fd := tokenPipe(t, "token\n")
	for name, args := range map[string][]string{
		"another command": {"search", "--connect-token-fd", strconv.Itoa(fd)},
		"no flag":         {"mcp", "--connect-state", "/x"},
		"standard input":  {"mcp", "--connect-token-fd", "0"},
		"not a number":    {"mcp", "--connect-token-fd", "three"},
		"nothing after":   {"mcp", "--connect-token-fd"},
	} {
		t.Run(name, func(t *testing.T) {
			_, found := connectTokenFDArg(args)
			assert.False(t, found)
		})
	}
	for name, args := range map[string][]string{
		"a value of its own": {"mcp", "--connect-token-fd", strconv.Itoa(fd)},
		"joined with an =":   {"mcp", "--connect-token-fd=" + strconv.Itoa(fd)},
		"after a root flag":  {"--json", "mcp", "--connect-token-fd", strconv.Itoa(fd)},
	} {
		t.Run(name, func(t *testing.T) {
			got, found := connectTokenFDArg(args)
			require.True(t, found)
			assert.Equal(t, fd, got)
		})
	}
}

// A stale token in the environment is taken out at startup, before the hooks
// that could pass it to a child, and the command then refuses to serve.
func TestPrepareConnectTokenTakesAStaleEnvironmentTokenOut(t *testing.T) {
	t.Setenv("BASECAMP_CONNECT_TASK_TOKEN", "stale")
	t.Cleanup(func() { connectTokenEnvRefused = false })

	PrepareConnectToken([]string{"mcp", "--connect-state", "/x"})

	assert.Empty(t, os.Getenv("BASECAMP_CONNECT_TASK_TOKEN"))
	assert.True(t, connectTokenEnvRefused)
}
