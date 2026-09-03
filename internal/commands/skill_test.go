package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/version"
	"github.com/basecamp/basecamp-cli/skills"
)

func TestSkillInstallRunE(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Create ~/.claude so DetectClaude() returns true and the symlink is created
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := newSkillInstallCmd()
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	// RunE requires app context for app.OK(); without it, falls back to fmt.Fprintf
	err := cmd.RunE(cmd, nil)
	if err != nil {
		t.Fatalf("RunE() error = %v", err)
	}

	// Verify SKILL.md was written
	skillFile := filepath.Join(home, ".agents", "skills", "basecamp", "SKILL.md")
	got, err := os.ReadFile(skillFile)
	if err != nil {
		t.Fatalf("skill file not created: %v", err)
	}
	embedded, _ := skills.FS.ReadFile("basecamp/SKILL.md")
	if string(got) != string(embedded) {
		t.Error("skill file content does not match embedded")
	}

	// Verify symlink was created with correct relative target
	symlinkPath := filepath.Join(home, ".claude", "skills", "basecamp")
	linkTarget, err := os.Readlink(symlinkPath)
	if err != nil {
		t.Fatalf("symlink not created: %v", err)
	}
	wantTarget := filepath.Join("..", "..", ".agents", "skills", "basecamp")
	if linkTarget != wantTarget {
		t.Errorf("symlink target = %q, want %q", linkTarget, wantTarget)
	}
}

func TestSkillInstallIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := newSkillInstallCmd()
	cmd.SetContext(context.Background())
	cmd.SetOut(&bytes.Buffer{})

	// Run twice — both should succeed
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("first RunE() error = %v", err)
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("second RunE() error = %v", err)
	}

	// Symlink still valid after second run
	symlinkPath := filepath.Join(home, ".claude", "skills", "basecamp")
	if _, err := os.Readlink(symlinkPath); err != nil {
		t.Fatalf("symlink broken after second install: %v", err)
	}
}

func TestSkillInstallPreservesNonEmptyUnmanagedClaudeDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// ~/.claude dir exists so DetectClaude() triggers symlink path
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Pre-create a non-empty, unmarked directory where the symlink would go.
	symlinkPath := filepath.Join(home, ".claude", "skills", "basecamp")
	if err := os.MkdirAll(symlinkPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(symlinkPath, "blocker.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newSkillInstallCmd()
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	var unmanaged *unmanagedSkillDirError
	require.ErrorAs(t, err, &unmanaged)
	data, readErr := os.ReadFile(filepath.Join(symlinkPath, "blocker.txt"))
	require.NoError(t, readErr)
	assert.Equal(t, "x", string(data))
	_, statErr := os.Stat(filepath.Join(symlinkPath, "SKILL.md"))
	assert.True(t, os.IsNotExist(statErr), "unmanaged directory must not be claimed or overwritten")
}

func TestClaimSkillDirRejectsPopulatedUnmarkedPredefinedDestination(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine"), 0o644))

	err := claimSkillDir(dir)
	var unmanaged *unmanagedSkillDirError
	require.ErrorAs(t, err, &unmanaged)
	_, statErr := os.Stat(filepath.Join(dir, ownershipMarkerFile))
	assert.True(t, os.IsNotExist(statErr))
}

func TestClaimSkillDirRejectsEmptyUnmarkedPredefinedDestination(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	err := claimSkillDir(dir)
	var unmanaged *unmanagedSkillDirError
	require.ErrorAs(t, err, &unmanaged)
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Empty(t, entries)
}

func TestClaimSkillDirAcceptsMarkerlessManagedPayload(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	embedded, err := skills.FS.ReadFile("basecamp/SKILL.md")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, skillFilename), embedded, 0o644))

	require.NoError(t, claimSkillDir(dir))
}

func TestClaimPredefinedSkillDirAcceptsManagedCanonicalLinkOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_, err := installSkillFiles()
	require.NoError(t, err)
	canonical := filepath.Join(home, ".agents", "skills", "basecamp")

	managedLink := filepath.Join(t.TempDir(), "basecamp")
	require.NoError(t, os.Symlink(canonical, managedLink))
	require.NoError(t, claimPredefinedSkillDir(managedLink))

	unrelated := t.TempDir()
	unrelatedLink := filepath.Join(t.TempDir(), "basecamp")
	require.NoError(t, os.Symlink(unrelated, unrelatedLink))
	var unmanaged *unmanagedSkillDirError
	require.ErrorAs(t, claimPredefinedSkillDir(unrelatedLink), &unmanaged)
}

func TestWizardSkillLocationsUsesClaudeConfigDir(t *testing.T) {
	custom := filepath.Join(t.TempDir(), "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", custom)

	locations, err := wizardSkillLocations()
	require.NoError(t, err)
	index := slices.IndexFunc(locations, func(location skillLocation) bool {
		return location.Name == "Claude Code (Global)"
	})
	require.NotEqual(t, -1, index)
	assert.Equal(t, filepath.Join(custom, "skills", "basecamp", skillFilename), locations[index].Path)
}

func TestLinkSkillToClaudeRefreshesManagedCopyWithUserFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	_, err := installSkillFiles()
	require.NoError(t, err)

	dir := filepath.Join(home, ".claude", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, skillFilename), []byte("old"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ownershipMarkerFile), []byte("managed"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep"), 0o644))

	_, notice, err := linkSkillToClaude()
	require.NoError(t, err)
	assert.Contains(t, notice, "copied files instead")
	embedded, readErr := skills.FS.ReadFile("basecamp/SKILL.md")
	require.NoError(t, readErr)
	got, readErr := os.ReadFile(filepath.Join(dir, skillFilename))
	require.NoError(t, readErr)
	assert.Equal(t, embedded, got)
	notes, readErr := os.ReadFile(filepath.Join(dir, "notes.txt"))
	require.NoError(t, readErr)
	assert.Equal(t, "keep", string(notes))
}

func TestSkillInstallOutputKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := newSkillInstallCmd()
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	// Without app context, RunE falls back to plain text output.
	// Test the result map construction directly by running the command
	// and verifying the file paths exist.
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}

	expectedSkillPath := filepath.Join(home, ".agents", "skills", "basecamp", "SKILL.md")
	expectedSymlinkPath := filepath.Join(home, ".claude", "skills", "basecamp")

	if _, err := os.Stat(expectedSkillPath); err != nil {
		t.Errorf("expected skill_path %q to exist", expectedSkillPath)
	}
	if _, err := os.Lstat(expectedSymlinkPath); err != nil {
		t.Errorf("expected symlink_path %q to exist", expectedSymlinkPath)
	}
}

func TestSkillPrintOutputMatchesEmbedded(t *testing.T) {
	cmd := NewSkillCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("skill print RunE() error = %v", err)
	}

	embedded, _ := skills.FS.ReadFile("basecamp/SKILL.md")
	if buf.String() != string(embedded) {
		t.Error("skill print output does not match embedded SKILL.md")
	}
}

func TestCopySkillFiles(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "dest")

	if err := os.WriteFile(filepath.Join(src, skillFilename), []byte("skill content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ownershipMarkerFile), []byte("managed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "extra.txt"), []byte("extra"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "extra.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := copySkillFiles(src, dst); err != nil {
		t.Fatalf("copySkillFiles() error = %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dst, skillFilename))
	if err != nil {
		t.Fatalf("reading SKILL.md: %v", err)
	}
	if string(got) != "skill content" {
		t.Errorf("SKILL.md = %q, want %q", got, "skill content")
	}
	got, err = os.ReadFile(filepath.Join(dst, "extra.txt"))
	if err != nil {
		t.Fatalf("reading extra.txt: %v", err)
	}
	if string(got) != "keep" {
		t.Errorf("extra.txt = %q, want preserved user content", got)
	}
}

func TestWriteWizardSkillRejectsSymlinkAtPredefinedDestination(t *testing.T) {
	target := filepath.Join(t.TempDir(), "target")
	require.NoError(t, os.WriteFile(target, []byte("keep"), 0o644))
	link := filepath.Join(t.TempDir(), skillFilename)
	require.NoError(t, os.Symlink(target, link))

	err := writeWizardSkill(link, []byte("replacement"), true)
	require.Error(t, err)
	got, readErr := os.ReadFile(target)
	require.NoError(t, readErr)
	assert.Equal(t, "keep", string(got))
}

func TestCopySkillFilesRejectsNonRegularManagedDestination(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "dest")

	require.NoError(t, os.WriteFile(filepath.Join(src, skillFilename), []byte("content"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(src, ownershipMarkerFile), []byte("managed"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dst, skillFilename), 0o755))

	err := copySkillFiles(src, dst)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "was not written by basecamp-cli")
}

func TestCopySkillFilesAcceptsLegacyVersionOwnershipWithoutMarker(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "dest")
	require.NoError(t, os.WriteFile(filepath.Join(src, skillFilename), []byte("content"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(src, installedVersionFile), []byte("0.9.1"), 0o644))

	require.NoError(t, copySkillFiles(src, dst))
	assert.FileExists(t, filepath.Join(dst, skillFilename))
	assert.FileExists(t, filepath.Join(dst, installedVersionFile))
	_, err := os.Stat(filepath.Join(dst, ownershipMarkerFile))
	assert.True(t, os.IsNotExist(err))
}

// Pin the literals rather than deriving them, so a test can't mirror a typo the
// code has. Codex's entry is computed by codexGlobalSkillPath and covered
// separately.
//
// These are install targets, not the full set of paths an agent reads. opencode
// takes an optional plural throughout — its own table reads
// `~/.config/opencode/skill(s)/<name>/SKILL.md` — so the singular form works too
// and is kept alive for refresh in legacySkillLocations.
func TestSkillLocationsMatchAgentSearchPaths(t *testing.T) {
	want := map[string]string{
		"Agents (Shared)":       "~/.agents/skills/basecamp/SKILL.md",
		"Claude Code (Global)":  "~/.claude/skills/basecamp/SKILL.md",
		"Claude Code (Project)": ".claude/skills/basecamp/SKILL.md",
		"OpenCode (Global)":     "~/.config/opencode/skills/basecamp/SKILL.md",
		"OpenCode (Project)":    ".opencode/skills/basecamp/SKILL.md",
	}

	got := make(map[string]string, len(skillLocations))
	for _, loc := range skillLocations {
		got[loc.Name] = loc.Path
	}

	for name, path := range want {
		assert.Equal(t, path, got[name], "install target for %s", name)
	}
}

// A wizard install written before #624 sits at opencode's singular path.
// opencode still loads it, so dropping it from the refresh set does not break
// the skill — it freezes it at the version that wrote it, which is worse than
// breaking because nothing reports it.
func TestRefreshAllInstalledSkills_LegacyOpenCodePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home) // no claude binary

	origVersion := version.Version
	version.Version = "5.0.0"
	defer func() { version.Version = origVersion }()

	embedded, err := skills.FS.ReadFile("basecamp/SKILL.md")
	require.NoError(t, err)

	baseline := filepath.Join(home, ".agents", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(baseline, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(baseline, "SKILL.md"), []byte("old"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(baseline, installedVersionFile), []byte("4.0.0"), 0o644))

	// Singular — what the wizard wrote before #624.
	legacy := filepath.Join(home, ".config", "opencode", "skill", "basecamp")
	require.NoError(t, os.MkdirAll(legacy, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "SKILL.md"), []byte("old"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, installedVersionFile), []byte("4.0.0"), 0o644))

	require.True(t, refreshAllInstalledSkills())

	got, readErr := os.ReadFile(filepath.Join(legacy, "SKILL.md"))
	require.NoError(t, readErr)
	assert.Equal(t, string(embedded), string(got),
		"a pre-#624 opencode install must keep getting refreshed")

	// Refreshing a legacy install must not conjure the new path.
	_, statErr := os.Stat(filepath.Join(home, ".config", "opencode", "skills", "basecamp", "SKILL.md"))
	assert.True(t, os.IsNotExist(statErr), "refresh updates in place; it does not install elsewhere")
}

func TestNormalizeSkillPath(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"~/.claude/skills/basecamp/SKILL.md", "~/.claude/skills/basecamp/SKILL.md"},
		{"/tmp/skills", filepath.Join("/tmp/skills", "basecamp", "SKILL.md")},
		{"/tmp/basecamp", filepath.Join("/tmp/basecamp", "SKILL.md")},
		{"/tmp/basecamp/", filepath.Join("/tmp/basecamp", "SKILL.md")},
		{"/tmp/other.md", "/tmp/other.md"},
		{"  ~/.claude/skills/basecamp/SKILL.md  ", "~/.claude/skills/basecamp/SKILL.md"},
	}
	for _, tt := range tests {
		got := normalizeSkillPath(tt.input)
		if got != tt.want {
			t.Errorf("normalizeSkillPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExpandSkillPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}

	tests := []struct {
		input, want string
	}{
		{"~/foo/bar", filepath.Join(home, "foo/bar")},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
		{"~", home},
	}
	for _, tt := range tests {
		got := expandSkillPath(tt.input)
		if got != tt.want {
			t.Errorf("expandSkillPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSkillInstallResultMap(t *testing.T) {
	// Run the actual install command and verify the result map is built
	// correctly by checking paths exist with the expected structure.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := newSkillInstallCmd()
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE() error = %v", err)
	}

	// Without app context, the fallback path writes "Installed skill to <path>".
	// Verify the output references the correct path.
	output := buf.String()
	expectedPath := filepath.Join(home, ".agents", "skills", "basecamp", "SKILL.md")
	if !strings.Contains(output, expectedPath) {
		t.Errorf("output = %q, want it to contain %q", output, expectedPath)
	}

	// Verify both paths from the result map exist on disk.
	if _, err := os.Stat(expectedPath); err != nil {
		t.Errorf("skill_path %q does not exist", expectedPath)
	}
	symlinkPath := filepath.Join(home, ".claude", "skills", "basecamp")
	if _, err := os.Lstat(symlinkPath); err != nil {
		t.Errorf("symlink_path %q does not exist", symlinkPath)
	}
}

func TestSkillInstallNoClaude(t *testing.T) {
	// When Claude is not detected, skill install should NOT create ~/.claude/
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home) // no claude binary

	cmd := newSkillInstallCmd()
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := cmd.RunE(cmd, nil)
	if err != nil {
		t.Fatalf("RunE() error = %v", err)
	}

	// Baseline skill should be installed
	skillFile := filepath.Join(home, ".agents", "skills", "basecamp", "SKILL.md")
	if _, err := os.Stat(skillFile); err != nil {
		t.Errorf("skill file should exist: %v", err)
	}

	// ~/.claude should NOT have been created
	claudeDir := filepath.Join(home, ".claude")
	if _, err := os.Stat(claudeDir); err == nil {
		t.Error("~/.claude should not be created when Claude is not detected")
	}
}

func TestInstallSkillFilesStampsVersion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	_, err := installSkillFiles()
	require.NoError(t, err)

	versionFile := filepath.Join(home, ".agents", "skills", "basecamp", installedVersionFile)
	got, err := os.ReadFile(versionFile)
	require.NoError(t, err)
	assert.Equal(t, version.Version, string(got))
}

func TestInstalledSkillVersion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// No file → empty string
	assert.Equal(t, "", installedSkillVersion())

	// Create version file
	dir := filepath.Join(home, ".agents", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, installedVersionFile), []byte("1.2.3\n"), 0o644))

	assert.Equal(t, "1.2.3", installedSkillVersion())
}

func TestRefreshSkillsIfVersionChanged_SentinelMissing(t *testing.T) {
	home := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configDir)

	// Install baseline skill so refresh has something to update
	_, err := installSkillFiles()
	require.NoError(t, err)

	// Save original version and set a non-dev version for testing
	origVersion := version.Version
	version.Version = "1.0.0"
	defer func() { version.Version = origVersion }()

	refreshed := RefreshSkillsIfVersionChanged()
	assert.True(t, refreshed, "should refresh when sentinel is missing")

	// Sentinel should now exist
	sentinel, err := os.ReadFile(filepath.Join(configDir, "basecamp", ".last-run-version"))
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", string(sentinel))
}

func TestRefreshSkillsIfVersionChanged_SentinelMatches(t *testing.T) {
	home := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configDir)

	origVersion := version.Version
	version.Version = "1.0.0"
	defer func() { version.Version = origVersion }()

	// Install skill and write matching sentinel
	_, err := installSkillFiles()
	require.NoError(t, err)
	sentinelDir := filepath.Join(configDir, "basecamp")
	require.NoError(t, os.MkdirAll(sentinelDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sentinelDir, ".last-run-version"), []byte("1.0.0"), 0o644))

	refreshed := RefreshSkillsIfVersionChanged()
	assert.False(t, refreshed, "should not refresh when sentinel matches")
}

func TestRefreshSkillsIfVersionChanged_SentinelMismatched(t *testing.T) {
	home := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configDir)

	origVersion := version.Version
	version.Version = "2.0.0"
	defer func() { version.Version = origVersion }()

	// Install baseline skill and write old sentinel
	_, err := installSkillFiles()
	require.NoError(t, err)
	sentinelDir := filepath.Join(configDir, "basecamp")
	require.NoError(t, os.MkdirAll(sentinelDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sentinelDir, ".last-run-version"), []byte("1.0.0"), 0o644))

	refreshed := RefreshSkillsIfVersionChanged()
	assert.True(t, refreshed, "should refresh when sentinel mismatches")

	// Sentinel should be updated
	sentinel, err := os.ReadFile(filepath.Join(sentinelDir, ".last-run-version"))
	require.NoError(t, err)
	assert.Equal(t, "2.0.0", string(sentinel))

	// Installed version should be updated
	assert.Equal(t, "2.0.0", installedSkillVersion())
}

func TestRefreshSkillsIfVersionChanged_SkipsDev(t *testing.T) {
	origVersion := version.Version
	version.Version = "dev"
	defer func() { version.Version = origVersion }()

	refreshed := RefreshSkillsIfVersionChanged()
	assert.False(t, refreshed, "should skip for dev builds")
}

func TestRefreshSkillsIfVersionChanged_NoSentinelUpdateOnFailure(t *testing.T) {
	home := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configDir)

	origVersion := version.Version
	version.Version = "3.0.0"
	defer func() { version.Version = origVersion }()

	// Install baseline skill, then make the skill file read-only
	// so refreshAllInstalledSkills() will fail on write
	_, err := installSkillFiles()
	require.NoError(t, err)

	skillFile := filepath.Join(home, ".agents", "skills", "basecamp", "SKILL.md")
	require.NoError(t, os.Chmod(skillFile, 0o444))
	defer os.Chmod(skillFile, 0o644) //nolint:errcheck // cleanup

	// Write old sentinel
	sentinelDir := filepath.Join(configDir, "basecamp")
	require.NoError(t, os.MkdirAll(sentinelDir, 0o755))
	sentinelPath := filepath.Join(sentinelDir, ".last-run-version")
	require.NoError(t, os.WriteFile(sentinelPath, []byte("2.0.0"), 0o644))

	refreshed := RefreshSkillsIfVersionChanged()
	assert.False(t, refreshed, "should not report refresh on failure")

	// Sentinel should NOT be updated (so next run retries)
	sentinel, err := os.ReadFile(sentinelPath)
	require.NoError(t, err)
	assert.Equal(t, "2.0.0", string(sentinel), "sentinel should remain unchanged on failure")
}

func TestRefreshSkillsIfVersionChangedRetriesInvalidClaudeConfig(t *testing.T) {
	home := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home)
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("CLAUDE_CONFIG_DIR", "relative/invalid")

	origVersion := version.Version
	version.Version = "3.0.0"
	defer func() { version.Version = origVersion }()
	_, err := installSkillFiles()
	require.NoError(t, err)
	sentinelDir := filepath.Join(configDir, "basecamp")
	require.NoError(t, os.MkdirAll(sentinelDir, 0o755))
	sentinel := filepath.Join(sentinelDir, ".last-run-version")
	require.NoError(t, os.WriteFile(sentinel, []byte("2.0.0"), 0o644))

	assert.False(t, RefreshSkillsIfVersionChanged())
	got, err := os.ReadFile(sentinel)
	require.NoError(t, err)
	assert.Equal(t, "2.0.0", string(got))
}

func TestRefreshSkillsIfVersionChangedAdvancesSentinelForUnmanagedBaseline(t *testing.T) {
	home := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home)
	t.Setenv("XDG_CONFIG_HOME", configDir)

	origVersion := version.Version
	version.Version = "3.0.0"
	defer func() { version.Version = origVersion }()

	dir := filepath.Join(home, ".agents", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, skillFilename), []byte("user"), 0o644))

	assert.False(t, RefreshSkillsIfVersionChanged())
	sentinel := filepath.Join(configDir, "basecamp", ".last-run-version")
	got, err := os.ReadFile(sentinel)
	require.NoError(t, err)
	assert.Equal(t, "3.0.0", string(got))
	data, err := os.ReadFile(filepath.Join(dir, skillFilename))
	require.NoError(t, err)
	assert.Equal(t, "user", string(data))
}

func TestRefreshSkillsIfVersionChangedRetriesManagedLocationFailureWithoutManagedBaseline(t *testing.T) {
	home := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home)
	t.Setenv("XDG_CONFIG_HOME", configDir)

	origVersion := version.Version
	version.Version = "3.0.0"
	defer func() { version.Version = origVersion }()

	baseline := filepath.Join(home, ".agents", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(baseline, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(baseline, skillFilename), []byte("user"), 0o644))

	claudeDir := filepath.Join(home, ".claude", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))
	embedded, err := skills.FS.ReadFile("basecamp/SKILL.md")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, skillFilename), embedded, 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(claudeDir, ownershipMarkerFile), 0o755))

	assert.False(t, RefreshSkillsIfVersionChanged())
	_, err = os.Stat(filepath.Join(configDir, "basecamp", ".last-run-version"))
	assert.True(t, os.IsNotExist(err), "a failed managed location must keep the sentinel stale")
}

func TestRefreshSkillsIfVersionChangedRetriesFailedClaudeLinkRepair(t *testing.T) {
	home := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configDir)
	installExecutableStub(t, "claude")

	origVersion := version.Version
	version.Version = "3.0.0"
	defer func() { version.Version = origVersion }()

	link := filepath.Join(home, ".claude", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.Symlink(claudeSkillLinkTarget, link))
	opencode := filepath.Join(home, ".config", "opencode", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(opencode, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(opencode, skillFilename), []byte("old"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(opencode, ownershipMarkerFile), []byte("managed"), 0o644))

	assert.False(t, RefreshSkillsIfVersionChanged(), "a partial refresh must not report success")
	_, err := os.Stat(filepath.Join(configDir, "basecamp", ".last-run-version"))
	assert.True(t, os.IsNotExist(err), "a failed managed link repair must keep the sentinel stale")
}

func TestRefreshSkillsIfVersionChangedAcceptsManagedClaudeLink(t *testing.T) {
	home := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home)
	t.Setenv("XDG_CONFIG_HOME", configDir)

	origVersion := version.Version
	version.Version = "3.0.0"
	defer func() { version.Version = origVersion }()

	_, err := installSkillFiles()
	require.NoError(t, err)
	_, _, err = linkSkillToClaude()
	require.NoError(t, err)

	assert.True(t, RefreshSkillsIfVersionChanged())
	sentinel := filepath.Join(configDir, "basecamp", ".last-run-version")
	got, err := os.ReadFile(sentinel)
	require.NoError(t, err)
	assert.Equal(t, "3.0.0", string(got))
}

func TestRefreshAllInstalledSkillsRejectsSymlinkedSkillDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home)

	target := filepath.Join(t.TempDir(), "target")
	require.NoError(t, os.MkdirAll(target, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, skillFilename), []byte("outside"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(target, ownershipMarkerFile), []byte("managed"), 0o644))
	link := filepath.Join(home, ".config", "opencode", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.Symlink(target, link))

	assert.False(t, refreshAllInstalledSkills())
	got, err := os.ReadFile(filepath.Join(target, skillFilename))
	require.NoError(t, err)
	assert.Equal(t, "outside", string(got))
	linkInfo, err := os.Lstat(link)
	require.NoError(t, err)
	assert.NotZero(t, linkInfo.Mode()&os.ModeSymlink)
	linkTarget, err := os.Readlink(link)
	require.NoError(t, err)
	assert.Equal(t, target, linkTarget)
}

func TestRefreshAllInstalledSkillsRejectsSymlinkedSkillAncestor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home)

	externalConfig := t.TempDir()
	target := filepath.Join(externalConfig, "opencode", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(target, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, skillFilename), []byte("outside"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(target, ownershipMarkerFile), []byte("managed"), 0o644))
	require.NoError(t, os.Symlink(externalConfig, filepath.Join(home, ".config")))

	assert.False(t, refreshAllInstalledSkills())
	got, err := os.ReadFile(filepath.Join(target, skillFilename))
	require.NoError(t, err)
	assert.Equal(t, "outside", string(got))
}

func TestLinkSkillToClaudeUsesResolvedConfigDirectoryForRelativeTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_, err := installSkillFiles()
	require.NoError(t, err)

	realConfig := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.MkdirAll(realConfig, 0o755))
	configAlias := filepath.Join(home, "claude-alias")
	require.NoError(t, os.Symlink(realConfig, configAlias))
	t.Setenv("CLAUDE_CONFIG_DIR", configAlias)

	link, _, err := linkSkillToClaude()
	require.NoError(t, err)
	info, err := os.Lstat(link)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink)
	got, err := os.ReadFile(filepath.Join(link, skillFilename))
	require.NoError(t, err)
	embedded, err := skills.FS.ReadFile("basecamp/SKILL.md")
	require.NoError(t, err)
	assert.Equal(t, embedded, got)
}

func TestLinkSkillToClaudeAcceptsLegacyTargetWhenAgentsDirectoryIsSymlinked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	realAgents := filepath.Join(t.TempDir(), "agents")
	baseline := filepath.Join(realAgents, "skills", "basecamp")
	require.NoError(t, os.MkdirAll(baseline, 0o755))
	embedded, err := skills.FS.ReadFile("basecamp/SKILL.md")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(baseline, skillFilename), embedded, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(baseline, ownershipMarkerFile), []byte("managed"), 0o644))
	require.NoError(t, os.Symlink(realAgents, filepath.Join(home, ".agents")))

	link := filepath.Join(home, ".claude", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.Symlink(claudeSkillLinkTarget, link))

	got, _, err := linkSkillToClaude()
	require.NoError(t, err)
	assert.Equal(t, link, got)
	data, err := os.ReadFile(filepath.Join(link, skillFilename))
	require.NoError(t, err)
	assert.Equal(t, embedded, data)
}

func TestInstallSkillFilesRejectsSymlinkedParent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	external := t.TempDir()
	require.NoError(t, os.Symlink(external, filepath.Join(home, ".agents")))

	_, err := installSkillFiles()
	var unmanaged *unmanagedSkillDirError
	require.ErrorAs(t, err, &unmanaged)
	_, statErr := os.Lstat(filepath.Join(external, "skills", "basecamp", skillFilename))
	assert.True(t, os.IsNotExist(statErr))
}

func TestClaimPredefinedSkillDirRejectsProjectRelativeSymlinkedParent(t *testing.T) {
	project := t.TempDir()
	t.Chdir(project)
	external := t.TempDir()
	require.NoError(t, os.Symlink(external, filepath.Join(project, ".opencode")))

	_, err := claimPredefinedSkillDirForWrite(filepath.Join(".opencode", "skills", "basecamp"))
	var unmanaged *unmanagedSkillDirError
	require.ErrorAs(t, err, &unmanaged)
	_, statErr := os.Lstat(filepath.Join(external, "skills", "basecamp"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestLinkSkillToClaudeRejectsSymlinkedSkillsParent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_, err := installSkillFiles()
	require.NoError(t, err)
	external := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
	require.NoError(t, os.Symlink(external, filepath.Join(home, ".claude", "skills")))

	_, _, err = linkSkillToClaude()
	var unmanaged *unmanagedSkillDirError
	require.ErrorAs(t, err, &unmanaged)
	_, statErr := os.Lstat(filepath.Join(external, "basecamp"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestLinkSkillToClaudeRejectsSymlinkedBaselineDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := filepath.Join(t.TempDir(), "basecamp")
	require.NoError(t, os.MkdirAll(target, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, skillFilename), []byte("skill"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(target, ownershipMarkerFile), []byte("managed"), 0o644))
	baseline := filepath.Join(home, ".agents", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(filepath.Dir(baseline), 0o755))
	require.NoError(t, os.Symlink(target, baseline))

	_, _, err := linkSkillToClaude()
	var unmanaged *unmanagedSkillDirError
	require.ErrorAs(t, err, &unmanaged)
	_, statErr := os.Lstat(filepath.Join(home, ".claude", "skills", "basecamp"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestRefreshManagedSkillCountsNonRegularSkillAsFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home)

	dir := filepath.Join(home, ".config", "opencode", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, skillFilename), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ownershipMarkerFile), []byte("managed"), 0o644))

	outcome := refreshInstalledSkills()
	assert.Equal(t, 0, outcome.updated)
	assert.Equal(t, 1, outcome.failed)
}

func TestRefreshManagedSkillRepairsMissingSkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home)

	dir := filepath.Join(home, ".config", "opencode", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ownershipMarkerFile), []byte("managed"), 0o644))

	outcome := refreshInstalledSkills()
	assert.Equal(t, 1, outcome.updated)
	assert.Equal(t, 0, outcome.failed)
	embedded, err := skills.FS.ReadFile("basecamp/SKILL.md")
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(dir, skillFilename))
	require.NoError(t, err)
	assert.Equal(t, embedded, got)
}

func TestRefreshManagedSkillCountsMalformedMarkerWithMissingSkillAsFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home)

	dir := filepath.Join(home, ".config", "opencode", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ownershipMarkerFile), 0o755))

	outcome := refreshInstalledSkills()
	assert.Equal(t, 0, outcome.updated)
	assert.Equal(t, 1, outcome.failed)
}

func TestRefreshAllInstalledSkillsClaimsVerifiedMarkerlessWizardPayload(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home)

	dir := filepath.Join(home, ".config", "opencode", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	embedded, err := skills.FS.ReadFile("basecamp/SKILL.md")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, skillFilename), embedded, 0o644))

	assert.True(t, refreshAllInstalledSkills())
	assert.True(t, regularFile(filepath.Join(dir, ownershipMarkerFile)))
}

func TestRefreshAllInstalledSkillsDoesNotClaimMarkerlessDirectoryWithUserFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home)
	dir := filepath.Join(home, ".config", "opencode", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	embedded, err := skills.FS.ReadFile("basecamp/SKILL.md")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, skillFilename), embedded, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine"), 0o644))

	assert.False(t, refreshAllInstalledSkills())
	_, statErr := os.Lstat(filepath.Join(dir, ownershipMarkerFile))
	assert.True(t, os.IsNotExist(statErr))
	data, err := os.ReadFile(filepath.Join(dir, "notes.txt"))
	require.NoError(t, err)
	assert.Equal(t, "mine", string(data))
}

func TestRefreshAllInstalledSkillsUsesConfiguredClaudeCopy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	custom := filepath.Join(home, "custom-claude")
	t.Setenv("CLAUDE_CONFIG_DIR", custom)

	dir := filepath.Join(custom, "skills", "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, skillFilename), []byte("old"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ownershipMarkerFile), []byte("managed"), 0o644))

	assert.True(t, refreshAllInstalledSkills())
	embedded, err := skills.FS.ReadFile("basecamp/SKILL.md")
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(dir, skillFilename))
	require.NoError(t, err)
	assert.Equal(t, embedded, got)
}

func TestRefreshAllInstalledSkillsSkipsRelativeCodexHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home)
	project := t.TempDir()
	t.Chdir(project)
	t.Setenv("CODEX_HOME", ".codex")

	index := slices.IndexFunc(skillLocations, func(location skillLocation) bool {
		return location.Name == "Codex (Global)"
	})
	require.NotEqual(t, -1, index)
	original := skillLocations[index].Path
	skillLocations[index].Path = codexGlobalSkillPath()
	t.Cleanup(func() { skillLocations[index].Path = original })

	dir := filepath.Join(project, ".codex", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, skillFilename), []byte("project-owned"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ownershipMarkerFile), []byte("managed"), 0o644))

	assert.False(t, refreshAllInstalledSkills())
	data, err := os.ReadFile(filepath.Join(dir, skillFilename))
	require.NoError(t, err)
	assert.Equal(t, "project-owned", string(data))
}

func TestRefreshAllInstalledSkills_MultipleLocations(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home) // no claude binary

	origVersion := version.Version
	version.Version = "5.0.0"
	defer func() { version.Version = origVersion }()

	embedded, err := skills.FS.ReadFile("basecamp/SKILL.md")
	require.NoError(t, err)

	// Pre-install skill at baseline and Claude global locations
	baseline := filepath.Join(home, ".agents", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(baseline, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(baseline, "SKILL.md"), []byte("old"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(baseline, installedVersionFile), []byte("4.0.0"), 0o644))

	claudeSkill := filepath.Join(home, ".claude", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(claudeSkill, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(claudeSkill, "SKILL.md"), []byte("old"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(claudeSkill, installedVersionFile), []byte("4.0.0"), 0o644))

	opencode := filepath.Join(home, ".config", "opencode", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(opencode, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(opencode, "SKILL.md"), []byte("old"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(opencode, installedVersionFile), []byte("4.0.0"), 0o644))

	refreshed := refreshAllInstalledSkills()
	assert.True(t, refreshed)

	// All three should be updated
	for _, path := range []string{
		filepath.Join(baseline, "SKILL.md"),
		filepath.Join(claudeSkill, "SKILL.md"),
		filepath.Join(opencode, "SKILL.md"),
	} {
		got, readErr := os.ReadFile(path)
		require.NoError(t, readErr, "reading %s", path)
		assert.Equal(t, string(embedded), string(got), "content mismatch at %s", path)
	}

	// Version stamp should be updated
	assert.Equal(t, "5.0.0", installedSkillVersion())
}

func TestRefreshAllInstalledSkills_SkipsAbsentLocations(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home) // no claude binary

	origVersion := version.Version
	version.Version = "5.0.0"
	defer func() { version.Version = origVersion }()

	// Only install at baseline
	baseline := filepath.Join(home, ".agents", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(baseline, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(baseline, "SKILL.md"), []byte("old"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(baseline, installedVersionFile), []byte("4.0.0"), 0o644))

	refreshed := refreshAllInstalledSkills()
	assert.True(t, refreshed)

	// Claude location should NOT have been created
	claudeSkill := filepath.Join(home, ".claude", "skills", "basecamp", "SKILL.md")
	_, err := os.Stat(claudeSkill)
	assert.True(t, os.IsNotExist(err), "should not create skill at absent location")
}

func TestRefreshAllInstalledSkills_SkipsProjectRelativePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home) // no claude binary

	origVersion := version.Version
	version.Version = "5.0.0"
	defer func() { version.Version = origVersion }()

	// Use a temp dir as working directory to avoid polluting the repo
	projectDir := t.TempDir()
	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(projectDir))
	defer os.Chdir(origDir) //nolint:errcheck // cleanup

	// Create project-relative skill file in the temp working directory
	require.NoError(t, os.MkdirAll(filepath.Join(".claude", "skills", "basecamp"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(".claude", "skills", "basecamp", "SKILL.md"), []byte("project"), 0o644))

	// Install baseline so refresh returns true
	baseline := filepath.Join(home, ".agents", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(baseline, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(baseline, "SKILL.md"), []byte("old"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(baseline, installedVersionFile), []byte("4.0.0"), 0o644))

	refreshAllInstalledSkills()

	// Project-relative file should be untouched
	got, err := os.ReadFile(filepath.Join(".claude", "skills", "basecamp", "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, "project", string(got), "project-relative skill should not be refreshed")
}

func TestRefreshAllInstalledSkillsPreservesUnmanagedSkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".agents", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	skillPath := filepath.Join(dir, skillFilename)
	require.NoError(t, os.WriteFile(skillPath, []byte("user-authored"), 0o644))

	assert.False(t, refreshAllInstalledSkills())
	data, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	assert.Equal(t, "user-authored", string(data))
	_, err = os.Stat(filepath.Join(dir, ownershipMarkerFile))
	assert.True(t, os.IsNotExist(err), "refresh must not claim an unmanaged directory")
}

func TestRepairClaudeSkillLink_PreservesUnmanagedBrokenSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Ensure ~/.claude/skills exists so the symlink can be placed there
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude", "skills"), 0o755))

	_, err := installSkillFiles()
	require.NoError(t, err)

	// Create a broken symlink
	symlinkPath := filepath.Join(home, ".claude", "skills", "basecamp")
	require.NoError(t, os.Symlink("/nonexistent/target", symlinkPath))

	// Verify it's broken
	_, err = os.Stat(symlinkPath)
	require.True(t, os.IsNotExist(err), "symlink should be broken")

	require.NoError(t, repairClaudeSkillLink())

	// An arbitrary target is user state, even when broken.
	target, readErr := os.Readlink(symlinkPath)
	require.NoError(t, readErr)
	assert.Equal(t, "/nonexistent/target", target)
}

func TestRepairClaudeSkillLink_HealthySymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create baseline skill and a healthy symlink
	baseline := filepath.Join(home, ".agents", "skills", "basecamp")
	require.NoError(t, os.MkdirAll(baseline, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(baseline, "SKILL.md"), []byte("skill"), 0o644))

	symlinkDir := filepath.Join(home, ".claude", "skills")
	require.NoError(t, os.MkdirAll(symlinkDir, 0o755))
	require.NoError(t, os.Symlink(baseline, filepath.Join(symlinkDir, "basecamp")))

	// Read the symlink target before repair
	targetBefore, _ := os.Readlink(filepath.Join(symlinkDir, "basecamp"))

	require.NoError(t, repairClaudeSkillLink())

	// Target should be unchanged (no unnecessary repair)
	targetAfter, _ := os.Readlink(filepath.Join(symlinkDir, "basecamp"))
	assert.Equal(t, targetBefore, targetAfter, "healthy symlink should not be modified")
}

// TestSkillWizardReportsWhenItCannotPrompt covers the gap between "the user
// said no" and "nobody could be asked". app.IsInteractive() looks at stdin and
// stdout, but huh draws the form to stderr — so `basecamp skill 2>somewhere`
// gets past the interactivity check and only then finds it has nowhere to draw.
//
// Every prompt error used to print "Installation canceled." and exit 0, which
// claims an answer nobody gave, and leaves a caller believing it declined an
// install it never saw. A real cancellation still exits 0; this must not.
func TestSkillWizardReportsWhenItCannotPrompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	nonInteractiveStdin(t, "devnull")

	buf := &bytes.Buffer{}
	styles := tui.NewStylesWithTheme(tui.ResolveTheme(false))

	err := skillPromptFailed(buf, styles, tui.ErrNotInteractive)

	require.Error(t, err, "being unable to prompt is not a successful cancellation")
	outErr := output.AsError(err)
	require.NotNil(t, outErr)
	assert.Equal(t, output.CodeUsage, outErr.Code)
	assert.Contains(t, outErr.Hint, "basecamp skill install",
		"the hint must name the non-interactive path that works")
	assert.NotContains(t, buf.String(), "canceled",
		"must not claim the user canceled when the user was never asked")
}

// TestSkillWizardTreatsCancellationAsSuccess is the other half: an actual
// cancellation is an answer, and answering no is not an error.
func TestSkillWizardTreatsCancellationAsSuccess(t *testing.T) {
	buf := &bytes.Buffer{}
	styles := tui.NewStylesWithTheme(tui.ResolveTheme(false))

	err := skillPromptFailed(buf, styles, tui.ErrCanceled)

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "canceled")
}

// TestSkillWizardPropagatesRealPromptErrors is the case that makes the other
// two mean anything. Cancellation is the *only* prompt outcome that exits 0, so
// the default has to be propagation.
//
// huh signals a real dismissal with ErrUserAborted and nothing else; a timeout,
// or a bubbletea or runtime failure, arrives as an ordinary error. Treating
// every error as cancellation — which this did — turns any of those into
// "Installation canceled." and exit 0, reporting a decision nobody made.
func TestSkillWizardPropagatesRealPromptErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		// huh's own values, spelled out rather than imported: the launcher
		// backstop keeps huh to a single import path, and these are plain
		// sentinels (form.go:51-57).
		{"timeout", errors.New("timeout")},
		{"a wrapped bubbletea failure", fmt.Errorf("huh: %w", errors.New("could not open a new TTY"))},
		{"anything unrecognized", errors.New("boom")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			styles := tui.NewStylesWithTheme(tui.ResolveTheme(false))

			err := skillPromptFailed(buf, styles, tc.err)

			require.Error(t, err, "a failure that is not a cancellation must not exit 0")
			assert.ErrorIs(t, err, tc.err, "the cause must survive so it can be diagnosed")
			assert.NotContains(t, buf.String(), "canceled",
				"must not claim the user canceled when the prompt failed")
		})
	}
}
