//go:build linux || darwin

package connect

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/fakebasecamp"
)

// waitFor bounds every wait here: a command to finish, a line to be
// written, the fake to see a request. It only bounds a failure; nothing
// waits it out. It covers a reconnect's backoff, which starts at a second
// of full jitter, under the race detector on a loaded runner.
const waitFor = 30 * time.Second

// Agent is the profile every test connects the default world's agent as.
const Agent = "agent"

// Harness is one test's Basecamp, and a home of its own to run the CLI in:
// its config, its credentials and the connector's state are the test's
// alone.
type Harness struct {
	t *testing.T
	// Fake is the Basecamp the CLI talks to.
	Fake *fakebasecamp.Server
	// Home is HOME; the XDG directories are under it.
	Home string

	env []string
}

// NewHarness serves world on the fake, and makes a home that points the CLI
// at it.
func NewHarness(t *testing.T, world *fakebasecamp.World, opts ...fakebasecamp.Option) *Harness {
	t.Helper()
	s := fakebasecamp.Start(t, world, opts...)
	home := t.TempDir()
	return &Harness{t: t, Fake: s, Home: home, env: cliEnv(home, s.URL())}
}

// cliEnv is the whole environment a CLI subprocess gets. It starts empty
// rather than from this process's, so nothing in the environment the tests
// run in (a BASECAMP_TOKEN, a BASECAMP_NONINTERACTIVE, a profile, a real
// config directory) reaches the CLI.
func cliEnv(home, baseURL string) []string {
	env := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"BASECAMP_NO_KEYRING=1",
		"BASECAMP_NO_UPDATE_CHECK=1",
		"BASECAMP_BASE_URL=" + baseURL,
		"BASECAMP_OAUTH_ISSUER=" + baseURL,
	}
	// What the binary itself needs from the host: a PATH, a temp directory,
	// and the race detector's options when the run sets them.
	for _, key := range []string{"PATH", "TMPDIR", "GORACE"} {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return env
}

// Result is how a CLI command that ran to completion ended.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// Run runs one CLI command to completion in the harness's home and returns
// how it ended. It fails the test only when the command could not be run or
// did not finish.
func (h *Harness) Run(t *testing.T, args ...string) Result {
	t.Helper()
	bin := binary(t) // before the clock starts: the first build can be slow
	ctx, cancel := context.WithTimeout(t.Context(), waitFor)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = h.env
	cmd.Dir = h.Home
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	requireNoRace(t, "basecamp "+strings.Join(args, " "), res.Stderr)
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		t.Fatalf("basecamp %s did not finish within %s\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), waitFor, res.Stdout, res.Stderr)
	case errors.As(err, &exit):
		res.ExitCode = exit.ExitCode()
	case err != nil:
		t.Fatalf("basecamp %s: %v", strings.Join(args, " "), err)
	}
	return res
}

// MustRun runs one CLI command and requires that it succeeds.
func (h *Harness) MustRun(t *testing.T, args ...string) Result {
	t.Helper()
	res := h.Run(t, args...)
	require.Zero(t, res.ExitCode, "basecamp %s\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), res.Stdout, res.Stderr)
	return res
}

// Setup connects the default world's agent as the Agent profile and sets
// it up to serve the default project for the operator, the way a person
// does: the connection ceremony with no browser, which the fake approves as
// soon as it is asked, then `connect setup`.
func (h *Harness) Setup(t *testing.T) {
	t.Helper()
	h.MustRun(t, "auth", "agent", "connect", "-P", Agent, "--no-browser")
	h.MustRun(t, "connect", "setup", "-P", Agent,
		"--operator", strconv.FormatInt(fakebasecamp.OperatorID, 10),
		"--serve", strconv.FormatInt(fakebasecamp.ProjectID, 10),
		"--json")
}

// ConnectJSON is where `connect setup` writes the Agent profile's
// connect.json in this home.
func (h *Harness) ConnectJSON() string {
	return filepath.Join(h.Home, ".config", "basecamp", "connect", Agent, "connect.json")
}

// requireNoRace fails the test when a race-built subprocess reported a
// race. The process may still exit as it would have, so its stderr is the
// only place to see it.
func requireNoRace(t *testing.T, what, stderr string) {
	t.Helper()
	if strings.Contains(stderr, "WARNING: DATA RACE") {
		t.Errorf("%s reported a data race:\n%s", what, stderr)
	}
}
