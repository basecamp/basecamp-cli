package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/harness"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/version"
	"github.com/basecamp/basecamp-cli/skills"
)

const skillFilename = "SKILL.md"
const installedVersionFile = ".installed-version"
const primarySkillName = "basecamp"

var embeddedSkillNames = []string{primarySkillName, "basecamp-import"}

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
		Short: "Manage the embedded agent skill files",
		Long:  "Print the main embedded SKILL.md or install the embedded Basecamp skill bundle.",
		RunE: func(cmd *cobra.Command, args []string) error {
			var app *appctx.App
			if ctx := cmd.Context(); ctx != nil {
				app = appctx.FromContext(ctx)
			}

			// Non-interactive: print skill content (piped, --json, --agent, config-driven machine output)
			if app == nil || !app.IsInteractive() || app.IsMachineOutput() {
				if app != nil && app.Flags.JQFilter != "" {
					return output.ErrJQNotSupported("the skill command")
				}
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
		Short: "Install the Basecamp agent skills",
		Long:  "Copies the embedded Basecamp skills to ~/.agents/skills/ and creates Claude Code symlinks when Claude Code is detected.",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			skillPath, err := installSkillFiles()
			if err != nil {
				return err
			}

			result := map[string]any{
				"skill_path":  skillPath,
				"skill_paths": canonicalSkillFiles(),
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

			summary := "Basecamp skills installed"
			if app != nil {
				return app.OK(result, output.WithSummary(summary))
			}
			// Fallback if app context not available (shouldn't happen in practice)
			fmt.Fprintf(cmd.OutOrStdout(), "Installed skills to %s\n", strings.Join(canonicalSkillFiles(), ", "))
			return nil
		},
	}
}

// installSkillFiles writes the embedded skill bundle to ~/.agents/skills/
// and returns the main Basecamp skill path.
func installSkillFiles() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}

	root := filepath.Join(home, ".agents", "skills")
	if _, err := installSkillBundle(root); err != nil {
		return "", err
	}
	return skillFilePath(root, primarySkillName), nil
}

func canonicalSkillFiles() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	root := filepath.Join(home, ".agents", "skills")
	paths := make([]string, 0, len(embeddedSkillNames))
	for _, name := range embeddedSkillNames {
		paths = append(paths, skillFilePath(root, name))
	}
	return paths
}

func installSkillBundle(root string) ([]string, error) {
	paths := make([]string, 0, len(embeddedSkillNames))
	for _, name := range embeddedSkillNames {
		path, err := installEmbeddedSkill(root, name)
		if err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func installEmbeddedSkill(root, name string) (string, error) {
	data, err := embeddedSkillData(name)
	if err != nil {
		return "", err
	}

	skillDir := filepath.Join(root, name)
	skillFile := filepath.Join(skillDir, skillFilename)
	if err := os.MkdirAll(skillDir, 0o755); err != nil { //nolint:gosec // G301: Skill files are not secrets
		return "", fmt.Errorf("creating %s skill directory: %w", name, err)
	}
	if err := os.WriteFile(skillFile, data, 0o644); err != nil { //nolint:gosec // G306: Skill files are not secrets
		return "", fmt.Errorf("writing %s skill file: %w", name, err)
	}

	// Best-effort: stamp installed version.
	_ = os.WriteFile(filepath.Join(skillDir, installedVersionFile), []byte(version.Version), 0o644) //nolint:gosec // G306: not a secret

	return skillFile, nil
}

func embeddedSkillData(name string) ([]byte, error) {
	data, err := skills.FS.ReadFile(name + "/" + skillFilename)
	if err != nil {
		return nil, fmt.Errorf("reading embedded %s skill: %w", name, err)
	}
	return data, nil
}

func skillFilePath(root, name string) string {
	return filepath.Join(root, name, skillFilename)
}

func skillRootForFile(path string) string {
	return filepath.Dir(filepath.Dir(path))
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

	selectedPath, err := tui.Select("  Where would you like to install the Basecamp skills?", options)
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
	data, readErr := embeddedSkillData(primarySkillName)
	if readErr != nil {
		return readErr
	}

	// Write to selected location
	dir := filepath.Dir(expandedPath)
	if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil { //nolint:gosec // G301: Skill files are not secrets
		return fmt.Errorf("creating directory: %w", mkErr)
	}
	if writeErr := os.WriteFile(expandedPath, data, 0o644); writeErr != nil { //nolint:gosec // G306: Skill files are not secrets
		return fmt.Errorf("writing skill file: %w", writeErr)
	}

	result := map[string]any{"skill_path": expandedPath}
	if filepath.Base(filepath.Dir(expandedPath)) == primarySkillName && filepath.Base(expandedPath) == skillFilename {
		root := skillRootForFile(expandedPath)
		if paths, bundleErr := installSkillBundle(root); bundleErr == nil {
			result["skill_paths"] = paths
		} else {
			result["notice"] = fmt.Sprintf("could not write companion skills to %s: %v", root, bundleErr)
		}
	}

	if _, canonicalErr := installSkillFiles(); canonicalErr != nil {
		result["notice"] = fmt.Sprintf("could not write canonical skills: %v", canonicalErr)
	} else {
		result["canonical_skill_paths"] = canonicalSkillFiles()
	}

	return app.OK(result,
		output.WithSummary(fmt.Sprintf("Basecamp skills installed → %s", expandedPath)))
}

// normalizeSkillPath appends basecamp/SKILL.md to directory paths for the main skill.
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

// linkSkillToClaude connects the installed Basecamp skill bundle to Claude Code.
// Returns the main symlink path, an optional notice, and any error.
func linkSkillToClaude() (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("getting home directory: %w", err)
	}

	agentsRoot := filepath.Join(home, ".agents", "skills")
	symlinkDir := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(symlinkDir, 0o755); err != nil { //nolint:gosec // G301: Skill files are not secrets
		return "", "", fmt.Errorf("creating symlink directory: %w", err)
	}

	notices := []string{}
	for _, name := range embeddedSkillNames {
		skillDir := filepath.Join(agentsRoot, name)
		symlinkPath := filepath.Join(symlinkDir, name)

		// Remove existing entry at symlink path.
		_ = os.Remove(symlinkPath)

		symlinkTarget := filepath.Join("..", "..", ".agents", "skills", name)
		if err := os.Symlink(symlinkTarget, symlinkPath); err != nil {
			notices = append(notices, fmt.Sprintf("%s symlink failed (%v), copied files instead", name, err))
			if copyErr := copySkillFiles(skillDir, symlinkPath); copyErr != nil {
				return "", "", fmt.Errorf("creating %s symlink: %w (copy fallback also failed: %w)", name, err, copyErr)
			}
		}
	}

	return filepath.Join(symlinkDir, primarySkillName), strings.Join(notices, "; "), nil
}

// installedSkillVersion reads the .installed-version file from the baseline
// skill directory. Returns "" if absent or unreadable.
func installedSkillVersion() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".agents", "skills", "basecamp", installedVersionFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// RefreshSkillsIfVersionChanged checks the CLI version sentinel and silently
// refreshes installed skills when the version has changed. Returns true if
// skills were refreshed.
func RefreshSkillsIfVersionChanged() bool {
	if version.Version == "dev" {
		return false
	}

	sentinelPath := filepath.Join(config.GlobalConfigDir(), ".last-run-version")

	data, err := os.ReadFile(sentinelPath)
	if err == nil && strings.TrimSpace(string(data)) == version.Version {
		return false
	}

	refreshed := refreshAllInstalledSkills()

	// Repair Claude symlink if broken (e.g. baseline dir was recreated)
	if harness.DetectClaude() {
		repairClaudeSkillLink()
	}

	// Update sentinel only when no refresh was needed or it succeeded.
	// On transient failure, leave the sentinel stale so the next run retries.
	needsRefresh := baselineSkillInstalled()
	if !needsRefresh || refreshed {
		_ = os.MkdirAll(filepath.Dir(sentinelPath), 0o755)             //nolint:gosec // G301: config dir
		_ = os.WriteFile(sentinelPath, []byte(version.Version), 0o644) //nolint:gosec // G306: not a secret
	}

	return refreshed
}

func refreshAllInstalledSkills() bool {
	updated := 0
	failed := 0
	for _, loc := range skillLocations {
		// Skip project-relative paths — no reliable project root in PostRunE.
		if !strings.HasPrefix(loc.Path, "~") && !filepath.IsAbs(loc.Path) {
			continue
		}

		expanded := expandSkillPath(loc.Path)
		if _, statErr := os.Stat(expanded); statErr != nil {
			if !os.IsNotExist(statErr) {
				failed++ // permission or IO error on a known location
			}
			continue
		}

		if _, err := installSkillBundle(skillRootForFile(expanded)); err != nil {
			failed++
			continue
		}
		updated++
	}

	return updated > 0 && failed == 0
}

// repairClaudeSkillLink keeps Claude Code skill symlinks pointed at the installed bundle.
// Directory copies are refreshed in place by the file refresh path.
func repairClaudeSkillLink() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	for _, name := range embeddedSkillNames {
		symlinkPath := filepath.Join(home, ".claude", "skills", name)
		info, err := os.Lstat(symlinkPath)
		if err != nil {
			continue
		}

		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}

		if _, statErr := os.Stat(symlinkPath); statErr == nil {
			continue
		}

		_, _, _ = linkSkillToClaude()
		return
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
