//go:build unix

package claude

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// The preflight asks Claude Code's help for every flag a session passes, new
// or resumed, so a flag added to Args is asked about without anyone
// remembering to.
func TestSessionFlagsAreEveryFlagASessionPasses(t *testing.T) {
	flags := SessionFlags(policy{}, "opus")
	for _, resume := range []bool{false, true} {
		args, err := Args(driver.SessionConfig{Policy: policy{}}, "00000000-0000-4000-8000-000000000000", resume, "mcp.json", "opus")
		require.NoError(t, err)
		for _, f := range driver.FlagsOf(args) {
			assert.Contains(t, flags, f)
		}
	}
	assert.Contains(t, flags, "--session-id")
	assert.Contains(t, flags, "--resume")
	assert.Contains(t, flags, "--model")
	assert.NotContains(t, SessionFlags(policy{}, ""), "--model", "a model is asked about only when one is passed")
}

// The preflight starts the claude a session starts, with a session's
// environment: CLAUDE_CONFIG_DIR, which decides which login Claude Code
// reads, reaches it, and the connector's other variables do not. Its login is
// asked with the host's settings off, as a session runs.
func TestPreflightRunsClaudeAsASessionWould(t *testing.T) {
	dir := t.TempDir()
	seen := filepath.Join(dir, "env")
	exe := filepath.Join(dir, "claude")
	script := `#!/bin/sh
env > ` + seen + `
case "$1" in
  --version) echo "2.1.283 (Claude Code)" ;;
  --help) echo "-p --input-format --output-format --verbose --setting-sources --permission-mode --permission-prompts --tools --allowed-tools --strict-mcp-config --mcp-config --session-id --resume" ;;
  --setting-sources) if [ "$2" = "" ] && [ "$3" = auth ] && [ "$CLAUDE_CONFIG_DIR" = /config ]; then echo '{"loggedIn":true}'; else echo '{"loggedIn":false}'; exit 1; fi ;;
  auth) echo '{"loggedIn":false}'; exit 1 ;; # asked with the host's settings on
esac
`
	require.NoError(t, os.WriteFile(exe, []byte(script), 0o700))
	host := map[string]string{"PATH": "/usr/bin:/bin", "HOME": dir, "CLAUDE_CONFIG_DIR": "/config", "BASECAMP_TOKEN": "not-a-real-token"}
	d := New(Options{Binary: exe, Lookup: func(k string) (string, bool) { v, ok := host[k]; return v, ok }})

	p := d.Preflight(context.Background(), policy{})
	_, failed := p.Failed()
	assert.False(t, failed, "%+v", p.Checks)
	assert.Equal(t, Product, p.Product)
	assert.Equal(t, "2.1.283", p.Version)
	env, err := os.ReadFile(seen)
	require.NoError(t, err)
	assert.Contains(t, string(env), "CLAUDE_CONFIG_DIR=/config")
	assert.NotContains(t, string(env), "BASECAMP_TOKEN")

	delete(host, "CLAUDE_CONFIG_DIR")
	c, failed := d.Preflight(context.Background(), policy{}).Failed()
	require.True(t, failed)
	assert.Equal(t, "Claude Code is logged out on this computer — run `claude` and log in", c.Message)
}
