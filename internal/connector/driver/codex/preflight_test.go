//go:build unix

package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

func TestSessionFlagsAreEveryFlagASessionPasses(t *testing.T) {
	args, err := Args(driver.SessionConfig{Policy: testPolicy{}}, "", "")
	require.NoError(t, err)
	assert.Equal(t, driver.FlagsOf(args), SessionFlags(testPolicy{}, ""))
	assert.Contains(t, SessionFlags(testPolicy{}, ""), "--strict-config")
}

// Codex's preflight starts it and asks `codex exec --help` for the session's
// flags. A login it cannot confirm is a warning, never a stop.
func TestPreflightChecksCodexWithoutBlockingOnItsLogin(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "codex")
	help := strings.Join(SessionFlags(testPolicy{}, ""), " ")
	script := `#!/bin/sh
case "$1 $2" in
  "--version ") echo "codex-cli 0.157.1" ;;
  "exec --help") echo "` + help + `" ;;
  "login status") echo "Not logged in" >&2; exit 1 ;;
esac
`
	require.NoError(t, os.WriteFile(exe, []byte(script), 0o700))
	d := New(Options{Binary: exe, Lookup: func(k string) (string, bool) {
		if k == "PATH" {
			return "/usr/bin:/bin", true
		}
		return "", false
	}})
	p := d.Preflight(context.Background(), testPolicy{})
	_, failed := p.Failed()
	assert.False(t, failed)
	assert.Equal(t, "0.157.1", p.Version)
	require.Len(t, p.Checks, 3)
	assert.Equal(t, driver.PreflightPass, p.Checks[1].Status)
	assert.Equal(t, driver.PreflightWarn, p.Checks[2].Status)
	assert.Equal(t, "Codex may be logged out on this computer — run `codex login`", p.Checks[2].Message)
}
