package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/harness"
)

// TestValidPluginScope guards the argv-injection whitelist: scope values come
// from installed_plugins.json (not first-party), so a "-"-leading value must
// not reach `claude plugin uninstall --scope <scope>`.
func TestValidPluginScope(t *testing.T) {
	valid := []string{"user", "project", "local"}
	for _, s := range valid {
		if !validPluginScope(s) {
			t.Errorf("validPluginScope(%q) = false, want true", s)
		}
	}

	// "global" is not a scope `claude plugin uninstall --scope` accepts, so it
	// must be treated as invalid: keeping it valid would make a scoped uninstall
	// fail silently while suppressing the unscoped fallback, stranding the plugin.
	invalid := []string{"", "global", "-rf", "--force", "User", "system", "/etc", "user ", " user"}
	for _, s := range invalid {
		if validPluginScope(s) {
			t.Errorf("validPluginScope(%q) = true, want false", s)
		}
	}
}

// stubClaudeUninstall writes a stub `claude` binary that logs every invocation
// to logFile and exits with a non-zero code for scoped uninstall calls when
// failScoped is true (otherwise they succeed). Unscoped uninstalls succeed once
// per key then fail (hard-coded), so the retry loop runs once before ending,
// mirroring real "entry gone" behavior. Returns its absolute path.
func stubClaudeUninstall(t *testing.T, failScoped bool) string {
	t.Helper()
	dir := t.TempDir()
	logFile := filepath.Join(dir, "calls.log")
	markerDir := filepath.Join(dir, "markers")
	require.NoError(t, os.MkdirAll(markerDir, 0o755))

	scopedExit := "0"
	if failScoped {
		scopedExit = "1"
	}
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> \"" + logFile + "\"\n" +
		"case \"$1 $2\" in\n" +
		"  \"plugin uninstall\")\n" +
		"    if [ \"$4\" = \"--scope\" ]; then exit " + scopedExit + "; fi\n" +
		// unscoped: succeed once per key, then fail so the retry loop ends.
		"    MARKER=\"" + markerDir + "/$3.removed\"\n" +
		"    if [ ! -f \"$MARKER\" ]; then > \"$MARKER\"; exit 0; fi\n" +
		"    exit 1\n" +
		"    ;;\n" +
		"  *) exit 0 ;;\n" +
		"esac\n"
	path := filepath.Join(dir, "claude")
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755)) //nolint:gosec // G306: test helper
	return path
}

func readClaudeCalls(t *testing.T, claudePath string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(filepath.Dir(claudePath), "calls.log"))
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return string(data)
}

// TestRemoveStaleClaudePluginsAllScopesInvalid verifies the YL7 fix: when every
// recorded scope fails validPluginScope (no scoped uninstall is attempted), we
// fall back to an unscoped removal so the plugin isn't silently left installed.
func TestRemoveStaleClaudePluginsAllScopesInvalid(t *testing.T) {
	claude := stubClaudeUninstall(t, false)
	plugins := []harness.StalePlugin{{Key: "basecamp@37signals", Scopes: []string{"-rf", "--force"}}}

	removed, scopes := removeStaleClaudePlugins(context.Background(), claude, plugins)

	calls := readClaudeCalls(t, claude)
	assert.NotContains(t, calls, "--scope", "no scoped uninstall should be attempted for invalid scopes")
	assert.Contains(t, calls, "plugin uninstall basecamp@37signals", "unscoped fallback should run")
	assert.Equal(t, []string{"basecamp@37signals"}, removed)
	assert.Empty(t, scopes)
}

// TestRemoveStaleClaudePluginsGlobalScopeFallsBack verifies that a stale entry
// whose only recorded scope is "global" (which `claude plugin uninstall --scope`
// rejects) is treated as all-invalid, so the unscoped fallback removes it rather
// than leaving it silently installed behind a failing scoped uninstall.
func TestRemoveStaleClaudePluginsGlobalScopeFallsBack(t *testing.T) {
	claude := stubClaudeUninstall(t, false)
	plugins := []harness.StalePlugin{{Key: "basecamp@37signals", Scopes: []string{"global"}}}

	removed, scopes := removeStaleClaudePlugins(context.Background(), claude, plugins)

	calls := readClaudeCalls(t, claude)
	assert.NotContains(t, calls, "--scope", "no scoped uninstall should be attempted for a global scope")
	assert.Contains(t, calls, "plugin uninstall basecamp@37signals", "unscoped fallback should run")
	assert.Equal(t, []string{"basecamp@37signals"}, removed)
	assert.Empty(t, scopes)
}

// TestRemoveStaleClaudePluginsMixedScopes verifies that when a plugin key has
// BOTH a valid scope and an invalid legacy scope (e.g. "global"), the valid
// scope's successful uninstall does not suppress the unscoped fallback: the
// entry installed under the invalid scope must still be removed, and the key
// must appear in removed exactly once.
func TestRemoveStaleClaudePluginsMixedScopes(t *testing.T) {
	claude := stubClaudeUninstall(t, false)
	plugins := []harness.StalePlugin{{Key: "basecamp@37signals", Scopes: []string{"user", "global"}}}

	removed, scopes := removeStaleClaudePlugins(context.Background(), claude, plugins)

	calls := readClaudeCalls(t, claude)
	assert.Contains(t, calls, "plugin uninstall basecamp@37signals --scope user",
		"valid scope should be uninstalled explicitly")
	assert.NotContains(t, calls, "--scope global", "invalid scope must never reach the argv")
	assert.Contains(t, calls, "plugin uninstall basecamp@37signals\n",
		"unscoped fallback should run for the invalid-scope entry")
	assert.Equal(t, []string{"basecamp@37signals"}, removed, "key reported once despite two removal paths")
	assert.Equal(t, []string{"user"}, scopes,
		"valid scope recorded exactly once: the scoped-uninstall success and the fallback must not double-add it")
}

// TestRemoveStaleClaudePluginsMixedScopesValidUninstallFails verifies that when
// a plugin mixes a VALID scope whose scoped uninstall fails at runtime with an
// INVALID scope, the unscoped fallback (which strips every install of the key,
// including the valid-scoped one) still records the valid scope in the returned
// scopes list so runClaudeSetup reinstalls it instead of silently dropping it.
func TestRemoveStaleClaudePluginsMixedScopesValidUninstallFails(t *testing.T) {
	claude := stubClaudeUninstall(t, true)
	plugins := []harness.StalePlugin{{Key: "basecamp@37signals", Scopes: []string{"project", "global"}}}

	removed, scopes := removeStaleClaudePlugins(context.Background(), claude, plugins)

	calls := readClaudeCalls(t, claude)
	assert.Contains(t, calls, "plugin uninstall basecamp@37signals --scope project",
		"valid scope should be attempted explicitly even though it fails")
	assert.NotContains(t, calls, "--scope global", "invalid scope must never reach the argv")
	assert.Contains(t, calls, "plugin uninstall basecamp@37signals\n",
		"unscoped fallback should run for the invalid-scope entry")
	assert.Equal(t, []string{"basecamp@37signals"}, removed)
	assert.Equal(t, []string{"project"}, scopes,
		"valid scope stripped by the unscoped fallback must be recorded exactly once for reinstall")
}

// TestRemoveStaleClaudePluginsValidScopeUninstallFails verifies the regression
// fix: when scopes are VALID but the scoped uninstall fails at runtime, we must
// NOT fall back to an unscoped removal (which would wrongly strip every scope).
func TestRemoveStaleClaudePluginsValidScopeUninstallFails(t *testing.T) {
	claude := stubClaudeUninstall(t, true)
	plugins := []harness.StalePlugin{{Key: "basecamp@37signals", Scopes: []string{"user", "project"}}}

	removed, scopes := removeStaleClaudePlugins(context.Background(), claude, plugins)

	calls := readClaudeCalls(t, claude)
	assert.Contains(t, calls, "plugin uninstall basecamp@37signals --scope user")
	assert.Contains(t, calls, "plugin uninstall basecamp@37signals --scope project")
	assert.NotContains(t, calls, "plugin uninstall basecamp@37signals\n",
		"no unscoped fallback when valid scopes were attempted but failed")
	assert.Empty(t, removed)
	assert.Empty(t, scopes)
}
