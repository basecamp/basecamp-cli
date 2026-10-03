package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/harness"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/skills"
)

const agentRemoveTimeout = 20 * time.Second

var legacyManagedSkillHashes = map[string]struct{}{
	// Every unique skills/basecamp/SKILL.md payload shipped from v0.1.0 through
	// v0.11.0, plus 17a00ac immediately before this command. Exact hashes let us
	// recognize pre-marker wizard installs without claiming user-authored files.
	// Any release that ships before the ownership marker does must be added here.
	"9ba73c37394e2f3fd41b1fb88dfcb5765c8d28f817a4d8c186cb3bc6eb9b7c0b": {},
	"5b8dbaee9258079695e078c2fffea835d53ac408aa1265fd5116fd4a8657aaed": {},
	"7f388068176a382e1b452452e88ebb9a4712265ca777c81f47350df0845c8839": {},
	"866ffee85417ea2d204efc5b49e4d6bf2c7f74fd3d3e0d4773fe2fbb70640e36": {},
	"ca0db118c4c69211dd9c0169cce36850c8cca1331e83544e0e4638435a157f43": {},
	"2c295c087b0110ca67c3c12ae8a4deda0d2f04390cefeef6719d924526f0e72a": {},
	"c5dfa70f7a7d9ce5ff4d6c780949bb821447c87db345a22e8f533004a2bd0b3f": {},
	"c8465eadd9f1c7cfae8235812d85b424f15f0a65b9fa3878be046deb58b9efcf": {},
	"21dbba9a6419d3bbf215976e591cafb9fdb3f2a4ca7b20a9953efec7f9691e96": {},
	"5bdfcb49c9808011087790c006f8665cc5d8a079961a1dcef760547d8dd9280c": {},
	"a5e60a1c55ec381dab3265625d97461b7c32edd49837a03642abba347852421d": {},
	"e1394abe6ff5affa3d94e8ea9b6460ebfd7ac06374070d7d5e10731004178bb3": {},
	"dad3d2ed690e52fd22c28941665433814776fdb21a3adc5d3cd1b802d3ee9da7": {},
}

var runAgentRemoveCommand = func(ctx context.Context, path, dir string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, path, args...) //nolint:gosec // path comes from exec.LookPath
	command.Dir = dir
	if configDir := claudeConfigDirFromContext(ctx); configDir != "" {
		command.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+configDir)
	}
	startRemoveProcessGroup(command)
	command.WaitDelay = time.Second
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		return nil, err
	}
	read := make(chan commandRead, 1)
	go func() {
		data, readErr := io.ReadAll(stdout)
		read <- commandRead{data: data, err: readErr}
	}()
	var output commandRead
	select {
	case output = <-read:
	case <-ctx.Done():
		_ = killRemoveProcessGroup(command)
		select {
		case output = <-read:
		case <-time.After(time.Second):
			_ = stdout.Close()
			output = <-read
		}
	}
	waitErr := command.Wait()
	if ctx.Err() != nil {
		return output.data, ctx.Err()
	}
	if waitErr != nil {
		return output.data, waitErr
	}
	return output.data, output.err
}

type commandRead struct {
	data []byte
	err  error
}

type claudeConfigContextKey struct{}

func withClaudeConfigDir(ctx context.Context, configDir string) context.Context {
	return context.WithValue(ctx, claudeConfigContextKey{}, configDir)
}

func claudeConfigDirFromContext(ctx context.Context) string {
	value, _ := ctx.Value(claudeConfigContextKey{}).(string)
	return value
}

type setupRemoveError struct {
	err      *output.Error
	removed  []string
	failures []string
}

var _ output.ErrorMetaProvider = (*setupRemoveError)(nil)

func (e *setupRemoveError) Error() string { return e.err.Error() }

func (e *setupRemoveError) Unwrap() error { return e.err }

func (e *setupRemoveError) ErrorMetadata() map[string]any {
	return map[string]any{
		"removed":  e.removed,
		"failures": e.failures,
	}
}

type claudePluginScope struct {
	Name        string
	ProjectPath string
}

type claudePluginInstallation struct {
	Key         string
	Scopes      []claudePluginScope
	ScopesKnown bool
}

var codexPluginInstalled = harness.CodexPluginInstalledContext

// runRemoveAgentSetup removes only coding-agent integrations managed by the
// Basecamp CLI. Authentication, Basecamp configuration, and project data are
// intentionally outside this command's scope.
func runRemoveAgentSetup(cmd *cobra.Command, app *appctx.App) error {
	home, err := harness.UserHomeDir()
	homeAvailable := err == nil && home != ""
	if !homeAvailable {
		home = ""
	}

	removed := make([]string, 0)
	failures := make([]string, 0)
	if !homeAvailable {
		failures = append(failures, fmt.Sprintf("home-based skill cleanup: %v", err))
	}

	baseline := ""
	if homeAvailable {
		baseline = filepath.Join(home, ".agents", "skills", "basecamp")
	}
	claudeConfig, claudeConfigErr := harness.ClaudeConfigDir()
	// A managed Claude link can only be recognized while the baseline it points
	// at is still present, so the baseline cleanup is deferred until every link
	// slot has been inspected. Any failure there keeps the baseline for a retry.
	claudeLinksHandled := claudeConfigErr == nil
	if claudeConfigErr != nil {
		failures = append(failures, "Claude Code configuration: "+claudeConfigErr.Error())
	} else {
		claudeRemoved, claudeFailures := removeClaudePlugin(cmd.Context(), claudeConfig)
		if claudeRemoved {
			removed = append(removed, "Claude Code plugin")
		}
		failures = append(failures, claudeFailures...)
		if homeAvailable {
			defaultClaudeConfig := filepath.Join(home, ".claude")
			if !pathEntriesEquivalent(defaultClaudeConfig, claudeConfig) {
				legacyRemoved, legacyFailures := removeClaudePlugin(cmd.Context(), defaultClaudeConfig)
				if legacyRemoved {
					removed = append(removed, "legacy Claude Code plugin")
				}
				for _, failure := range legacyFailures {
					failures = append(failures, "legacy "+failure)
				}
			}
		}

		claudeSkill := filepath.Join(claudeConfig, "skills", "basecamp")
		// A custom Claude config may place its skill at the shared baseline.
		// Keep that baseline intact until every other managed link has been
		// inspected, then remove it in the final baseline cleanup below.
		if baseline == "" || !pathEntriesEquivalent(claudeSkill, baseline) {
			if didRemove, removeErr := removeClaudeSkill(claudeSkill, baseline); removeErr != nil {
				claudeLinksHandled = false
				failures = append(failures, "Claude Code skill: "+removeErr.Error())
			} else if didRemove {
				removed = append(removed, "Claude Code skill")
			}
		}
	}

	// The default path may be a managed link left by an older install. Clean it
	// independently so a malformed custom CLAUDE_CONFIG_DIR cannot leave that
	// link broken when the shared baseline is removed below.
	legacyClaudeSkill := ""
	if homeAvailable {
		legacyClaudeSkill = filepath.Join(home, ".claude", "skills", "basecamp")
	}
	configuredClaudeSkill := ""
	if claudeConfigErr == nil {
		configuredClaudeSkill = filepath.Join(claudeConfig, "skills", "basecamp")
	}
	if legacyClaudeSkill != "" && (configuredClaudeSkill == "" || !pathEntriesEquivalent(legacyClaudeSkill, configuredClaudeSkill)) {
		if didRemove, removeErr := removeClaudeSkill(legacyClaudeSkill, baseline); removeErr != nil {
			claudeLinksHandled = false
			failures = append(failures, "legacy Claude Code skill: "+removeErr.Error())
		} else if didRemove {
			removed = append(removed, "legacy Claude Code skill")
		}
	}
	projectClaudeSkill := filepath.Join(".claude", "skills", "basecamp")
	// The shared baseline is removed only after every Claude link slot has been
	// handled, so a failed cleanup can still be retried safely.
	if !pathEntriesEquivalent(projectClaudeSkill, configuredClaudeSkill) &&
		!pathEntriesEquivalent(projectClaudeSkill, legacyClaudeSkill) &&
		!pathEntriesEquivalent(projectClaudeSkill, baseline) {
		if didRemove, removeErr := removeOwnedOrLegacySkill(projectClaudeSkill); removeErr != nil {
			failures = append(failures, "project Claude Code skill: "+removeErr.Error())
		} else if didRemove {
			removed = append(removed, "project Claude Code skill")
		}
	}

	codexRemoved, codexFailure := removeCodexPlugin(cmd.Context())
	if codexRemoved {
		removed = append(removed, "Codex plugin")
	}
	if codexFailure != "" {
		failures = append(failures, codexFailure)
	}

	resolvedCodexHome, codexHomeErr := harness.CodexHome()
	if codexHomeErr != nil {
		failures = append(failures, "Codex configuration: "+codexHomeErr.Error())
		if homeAvailable {
			resolvedCodexHome = filepath.Join(home, ".codex")
		}
	}
	codexSkill := ""
	if resolvedCodexHome != "" {
		codexSkill = filepath.Join(resolvedCodexHome, "skills", "basecamp")
	}
	if codexSkill != "" && !pathEntriesEquivalent(codexSkill, baseline) {
		// The old wizard wrote its Codex payload straight here, so this may be a
		// pre-marker install recognized by its exact shipped SKILL.md.
		if didRemove, removeErr := removeOwnedOrLegacySkill(codexSkill); removeErr != nil {
			failures = append(failures, "Codex skill: "+removeErr.Error())
		} else if didRemove {
			removed = append(removed, "Codex skill")
		}
	}
	legacyCodexSkill := ""
	if homeAvailable {
		legacyCodexSkill = filepath.Join(home, ".codex", "skills", "basecamp")
	}
	if legacyCodexSkill != "" && !pathEntriesEquivalent(legacyCodexSkill, codexSkill) &&
		!pathEntriesEquivalent(legacyCodexSkill, baseline) {
		if didRemove, removeErr := removeOwnedOrLegacySkill(legacyCodexSkill); removeErr != nil {
			failures = append(failures, "legacy Codex skill: "+removeErr.Error())
		} else if didRemove {
			removed = append(removed, "legacy Codex skill")
		}
	}

	removeOpenCodeSkills(home, baseline, &removed, &failures)
	removeSkillAgentSkills(home, baseline, &removed, &failures)

	if baseline != "" && claudeLinksHandled {
		if didRemove, removeErr := removeOwnedOrLegacySkill(baseline); removeErr != nil {
			failures = append(failures, "agent skill: "+removeErr.Error())
		} else if didRemove {
			removed = append(removed, "agent skill")
		}
	}

	result := map[string]any{
		"removed":  removed,
		"failures": failures,
	}
	if len(failures) > 0 {
		return &setupRemoveError{
			err: &output.Error{
				Code:    "setup_remove_failed",
				Message: "coding-agent integration removal incomplete: " + strings.Join(failures, "; "),
				Hint:    "Resolve the reported item, then run: basecamp setup agents --remove",
			},
			removed:  removed,
			failures: failures,
		}
	}

	return app.OK(result, output.WithSummary("Coding-agent integrations removed"))
}

// Shared-skill agents can also have an explicitly installed home-local copy.
func removeSkillAgentSkills(home, baseline string, removed, failures *[]string) {
	seen := make(map[string]bool)
	for _, agent := range harness.SkillAgents() {
		homes := []string{agent.Home()}
		if home != "" {
			homes = append(homes, filepath.Join(home, agent.HomeDir))
		}
		for _, agentHome := range homes {
			if agentHome == "" {
				continue
			}
			dir := filepath.Join(agentHome, "skills", "basecamp")
			if pathEntriesEquivalent(dir, baseline) {
				continue
			}
			if seen[dir] {
				continue
			}
			seen[dir] = true
			if didRemove, err := removeOwnedOrLegacySkill(dir); err != nil {
				*failures = append(*failures, agent.Name+" skill: "+err.Error())
			} else if didRemove {
				*removed = append(*removed, agent.Name+" skill")
			}
		}
	}
}

func removeOpenCodeSkills(home, baseline string, removed, failures *[]string) {
	locations := append(append([]skillLocation{}, skillLocations...), legacySkillLocations...)
	seen := make(map[string]struct{})
	_, projectRootErr := os.Getwd()
	for _, location := range locations {
		if !strings.HasPrefix(location.Name, "OpenCode") {
			continue
		}
		path := location.Path
		if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
			if home == "" {
				continue
			}
			path = filepath.Join(home, path[2:])
		} else if projectRootErr != nil {
			*failures = append(*failures, location.Name+" skill: getting working directory: "+projectRootErr.Error())
			continue
		}
		dir := filepath.Clean(filepath.Dir(path))
		if absolute, err := filepath.Abs(dir); err == nil {
			dir = absolute
		}
		if pathEntriesEquivalent(dir, baseline) {
			continue
		}
		// Deduplicate configured path entries, not their resolved destinations.
		// An unmanaged symlink at one location may point at a managed directory
		// that is also listed directly and still needs to be removed.
		if _, duplicate := seen[dir]; duplicate {
			continue
		}
		seen[dir] = struct{}{}
		didRemove, err := removeOwnedOrLegacySkill(dir)
		if err != nil {
			*failures = append(*failures, location.Name+" skill: "+err.Error())
		} else if didRemove {
			*removed = append(*removed, location.Name+" skill")
		}
	}
}

func removeClaudePlugin(parent context.Context, configDir string) (bool, []string) {
	parent = withClaudeConfigDir(parent, configDir)
	installations, err := readClaudePluginInstallations(configDir)
	if err != nil {
		return false, []string{"Claude Code plugin: " + err.Error()}
	}
	if len(installations) == 0 {
		return false, nil
	}
	claudePath := harness.FindClaudeBinary()
	if claudePath == "" {
		return false, []string{"Claude Code plugin: claude binary not found"}
	}

	removed := false
	var failures []string
	for _, installation := range installations {
		valid, needsFallback := validClaudeScopes(installation)
		var scopedFailures []string
		for _, scope := range valid {
			if scope.Name != "user" && !validProjectPath(scope.ProjectPath) {
				scopedFailures = append(scopedFailures, fmt.Sprintf("Claude Code plugin %s (%s): recorded project path is missing or invalid", installation.Key, scope.Name))
				continue
			}
			workingDir := scope.ProjectPath
			if scope.Name == "user" {
				workingDir = ""
			}
			commandOutput, commandErr := runRemoveStep(parent, claudePath, workingDir, "plugin", "uninstall", installation.Key, "--scope", scope.Name)
			if commandErr != nil {
				if !claudePluginAbsent(commandOutput, installation.Key) {
					scopedFailures = append(scopedFailures, fmt.Sprintf("Claude Code plugin %s (%s): %s", installation.Key, scope.Name, commandOutputFailure(commandOutput, commandErr)))
				}
			} else {
				removed = true
			}
		}

		if needsFallback {
			fallbackRemoved, fallbackErr := uninstallClaudeUnscoped(parent, claudePath, installation.Key)
			if fallbackErr == nil {
				removed = removed || fallbackRemoved
				continue
			}
			if fallbackRemoved {
				removed = true
			}
			failures = append(failures, fmt.Sprintf("Claude Code plugin %s: %s", installation.Key, fallbackErr))
			failures = append(failures, scopedFailures...)
			continue
		}
		failures = append(failures, scopedFailures...)
	}
	return removed, failures
}

func validClaudeScopes(installation claudePluginInstallation) ([]claudePluginScope, bool) {
	var valid []claudePluginScope
	needsFallback := !installation.ScopesKnown || len(installation.Scopes) == 0
	for _, scope := range installation.Scopes {
		if validPluginScope(scope.Name) {
			valid = append(valid, scope)
		} else {
			needsFallback = true
		}
	}
	return valid, needsFallback
}

// uninstallClaudeUnscoped handles registry formats that cannot name every
// scope. Claude removes one matching installation per invocation, so retry up
// to a safety cap and stop only on an explicit absent-plugin response.
func uninstallClaudeUnscoped(parent context.Context, claudePath, key string) (bool, error) {
	ctx, cancel := context.WithTimeout(parent, agentRemoveTimeout)
	defer cancel()

	removed := false
	for range 10 {
		output, err := runRemoveStep(ctx, claudePath, "", "plugin", "uninstall", key)
		if err != nil {
			if claudePluginAbsent(output, key) {
				return removed, nil
			}
			return removed, errors.New(commandOutputFailure(output, err))
		}
		removed = true
	}
	return removed, errors.New("uninstall safety limit reached")
}

func claudePluginAbsent(output []byte, key string) bool {
	message := strings.ToLower(string(output))
	absent := strings.Contains(message, "not installed") || strings.Contains(message, "no installed plugin") || strings.Contains(message, "not found")
	return absent && strings.Contains(message, strings.ToLower(key))
}

func removeCodexPlugin(parent context.Context) (bool, string) {
	codexPath := harness.FindCodexBinary()
	if codexPath == "" {
		if codexHomeExists() {
			return false, "Codex plugin: codex binary not found"
		}
		return false, ""
	}
	installed, queryErr := codexPluginInstalled(parent)
	if queryErr != nil {
		return false, "Codex plugin: checking installed state: " + queryErr.Error()
	}
	if !installed {
		return false, ""
	}

	commandOutput, err := runRemoveStep(parent, codexPath, "", "plugin", "remove", harness.CodexExpectedPluginKey, "--json")
	if err == nil {
		return true, ""
	}
	if codexPluginAbsent(commandOutput) {
		return false, ""
	}
	return false, "Codex plugin: " + commandOutputFailure(commandOutput, err)
}

func codexPluginAbsent(output []byte) bool {
	message := strings.ToLower(string(output))
	return strings.Contains(message, strings.ToLower(harness.CodexExpectedPluginKey)) &&
		(strings.Contains(message, "not installed") || strings.Contains(message, "not found"))
}

func runRemoveStep(parent context.Context, path, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, agentRemoveTimeout)
	defer cancel()
	output, err := runAgentRemoveCommand(ctx, path, dir, args...)
	if ctx.Err() != nil {
		return output, ctx.Err()
	}
	return output, err
}

func codexHomeExists() bool {
	resolved, err := harness.CodexHome()
	if err != nil {
		return false
	}
	info, err := os.Stat(resolved)
	return err == nil && info.IsDir()
}

func pathsEquivalent(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if left == right {
		return true
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

// pathEntriesEquivalent compares installation slots without following the
// final path component. Parent directory aliases should collapse to one slot,
// but two distinct symlinks that happen to share a target must both be handled.
func pathEntriesEquivalent(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	left, leftErr := filepath.Abs(filepath.Clean(left))
	right, rightErr := filepath.Abs(filepath.Clean(right))
	if leftErr != nil || rightErr != nil {
		return filepath.Clean(left) == filepath.Clean(right)
	}
	if left == right {
		return true
	}
	if filepath.Base(left) != filepath.Base(right) {
		return false
	}
	leftParent, leftErr := filepath.EvalSymlinks(filepath.Dir(left))
	rightParent, rightErr := filepath.EvalSymlinks(filepath.Dir(right))
	return leftErr == nil && rightErr == nil && pathsEquivalent(leftParent, rightParent)
}

func validProjectPath(path string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func commandFailure(err error) string {
	if err == nil {
		return "unknown failure"
	}
	return err.Error()
}

func commandOutputFailure(commandOutput []byte, err error) string {
	message := strings.TrimSpace(string(commandOutput))
	if len(message) > 500 {
		message = message[:500]
	}
	if message != "" {
		return message
	}
	return commandFailure(err)
}

func readClaudePluginInstallations(configDir string) ([]claudePluginInstallation, error) {
	path := filepath.Join(configDir, "plugins", "installed_plugins.json")
	data, err := os.ReadFile(path) //nolint:gosec // canonical user configuration path
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	installations, ok := parseClaudePluginInstallations(data)
	if !ok {
		return nil, fmt.Errorf("parsing %s", path)
	}
	return installations, nil
}

func parseClaudePluginInstallations(data []byte) ([]claudePluginInstallation, bool) {
	var envelope map[string]any
	if json.Unmarshal(data, &envelope) == nil && envelope != nil {
		if rawPlugins, exists := envelope["plugins"]; exists {
			pluginMap, ok := rawPlugins.(map[string]any)
			if !ok {
				return nil, false
			}
			return installationsFromMap(pluginMap, true), true
		}
		return installationsFromMap(envelope, false), true
	}

	var list []map[string]any
	if json.Unmarshal(data, &list) != nil || list == nil {
		return nil, false
	}
	byKey := make(map[string]*claudePluginInstallation)
	for _, entry := range list {
		key := pluginKeyFromEntry(entry)
		if !basecampClaudePluginKey(key) {
			continue
		}
		installation := byKey[key]
		if installation == nil {
			installation = &claudePluginInstallation{Key: key, ScopesKnown: true}
			byKey[key] = installation
		}
		if scope, ok := entry["scope"].(string); ok && scope != "" {
			installation.Scopes = appendUniqueClaudeScope(installation.Scopes, claudeScopeFromEntry(scope, entry))
		} else {
			installation.ScopesKnown = false
		}
	}
	return sortedInstallations(byKey), true
}

func installationsFromMap(pluginMap map[string]any, v2 bool) []claudePluginInstallation {
	byKey := make(map[string]*claudePluginInstallation)
	for key, raw := range pluginMap {
		if !basecampClaudePluginKey(key) {
			continue
		}
		installation := &claudePluginInstallation{Key: key}
		if entries, ok := raw.([]any); ok {
			installation.ScopesKnown = true
			for _, rawEntry := range entries {
				entry, entryOK := rawEntry.(map[string]any)
				if !entryOK {
					installation.ScopesKnown = false
					continue
				}
				if scope, scopeOK := entry["scope"].(string); scopeOK && scope != "" {
					installation.Scopes = appendUniqueClaudeScope(installation.Scopes, claudeScopeFromEntry(scope, entry))
				} else {
					installation.ScopesKnown = false
				}
			}
		} else if entry, ok := raw.(map[string]any); ok {
			if scope, ok := entry["scope"].(string); ok && scope != "" {
				installation.ScopesKnown = true
				installation.Scopes = appendUniqueClaudeScope(installation.Scopes, claudeScopeFromEntry(scope, entry))
			}
		}
		if v2 && raw == nil {
			installation.ScopesKnown = false
		}
		byKey[key] = installation
	}
	return sortedInstallations(byKey)
}

func sortedInstallations(byKey map[string]*claudePluginInstallation) []claudePluginInstallation {
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]claudePluginInstallation, 0, len(keys))
	for _, key := range keys {
		out = append(out, *byKey[key])
	}
	return out
}

func appendUniqueClaudeScope(values []claudePluginScope, value claudePluginScope) []claudePluginScope {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func claudeScopeFromEntry(scope string, entry map[string]any) claudePluginScope {
	projectPath, _ := entry["projectPath"].(string)
	if projectPath == "" {
		projectPath, _ = entry["project_path"].(string)
	}
	return claudePluginScope{Name: scope, ProjectPath: projectPath}
}

func pluginKeyFromEntry(entry map[string]any) string {
	for _, field := range []string{"package", "id", "name"} {
		if value, ok := entry[field].(string); ok && basecampClaudePluginKey(value) {
			return value
		}
	}
	return ""
}

func basecampClaudePluginKey(key string) bool {
	return key == harness.ClaudePluginName || key == harness.ClaudeExpectedPluginKey || key == "basecamp@basecamp"
}

// removeClaudeSkill removes only the canonical symlink written by Basecamp or
// a marked/legacy-managed copy created by the symlink fallback.
func removeClaudeSkill(path, baseline string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspecting %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, readErr := os.Readlink(path)
		if readErr != nil {
			return false, fmt.Errorf("reading %s: %w", path, readErr)
		}
		managedBaseline := baseline != "" && ownedOrLegacySkillDir(baseline)
		managedTarget := managedBaseline && pathsEquivalent(path, baseline)
		if !managedTarget && managedBaseline {
			managedTarget = brokenLinkTargetsPath(path, target, baseline)
		}
		if !managedTarget {
			return false, nil
		}
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return false, fmt.Errorf("removing %s: %w", path, removeErr)
		}
		return true, nil
	}
	return removeOwnedOrLegacySkill(path)
}

func ownedOrLegacySkillDir(dir string) bool {
	if ownedSkillDir(dir) {
		return true
	}
	info, err := os.Lstat(dir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false
	}
	// Extra user files do not change ownership of the legacy payload. Cleanup
	// removes only that payload and its managed link, leaving those files intact.
	if !regularFile(filepath.Join(dir, skillFilename)) {
		return false
	}
	installed, err := os.ReadFile(filepath.Join(dir, skillFilename)) //nolint:gosec // fixed skill path
	return err == nil && recognizedManagedSkillPayload(installed)
}

func brokenLinkTargetsPath(linkPath, target, expected string) bool {
	linkParent, err := filepath.EvalSymlinks(filepath.Dir(linkPath))
	if err != nil {
		linkParent = filepath.Dir(linkPath)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(linkParent, target)
	}
	return resolveExistingPathPrefix(target) == resolveExistingPathPrefix(expected)
}

func resolveExistingPathPrefix(path string) string {
	path = filepath.Clean(path)
	missing := make([]string, 0)
	for {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return filepath.Clean(path)
		}
		missing = append(missing, filepath.Base(path))
		path = parent
	}
}

// removeOwnedSkillFiles removes only files written by Basecamp from a skill
// directory carrying a current ownership marker or the legacy version marker.
// Additional files keep the now-unmanaged directory in place.
func removeOwnedSkillFiles(dir string) (bool, error) {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspecting %s: %w", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !ownedSkillDir(dir) {
		return false, nil
	}

	paths := []string{
		filepath.Join(dir, skillFilename),
		filepath.Join(dir, installedVersionFile),
		filepath.Join(dir, ownershipMarkerFile),
	}
	for _, path := range paths {
		entry, statErr := os.Lstat(path)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return false, fmt.Errorf("inspecting %s: %w", path, statErr)
		}
		if !entry.Mode().IsRegular() {
			return false, fmt.Errorf("%s is not a regular file", path)
		}
	}
	for _, path := range paths {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return false, fmt.Errorf("removing %s: %w", path, removeErr)
		}
	}
	_ = os.Remove(dir)
	return true, nil
}

// removeOwnedOrLegacySkill also recognizes installs that predate the ownership
// marker, such as the old wizard's direct Codex payload. Exact embedded or
// allowlisted content in a directory holding nothing else of ours is the
// required provenance; a merely similar skill is user state and stays put.
func removeOwnedOrLegacySkill(dir string) (bool, error) {
	if ownedSkillDir(dir) {
		return removeOwnedSkillFiles(dir)
	}
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspecting %s: %w", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("inspecting %s: %w", dir, err)
	}
	skillEntry := -1
	for i, entry := range entries {
		if entry.Name() == skillFilename && entry.Type().IsRegular() {
			skillEntry = i
			break
		}
	}
	if skillEntry == -1 {
		return false, nil
	}
	installed, err := os.ReadFile(filepath.Join(dir, skillFilename)) //nolint:gosec // fixed legacy skill path
	if err != nil {
		return false, fmt.Errorf("reading legacy managed skill: %w", err)
	}
	if !recognizedManagedSkillPayload(installed) {
		return false, nil
	}
	if err := os.Remove(filepath.Join(dir, skillFilename)); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("removing legacy managed skill: %w", err)
	}
	_ = os.Remove(dir)
	return true, nil
}

func recognizedManagedSkillPayload(data []byte) bool {
	embedded, err := skills.FS.ReadFile("basecamp/SKILL.md")
	if err == nil && bytes.Equal(data, embedded) {
		return true
	}
	sum := sha256.Sum256(data)
	_, recognized := legacyManagedSkillHashes[fmt.Sprintf("%x", sum)]
	return recognized
}

func ownedSkillDir(dir string) bool {
	// Parent aliases are supported, but a marker beyond a symlinked skill leaf
	// does not establish ownership of that leaf.
	info, err := os.Lstat(dir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false
	}
	return regularFile(filepath.Join(dir, ownershipMarkerFile)) || regularFile(filepath.Join(dir, installedVersionFile))
}

func regularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}
