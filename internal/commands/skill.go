package commands

import (
	"errors"
	"fmt"
	"io"
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
const ownershipMarkerFile = ".managed-by-basecamp-cli"

var claudeSkillLinkTarget = filepath.Join("..", "..", ".agents", "skills", "basecamp")

type unmanagedSkillDirError struct{ dir string }

func (e *unmanagedSkillDirError) Error() string {
	return fmt.Sprintf("%s exists but was not written by basecamp-cli; move it aside to let Basecamp install its skill there", e.dir)
}

// skillLocation represents a predefined skill installation target.
type skillLocation struct {
	Name string
	Path string
}

var skillLocations = []skillLocation{
	{Name: "Agents (Shared)", Path: "~/.agents/skills/basecamp/SKILL.md"},
	{Name: "Claude Code (Global)", Path: "~/.claude/skills/basecamp/SKILL.md"},
	{Name: "Claude Code (Project)", Path: ".claude/skills/basecamp/SKILL.md"},
	{Name: "OpenCode (Global)", Path: "~/.config/opencode/skills/basecamp/SKILL.md"},
	{Name: "OpenCode (Project)", Path: ".opencode/skills/basecamp/SKILL.md"},
	{Name: "Codex (Global)", Path: codexGlobalSkillPath()},
}

// legacySkillLocations are paths an agent still reads but that we no longer
// offer as install targets. They are refreshed, never suggested.
//
// opencode accepts both spellings — its own docs table reads
// `~/.config/opencode/skill(s)/<name>/SKILL.md`, the same `agent(s)`/`command(s)`
// optional plural it uses elsewhere. #624 moved the install targets to the
// plural form and dropped the singular entirely, which left anyone who had
// picked OpenCode in the wizard with a file opencode still loads but that
// nothing updates: working, and silently frozen at the version that wrote it.
var legacySkillLocations = []skillLocation{
	{Name: "OpenCode (Global, legacy path)", Path: "~/.config/opencode/skill/basecamp/SKILL.md"},
	{Name: "OpenCode (Project, legacy path)", Path: ".opencode/skill/basecamp/SKILL.md"},
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
		Short: "Install the basecamp agent skill",
		Long:  "Copies the embedded SKILL.md to ~/.agents/skills/basecamp/ and creates a symlink in Claude's configured skills directory (if Claude Code is detected).",
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
	home, err := harness.UserHomeDir()
	if err != nil {
		return "", err
	}

	skillDir := filepath.Join(home, ".agents", "skills", "basecamp")
	skillFile := filepath.Join(skillDir, skillFilename)

	data, err := skills.FS.ReadFile("basecamp/SKILL.md")
	if err != nil {
		return "", fmt.Errorf("reading embedded skill: %w", err)
	}

	created, err := claimSkillDirForWrite(skillDir)
	if err != nil {
		return "", err
	}
	if err := writeSkillFile(skillFile, data); err != nil {
		if created {
			_ = os.Remove(skillDir) // succeeds only while the claimed directory is empty
		}
		return "", fmt.Errorf("writing skill file: %w", err)
	}

	// Best-effort: stamp installed version
	_ = writeSkillFile(filepath.Join(skillDir, installedVersionFile), []byte(version.Version))
	if err := writeSkillFile(filepath.Join(skillDir, ownershipMarkerFile), []byte("This skill is managed by basecamp-cli. Manual edits will be overwritten on upgrade.\n")); err != nil {
		return "", fmt.Errorf("writing skill ownership marker: %w", err)
	}

	return skillFile, nil
}

// claimSkillDir is the ownership gate for the shared skill. A prior Basecamp
// install is recognized by either the current ownership marker or the legacy
// version sentinel. Populated unmarked directories and symlinks are user state.
func claimSkillDir(dir string) error {
	_, err := claimSkillDirForWrite(dir)
	return err
}

// claimSkillDirForWrite also reports whether this call created the leaf
// directory, allowing a failed first write to roll back only its own empty
// claim and leave pre-existing directories untouched.
func claimSkillDirForWrite(dir string) (bool, error) {
	home, err := harness.UserHomeDir()
	if err != nil {
		return false, err
	}
	if skillPathWithin(home, dir) {
		symlinked, inspectErr := hasSymlinkComponent(home, filepath.Dir(dir))
		if inspectErr != nil {
			return false, inspectErr
		}
		if symlinked {
			return false, &unmanagedSkillDirError{dir: dir}
		}
	}
	return claimSkillDirLeafForWrite(dir)
}

func claimSkillDirLeafForWrite(dir string) (bool, error) {
	info, err := os.Lstat(dir)
	switch {
	case os.IsNotExist(err):
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil { //nolint:gosec // G301: Skill files are public documentation
			return false, fmt.Errorf("creating skill directory: %w", mkErr)
		}
		return true, nil
	case err != nil:
		return false, fmt.Errorf("inspecting skill directory: %w", err)
	case info.Mode()&os.ModeSymlink != 0 || !info.IsDir():
		return false, &unmanagedSkillDirError{dir: dir}
	case !ownedSkillDir(dir):
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			return false, fmt.Errorf("inspecting skill directory: %w", readErr)
		}
		legacyManaged := false
		if len(entries) == 1 && entries[0].Name() == skillFilename && entries[0].Type().IsRegular() {
			installed, readFileErr := os.ReadFile(filepath.Join(dir, skillFilename))
			if readFileErr != nil {
				return false, fmt.Errorf("inspecting skill directory: %w", readFileErr)
			}
			legacyManaged = recognizedManagedSkillPayload(installed)
		}
		if !legacyManaged {
			return false, &unmanagedSkillDirError{dir: dir}
		}
	}
	return false, nil
}

// claimPredefinedSkillDir accepts the managed Claude link that points back to
// the canonical Basecamp skill, while preserving claimSkillDir's refusal to
// follow any other symlink.
func claimPredefinedSkillDir(dir string) error {
	_, err := claimPredefinedSkillDirForWrite(dir)
	return err
}

func claimPredefinedSkillDirForWrite(dir string) (bool, error) {
	root, rootErr := predefinedSkillRoot(dir)
	if rootErr != nil {
		return false, rootErr
	}
	symlinked, inspectErr := hasSymlinkComponent(root, filepath.Dir(dir))
	if inspectErr != nil {
		return false, inspectErr
	}
	if symlinked {
		return false, &unmanagedSkillDirError{dir: dir}
	}

	info, err := os.Lstat(dir)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return claimSkillDirLeafForWrite(dir)
	}

	home, homeErr := harness.UserHomeDir()
	if homeErr != nil {
		return false, &unmanagedSkillDirError{dir: dir}
	}
	resolved, resolveErr := filepath.EvalSymlinks(dir)
	canonical := filepath.Join(home, ".agents", "skills", "basecamp")
	resolvedCanonical, canonicalErr := filepath.EvalSymlinks(canonical)
	if resolveErr == nil && canonicalErr == nil && filepath.Clean(resolved) == filepath.Clean(resolvedCanonical) && ownedSkillDir(resolvedCanonical) {
		return false, nil
	}
	return false, &unmanagedSkillDirError{dir: dir}
}

func predefinedSkillRoot(dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getting working directory: %w", err)
		}
		return cwd, nil
	}
	home, err := harness.UserHomeDir()
	if err != nil {
		return "", err
	}
	for _, configured := range []struct {
		env     string
		resolve func() (string, error)
	}{
		{env: "CLAUDE_CONFIG_DIR", resolve: harness.ClaudeConfigDir},
		{env: "CODEX_HOME", resolve: harness.CodexHome},
	} {
		if os.Getenv(configured.env) == "" {
			continue
		}
		root, resolveErr := configured.resolve()
		if resolveErr != nil {
			return "", resolveErr
		}
		if skillPathWithin(root, dir) {
			return root, nil
		}
	}
	if skillPathWithin(home, dir) {
		return home, nil
	}
	// A path outside HOME and the configured agent roots is caller-supplied;
	// only its leaf ownership is ours to validate here.
	return filepath.Dir(dir), nil
}

func skillPathWithin(root, target string) bool {
	root, rootErr := filepath.Abs(filepath.Clean(root))
	target, targetErr := filepath.Abs(filepath.Clean(target))
	if rootErr != nil || targetErr != nil {
		return false
	}
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func writeSkillFile(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return &unmanagedSkillDirError{dir: path}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspecting %s: %w", path, err)
	}
	return os.WriteFile(path, data, 0o644) //nolint:gosec // G306: Skill files are public documentation
}

func writeWizardSkill(path string, data []byte, predefined bool) error {
	if predefined {
		return writeSkillFile(path, data)
	}
	return os.WriteFile(path, data, 0o644) //nolint:gosec // G306: user explicitly selected this custom path
}

// runSkillWizard runs the interactive skill installation wizard.
// skillPromptFailed decides what a failed prompt means. Exactly one outcome is
// a success: the user was asked and said no. Everything else is a failure and
// has to be reported, because "Installation canceled." plus exit 0 claims an
// answer nobody gave and leaves the caller believing it declined an install it
// never saw.
//
// Three cases, and the default is deliberately *not* cancellation:
//
//   - tui.ErrCanceled — the user dismissed the prompt. Report and exit 0.
//   - tui.ErrNotInteractive — nothing could be asked. app.IsInteractive() checks
//     stdin and stdout, but huh draws to stderr, so `basecamp skill 2>somewhere`
//     enters the wizard and only then finds it has nowhere to draw.
//   - anything else — a timeout, or a bubbletea or runtime failure. Propagate
//     it; guessing that it meant "no" is how a real error disappears.
func skillPromptFailed(w io.Writer, styles *tui.Styles, err error) error {
	switch {
	case errors.Is(err, tui.ErrCanceled):
		fmt.Fprintln(w, styles.Muted.Render("  Installation canceled."))
		return nil
	case errors.Is(err, tui.ErrNotInteractive):
		return output.ErrUsageHint(
			"Can't show the installation prompts here",
			"Installing interactively needs a terminal on both stdin and stderr. "+
				"Run basecamp skill install to install without prompts, or basecamp skill to print the file.")
	default:
		return fmt.Errorf("showing the installation prompts: %w", err)
	}
}

func runSkillWizard(cmd *cobra.Command, app *appctx.App) error {
	w := cmd.OutOrStdout()
	styles := tui.NewStylesWithTheme(tui.ResolveTheme(tui.DetectDark()))

	fmt.Fprintln(w)
	fmt.Fprintln(w, styles.Heading.Render("  Basecamp Skill Installation"))
	fmt.Fprintln(w)

	// Build options
	locations, locationErr := wizardSkillLocations()
	if locationErr != nil {
		return fmt.Errorf("resolving skill locations: %w", locationErr)
	}
	options := make([]tui.SelectOption, 0, len(locations)+1)
	for _, loc := range locations {
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
		return skillPromptFailed(w, styles, err)
	}

	selectedPredefined := selectedPath != "other"

	// Handle custom path
	if selectedPath == "other" {
		selectedPath, err = tui.Input("  Enter custom path", "/path/to/skills/basecamp/SKILL.md")
		if err != nil {
			return skillPromptFailed(w, styles, err)
		}
		if selectedPath == "" {
			fmt.Fprintln(w, styles.Muted.Render("  Installation canceled."))
			return nil
		}
		selectedPath = normalizeSkillPath(selectedPath)
	}

	expandedPath := expandSkillPath(selectedPath)

	// Check for existing file
	if _, statErr := os.Stat(expandedPath); statErr == nil {
		overwrite, confirmErr := tui.Confirm(
			fmt.Sprintf("  File already exists at %s. Overwrite?", selectedPath), false)
		if confirmErr != nil {
			return skillPromptFailed(w, styles, confirmErr)
		}
		if !overwrite {
			fmt.Fprintln(w, styles.Muted.Render("  Installation canceled."))
			return nil
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
	created := false
	if selectedPredefined {
		var claimErr error
		created, claimErr = claimPredefinedSkillDirForWrite(dir)
		if claimErr != nil {
			return claimErr
		}
	} else if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil { //nolint:gosec // G301: Skill files are not secrets
		return fmt.Errorf("creating directory: %w", mkErr)
	}
	writeErr := writeWizardSkill(expandedPath, data, selectedPredefined)
	if writeErr != nil {
		if created {
			_ = os.Remove(dir) // succeeds only while the claimed directory is empty
		}
		return fmt.Errorf("writing skill file: %w", writeErr)
	}
	if selectedPredefined {
		if markerErr := writeSkillFile(filepath.Join(dir, ownershipMarkerFile), []byte("This skill is managed by basecamp-cli. Manual edits will be overwritten on upgrade.\n")); markerErr != nil {
			return fmt.Errorf("writing skill ownership marker: %w", markerErr)
		}
		if versionErr := writeSkillFile(filepath.Join(dir, installedVersionFile), []byte(version.Version)); versionErr != nil {
			return fmt.Errorf("writing installed skill version: %w", versionErr)
		}
	}

	// Also write to canonical location
	result := map[string]any{"skill_path": expandedPath}
	home, homeErr := harness.UserHomeDir()
	if homeErr == nil {
		canonicalDir := filepath.Join(home, ".agents", "skills", "basecamp")
		canonicalFile := filepath.Join(canonicalDir, skillFilename)
		if canonicalFile != expandedPath {
			if _, installErr := installSkillFiles(); installErr != nil {
				result["notice"] = fmt.Sprintf("could not write to %s: %v", canonicalFile, installErr)
			}
		} else {
			// The user explicitly confirmed this exact destination above, so it
			// is safe to mark the selected canonical directory as CLI-managed.
			_ = writeSkillFile(filepath.Join(canonicalDir, ownershipMarkerFile), []byte("This skill is managed by basecamp-cli. Manual edits will be overwritten on upgrade.\n"))
			_ = writeSkillFile(filepath.Join(canonicalDir, installedVersionFile), []byte(version.Version))
		}
	}

	return app.OK(result,
		output.WithSummary(fmt.Sprintf("Basecamp skill installed → %s", expandedPath)))
}

func wizardSkillLocations() ([]skillLocation, error) {
	locations := append([]skillLocation(nil), skillLocations...)
	claudeConfig, err := harness.ClaudeConfigDir()
	if err != nil {
		return nil, err
	}
	for i := range locations {
		if locations[i].Name == "Claude Code (Global)" {
			locations[i].Path = filepath.Join(claudeConfig, "skills", "basecamp", skillFilename)
			break
		}
	}
	return locations, nil
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
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		return "~/.codex/skills/basecamp/SKILL.md"
	}
	return filepath.Join(codexHome, "skills", "basecamp", skillFilename)
}

// linkSkillToClaude creates a symlink in Claude's configured skill directory
// pointing to the baseline skill directory. Returns (symlinkPath, notice, error).
func linkSkillToClaude() (string, string, error) {
	home, err := harness.UserHomeDir()
	if err != nil {
		return "", "", err
	}

	skillDir := filepath.Join(home, ".agents", "skills", "basecamp")
	claudeConfig, err := harness.ClaudeConfigDir()
	if err != nil {
		return "", "", err
	}
	symlinkDir := filepath.Join(claudeConfig, "skills")
	symlinkPath := filepath.Join(symlinkDir, "basecamp")

	skillInfo, skillErr := os.Lstat(skillDir)
	if skillErr != nil || skillInfo.Mode()&os.ModeSymlink != 0 || !skillInfo.IsDir() ||
		!ownedSkillDir(skillDir) || !regularFile(filepath.Join(skillDir, skillFilename)) {
		return "", "", &unmanagedSkillDirError{dir: skillDir}
	}
	claudeRoot := claudeConfig
	if os.Getenv("CLAUDE_CONFIG_DIR") == "" {
		claudeRoot = home
	}
	symlinked, inspectErr := hasSymlinkComponent(claudeRoot, symlinkDir)
	if inspectErr != nil {
		return "", "", inspectErr
	}
	if symlinked {
		return "", "", &unmanagedSkillDirError{dir: symlinkDir}
	}

	if err := os.MkdirAll(symlinkDir, 0o755); err != nil { //nolint:gosec // G301: Skill files are not secrets
		return "", "", fmt.Errorf("creating symlink directory: %w", err)
	}
	symlinkTarget := skillDir
	resolvedSymlinkDir, dirErr := filepath.EvalSymlinks(symlinkDir)
	resolvedSkillDir, skillErr := filepath.EvalSymlinks(skillDir)
	if dirErr == nil && skillErr == nil {
		if relativeTarget, relErr := filepath.Rel(resolvedSymlinkDir, resolvedSkillDir); relErr == nil {
			symlinkTarget = relativeTarget
		}
	}

	if err := removeExistingClaudeSkillLink(symlinkPath, symlinkTarget, skillDir); err != nil {
		return "", "", err
	}

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

func removeExistingClaudeSkillLink(path, expectedTarget, expectedDestination string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspecting existing skill link: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, readErr := os.Readlink(path)
		if readErr != nil || (target != expectedTarget && !pathsEquivalent(path, expectedDestination)) {
			return &unmanagedSkillDirError{dir: path}
		}
		if removeErr := os.Remove(path); removeErr != nil {
			return fmt.Errorf("removing existing skill link: %w", removeErr)
		}
		return nil
	}
	if !info.IsDir() || !ownedSkillDir(path) {
		return &unmanagedSkillDirError{dir: path}
	}
	// A directory is the copy fallback from an earlier install. Leave its
	// managed files in place until copySkillFiles has a replacement ready;
	// this also preserves any additional user files in the directory.
	return nil
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

	outcome := refreshInstalledSkills()

	// Repair Claude symlink if broken (e.g. baseline dir was recreated)
	if harness.DetectClaude() {
		if repairErr := repairClaudeSkillLink(); repairErr != nil {
			outcome.failed++
		}
	}

	// No work and a successful refresh both advance the sentinel. Any failure at
	// any managed location leaves it stale so the next run retries.
	if outcome.failed == 0 {
		// 0o700: GlobalConfigDir can hold credentials.json; keep it owner-only.
		_ = os.MkdirAll(filepath.Dir(sentinelPath), 0o700)
		_ = os.WriteFile(sentinelPath, []byte(version.Version), 0o644) //nolint:gosec // G306: not a secret
	}

	return outcome.updated > 0 && outcome.failed == 0
}

func refreshAllInstalledSkills() bool {
	outcome := refreshInstalledSkills()
	return outcome.updated > 0 && outcome.failed == 0
}

type skillRefreshOutcome struct {
	updated int
	failed  int
}

func refreshInstalledSkills() skillRefreshOutcome {
	embedded, err := skills.FS.ReadFile("basecamp/SKILL.md")
	if err != nil {
		return skillRefreshOutcome{failed: 1}
	}

	outcome := skillRefreshOutcome{}
	locations := append(append([]skillLocation{}, skillLocations...), legacySkillLocations...)
	claudeConfig, claudeConfigErr := harness.ClaudeConfigDir()
	if claudeConfigErr != nil {
		outcome.failed++
	} else {
		configured := filepath.Join(claudeConfig, "skills", "basecamp", skillFilename)
		locations = append(locations, skillLocation{Name: "Claude Code (configured)", Path: configured})
	}
	for _, loc := range locations {
		// Skip project-relative paths — no reliable project root in PostRunE.
		if !strings.HasPrefix(loc.Path, "~") && !filepath.IsAbs(loc.Path) {
			continue
		}

		expanded := expandSkillPath(loc.Path)
		dir := filepath.Dir(expanded)
		root, rootErr := refreshLocationRoot(loc, expanded, claudeConfig)
		if rootErr != nil {
			outcome.failed++
			continue
		}
		symlinked, inspectErr := hasSymlinkComponent(root, dir)
		if inspectErr != nil {
			outcome.failed++
			continue
		}
		if symlinked {
			// Claude's canonical installation is intentionally a symlink to the
			// shared managed baseline. Leave it for repairClaudeSkillLink below;
			// every other predefined symlink remains untrusted.
			if strings.HasPrefix(loc.Name, "Claude Code") && managedClaudeSkillLink(dir) {
				continue
			}
			if ownedOrLegacySkillDir(dir) || invalidSkillMarker(dir) {
				outcome.failed++
			}
			continue
		}
		dirInfo, dirErr := os.Lstat(dir)
		if os.IsNotExist(dirErr) {
			continue
		}
		if dirErr != nil {
			outcome.failed++
			continue
		}
		// Never follow a parent symlink while refreshing a predefined location.
		if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
			continue
		}
		fileInfo, statErr := os.Lstat(expanded)
		if statErr != nil {
			if os.IsNotExist(statErr) && ownedSkillDir(dir) {
				if writeErr := writeSkillFile(expanded, embedded); writeErr == nil {
					outcome.updated++
				} else {
					outcome.failed++
				}
			} else if os.IsNotExist(statErr) && invalidSkillMarker(dir) {
				outcome.failed++
			} else if !os.IsNotExist(statErr) {
				outcome.failed++ // permission or IO error on a known location
			}
			continue
		}
		if !fileInfo.Mode().IsRegular() {
			if ownedSkillDir(dir) {
				outcome.failed++
			}
			continue
		}
		if !ownedSkillDir(dir) {
			if invalidSkillMarker(dir) {
				outcome.failed++
				continue
			}
			entries, readDirErr := os.ReadDir(dir)
			if readDirErr != nil {
				outcome.failed++
				continue
			}
			if len(entries) != 1 || entries[0].Name() != skillFilename || !entries[0].Type().IsRegular() {
				continue
			}
			installed, readErr := os.ReadFile(expanded) //nolint:gosec // predefined agent skill path
			if readErr != nil {
				outcome.failed++
				continue
			}
			if !recognizedManagedSkillPayload(installed) {
				continue
			}
			if markerErr := writeSkillFile(filepath.Join(dir, ownershipMarkerFile), []byte("This skill is managed by basecamp-cli. Manual edits will be overwritten on upgrade.\n")); markerErr != nil {
				outcome.failed++
				continue
			}
		}

		if writeErr := writeSkillFile(expanded, embedded); writeErr == nil {
			outcome.updated++
		} else {
			outcome.failed++
		}
	}

	// Stamp installed version in the baseline directory only on full success.
	if outcome.failed == 0 && outcome.updated > 0 {
		if home, err := harness.UserHomeDir(); err == nil {
			baselineDir := filepath.Join(home, ".agents", "skills", "basecamp")
			if ownedSkillDir(baselineDir) {
				if stampErr := writeSkillFile(filepath.Join(baselineDir, installedVersionFile), []byte(version.Version)); stampErr != nil {
					outcome.failed++
				}
			}
		}
	}

	return outcome
}

func refreshLocationRoot(loc skillLocation, expanded, claudeConfig string) (string, error) {
	if loc.Name == "Claude Code (configured)" {
		if os.Getenv("CLAUDE_CONFIG_DIR") == "" {
			return harness.UserHomeDir()
		}
		return claudeConfig, nil
	}
	if loc.Name == "Codex (Global)" {
		if os.Getenv("CODEX_HOME") != "" {
			return harness.CodexHome()
		}
		return harness.UserHomeDir()
	}
	if strings.HasPrefix(loc.Path, "~") {
		return harness.UserHomeDir()
	}
	return filepath.VolumeName(expanded) + string(filepath.Separator), nil
}

func invalidSkillMarker(dir string) bool {
	for _, marker := range []string{ownershipMarkerFile, installedVersionFile} {
		markerInfo, markerErr := os.Lstat(filepath.Join(dir, marker))
		if markerErr == nil && !markerInfo.Mode().IsRegular() {
			return true
		}
		if markerErr != nil && !os.IsNotExist(markerErr) {
			return true
		}
	}
	return false
}

func managedClaudeSkillLink(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	target, err := os.Readlink(path)
	if err != nil {
		return false
	}
	home, err := harness.UserHomeDir()
	if err != nil {
		return false
	}
	baseline := filepath.Join(home, ".agents", "skills", "basecamp")
	if !ownedOrLegacySkillDir(baseline) {
		return false
	}
	return pathsEquivalent(path, baseline) || brokenLinkTargetsPath(path, target, baseline)
}

// repairClaudeSkillLink repairs a broken basecamp-cli symlink in Claude's
// configured skill directory. If the path is a directory (copy fallback), the
// file refresh already handled it. Unmanaged symlinks are preserved.
func repairClaudeSkillLink() error {
	claudeConfig, err := harness.ClaudeConfigDir()
	if err != nil {
		return err
	}

	symlinkPath := filepath.Join(claudeConfig, "skills", "basecamp")
	info, err := os.Lstat(symlinkPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // doesn't exist, nothing to repair
		}
		return fmt.Errorf("inspecting Claude skill link: %w", err)
	}

	if info.Mode()&os.ModeSymlink == 0 {
		return nil // not a symlink (directory copy fallback), file refresh handled it
	}

	// It's a symlink — check if the target is reachable. Only a missing target
	// proves the link is broken; permission and I/O errors must not trigger a
	// destructive repair attempt.
	if _, statErr := os.Stat(symlinkPath); statErr == nil {
		return nil // symlink is healthy
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("checking Claude skill link target: %w", statErr)
	}

	target, err := os.Readlink(symlinkPath)
	if err != nil {
		return fmt.Errorf("reading Claude skill link: %w", err)
	}
	home, err := harness.UserHomeDir()
	if err != nil {
		return err
	}
	baseline := filepath.Join(home, ".agents", "skills", "basecamp")
	if !brokenLinkTargetsPath(symlinkPath, target, baseline) {
		return nil
	}

	_, _, err = linkSkillToClaude()
	return err
}

func copySkillFiles(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil { //nolint:gosec // G301: Skill files are not secrets
		return err
	}
	for _, name := range []string{skillFilename, installedVersionFile, ownershipMarkerFile} {
		data, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			if os.IsNotExist(err) && name != skillFilename {
				continue
			}
			return fmt.Errorf("reading managed skill file %s: %w", name, err)
		}
		if err := writeSkillFile(filepath.Join(dst, name), data); err != nil {
			return fmt.Errorf("writing managed skill file %s: %w", name, err)
		}
	}
	return nil
}
