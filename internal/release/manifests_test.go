// Package release_test verifies the agent plugin manifests and release wiring.
//
// The repository root is the live plugin payload for both Claude Code and
// Codex: the 37signals marketplace sources basecamp/basecamp-cli directly.
// These tests keep the two thin per-agent manifests consistent with each
// other and with the files they reference. Marketplace-shape enforcement
// (allowed fields, exact values) belongs to each agent's own validator.
package release_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// semverPattern is the strict semver 2.0.0 grammar (semver.org). Go's \d is
// ASCII-only, so non-ASCII digits are rejected.
var semverPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)` +
	`(?:-(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*)?` +
	`(?:\+[0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*)?$`)

type codexInterface struct {
	DisplayName      string `json:"displayName"`
	ShortDescription string `json:"shortDescription"`
	LongDescription  string `json:"longDescription"`
	DeveloperName    string `json:"developerName"`
	Category         string `json:"category"`
	ComposerIcon     string `json:"composerIcon"`
	Logo             string `json:"logo"`
}

type pluginManifest struct {
	Name      string          `json:"name"`
	Version   string          `json:"version"`
	Skills    string          `json:"skills"`
	Interface *codexInterface `json:"interface"`
}

func TestManifestsParseWithMatchingIdentity(t *testing.T) {
	root := repositoryRoot(t)
	claude := readManifest(t, filepath.Join(root, ".claude-plugin", "plugin.json"))
	codex := readManifest(t, filepath.Join(root, ".codex-plugin", "plugin.json"))

	assert.Equal(t, "basecamp", claude.Name)
	assert.Equal(t, "basecamp", codex.Name)
	assert.Regexp(t, semverPattern, claude.Version)
	assert.Regexp(t, semverPattern, codex.Version)
	assert.Equal(t, claude.Version, codex.Version, "manifest versions must stay in lockstep")
}

func TestSemverPatternIsStrict(t *testing.T) {
	tests := []struct {
		version string
		valid   bool
	}{
		{version: "1.2.3", valid: true},
		{version: "1.2.3-alpha.1+build.5", valid: true},
		{version: "1.2.3-0", valid: true},
		{version: "1.2.3-01", valid: false},
		{version: "1.2.3-alpha..1", valid: false},
		{version: "1.2.3٠", valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			assert.Equal(t, tt.valid, semverPattern.MatchString(tt.version))
		})
	}
}

func TestCodexManifestReferencesExistingPaths(t *testing.T) {
	root := repositoryRoot(t)
	manifest := readManifest(t, filepath.Join(root, ".codex-plugin", "plugin.json"))

	require.NotEmpty(t, manifest.Skills, "codex manifest must point at the skills directory")
	skillsDir := requireRepoRelative(t, root, manifest.Skills)
	info, err := os.Stat(skillsDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	require.NotNil(t, manifest.Interface)
	for name, path := range map[string]string{
		"composerIcon": manifest.Interface.ComposerIcon,
		"logo":         manifest.Interface.Logo,
	} {
		require.NotEmpty(t, path, name)
		_, err := os.Stat(requireRepoRelative(t, root, path))
		assert.NoError(t, err, name)
	}

	for _, skill := range []string{
		filepath.Join("skills", "basecamp", "SKILL.md"),
		filepath.Join("skills", "basecamp-doctor", "SKILL.md"),
	} {
		_, err := os.Stat(filepath.Join(root, skill))
		assert.NoError(t, err, skill)
	}
}

func TestCodexManifestInterfaceIsPresentable(t *testing.T) {
	root := repositoryRoot(t)
	manifest := readManifest(t, filepath.Join(root, ".codex-plugin", "plugin.json"))

	require.NotNil(t, manifest.Interface)
	assert.NotEmpty(t, manifest.Interface.DisplayName)
	assert.NotEmpty(t, manifest.Interface.ShortDescription)
	assert.NotEmpty(t, manifest.Interface.LongDescription)
	assert.NotEmpty(t, manifest.Interface.DeveloperName)
	assert.NotEmpty(t, manifest.Interface.Category)
}

// TestHooksFileCommandsInvokeBasecamp tolerates absence: hooks/hooks.json is
// deliberately not shipped yet. When it lands, every hook leaf must invoke the
// basecamp CLI (directly or through the missing-binary wrapper) so both agents
// run only first-party hook commands.
func TestHooksFileCommandsInvokeBasecamp(t *testing.T) {
	root := repositoryRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "hooks", "hooks.json"))
	if os.IsNotExist(err) {
		t.Skip("hooks/hooks.json not present (expected until hooks ship)")
	}
	require.NoError(t, err)

	var config struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(data, &config))
	require.NotEmpty(t, config.Hooks)

	for event, matchers := range config.Hooks {
		require.NotEmpty(t, matchers, event)
		for _, matcher := range matchers {
			require.NotEmpty(t, matcher.Hooks, event)
			for _, hook := range matcher.Hooks {
				assert.Equal(t, "command", hook.Type, event)
				validPrefix := strings.HasPrefix(hook.Command, "basecamp ") ||
					strings.Contains(hook.Command, "exec basecamp ")
				assert.True(t, validPrefix, "%s hook must invoke basecamp: %q", event, hook.Command)
			}
		}
	}
}

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

	assert.Equal(t, "1.2.3", readManifest(t, filepath.Join(fixture, ".codex-plugin", "plugin.json")).Version)
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

	// Both stampers must run only inside the stable (non-prerelease) branch:
	// no invocation before the PRERELEASE guard.
	guard := strings.Index(releaseScript, `if [[ "${PRERELEASE}" == "true" ]]`)
	require.NotEqual(t, -1, guard)
	beforeGuard := releaseScript[:guard]
	assert.NotContains(t, beforeGuard, "scripts/stamp-plugin-version.sh")
	assert.NotContains(t, beforeGuard, "scripts/stamp-codex-plugin-version.sh")
	afterGuard := releaseScript[guard:]
	assert.Contains(t, afterGuard, `scripts/stamp-plugin-version.sh "${VERSION}"`)
	assert.Contains(t, afterGuard, `scripts/stamp-codex-plugin-version.sh "${VERSION}"`)
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func readManifest(t *testing.T, path string) pluginManifest {
	t.Helper()
	var manifest pluginManifest
	require.NoError(t, json.Unmarshal([]byte(readFile(t, path)), &manifest))
	return manifest
}

// requireRepoRelative resolves a manifest path against the repository root and
// fails if it escapes it.
func requireRepoRelative(t *testing.T, root, path string) string {
	t.Helper()
	resolved := filepath.Clean(filepath.Join(root, filepath.FromSlash(path)))
	rel, err := filepath.Rel(root, resolved)
	require.NoError(t, err)
	require.False(t, rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)),
		"manifest path escapes repository root: %s", path)
	return resolved
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
