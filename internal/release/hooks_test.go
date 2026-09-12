package release_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hookCommands returns every leaf command string in hooks/hooks.json.
func hookCommands(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), "hooks", "hooks.json"))
	if os.IsNotExist(err) {
		t.Skip("hooks/hooks.json not present (expected until hooks ship)")
	}
	require.NoError(t, err)

	var config struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(data, &config))

	var commands []string
	for _, matchers := range config.Hooks {
		for _, matcher := range matchers {
			for _, hook := range matcher.Hooks {
				commands = append(commands, hook.Command)
			}
		}
	}
	require.NotEmpty(t, commands)
	return commands
}

// writeExecutable writes a shell script and marks it runnable.
func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o755))
}

// TestHookCommandsResolveBasecamp executes every hooks.json command against
// fake basecamp/mise binaries to prove the resolution contract the string
// matcher in TestHooksFileCommandsInvokeBasecamp cannot see: basecamp on PATH
// wins (mise is never consulted), a mise-only install is resolved through
// `mise which` and executed, and when neither is available the hook fails open
// with exit 0 so it never blocks a tool call.
func TestHookCommandsResolveBasecamp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook commands are POSIX shell")
	}

	for _, command := range hookCommands(t) {
		name := command
		if idx := strings.Index(command, "agent-hook "); idx >= 0 {
			name = command[idx:]
		}

		t.Run(name, func(t *testing.T) {
			// A basecamp reachable on PATH, and a different one reachable only
			// by resolving through mise, each record which of them ran.
			pathBin := filepath.Join(t.TempDir(), "path")
			miseBin := filepath.Join(t.TempDir(), "mise")
			miseTool := filepath.Join(t.TempDir(), "tool")
			writeExecutable(t, filepath.Join(pathBin, "basecamp"),
				"#!/bin/sh\necho \"path $*\" >> \"$MARKER\"\n")
			writeExecutable(t, filepath.Join(miseTool, "basecamp"),
				"#!/bin/sh\necho \"mise $*\" >> \"$MARKER\"\n")
			writeExecutable(t, filepath.Join(miseBin, "mise"),
				"#!/bin/sh\nwhile [ $# -gt 0 ] && [ \"$1\" != which ]; do shift; done\n"+
					"[ \"$1\" = which ] && [ \"$2\" = basecamp ] && echo \""+filepath.Join(miseTool, "basecamp")+"\" && exit 0\nexit 1\n")

			run := func(t *testing.T, path string) (string, error) {
				marker := filepath.Join(t.TempDir(), "marker")
				cmd := exec.Command("/bin/sh", "-c", command)
				cmd.Env = []string{"HOME=" + t.TempDir(), "MARKER=" + marker, "PATH=" + path}
				err := cmd.Run()
				out, readErr := os.ReadFile(marker)
				if os.IsNotExist(readErr) {
					return "", err
				}
				require.NoError(t, readErr)
				return string(out), err
			}

			t.Run("basecamp on PATH wins over mise", func(t *testing.T) {
				out, err := run(t, pathBin+":"+miseBin+":/usr/bin:/bin")
				require.NoError(t, err)
				assert.Contains(t, out, "path agent-hook", "PATH basecamp must run")
				assert.NotContains(t, out, "mise agent-hook", "mise must not be consulted")
			})

			t.Run("mise resolves a basecamp that is off PATH", func(t *testing.T) {
				out, err := run(t, miseBin+":/usr/bin:/bin")
				require.NoError(t, err)
				assert.Contains(t, out, "mise agent-hook", "mise-resolved basecamp must run")
			})

			t.Run("neither available fails open", func(t *testing.T) {
				out, err := run(t, "/usr/bin:/bin")
				assert.NoError(t, err, "hook must exit 0 when basecamp cannot be resolved")
				assert.Empty(t, out, "no basecamp should run")
			})
		})
	}
}
