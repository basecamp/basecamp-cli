//go:build unix

package commands

import (
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/setup"
)

// Setup records a launcher word for word, keeps it on a run that does not
// mention it, replaces it whole, and removes it with --no-launcher.
func TestConnectSetupRecordsALauncher(t *testing.T) {
	s := startConnectSetupServer(t)
	out, err := runConnectSetupCmd(t, connectSetupApp(t, s, "agent"),
		"--operator", fmt.Sprint(setupOperatorPerson), serveArg(),
		"--launcher", "sandbox", "--launcher", "--profile", "--launcher", "two words")
	require.NoError(t, err, out)
	f, err := setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Equal(t, []string{"sandbox", "--profile", "two words"}, f.Launcher)

	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--trust", "operator")
	require.NoError(t, err, out)
	f, err = setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Equal(t, []string{"sandbox", "--profile", "two words"}, f.Launcher, "a run that does not mention it keeps it")

	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--launcher", "/opt/box/sandbox")
	require.NoError(t, err, out)
	f, err = setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Equal(t, []string{"/opt/box/sandbox"}, f.Launcher, "replaced whole")

	out, err = runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--no-launcher")
	require.NoError(t, err, out)
	f, err = setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Nil(t, f.Launcher)
}

// A launcher setup cannot record is refused before anything is written.
func TestConnectSetupRefusesABadLauncher(t *testing.T) {
	op := fmt.Sprint(setupOperatorPerson)
	for name, args := range map[string][]string{
		"a relative path":       {"--operator", op, serveArg(), "--launcher", "./sandbox"},
		"an empty word":         {"--operator", op, serveArg(), "--launcher", "sandbox", "--launcher", ""},
		"a control character":   {"--operator", op, serveArg(), "--launcher", "sandbox\x1b[2J"},
		"both set and removed":  {"--operator", op, serveArg(), "--launcher", "sandbox", "--no-launcher"},
		"an option for program": {"--operator", op, serveArg(), "--launcher=--rm"},
	} {
		t.Run(name, func(t *testing.T) {
			s := startConnectSetupServer(t)
			out, err := runConnectSetupCmd(t, connectSetupApp(t, s, "agent"), args...)
			require.Error(t, err, out)
			assertNotWritten(t, "agent")
		})
	}
}

// Passing a launcher is a scriptable setup, never the guided one, which
// would drop it.
func TestConnectSetupLauncherFlagsAreScriptable(t *testing.T) {
	for _, name := range []string{"launcher", "no-launcher"} {
		assert.True(t, slices.Contains(connectSetupPolicyFlags, name), name)
		assert.NotNil(t, newConnectSetupCmd().Flags().Lookup(name), name)
	}
}

// Show prints each word as it is passed, so a word holding a space reads as
// one word, and a control character never reaches the terminal.
func TestConnectShowDisplaysTheLauncherWordByWord(t *testing.T) {
	f := setup.New("agent")
	f.Launcher = []string{"sandbox", "two words"}
	d := connectShowDisplay("/tmp/connect.json", f, false)
	assert.Equal(t, `"sandbox" "two words"`, d["launcher"])

	md := connectShowDisplay("/tmp/connect.json", f, true)
	assert.Equal(t, "` sandbox ` ` two words `", md["launcher"])

	f.Launcher = nil
	assert.NotContains(t, connectShowDisplay("/tmp/connect.json", f, false), "launcher")
}

func TestConnectDoctorLauncherCheck(t *testing.T) {
	t.Cleanup(func() { connectLookPath = exec.LookPath })

	connectLookPath = func(file string) (string, error) {
		assert.Equal(t, "sandbox", file, "only the program is looked up")
		return "/home/me/.local/bin/sandbox", nil
	}
	c := launcherCheck("agent", []string{"sandbox", "two words"})
	assert.Equal(t, setup.StatusPass, c.Status)
	assert.Contains(t, c.Message, "sandbox 'two words'")
	assert.Contains(t, c.Message, "/home/me/.local/bin/sandbox")

	connectLookPath = func(string) (string, error) { return "", errors.New("executable file not found in $PATH") }
	c = launcherCheck("my agent", []string{"sandbox"})
	assert.Equal(t, setup.StatusFail, c.Status)
	assert.Contains(t, c.Message, "cannot be started here")
	assert.Equal(t, "Install it, or remove it: basecamp connect setup -P 'my agent' --no-launcher", c.Hint)
}
