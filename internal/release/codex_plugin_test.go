package release_test

import (
	"context"
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

func TestStampCodexPluginVersionDoesNotChangeClaudeManifest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release scripts require a POSIX shell")
	}
	root := repositoryRoot(t)
	fixture := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(fixture, ".codex-plugin"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(fixture, ".claude-plugin"), 0o755))
	copyFile(t, filepath.Join(root, ".codex-plugin", "plugin.json"), filepath.Join(fixture, ".codex-plugin", "plugin.json"))
	copyFile(t, filepath.Join(root, ".claude-plugin", "plugin.json"), filepath.Join(fixture, ".claude-plugin", "plugin.json"))
	claudeBefore, err := os.ReadFile(filepath.Join(fixture, ".claude-plugin", "plugin.json"))
	require.NoError(t, err)

	cmd := exec.CommandContext(context.Background(), filepath.Join(root, "scripts", "stamp-codex-plugin-version.sh"), "1.2.3")
	cmd.Dir = fixture
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))

	assert.Equal(t, "1.2.3", manifestVersion(t, filepath.Join(fixture, ".codex-plugin", "plugin.json")))
	claudeAfter, err := os.ReadFile(filepath.Join(fixture, ".claude-plugin", "plugin.json"))
	require.NoError(t, err)
	assert.Equal(t, claudeBefore, claudeAfter)
}

func TestStampCodexPluginVersionCleansTemporaryFileAfterFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release scripts require a POSIX shell")
	}
	root := repositoryRoot(t)
	fixture := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(fixture, ".codex-plugin"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fixture, ".codex-plugin", "plugin.json"), []byte("not json\n"), 0o644))

	cmd := exec.CommandContext(context.Background(), filepath.Join(root, "scripts", "stamp-codex-plugin-version.sh"), "1.2.3")
	cmd.Dir = fixture
	require.Error(t, cmd.Run())

	temporaryFiles, err := filepath.Glob(filepath.Join(fixture, ".codex-plugin", "plugin.json.tmp*"))
	require.NoError(t, err)
	assert.Empty(t, temporaryFiles)
}

func TestCodexPluginCheckPassesRepositoryPayload(t *testing.T) {
	root := repositoryRoot(t)
	cmd := exec.CommandContext(context.Background(), requirePython3(t), filepath.Join(root, "scripts", "check-codex-plugin.py"), root)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	assert.Contains(t, string(output), "Codex plugin check passed")
}

func TestCodexPluginCheckRejectsMissingManifest(t *testing.T) {
	root := repositoryRoot(t)
	cmd := exec.CommandContext(context.Background(), requirePython3(t), filepath.Join(root, "scripts", "check-codex-plugin.py"), t.TempDir())
	output, err := cmd.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(output), ".codex-plugin/plugin.json")
}

func TestCodexPluginCheckUsesStrictSemver(t *testing.T) {
	root := repositoryRoot(t)
	python := requirePython3(t)
	checker := filepath.Join(root, "scripts", "check-codex-plugin.py")
	probe := `import importlib.util, sys; spec = importlib.util.spec_from_file_location("checker", sys.argv[1]); module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module); raise SystemExit(0 if module.SEMVER.fullmatch(sys.argv[2]) else 1)`
	tests := []struct {
		version string
		valid   bool
	}{
		{version: "1.2.3", valid: true},
		{version: "1.2.3-alpha.1+build.5", valid: true},
		{version: "1.2.3-0", valid: true},
		{version: "1.2.3-01", valid: false},
		{version: "1.2.3-alpha..1", valid: false},
		{version: "1.2.3\u0660", valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			cmd := exec.CommandContext(context.Background(), python, "-c", probe, checker, tt.version)
			err := cmd.Run()
			assert.Equal(t, tt.valid, err == nil)
		})
	}
}

func TestCodexPluginCheckerAcceptsWhitespaceIndependentCommands(t *testing.T) {
	root := repositoryRoot(t)
	python := requirePython3(t)
	fixture := t.TempDir()
	for _, relative := range []string{
		".codex-plugin/plugin.json",
		".claude-plugin/plugin.json",
		"hooks/hooks.json",
		"skills/basecamp/SKILL.md",
		"skills/basecamp-doctor/SKILL.md",
		"assets/bc5-snowglobe.png",
		"internal/commands/codex_hook.go",
	} {
		copyFixtureFile(t, root, fixture, relative)
	}
	commandPath := filepath.Join(fixture, "internal", "commands", "codex_hook.go")
	commandSource := readFile(t, commandPath)
	commandSource = strings.ReplaceAll(commandSource, "Use:    \"codex-hook\"", "Use:\t\"codex-hook\"")
	commandSource = strings.ReplaceAll(commandSource, "Use:   \"session-start\"", "Use:\t\t\"session-start\"")
	require.NoError(t, os.WriteFile(commandPath, []byte(commandSource), 0o644))
	rootPath := filepath.Join(fixture, "internal", "cli", "root.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(rootPath), 0o755))
	require.NoError(t, os.WriteFile(rootPath, []byte("package cli\n\nvar _ = commands . NewCodexHookCmd ( )\n"), 0o644))

	cmd := exec.CommandContext(context.Background(), python, filepath.Join(root, "scripts", "check-codex-plugin.py"), fixture)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}

func TestCodexPluginCheckerDoesNotDuplicateUnreadableSourceErrors(t *testing.T) {
	root := repositoryRoot(t)
	fixture := t.TempDir()
	for _, relative := range []string{
		".codex-plugin/plugin.json",
		".claude-plugin/plugin.json",
		"hooks/hooks.json",
		"skills/basecamp/SKILL.md",
		"skills/basecamp-doctor/SKILL.md",
		"assets/bc5-snowglobe.png",
	} {
		copyFixtureFile(t, root, fixture, relative)
	}

	cmd := exec.CommandContext(context.Background(), requirePython3(t), filepath.Join(root, "scripts", "check-codex-plugin.py"), fixture)
	output, err := cmd.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(output), "cannot read hidden command source")
	assert.Contains(t, string(output), "cannot read command registration")
	assert.NotContains(t, string(output), "hidden command source missing")
	assert.NotContains(t, string(output), "Codex hook command is not registered")
}

func TestCodexPluginCheckerReportsInvalidUTF8(t *testing.T) {
	root := repositoryRoot(t)
	fixture := t.TempDir()
	for _, relative := range []string{
		".codex-plugin/plugin.json",
		".claude-plugin/plugin.json",
		"hooks/hooks.json",
		"skills/basecamp/SKILL.md",
		"skills/basecamp-doctor/SKILL.md",
		"assets/bc5-snowglobe.png",
		"internal/commands/codex_hook.go",
		"internal/cli/root.go",
	} {
		copyFixtureFile(t, root, fixture, relative)
	}
	for _, relative := range []string{
		".codex-plugin/plugin.json",
		"internal/commands/codex_hook.go",
		"internal/cli/root.go",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(fixture, relative), []byte{0xff}, 0o644))
	}

	cmd := exec.CommandContext(context.Background(), requirePython3(t), filepath.Join(root, "scripts", "check-codex-plugin.py"), fixture)
	output, err := cmd.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(output), "cannot read JSON")
	assert.Contains(t, string(output), "cannot read hidden command source")
	assert.Contains(t, string(output), "cannot read command registration")
	assert.NotContains(t, string(output), "Traceback")
}

func TestReleaseWiringStampsBothStableManifests(t *testing.T) {
	root := repositoryRoot(t)
	goreleaser := readFile(t, filepath.Join(root, ".goreleaser.yaml"))
	releaseScript := readFile(t, filepath.Join(root, "scripts", "release.sh"))

	assert.Contains(t, goreleaser, "scripts/stamp-plugin-version.sh {{ .Version }}")
	assert.Contains(t, goreleaser, "scripts/stamp-codex-plugin-version.sh {{ .Version }}")
	assert.Contains(t, releaseScript, `scripts/stamp-plugin-version.sh "${VERSION}"`)
	assert.Contains(t, releaseScript, `scripts/stamp-codex-plugin-version.sh "${VERSION}"`)
	assert.Contains(t, releaseScript, "git add nix/package.nix .claude-plugin/plugin.json .codex-plugin/plugin.json")
}

func TestReleaseWiringSkipsPluginStampsForPrereleases(t *testing.T) {
	root := repositoryRoot(t)
	goreleaser := readFile(t, filepath.Join(root, ".goreleaser.yaml"))
	releaseScript := readFile(t, filepath.Join(root, "scripts", "release.sh"))

	assert.Contains(t, goreleaser, "if .Prerelease")
	assert.Contains(t, goreleaser, "Skipping plugin version stamps for prerelease")
	prereleaseStart := strings.Index(releaseScript, `if [[ "${PRERELEASE}" == "true" ]]`)
	require.NotEqual(t, -1, prereleaseStart)
	stableStart := strings.Index(releaseScript[prereleaseStart:], "\nelse\n  # --- Update Nix flake ---")
	require.NotEqual(t, -1, stableStart)
	stableEnd := strings.Index(releaseScript[prereleaseStart+stableStart:], "\nfi\n\n# --- Commit release prep ---")
	require.NotEqual(t, -1, stableEnd)
	prereleaseBlock := releaseScript[prereleaseStart : prereleaseStart+stableStart]
	stableBlock := releaseScript[prereleaseStart+stableStart : prereleaseStart+stableStart+stableEnd]
	assert.NotContains(t, prereleaseBlock, "stamp-plugin-version.sh")
	assert.NotContains(t, prereleaseBlock, "stamp-codex-plugin-version.sh")
	assert.Contains(t, stableBlock, `scripts/stamp-plugin-version.sh "${VERSION}"`)
	assert.Contains(t, stableBlock, `scripts/stamp-codex-plugin-version.sh "${VERSION}"`)
}

func TestClaudeAndCodexManifestVersionsMatch(t *testing.T) {
	root := repositoryRoot(t)
	assert.Equal(t,
		manifestVersion(t, filepath.Join(root, ".claude-plugin", "plugin.json")),
		manifestVersion(t, filepath.Join(root, ".codex-plugin", "plugin.json")),
	)
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func requirePython3(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for the Codex plugin checker")
	}
	return path
}

func manifestVersion(t *testing.T, path string) string {
	t.Helper()
	var manifest struct {
		Version string `json:"version"`
	}
	require.NoError(t, json.Unmarshal([]byte(readFile(t, path)), &manifest))
	return manifest.Version
}

func copyFile(t *testing.T, source, destination string) {
	t.Helper()
	data, err := os.ReadFile(source)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(destination, data, 0o644))
}

func copyFixtureFile(t *testing.T, sourceRoot, destinationRoot, relative string) {
	t.Helper()
	destination := filepath.Join(destinationRoot, relative)
	require.NoError(t, os.MkdirAll(filepath.Dir(destination), 0o755))
	copyFile(t, filepath.Join(sourceRoot, relative), destination)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}
