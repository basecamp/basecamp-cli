package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/skills"
)

func runSetupAgentsRemove(t *testing.T) ([]byte, error) {
	t.Helper()
	app, out := setupQuickstartTestApp(t, "", "")
	app.Flags.JSON = true
	t.Cleanup(app.Close)

	cmd := NewSetupCmd()
	cmd.SetArgs([]string{"agents", "--remove"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	return out.Bytes(), err
}

func runSetupAgentsRemoveWithErrorEnvelope(t *testing.T) ([]byte, error) {
	t.Helper()
	app, out := setupQuickstartTestApp(t, "", "")
	app.Flags.JSON = true
	t.Cleanup(app.Close)

	cmd := NewSetupCmd()
	cmd.SetArgs([]string{"agents", "--remove"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	require.Error(t, err)
	require.NoError(t, app.Err(err))
	return out.Bytes(), err
}

func stubAgentRemoveCommand(t *testing.T, fn func(context.Context, string, string, ...string) ([]byte, error)) {
	t.Helper()
	original := runAgentRemoveCommand
	runAgentRemoveCommand = fn
	t.Cleanup(func() { runAgentRemoveCommand = original })
}

func stubCodexInstalled(t *testing.T, installed bool, err error) {
	t.Helper()
	original := codexPluginInstalled
	codexPluginInstalled = func(context.Context) (bool, error) { return installed, err }
	t.Cleanup(func() { codexPluginInstalled = original })
}

func installExecutableStub(t *testing.T, name string) string {
	t.Helper()
	binDir := t.TempDir()
	path := filepath.Join(binDir, name)
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", binDir)
	return path
}

func writeClaudeRegistry(t *testing.T, configDir, content string) {
	t.Helper()
	dir := filepath.Join(configDir, "plugins")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "installed_plugins.json"), []byte(content), 0o600))
}

func TestSetupAgentsRemoveDeletesManagedSkillsAndPreservesUserFiles(t *testing.T) {
	home := emptyHome(t)
	_, err := installSkillFiles()
	require.NoError(t, err)
	_, _, err = linkSkillToClaude()
	require.NoError(t, err)

	baseline := filepath.Join(home, ".agents", "skills", "basecamp")
	require.NoError(t, os.WriteFile(filepath.Join(baseline, "notes.txt"), []byte("keep me"), 0o600))

	response, err := runSetupAgentsRemove(t)
	require.NoError(t, err)
	var envelope struct {
		Summary string `json:"summary"`
		Data    struct {
			Removed  []string `json:"removed"`
			Failures []string `json:"failures"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response, &envelope), string(response))
	assert.Equal(t, "Coding-agent integrations removed", envelope.Summary)
	assert.ElementsMatch(t, []string{"Claude Code skill", "agent skill"}, envelope.Data.Removed)
	assert.Empty(t, envelope.Data.Failures)

	for _, name := range []string{skillFilename, installedVersionFile, ownershipMarkerFile} {
		_, statErr := os.Lstat(filepath.Join(baseline, name))
		assert.True(t, os.IsNotExist(statErr), name)
	}
	data, readErr := os.ReadFile(filepath.Join(baseline, "notes.txt"))
	require.NoError(t, readErr)
	assert.Equal(t, "keep me", string(data))
	_, statErr := os.Lstat(filepath.Join(home, ".claude", "skills", "basecamp"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestSetupAgentsRemoveDeletesManagedOpenCodeSkills(t *testing.T) {
	home := emptyHome(t)
	project := t.TempDir()
	locations := []string{
		filepath.Join(home, ".config", "opencode", "skills", "basecamp"),
		filepath.Join(home, ".config", "opencode", "skill", "basecamp"),
		filepath.Join(project, ".opencode", "skills", "basecamp"),
	}
	t.Chdir(project)
	for _, dir := range locations {
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, skillFilename), []byte("managed"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ownershipMarkerFile), []byte("managed"), 0o600))
	}

	_, err := runSetupAgentsRemove(t)
	require.NoError(t, err)
	for _, dir := range locations {
		_, statErr := os.Lstat(filepath.Join(dir, skillFilename))
		assert.True(t, os.IsNotExist(statErr), dir)
	}
}

func TestSetupAgentsRemoveDoesNotDeduplicateOpenCodeSymlinkDestination(t *testing.T) {
	home := emptyHome(t)
	project := t.TempDir()
	t.Chdir(project)

	projectSkill := filepath.Join(project, ".opencode", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(projectSkill, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectSkill, skillFilename), []byte("managed"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(projectSkill, ownershipMarkerFile), []byte("managed"), 0o600))

	globalSkill := filepath.Join(home, ".config", "opencode", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(filepath.Dir(globalSkill), 0o755))
	require.NoError(t, os.Symlink(projectSkill, globalSkill))

	_, err := runSetupAgentsRemove(t)
	require.ErrorContains(t, err, "OpenCode (Global) skill: unsafe symlink traversal skipped")
	_, statErr := os.Lstat(filepath.Join(projectSkill, skillFilename))
	assert.True(t, os.IsNotExist(statErr), "the directly listed managed project skill must be removed")
	linkInfo, statErr := os.Lstat(globalSkill)
	require.NoError(t, statErr)
	assert.NotZero(t, linkInfo.Mode()&os.ModeSymlink, "the unmanaged symlink itself must be preserved")
}

func TestSetupAgentsRemovePreservesOpenCodeSkillBehindSymlinkedParent(t *testing.T) {
	home := emptyHome(t)
	project := t.TempDir()
	t.Chdir(project)

	externalConfig := t.TempDir()
	externalSkill := filepath.Join(externalConfig, "opencode", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(externalSkill, 0o755))
	embedded, err := skills.FS.ReadFile("basecamp/SKILL.md")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(externalSkill, skillFilename), embedded, 0o600))
	require.NoError(t, os.Symlink(externalConfig, filepath.Join(home, ".config")))

	_, err = runSetupAgentsRemove(t)
	require.ErrorContains(t, err, "OpenCode (Global) skill: unsafe symlink traversal skipped")
	data, readErr := os.ReadFile(filepath.Join(externalSkill, skillFilename))
	require.NoError(t, readErr)
	assert.Equal(t, embedded, data, "cleanup must not follow a symlinked OpenCode parent")
}

func TestSetupAgentsRemoveDeletesManagedProjectClaudeSkill(t *testing.T) {
	emptyHome(t)
	project := t.TempDir()
	t.Chdir(project)
	dir := filepath.Join(project, ".claude", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, skillFilename), []byte("managed"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ownershipMarkerFile), []byte("managed"), 0o600))

	_, err := runSetupAgentsRemove(t)
	require.NoError(t, err)
	_, statErr := os.Lstat(filepath.Join(dir, skillFilename))
	assert.True(t, os.IsNotExist(statErr))
}

func TestSetupAgentsRemovePreservesProjectClaudeSkillBehindSymlinkedParent(t *testing.T) {
	emptyHome(t)
	project := t.TempDir()
	t.Chdir(project)

	externalClaude := t.TempDir()
	externalSkill := filepath.Join(externalClaude, "skills", "basecamp")
	require.NoError(t, os.MkdirAll(externalSkill, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(externalSkill, skillFilename), []byte("managed"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(externalSkill, ownershipMarkerFile), []byte("managed"), 0o600))
	require.NoError(t, os.Symlink(externalClaude, filepath.Join(project, ".claude")))

	_, err := runSetupAgentsRemove(t)
	require.ErrorContains(t, err, "project Claude Code skill: unsafe symlink traversal skipped")
	_, statErr := os.Lstat(filepath.Join(externalSkill, skillFilename))
	assert.NoError(t, statErr, "cleanup must not follow a symlinked project Claude parent")
}

func TestSetupAgentsRemoveDeletesAuthenticMarkerlessBaseline(t *testing.T) {
	home := emptyHome(t)
	dir := filepath.Join(home, ".agents", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	embedded, err := skills.FS.ReadFile("basecamp/SKILL.md")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, skillFilename), embedded, 0o600))

	_, err = runSetupAgentsRemove(t)
	require.NoError(t, err)
	_, statErr := os.Lstat(dir)
	assert.True(t, os.IsNotExist(statErr))
}

func TestSetupAgentsRemoveReportsSkippedSymlinkedBaseline(t *testing.T) {
	home := emptyHome(t)
	externalAgents := t.TempDir()
	baseline := filepath.Join(externalAgents, "skills", "basecamp")
	require.NoError(t, os.MkdirAll(baseline, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(baseline, skillFilename), []byte("managed"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(baseline, ownershipMarkerFile), []byte("managed"), 0o600))
	require.NoError(t, os.Symlink(externalAgents, filepath.Join(home, ".agents")))

	_, err := runSetupAgentsRemove(t)
	require.ErrorContains(t, err, "agent skill: unsafe symlink traversal skipped")
	_, statErr := os.Stat(filepath.Join(baseline, skillFilename))
	assert.NoError(t, statErr, "cleanup must not follow a symlinked baseline parent")
}

func TestSetupAgentsRemovePreservesUserFilesBesideMarkerlessManagedSkill(t *testing.T) {
	home := emptyHome(t)
	dir := filepath.Join(home, ".agents", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	embedded, err := skills.FS.ReadFile("basecamp/SKILL.md")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, skillFilename), embedded, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep me"), 0o600))

	_, err = runSetupAgentsRemove(t)
	require.NoError(t, err)
	_, statErr := os.Lstat(filepath.Join(dir, skillFilename))
	assert.True(t, os.IsNotExist(statErr))
	data, readErr := os.ReadFile(filepath.Join(dir, "notes.txt"))
	require.NoError(t, readErr)
	assert.Equal(t, "keep me", string(data))
}

func TestSetupAgentsRemoveUsesAbsoluteClaudeConfigWithInvalidRelativeHome(t *testing.T) {
	home := emptyHome(t)
	app, _ := setupQuickstartTestApp(t, "", "")
	t.Cleanup(app.Close)
	config := filepath.Join(home, "absolute-claude")
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	dir := filepath.Join(config, "skills", "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, skillFilename), []byte("managed"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ownershipMarkerFile), []byte("managed"), 0o600))
	t.Setenv("HOME", "relative-home")

	cmd := &cobra.Command{}
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	err := runRemoveAgentSetup(cmd, app)
	require.Error(t, err, "unavailable home-based cleanup must remain visible")
	assert.Contains(t, err.Error(), "home-based skill cleanup: getting home directory:")
	assert.NotContains(t, err.Error(), "getting home directory: getting home directory:")
	_, statErr := os.Lstat(filepath.Join(dir, skillFilename))
	assert.True(t, os.IsNotExist(statErr), "the self-contained Claude integration must still be removed")
}

func TestSafeSkillTraversalRejectsSymlinkedDefaultParents(t *testing.T) {
	home := t.TempDir()
	external := t.TempDir()
	for _, parent := range []string{".agents", ".claude", ".codex"} {
		require.NoError(t, os.Symlink(external, filepath.Join(home, parent)))
		safe, err := safeSkillTraversal(home, filepath.Join(home, parent, "skills", "basecamp"))
		require.NoError(t, err)
		assert.False(t, safe, parent)
	}
}

func TestRemoveCodexPluginReportsConfiguredHomeWithoutUserHome(t *testing.T) {
	t.Setenv("HOME", "")
	configured := filepath.Join(t.TempDir(), "codex")
	t.Setenv("CODEX_HOME", configured)
	require.NoError(t, os.MkdirAll(configured, 0o755))
	t.Setenv("PATH", t.TempDir())

	removed, failure := removeCodexPlugin(context.Background())
	assert.False(t, removed)
	assert.Contains(t, failure, "codex binary not found")
}

func TestSetupAgentsRemoveDeletesClaudeLinkThroughConfigAlias(t *testing.T) {
	home := emptyHome(t)
	_, err := installSkillFiles()
	require.NoError(t, err)
	realConfig := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.MkdirAll(realConfig, 0o755))
	alias := filepath.Join(home, "claude-alias")
	require.NoError(t, os.Symlink(realConfig, alias))
	t.Setenv("CLAUDE_CONFIG_DIR", alias)
	link, _, err := linkSkillToClaude()
	require.NoError(t, err)

	_, err = runSetupAgentsRemove(t)
	require.NoError(t, err)
	_, statErr := os.Lstat(link)
	assert.True(t, os.IsNotExist(statErr))
}

func TestRemoveClaudeSkillPreservesLinkToUnmanagedBaseline(t *testing.T) {
	home := t.TempDir()
	baseline := filepath.Join(home, ".agents", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(baseline, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(baseline, skillFilename), []byte("user skill"), 0o644))
	link := filepath.Join(home, ".claude", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.Symlink(claudeSkillLinkTarget, link))

	removed, err := removeClaudeSkill(link, baseline)
	require.NoError(t, err)
	assert.False(t, removed)
	_, statErr := os.Lstat(link)
	assert.NoError(t, statErr)
}

func TestSetupAgentsRemovePreservesUnmanagedSkillsAndLinks(t *testing.T) {
	home := emptyHome(t)
	baseline := filepath.Join(home, ".agents", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(baseline, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(baseline, skillFilename), []byte("user baseline"), 0o600))

	target := filepath.Join(home, "my-basecamp-skill")
	require.NoError(t, os.MkdirAll(target, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, skillFilename), []byte("user claude skill"), 0o600))
	claudeSkill := filepath.Join(home, ".claude", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(filepath.Dir(claudeSkill), 0o755))
	require.NoError(t, os.Symlink(target, claudeSkill))

	_, err := runSetupAgentsRemove(t)
	require.NoError(t, err)
	baselineData, readErr := os.ReadFile(filepath.Join(baseline, skillFilename))
	require.NoError(t, readErr)
	assert.Equal(t, "user baseline", string(baselineData))
	_, statErr := os.Lstat(claudeSkill)
	assert.NoError(t, statErr)
	claudeData, readErr := os.ReadFile(filepath.Join(claudeSkill, skillFilename))
	require.NoError(t, readErr)
	assert.Equal(t, "user claude skill", string(claudeData))
}

func TestRemoveOwnedSkillFilesRecognizesLegacyVersionMarker(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, skillFilename), []byte("legacy managed"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, installedVersionFile), []byte("0.9.1"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mine.txt"), []byte("keep"), 0o600))

	removed, err := removeOwnedSkillFiles(dir)
	require.NoError(t, err)
	assert.True(t, removed)
	_, err = os.Stat(filepath.Join(dir, "mine.txt"))
	assert.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, skillFilename))
	assert.True(t, os.IsNotExist(err))
}

func TestRemoveOwnedSkillFilesPreflightsEveryManagedPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "basecamp")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, installedVersionFile), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, skillFilename), []byte("do not partially remove"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ownershipMarkerFile), []byte("managed"), 0o600))

	removed, err := removeOwnedSkillFiles(dir)
	assert.False(t, removed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a regular file")
	_, statErr := os.Stat(filepath.Join(dir, skillFilename))
	assert.NoError(t, statErr, "preflight failure must not partially delete managed files")
}

func TestRemoveClaudePluginUninstallsEveryRecordedValidScope(t *testing.T) {
	home := t.TempDir()
	claude := installExecutableStub(t, "claude")
	project := t.TempDir()
	writeClaudeRegistry(t, filepath.Join(home, ".claude"), fmt.Sprintf(`{"version":2,"plugins":{"basecamp@37signals":[{"scope":"project","projectPath":%q},{"scope":"user"}]}}`, project))

	var calls []string
	stubAgentRemoveCommand(t, func(_ context.Context, path, dir string, args ...string) ([]byte, error) {
		assert.Equal(t, claude, path)
		calls = append(calls, dir+"|"+strings.Join(args, " "))
		return nil, nil
	})

	removed, failures := removeClaudePlugin(context.Background(), filepath.Join(home, ".claude"))
	assert.True(t, removed)
	assert.Empty(t, failures)
	assert.Equal(t, []string{
		project + "|plugin uninstall basecamp@37signals --scope project",
		"|plugin uninstall basecamp@37signals --scope user",
	}, calls)
}

func TestRemoveClaudePluginReportsPartialScopeFailure(t *testing.T) {
	home := t.TempDir()
	installExecutableStub(t, "claude")
	project := t.TempDir()
	writeClaudeRegistry(t, filepath.Join(home, ".claude"), fmt.Sprintf(`{"version":2,"plugins":{"basecamp@37signals":[{"scope":"project","projectPath":%q},{"scope":"user"}]}}`, project))
	stubAgentRemoveCommand(t, func(_ context.Context, _, _ string, args ...string) ([]byte, error) {
		if args[len(args)-1] == "project" {
			return []byte("permission denied"), errors.New("exit status 1")
		}
		return nil, nil
	})

	removed, failures := removeClaudePlugin(context.Background(), filepath.Join(home, ".claude"))
	assert.True(t, removed, "the user-scoped installation was still removed")
	require.Len(t, failures, 1)
	assert.Contains(t, failures[0], "project")
	assert.Contains(t, failures[0], "permission denied")
}

func TestRemoveClaudePluginIgnoresStaleProjectPathForUserScope(t *testing.T) {
	home := t.TempDir()
	installExecutableStub(t, "claude")
	stale := filepath.Join(t.TempDir(), "deleted")
	writeClaudeRegistry(t, filepath.Join(home, ".claude"), fmt.Sprintf(`{"version":2,"plugins":{"basecamp@37signals":[{"scope":"user","projectPath":%q}]}}`, stale))

	stubAgentRemoveCommand(t, func(_ context.Context, _, dir string, _ ...string) ([]byte, error) {
		assert.Empty(t, dir)
		return nil, nil
	})

	removed, failures := removeClaudePlugin(context.Background(), filepath.Join(home, ".claude"))
	assert.True(t, removed)
	assert.Empty(t, failures)
}

func TestRemoveClaudePluginFallsBackForUnknownScope(t *testing.T) {
	home := t.TempDir()
	installExecutableStub(t, "claude")
	writeClaudeRegistry(t, filepath.Join(home, ".claude"), `{"version":2,"plugins":{"basecamp@37signals":[{"scope":"global"}]}}`)

	calls := 0
	stubAgentRemoveCommand(t, func(_ context.Context, _, _ string, args ...string) ([]byte, error) {
		calls++
		assert.NotContains(t, args, "--scope")
		if calls == 1 {
			return nil, nil
		}
		return []byte("basecamp@37signals is not installed"), errors.New("exit status 1")
	})

	removed, failures := removeClaudePlugin(context.Background(), filepath.Join(home, ".claude"))
	assert.True(t, removed)
	assert.Empty(t, failures)
	assert.Equal(t, 2, calls)
}

func TestRemoveClaudePluginReportsFailureAfterUnscopedProgress(t *testing.T) {
	home := t.TempDir()
	installExecutableStub(t, "claude")
	writeClaudeRegistry(t, filepath.Join(home, ".claude"), `{"plugins":{"basecamp@37signals":null}}`)

	calls := 0
	stubAgentRemoveCommand(t, func(_ context.Context, _, _ string, _ ...string) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, nil
		}
		return []byte("permission denied"), errors.New("exit status 1")
	})

	removed, failures := removeClaudePlugin(context.Background(), filepath.Join(home, ".claude"))
	assert.True(t, removed)
	require.Len(t, failures, 1)
	assert.Contains(t, failures[0], "permission denied")
}

func TestRemoveClaudePluginFallsBackWhenRegistryMixesKnownAndUnknownScopes(t *testing.T) {
	home := t.TempDir()
	installExecutableStub(t, "claude")
	writeClaudeRegistry(t, filepath.Join(home, ".claude"), `{"version":2,"plugins":{"basecamp@37signals":[{"scope":"user"},{}]}}`)

	var calls []string
	stubAgentRemoveCommand(t, func(_ context.Context, _, _ string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if len(args) > 0 && args[len(args)-1] == "user" {
			return nil, nil
		}
		if len(calls) == 2 {
			return nil, nil
		}
		return []byte("basecamp@37signals is not installed"), errors.New("exit status 1")
	})

	removed, failures := removeClaudePlugin(context.Background(), filepath.Join(home, ".claude"))
	assert.True(t, removed)
	assert.Empty(t, failures)
	assert.Equal(t, []string{
		"plugin uninstall basecamp@37signals --scope user",
		"plugin uninstall basecamp@37signals",
		"plugin uninstall basecamp@37signals",
	}, calls)
}

func TestRemoveClaudePluginNeverForwardsUnknownScope(t *testing.T) {
	home := t.TempDir()
	installExecutableStub(t, "claude")
	project := t.TempDir()
	writeClaudeRegistry(t, filepath.Join(home, ".claude"), fmt.Sprintf(`{"version":2,"plugins":{"basecamp@37signals":[{"scope":"unexpected","projectPath":%q}]}}`, project))

	stubAgentRemoveCommand(t, func(_ context.Context, _, _ string, args ...string) ([]byte, error) {
		assert.NotContains(t, args, "--scope")
		return []byte("basecamp@37signals is not installed"), errors.New("exit status 1")
	})

	removed, failures := removeClaudePlugin(context.Background(), filepath.Join(home, ".claude"))
	assert.False(t, removed)
	assert.Empty(t, failures)
}

func TestRemoveClaudePluginAcceptsAbsentFallbackAfterScopedRemoval(t *testing.T) {
	home := t.TempDir()
	installExecutableStub(t, "claude")
	writeClaudeRegistry(t, filepath.Join(home, ".claude"), `{"version":2,"plugins":{"basecamp@37signals":[{"scope":"user"},{}]}}`)

	stubAgentRemoveCommand(t, func(_ context.Context, _, _ string, args ...string) ([]byte, error) {
		if slices.Contains(args, "--scope") {
			return nil, nil
		}
		return []byte("basecamp@37signals is not installed"), errors.New("exit status 1")
	})

	removed, failures := removeClaudePlugin(context.Background(), filepath.Join(home, ".claude"))
	assert.True(t, removed)
	assert.Empty(t, failures)
}

func TestRemoveClaudePluginAcceptsAbsentScopedInstallation(t *testing.T) {
	home := t.TempDir()
	installExecutableStub(t, "claude")
	writeClaudeRegistry(t, filepath.Join(home, ".claude"), `{"version":2,"plugins":{"basecamp@37signals":[{"scope":"user"}]}}`)

	stubAgentRemoveCommand(t, func(_ context.Context, _, _ string, args ...string) ([]byte, error) {
		assert.Equal(t, []string{"plugin", "uninstall", "basecamp@37signals", "--scope", "user"}, args)
		return []byte("basecamp@37signals is not installed"), errors.New("exit status 1")
	})

	removed, failures := removeClaudePlugin(context.Background(), filepath.Join(home, ".claude"))
	assert.False(t, removed)
	assert.Empty(t, failures)
}

func TestRemoveClaudePluginAcceptsNotFoundScopedInstallation(t *testing.T) {
	home := t.TempDir()
	installExecutableStub(t, "claude")
	writeClaudeRegistry(t, filepath.Join(home, ".claude"), `{"version":2,"plugins":{"basecamp@37signals":[{"scope":"user"}]}}`)

	stubAgentRemoveCommand(t, func(_ context.Context, _, _ string, args ...string) ([]byte, error) {
		assert.Equal(t, []string{"plugin", "uninstall", "basecamp@37signals", "--scope", "user"}, args)
		return []byte("Plugin basecamp@37signals not found"), errors.New("exit status 1")
	})

	removed, failures := removeClaudePlugin(context.Background(), filepath.Join(home, ".claude"))
	assert.False(t, removed)
	assert.Empty(t, failures)
}

func TestRemoveClaudePluginDoesNotSwallowUnrelatedAbsentPlugin(t *testing.T) {
	home := t.TempDir()
	installExecutableStub(t, "claude")
	writeClaudeRegistry(t, filepath.Join(home, ".claude"), `{"version":2,"plugins":{"basecamp@37signals":[{"scope":"user"}]}}`)
	stubAgentRemoveCommand(t, func(_ context.Context, _, _ string, _ ...string) ([]byte, error) {
		return []byte("marketplace helper is not installed"), errors.New("exit status 1")
	})

	removed, failures := removeClaudePlugin(context.Background(), filepath.Join(home, ".claude"))
	assert.False(t, removed)
	require.Len(t, failures, 1)
	assert.Contains(t, failures[0], "marketplace helper")
}

func TestRemoveClaudePluginReportsUnscopedSafetyLimit(t *testing.T) {
	home := t.TempDir()
	installExecutableStub(t, "claude")
	writeClaudeRegistry(t, filepath.Join(home, ".claude"), `{"plugins":{"basecamp@37signals":null}}`)
	stubAgentRemoveCommand(t, func(_ context.Context, _, _ string, _ ...string) ([]byte, error) {
		return nil, nil
	})

	removed, failures := removeClaudePlugin(context.Background(), filepath.Join(home, ".claude"))
	assert.True(t, removed)
	require.Len(t, failures, 1)
	assert.Contains(t, failures[0], "safety limit reached")
}

func TestUninstallClaudeUnscopedUsesSingleDeadline(t *testing.T) {
	var deadlines []time.Time
	calls := 0
	stubAgentRemoveCommand(t, func(ctx context.Context, _, _ string, _ ...string) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		deadlines = append(deadlines, deadline)
		calls++
		if calls < 3 {
			return nil, nil
		}
		return []byte("basecamp@37signals not found"), errors.New("exit status 1")
	})

	removed, err := uninstallClaudeUnscoped(context.Background(), "claude", "basecamp@37signals")
	require.NoError(t, err)
	assert.True(t, removed)
	require.Len(t, deadlines, 3)
	assert.Equal(t, deadlines[0], deadlines[1])
	assert.Equal(t, deadlines[0], deadlines[2])
}

func TestRunAgentRemoveCommandOutlivingGrandchild(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not available")
	}

	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	script := strconv.Quote(sleep) + " 120 & echo $! > " + strconv.Quote(pidFile) + "; exit 0"
	t.Cleanup(func() {
		raw, readErr := os.ReadFile(pidFile) //nolint:gosec // path is this test's TempDir
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

	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		_, _ = runAgentRemoveCommand(context.Background(), sh, "", "-c", script)
	}()
	select {
	case <-done:
		assert.Less(t, time.Since(start), 10*time.Second)
	case <-time.After(10 * time.Second):
		t.Fatal("removal command remained blocked on a grandchild's output pipe")
	}
}

func TestRemoveClaudePluginReportsUntargetableProjectScope(t *testing.T) {
	home := t.TempDir()
	installExecutableStub(t, "claude")
	writeClaudeRegistry(t, filepath.Join(home, ".claude"), `{"version":2,"plugins":{"basecamp@37signals":[{"scope":"local"}]}}`)
	stubAgentRemoveCommand(t, func(_ context.Context, _, _ string, _ ...string) ([]byte, error) {
		t.Fatal("an unlocated project installation must not be removed from the caller's cwd")
		return nil, nil
	})

	removed, failures := removeClaudePlugin(context.Background(), filepath.Join(home, ".claude"))
	assert.False(t, removed)
	require.Len(t, failures, 1)
	assert.Contains(t, failures[0], "project path is missing or invalid")
}

func TestParseClaudePluginInstallationsRejectsNullRoot(t *testing.T) {
	installations, ok := parseClaudePluginInstallations([]byte("null"))
	assert.False(t, ok)
	assert.Nil(t, installations)
}

func TestParseClaudePluginInstallationsChecksEveryIdentityField(t *testing.T) {
	installations, ok := parseClaudePluginInstallations([]byte(`[{"package":"unrelated","id":"basecamp@37signals","scope":"user"}]`))
	require.True(t, ok)
	require.Len(t, installations, 1)
	assert.Equal(t, "basecamp@37signals", installations[0].Key)
	assert.Equal(t, []claudePluginScope{{Name: "user"}}, installations[0].Scopes)
}

func TestSetupAgentsRemoveHonorsClaudeConfigDir(t *testing.T) {
	home := emptyHome(t)
	claude := installExecutableStub(t, "claude")
	customConfig := filepath.Join(home, "custom-claude")
	t.Setenv("CLAUDE_CONFIG_DIR", customConfig)
	writeClaudeRegistry(t, customConfig, `{"version":2,"plugins":{"basecamp@37signals":[{"scope":"user"}]}}`)
	skillDir := filepath.Join(customConfig, "skills", "basecamp")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, skillFilename), []byte("managed"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, ownershipMarkerFile), []byte("managed"), 0o644))
	defaultSkill := filepath.Join(home, ".claude", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(defaultSkill, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(defaultSkill, skillFilename), []byte("default-user-skill"), 0o644))

	stubAgentRemoveCommand(t, func(_ context.Context, path, dir string, args ...string) ([]byte, error) {
		assert.Equal(t, claude, path)
		assert.Empty(t, dir)
		assert.Equal(t, []string{"plugin", "uninstall", "basecamp@37signals", "--scope", "user"}, args)
		return nil, nil
	})

	_, err := runSetupAgentsRemove(t)
	require.NoError(t, err)
	_, statErr := os.Stat(filepath.Join(skillDir, skillFilename))
	assert.True(t, os.IsNotExist(statErr))
	data, readErr := os.ReadFile(filepath.Join(defaultSkill, skillFilename))
	require.NoError(t, readErr)
	assert.Equal(t, "default-user-skill", string(data))
}

func TestSetupAgentsRemoveCleansProvenDefaultLinkAfterCustomConfigMigration(t *testing.T) {
	home := emptyHome(t)
	_, err := installSkillFiles()
	require.NoError(t, err)
	customConfig := filepath.Join(home, "configs", "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", customConfig)
	customLink, _, err := linkSkillToClaude()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(customConfig, "skills", "basecamp"), customLink)

	defaultLink := filepath.Join(home, ".claude", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(filepath.Dir(defaultLink), 0o755))
	require.NoError(t, os.Symlink(claudeSkillLinkTarget, defaultLink))

	_, err = runSetupAgentsRemove(t)
	require.NoError(t, err)
	for _, path := range []string{customLink, defaultLink} {
		_, statErr := os.Lstat(path)
		assert.True(t, os.IsNotExist(statErr), path)
	}
}

func TestSetupAgentsRemoveDefersBaselineAliasedByClaudeConfig(t *testing.T) {
	home := emptyHome(t)
	baseline, err := installSkillFiles()
	require.NoError(t, err)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".agents"))

	defaultLink := filepath.Join(home, ".claude", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(filepath.Dir(defaultLink), 0o755))
	require.NoError(t, os.Symlink(claudeSkillLinkTarget, defaultLink))

	_, err = runSetupAgentsRemove(t)
	require.NoError(t, err)
	for _, path := range []string{baseline, defaultLink} {
		_, statErr := os.Lstat(path)
		assert.True(t, os.IsNotExist(statErr), path)
	}
}

func TestSetupAgentsRemoveCleansProvenDefaultLinkWhenClaudeConfigIsInvalid(t *testing.T) {
	home := emptyHome(t)
	baseline, err := installSkillFiles()
	require.NoError(t, err)
	defaultLink := filepath.Join(home, ".claude", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(filepath.Dir(defaultLink), 0o755))
	require.NoError(t, os.Symlink(claudeSkillLinkTarget, defaultLink))
	t.Setenv("CLAUDE_CONFIG_DIR", "relative/path")

	_, err = runSetupAgentsRemove(t)
	require.Error(t, err)
	_, statErr := os.Lstat(defaultLink)
	assert.True(t, os.IsNotExist(statErr), "managed default link must be removed despite the invalid custom config")
	_, statErr = os.Lstat(baseline)
	assert.NoError(t, statErr, "baseline must remain when a configured Claude link slot cannot be inspected")
}

func TestRemoveOwnedOrLegacyCodexSkillRecognizesAuthenticPremarkerInstall(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	embedded, err := skills.FS.ReadFile("basecamp/SKILL.md")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, skillFilename), embedded, 0o644))

	removed, err := removeOwnedOrLegacyCodexSkill(dir)
	require.NoError(t, err)
	assert.True(t, removed)
	_, statErr := os.Stat(dir)
	assert.True(t, os.IsNotExist(statErr))
}

func TestRemoveOwnedOrLegacyCodexSkillRecognizesAllowlistedPayload(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	payload := []byte("synthetic allowlisted legacy skill")
	sum := sha256.Sum256(payload)
	hash := fmt.Sprintf("%x", sum)
	legacyManagedSkillHashes[hash] = struct{}{}
	t.Cleanup(func() { delete(legacyManagedSkillHashes, hash) })
	require.NoError(t, os.WriteFile(filepath.Join(dir, skillFilename), payload, 0o644))

	removed, err := removeOwnedOrLegacyCodexSkill(dir)
	require.NoError(t, err)
	assert.True(t, removed)
}

func TestLegacyManagedSkillHashAllowlistDoesNotShrink(t *testing.T) {
	want := []string{
		"9ba73c37394e2f3fd41b1fb88dfcb5765c8d28f817a4d8c186cb3bc6eb9b7c0b",
		"5b8dbaee9258079695e078c2fffea835d53ac408aa1265fd5116fd4a8657aaed",
		"7f388068176a382e1b452452e88ebb9a4712265ca777c81f47350df0845c8839",
		"866ffee85417ea2d204efc5b49e4d6bf2c7f74fd3d3e0d4773fe2fbb70640e36",
		"ca0db118c4c69211dd9c0169cce36850c8cca1331e83544e0e4638435a157f43",
		"2c295c087b0110ca67c3c12ae8a4deda0d2f04390cefeef6719d924526f0e72a",
		"c5dfa70f7a7d9ce5ff4d6c780949bb821447c87db345a22e8f533004a2bd0b3f",
		"c8465eadd9f1c7cfae8235812d85b424f15f0a65b9fa3878be046deb58b9efcf",
		"21dbba9a6419d3bbf215976e591cafb9fdb3f2a4ca7b20a9953efec7f9691e96",
		"5bdfcb49c9808011087790c006f8665cc5d8a079961a1dcef760547d8dd9280c",
		"a5e60a1c55ec381dab3265625d97461b7c32edd49837a03642abba347852421d",
	}
	for _, hash := range want {
		_, ok := legacyManagedSkillHashes[hash]
		assert.True(t, ok, hash)
	}
}

func TestRemoveClaudeSkillRecognizesMarkerlessWizardPayload(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	embedded, err := skills.FS.ReadFile("basecamp/SKILL.md")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, skillFilename), embedded, 0o644))

	removed, err := removeClaudeSkill(dir, filepath.Join(t.TempDir(), "baseline"))
	require.NoError(t, err)
	assert.True(t, removed)
	_, statErr := os.Lstat(dir)
	assert.True(t, os.IsNotExist(statErr))
}

func TestRemoveOwnedOrLegacyCodexSkillPreservesNonmatchingUserSkill(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, skillFilename)
	require.NoError(t, os.WriteFile(path, []byte("user-authored"), 0o644))

	removed, err := removeOwnedOrLegacyCodexSkill(dir)
	require.NoError(t, err)
	assert.False(t, removed)
	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, "user-authored", string(data))
}

func TestSetupAgentsRemoveUsesOfficialCodexCommandWithAliasedHome(t *testing.T) {
	home := emptyHome(t)
	codex := installExecutableStub(t, "codex")
	stubCodexInstalled(t, true, nil)
	_, err := installSkillFiles()
	require.NoError(t, err)

	agentsHome := filepath.Join(home, ".agents")
	alias := filepath.Join(home, "codex-home-alias")
	require.NoError(t, os.Symlink(agentsHome, alias))
	t.Setenv("CODEX_HOME", alias)
	userPluginFile := filepath.Join(agentsHome, "plugins", "keep.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(userPluginFile), 0o755))
	require.NoError(t, os.WriteFile(userPluginFile, []byte("keep"), 0o600))

	stubAgentRemoveCommand(t, func(_ context.Context, path, dir string, args ...string) ([]byte, error) {
		assert.Equal(t, codex, path)
		assert.Empty(t, dir)
		assert.Equal(t, []string{"plugin", "remove", "basecamp@37signals", "--json"}, args)
		return []byte(`{"removed":true}`), nil
	})

	response, err := runSetupAgentsRemove(t)
	require.NoError(t, err)
	var envelope struct {
		Data struct {
			Removed []string `json:"removed"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response, &envelope), string(response))
	assert.Contains(t, envelope.Data.Removed, "Codex plugin")
	data, readErr := os.ReadFile(userPluginFile)
	require.NoError(t, readErr)
	assert.Equal(t, "keep", string(data), "CODEX_HOME contents are owned by Codex, not removed directly")
}

func TestRemoveCodexPluginTreatsAlreadyAbsentAsSuccess(t *testing.T) {
	installExecutableStub(t, "codex")
	stubCodexInstalled(t, false, nil)
	stubAgentRemoveCommand(t, func(_ context.Context, _, _ string, _ ...string) ([]byte, error) {
		t.Fatal("remove must not run when the plugin is absent")
		return nil, nil
	})

	removed, failure := removeCodexPlugin(context.Background())
	assert.False(t, removed)
	assert.Empty(t, failure)
}

func TestRemoveCodexPluginReportsUnrelatedNotFoundFailure(t *testing.T) {
	installExecutableStub(t, "codex")
	stubCodexInstalled(t, true, nil)
	stubAgentRemoveCommand(t, func(_ context.Context, _, _ string, _ ...string) ([]byte, error) {
		return []byte("marketplace metadata not found"), errors.New("exit status 1")
	})

	removed, failure := removeCodexPlugin(context.Background())
	assert.False(t, removed)
	assert.Contains(t, failure, "marketplace metadata not found")
}

func TestSetupAgentsRemoveDeletesOwnedDirectCodexSkill(t *testing.T) {
	home := emptyHome(t)
	t.Setenv("PATH", t.TempDir())
	codexHome := filepath.Join(home, "custom-codex")
	t.Setenv("CODEX_HOME", codexHome)
	dir := filepath.Join(codexHome, "skills", "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, skillFilename), []byte("managed"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ownershipMarkerFile), []byte("managed"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mine.txt"), []byte("keep"), 0o600))

	_, err := runSetupAgentsRemove(t)
	require.Error(t, err, "the missing binary is still reported")
	_, statErr := os.Stat(filepath.Join(dir, skillFilename))
	assert.True(t, os.IsNotExist(statErr))
	data, readErr := os.ReadFile(filepath.Join(dir, "mine.txt"))
	require.NoError(t, readErr)
	assert.Equal(t, "keep", string(data))
}

func TestSetupAgentsRemoveCleansDefaultCodexSkillWithCustomHome(t *testing.T) {
	home := emptyHome(t)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("CODEX_HOME", filepath.Join(home, "custom-codex"))
	legacy := filepath.Join(home, ".codex", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(legacy, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, skillFilename), []byte("managed"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, ownershipMarkerFile), []byte("managed"), 0o644))

	_, err := runSetupAgentsRemove(t)
	require.NoError(t, err)
	_, statErr := os.Lstat(filepath.Join(legacy, skillFilename))
	assert.True(t, os.IsNotExist(statErr))
}

func TestSetupAgentsRemoveDeletesManagedProjectRelativeCodexSkill(t *testing.T) {
	emptyHome(t)
	t.Setenv("PATH", t.TempDir())
	project := t.TempDir()
	t.Chdir(project)
	t.Setenv("CODEX_HOME", ".codex")
	dir := filepath.Join(project, ".codex", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, skillFilename), []byte("managed"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ownershipMarkerFile), []byte("managed"), 0o644))

	_, err := runSetupAgentsRemove(t)
	require.Error(t, err, "the missing Codex binary remains reportable")
	_, statErr := os.Lstat(filepath.Join(dir, skillFilename))
	assert.True(t, os.IsNotExist(statErr), "verified managed project-relative Codex data must be removed")
}

func TestSetupAgentsRemovePreservesUnownedDirectCodexSkill(t *testing.T) {
	home := emptyHome(t)
	t.Setenv("PATH", t.TempDir())
	codexHome := filepath.Join(home, "custom-codex")
	t.Setenv("CODEX_HOME", codexHome)
	dir := filepath.Join(codexHome, "skills", "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, skillFilename), []byte("user"), 0o644))

	_, err := runSetupAgentsRemove(t)
	require.Error(t, err, "the missing binary is still reported")
	data, readErr := os.ReadFile(filepath.Join(dir, skillFilename))
	require.NoError(t, readErr)
	assert.Equal(t, "user", string(data))
}

func TestSetupAgentsRemoveReportsMissingCodexBinaryForCustomHome(t *testing.T) {
	home := emptyHome(t)
	t.Setenv("PATH", t.TempDir())
	customHome := filepath.Join(home, "custom-codex")
	require.NoError(t, os.MkdirAll(customHome, 0o755))
	t.Setenv("CODEX_HOME", customHome)

	_, err := runSetupAgentsRemove(t)
	var structured *output.Error
	require.ErrorAs(t, err, &structured)
	assert.Contains(t, structured.Message, "codex binary not found")
}

func TestSetupAgentsRemoveContinuesAfterPluginFailure(t *testing.T) {
	home := emptyHome(t)
	t.Setenv("PATH", t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".codex"), 0o755))
	_, err := installSkillFiles()
	require.NoError(t, err)

	_, err = runSetupAgentsRemove(t)
	var structured *output.Error
	require.ErrorAs(t, err, &structured)
	assert.Equal(t, "setup_remove_failed", structured.Code)
	assert.Contains(t, structured.Message, "codex binary not found")
	_, statErr := os.Stat(filepath.Join(home, ".agents", "skills", "basecamp", skillFilename))
	assert.True(t, os.IsNotExist(statErr), "skill cleanup must continue after a plugin failure")
}

func TestSetupAgentsRemovePartialFailureHasStructuredMetadata(t *testing.T) {
	home := emptyHome(t)
	t.Setenv("PATH", t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".codex"), 0o755))
	_, err := installSkillFiles()
	require.NoError(t, err)

	response, err := runSetupAgentsRemoveWithErrorEnvelope(t)
	require.Error(t, err)
	var envelope struct {
		OK   bool `json:"ok"`
		Meta struct {
			Removed  []string `json:"removed"`
			Failures []string `json:"failures"`
		} `json:"meta"`
	}
	require.NoError(t, json.Unmarshal(response, &envelope), string(response))
	assert.False(t, envelope.OK)
	assert.Contains(t, envelope.Meta.Removed, "agent skill")
	require.NotEmpty(t, envelope.Meta.Failures)
	assert.Contains(t, envelope.Meta.Failures[0], "codex binary not found")
}
