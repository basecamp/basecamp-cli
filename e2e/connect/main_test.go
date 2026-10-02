//go:build linux || darwin

// Package connect runs `basecamp connect` end to end: the real binary, as
// subprocesses, against the fake Basecamp in internal/connector/fakebasecamp,
// or, under `make test-connect-dev`, against a local Basecamp (see Target).
//
// Against the fake, every test sets the agent up the way a person does, with
// `basecamp auth agent connect` and `basecamp connect setup`, in a home of
// its own, and then runs `basecamp connect` and reads what it writes. Its stdout is read
// as the protocol it is (typed NDJSON lines), its stderr as the slog lines
// they are, both continuously, so the process never blocks on a full pipe.
// Nothing waits on a timer: a test waits for a line, or for the fake to have
// seen something, with a deadline that only bounds a failure.
//
// It runs on the platforms the connector runs on.
package connect

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// buildDir holds the binary TestMain's run builds, removed when it ends.
var buildDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "basecamp-connect-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e/connect:", err)
		os.Exit(1)
	}
	buildDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

var (
	buildOnce sync.Once
	built     string
	errBuild  error
)

// binary is the basecamp binary under test, built the first time a test
// asks for it: listing the tests, or running none of them, builds nothing.
// It is built the way this test binary was, with -race when this is a race
// build and with -tags dev when this is a dev build, so a race in the
// connector fails the test that provoked it.
func binary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		built = filepath.Join(buildDir, "basecamp")
		args := []string{"build", "-o", built}
		if raceBuild {
			args = append(args, "-race")
		}
		if devBuild {
			args = append(args, "-tags", "dev")
		}
		args = append(args, "github.com/basecamp/basecamp-cli/cmd/basecamp")
		// The first build under -race compiles the standard library again;
		// this only bounds a build that hangs.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", args...)
		if !raceBuild {
			// As the release builds it. The race detector needs cgo.
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			errBuild = fmt.Errorf("go %v: %w\n%s", args, err, out)
		}
	})
	if errBuild != nil {
		t.Fatal(errBuild)
	}
	return built
}
