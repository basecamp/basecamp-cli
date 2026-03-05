package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/basecamp/basecamp-cli/skills"
)

func TestSkillInstallRunE(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

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

func TestSkillInstallFallbackOnNonEmptyDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Pre-create a non-empty directory where the symlink would go.
	// os.Remove can't remove non-empty dirs, so symlink creation will fail,
	// triggering the copy fallback.
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
	if err != nil {
		t.Fatalf("RunE() error = %v (fallback should have handled it)", err)
	}

	// Verify SKILL.md was copied (not symlinked)
	copied, err := os.ReadFile(filepath.Join(symlinkPath, "SKILL.md"))
	if err != nil {
		t.Fatal("SKILL.md not found in fallback copy location")
	}
	embedded, _ := skills.FS.ReadFile("basecamp/SKILL.md")
	if string(copied) != string(embedded) {
		t.Error("fallback copy content does not match embedded")
	}

	// Output should mention fallback (via stdout since no app context)
	output := buf.String()
	if output == "" {
		t.Error("expected fallback output, got empty")
	}
}

func TestSkillInstallOutputKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

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

	// Create test files in source (flat — no subdirs)
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("skill content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "extra.txt"), []byte("extra"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := copySkillFiles(src, dst); err != nil {
		t.Fatalf("copySkillFiles() error = %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
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
	if string(got) != "extra" {
		t.Errorf("extra.txt = %q, want %q", got, "extra")
	}
}

func TestCopySkillFilesRejectsSubdirs(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "dest")

	os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("content"), 0o644)
	os.MkdirAll(filepath.Join(src, "subdir"), 0o755)

	err := copySkillFiles(src, dst)
	if err == nil {
		t.Fatal("expected error for subdirectory in source")
	}
	if !strings.Contains(err.Error(), "subdirectory") {
		t.Errorf("error = %q, want subdirectory rejection message", err)
	}
}

func TestSkillInstallResultMap(t *testing.T) {
	// Run the actual install command and verify the result map is built
	// correctly by checking paths exist with the expected structure.
	home := t.TempDir()
	t.Setenv("HOME", home)

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
