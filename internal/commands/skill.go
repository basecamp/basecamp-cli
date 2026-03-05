package commands

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/skills"
)

// NewSkillCmd creates the skill command.
func NewSkillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Manage the embedded agent skill file",
		Long:  "Print or install the SKILL.md embedded in this binary.",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := skills.FS.ReadFile("basecamp/SKILL.md")
			if err != nil {
				return fmt.Errorf("reading embedded skill: %w", err)
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), string(data))
			return err
		},
	}
	cmd.AddCommand(newSkillInstallCmd())
	return cmd
}

func newSkillInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install the basecamp skill globally for Claude",
		Long:  "Copies the embedded SKILL.md to ~/.agents/skills/basecamp/ and creates a symlink in ~/.claude/skills/basecamp.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("getting home directory: %w", err)
			}

			skillDir := filepath.Join(home, ".agents", "skills", "basecamp")
			skillFile := filepath.Join(skillDir, "SKILL.md")
			symlinkDir := filepath.Join(home, ".claude", "skills")
			symlinkPath := filepath.Join(symlinkDir, "basecamp")

			data, err := skills.FS.ReadFile("basecamp/SKILL.md")
			if err != nil {
				return fmt.Errorf("reading embedded skill: %w", err)
			}

			if err := os.MkdirAll(skillDir, 0o755); err != nil { //nolint:gosec // G301: Skill files are not secrets
				return fmt.Errorf("creating skill directory: %w", err)
			}
			if err := os.WriteFile(skillFile, data, 0o644); err != nil { //nolint:gosec // G306: Skill files are not secrets
				return fmt.Errorf("writing skill file: %w", err)
			}

			if err := os.MkdirAll(symlinkDir, 0o755); err != nil { //nolint:gosec // G301: Skill files are not secrets
				return fmt.Errorf("creating symlink directory: %w", err)
			}

			// Remove existing entry at symlink path (idempotent)
			_ = os.Remove(symlinkPath)

			symlinkTarget := filepath.Join("..", "..", ".agents", "skills", "basecamp")
			notice := ""
			if err := os.Symlink(symlinkTarget, symlinkPath); err != nil {
				// Fallback: copy skill files directly
				notice = fmt.Sprintf("symlink failed (%v), copied files instead", err)
				if copyErr := copySkillFiles(skillDir, symlinkPath); copyErr != nil {
					return fmt.Errorf("creating symlink: %w (copy fallback also failed: %w)", err, copyErr)
				}
			}

			result := map[string]any{
				"skill_path":   skillFile,
				"symlink_path": symlinkPath,
			}
			if notice != "" {
				result["notice"] = notice
			}

			summary := "Basecamp skill installed"
			if app != nil {
				return app.OK(result, output.WithSummary(summary))
			}
			// Fallback if app context not available (shouldn't happen in practice)
			fmt.Fprintf(cmd.OutOrStdout(), "Installed skill to %s\n", skillFile)
			return nil
		},
	}
}

func copySkillFiles(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil { //nolint:gosec // G301: Skill files are not secrets
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			return fmt.Errorf("skill directory contains subdirectory %q; copy fallback only supports flat files", e.Name())
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil { //nolint:gosec // G306: Skill files are not secrets
			return err
		}
	}
	return nil
}
