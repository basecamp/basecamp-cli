package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/harness"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/skills"
)

const skillFilename = "SKILL.md"

// skillLocation represents a predefined skill installation target.
type skillLocation struct {
	Name string
	Path string
}

var skillLocations = []skillLocation{
	{Name: "Agents (Shared)", Path: "~/.agents/skills/basecamp/SKILL.md"},
	{Name: "Claude Code (Global)", Path: "~/.claude/skills/basecamp/SKILL.md"},
	{Name: "Claude Code (Project)", Path: ".claude/skills/basecamp/SKILL.md"},
	{Name: "OpenCode (Global)", Path: "~/.config/opencode/skill/basecamp/SKILL.md"},
	{Name: "OpenCode (Project)", Path: ".opencode/skill/basecamp/SKILL.md"},
	{Name: "Codex (Global)", Path: codexGlobalSkillPath()},
}

// NewSkillCmd creates the skill command.
func NewSkillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Manage the embedded agent skill file",
		Long:  "Print or install the SKILL.md embedded in this binary.",
		RunE: func(cmd *cobra.Command, args []string) error {
			var app *appctx.App
			if ctx := cmd.Context(); ctx != nil {
				app = appctx.FromContext(ctx)
			}

			// Non-interactive: print skill content (piped, --json, --agent, config-driven machine output)
			if app == nil || !app.IsInteractive() || app.IsMachineOutput() {
				data, err := skills.FS.ReadFile("basecamp/SKILL.md")
				if err != nil {
					return fmt.Errorf("reading embedded skill: %w", err)
				}
				_, err = fmt.Fprint(cmd.OutOrStdout(), string(data))
				return err
			}

			// Interactive: show agent picker wizard
			return runSkillWizard(cmd, app)
		},
	}
	cmd.AddCommand(newSkillInstallCmd())
	return cmd
}

func newSkillInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install the basecamp agent skill",
		Long:  "Copies the embedded SKILL.md to ~/.agents/skills/basecamp/ and creates a symlink in ~/.claude/skills/basecamp (if Claude Code is detected).",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			skillPath, err := installSkillFiles()
			if err != nil {
				return err
			}

			result := map[string]any{
				"skill_path": skillPath,
			}

			// Only create the Claude symlink if Claude is actually installed
			if harness.DetectClaude() {
				symlinkPath, notice, linkErr := linkSkillToClaude()
				if linkErr != nil {
					return linkErr
				}
				result["symlink_path"] = symlinkPath
				if notice != "" {
					result["notice"] = notice
				}
			}

			summary := "Basecamp skill installed"
			if app != nil {
				return app.OK(result, output.WithSummary(summary))
			}
			// Fallback if app context not available (shouldn't happen in practice)
			fmt.Fprintf(cmd.OutOrStdout(), "Installed skill to %s\n", skillPath)
			return nil
		},
	}
}

// installSkillFiles writes the embedded SKILL.md to ~/.agents/skills/basecamp/
// and returns the path to the installed file.
func installSkillFiles() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}

	skillDir := filepath.Join(home, ".agents", "skills", "basecamp")
	skillFile := filepath.Join(skillDir, skillFilename)

	data, err := skills.FS.ReadFile("basecamp/SKILL.md")
	if err != nil {
		return "", fmt.Errorf("reading embedded skill: %w", err)
	}

	if err := os.MkdirAll(skillDir, 0o755); err != nil { //nolint:gosec // G301: Skill files are not secrets
		return "", fmt.Errorf("creating skill directory: %w", err)
	}
	if err := os.WriteFile(skillFile, data, 0o644); err != nil { //nolint:gosec // G306: Skill files are not secrets
		return "", fmt.Errorf("writing skill file: %w", err)
	}

	return skillFile, nil
}

// runSkillWizard runs the interactive skill installation wizard.
func runSkillWizard(cmd *cobra.Command, app *appctx.App) error {
	w := cmd.OutOrStdout()
	styles := tui.NewStylesWithTheme(tui.ResolveTheme(tui.DetectDark()))

	fmt.Fprintln(w)
	fmt.Fprintln(w, styles.Heading.Render("  Basecamp Skill Installation"))
	fmt.Fprintln(w)

	// Build options
	options := make([]tui.SelectOption, 0, len(skillLocations)+1)
	for _, loc := range skillLocations {
		options = append(options, tui.SelectOption{
			Value: loc.Path,
			Label: fmt.Sprintf("%s (%s)", loc.Name, loc.Path),
		})
	}
	options = append(options, tui.SelectOption{
		Value: "other",
		Label: "Other (custom path)",
	})

	selectedPath, err := tui.Select("  Where would you like to install the Basecamp skill?", options)
	if err != nil {
		fmt.Fprintln(w, styles.Muted.Render("  Installation canceled."))
		return nil //nolint:nilerr // user canceled prompt
	}

	// Handle custom path
	if selectedPath == "other" {
		selectedPath, err = tui.Input("  Enter custom path", "/path/to/skills/basecamp/SKILL.md")
		if err != nil || selectedPath == "" {
			fmt.Fprintln(w, styles.Muted.Render("  Installation canceled."))
			return nil //nolint:nilerr // user canceled prompt
		}
		selectedPath = normalizeSkillPath(selectedPath)
	}

	expandedPath := expandSkillPath(selectedPath)

	// Check for existing file
	if _, statErr := os.Stat(expandedPath); statErr == nil {
		overwrite, confirmErr := tui.Confirm(
			fmt.Sprintf("  File already exists at %s. Overwrite?", selectedPath), false)
		if confirmErr != nil || !overwrite {
			fmt.Fprintln(w, styles.Muted.Render("  Installation canceled."))
			return nil //nolint:nilerr // user canceled or declined
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("checking existing file: %w", statErr)
	}

	// Read embedded skill
	data, readErr := skills.FS.ReadFile("basecamp/SKILL.md")
	if readErr != nil {
		return fmt.Errorf("reading embedded skill: %w", readErr)
	}

	// Write to selected location
	dir := filepath.Dir(expandedPath)
	if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil { //nolint:gosec // G301: Skill files are not secrets
		return fmt.Errorf("creating directory: %w", mkErr)
	}
	if writeErr := os.WriteFile(expandedPath, data, 0o644); writeErr != nil { //nolint:gosec // G306: Skill files are not secrets
		return fmt.Errorf("writing skill file: %w", writeErr)
	}

	// Also write to canonical location
	result := map[string]any{"skill_path": expandedPath}
	home, homeErr := os.UserHomeDir()
	if homeErr == nil {
		canonicalDir := filepath.Join(home, ".agents", "skills", "basecamp")
		canonicalFile := filepath.Join(canonicalDir, skillFilename)
		if canonicalFile != expandedPath {
			if mkErr := os.MkdirAll(canonicalDir, 0o755); mkErr != nil { //nolint:gosec // G301: Skill files are not secrets
				result["notice"] = fmt.Sprintf("could not write to %s: %v", canonicalFile, mkErr)
			} else if wErr := os.WriteFile(canonicalFile, data, 0o644); wErr != nil { //nolint:gosec // G306: Skill files are not secrets
				result["notice"] = fmt.Sprintf("could not write to %s: %v", canonicalFile, wErr)
			}
		}
	}

	return app.OK(result,
		output.WithSummary(fmt.Sprintf("Basecamp skill installed → %s", expandedPath)))
}

// normalizeSkillPath appends basecamp/SKILL.md to directory paths.
// Explicit file paths (any .md) are left as-is.
func normalizeSkillPath(path string) string {
	path = strings.TrimSpace(path)

	// Already points to a file — respect the user's choice
	if strings.HasSuffix(strings.ToLower(path), ".md") {
		return path
	}

	// Directory ending in "basecamp" — just append SKILL.md
	if strings.HasSuffix(path, "basecamp") || strings.HasSuffix(path, "basecamp/") ||
		strings.HasSuffix(path, "basecamp\\") {
		return filepath.Join(path, skillFilename)
	}

	// Bare directory — append basecamp/SKILL.md
	return filepath.Join(path, "basecamp", skillFilename)
}

// expandSkillPath expands ~ to the home directory.
func expandSkillPath(path string) string {
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return home
	}
	return path
}

func codexGlobalSkillPath() string {
	codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if codexHome == "" {
		return "~/.codex/skills/basecamp/SKILL.md"
	}
	return filepath.Join(codexHome, "skills", "basecamp", skillFilename)
}

// linkSkillToClaude creates a symlink at ~/.claude/skills/basecamp pointing to
// the baseline skill directory. Returns (symlinkPath, notice, error).
func linkSkillToClaude() (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("getting home directory: %w", err)
	}

	skillDir := filepath.Join(home, ".agents", "skills", "basecamp")
	symlinkDir := filepath.Join(home, ".claude", "skills")
	symlinkPath := filepath.Join(symlinkDir, "basecamp")

	if err := os.MkdirAll(symlinkDir, 0o755); err != nil { //nolint:gosec // G301: Skill files are not secrets
		return "", "", fmt.Errorf("creating symlink directory: %w", err)
	}

	// Remove existing entry at symlink path (idempotent)
	_ = os.Remove(symlinkPath)

	symlinkTarget := filepath.Join("..", "..", ".agents", "skills", "basecamp")
	notice := ""
	if err := os.Symlink(symlinkTarget, symlinkPath); err != nil {
		// Fallback: copy skill files directly
		notice = fmt.Sprintf("symlink failed (%v), copied files instead", err)
		if copyErr := copySkillFiles(skillDir, symlinkPath); copyErr != nil {
			return "", "", fmt.Errorf("creating symlink: %w (copy fallback also failed: %w)", err, copyErr)
		}
	}

	return symlinkPath, notice, nil
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
