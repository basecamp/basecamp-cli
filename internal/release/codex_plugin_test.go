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

func TestStampCodexPluginVersionDoesNotChangeClaudeManifest(t *testing.T) {
	root := repositoryRoot(t)
	fixture := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(fixture, ".codex-plugin"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(fixture, ".claude-plugin"), 0o755))
	copyFile(t, filepath.Join(root, ".codex-plugin", "plugin.json"), filepath.Join(fixture, ".codex-plugin", "plugin.json"))
	copyFile(t, filepath.Join(root, ".claude-plugin", "plugin.json"), filepath.Join(fixture, ".claude-plugin", "plugin.json"))
	claudeBefore, err := os.ReadFile(filepath.Join(fixture, ".claude-plugin", "plugin.json"))
	require.NoError(t, err)

	cmd := exec.Command(filepath.Join(root, "scripts", "stamp-codex-plugin-version.sh"), "1.2.3")
	cmd.Dir = fixture
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))

	assert.Equal(t, "1.2.3", manifestVersion(t, filepath.Join(fixture, ".codex-plugin", "plugin.json")))
	claudeAfter, err := os.ReadFile(filepath.Join(fixture, ".claude-plugin", "plugin.json"))
	require.NoError(t, err)
	assert.Equal(t, claudeBefore, claudeAfter)
}

func TestCodexPluginCheckPassesRepositoryPayload(t *testing.T) {
	root := repositoryRoot(t)
	cmd := exec.Command("python3", filepath.Join(root, "scripts", "check-codex-plugin.py"), root)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	assert.Contains(t, string(output), "Codex plugin check passed")
}

func TestCodexPluginCheckRejectsMissingManifest(t *testing.T) {
	root := repositoryRoot(t)
	cmd := exec.Command("python3", filepath.Join(root, "scripts", "check-codex-plugin.py"), t.TempDir())
	output, err := cmd.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(output), ".codex-plugin/plugin.json")
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
	prereleaseBranch := strings.Index(releaseScript, `if [[ "${PRERELEASE}" == "true" ]]`)
	claudeStamp := strings.Index(releaseScript, `scripts/stamp-plugin-version.sh "${VERSION}"`)
	codexStamp := strings.Index(releaseScript, `scripts/stamp-codex-plugin-version.sh "${VERSION}"`)
	require.NotEqual(t, -1, prereleaseBranch)
	require.Greater(t, claudeStamp, prereleaseBranch)
	require.Greater(t, codexStamp, prereleaseBranch)
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

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}
