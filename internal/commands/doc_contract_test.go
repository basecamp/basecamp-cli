package commands_test

import (
	"bufio"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/cli"
)

// TestDocContractArgs verifies that key commands declare the expected positional
// args in their Use: string, as parsed by cli.ParseArgs.
func TestDocContractArgs(t *testing.T) {
	root := buildRootWithAllCommands()

	contracts := []struct {
		path     string
		wantArgs []string // arg names in positional order; nil = no args
	}{
		{"basecamp todo", []string{"content"}},
		{"basecamp done", []string{"id|url"}},
		{"basecamp reopen", []string{"id|url"}},
		{"basecamp card", []string{"title", "body"}},
		{"basecamp comment", []string{"id|url", "content"}},
		{"basecamp message", []string{"title", "body"}},
		{"basecamp show", []string{"type", "id|url"}},
		{"basecamp search", []string{"query"}},
		{"basecamp assign", []string{"id"}},
		{"basecamp unassign", []string{"id"}},
		{"basecamp completion", []string{"shell"}},
		{"basecamp timeline", []string{"me"}},
		{"basecamp schedule", nil},                    // [action] stripped
		{"basecamp people", nil},                      // not runnable
		{"basecamp campfire", nil},                    // [action] stripped
		{"basecamp boost", nil},                       // [action] stripped
		{"basecamp projects list", nil},               // no args
		{"basecamp webhooks create", []string{"url"}}, // [flags] stripped
	}

	for _, tt := range contracts {
		t.Run(tt.path, func(t *testing.T) {
			cmd := findCommand(root, tt.path)
			require.NotNilf(t, cmd, "command %q not found in tree", tt.path)

			args := cli.ParseArgs(cmd)
			if tt.wantArgs == nil {
				assert.Nil(t, args, "expected no args for %s", tt.path)
				return
			}
			require.Len(t, args, len(tt.wantArgs), "arg count mismatch for %s", tt.path)
			for i, name := range tt.wantArgs {
				assert.Equal(t, name, args[i].Name, "arg[%d] name mismatch for %s", i, tt.path)
			}
		})
	}
}

// TestSkillMDQuickReferenceCommands validates that every command path mentioned
// in the Quick Reference table of SKILL.md exists in the live command tree.
// Skips schematic examples containing angle-bracket placeholders.
func TestSkillMDQuickReferenceCommands(t *testing.T) {
	root := buildRootWithAllCommands()

	// Read SKILL.md
	data, err := os.ReadFile("../../skills/basecamp/SKILL.md")
	require.NoError(t, err)

	// Extract Quick Reference section (from "## Quick Reference" to next "##")
	lines := extractSection(string(data), "## Quick Reference")
	require.NotEmpty(t, lines, "Quick Reference section not found in SKILL.md")

	// Parse table rows: | Task | `basecamp ...` |
	cmdRe := regexp.MustCompile("`(basecamp [^`]+)`")

	var checked int
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n")))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "|--") {
			continue
		}

		matches := cmdRe.FindAllStringSubmatch(line, -1)
		for _, m := range matches {
			fullCmd := m[1]
			// Extract command path (words before any flag or quoted arg)
			path := extractCommandPath(fullCmd)
			if path == "" {
				continue
			}

			// Skip schematic examples with angle-bracket placeholders in the
			// command name position (e.g., "basecamp <type> list")
			if strings.Contains(path, "<") {
				continue
			}

			// Try the full path, then progressively shorter paths to handle
			// cases where args follow the command (e.g., "basecamp recordings todos").
			cmd := findDeepestCommand(root, path)
			assert.NotNilf(t, cmd, "SKILL.md Quick Reference references %q but command not found (from: %s)", path, fullCmd)
			checked++
		}
	}
	require.NoError(t, scanner.Err())
	assert.Greater(t, checked, 0, "no commands found in Quick Reference table")
}

// findDeepestCommand tries progressively shorter paths until it finds a match.
// This handles cases where positional args follow the command name in examples
// (e.g., "basecamp recordings todos" where "todos" is an arg, not a subcommand).
func findDeepestCommand(root *cobra.Command, path string) *cobra.Command {
	parts := strings.Fields(path)
	for len(parts) >= 2 { // at least "basecamp <something>"
		if cmd := findCommand(root, strings.Join(parts, " ")); cmd != nil {
			return cmd
		}
		parts = parts[:len(parts)-1]
	}
	return nil
}

// findCommand traverses the command tree to find a command by its full path.
func findCommand(root *cobra.Command, path string) *cobra.Command {
	parts := strings.Fields(path)
	if len(parts) == 0 {
		return nil
	}
	// Skip the root name (e.g., "basecamp")
	cmd := root
	for _, name := range parts[1:] {
		found := false
		for _, sub := range cmd.Commands() {
			if sub.Name() == name || containsAlias(sub, name) {
				cmd = sub
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return cmd
}

// containsAlias checks if a command has the given alias.
func containsAlias(cmd *cobra.Command, name string) bool {
	for _, a := range cmd.Aliases {
		if a == name {
			return true
		}
	}
	return false
}

// extractSection returns lines from a markdown section starting at the given
// heading until the next heading of the same or higher level.
func extractSection(content, heading string) []string {
	lines := strings.Split(content, "\n")
	var result []string
	inSection := false
	headingLevel := strings.Count(strings.Fields(heading)[0], "#")

	for _, line := range lines {
		if strings.HasPrefix(line, heading) {
			inSection = true
			continue
		}
		if inSection {
			// Stop at next heading of same or higher level
			trimmed := strings.TrimLeft(line, "#")
			if len(line) > 0 && line[0] == '#' && len(line)-len(trimmed) <= headingLevel {
				break
			}
			result = append(result, line)
		}
	}
	return result
}

// extractCommandPath extracts the command path (basecamp subcommand ...) from
// a full command string, stopping at flags (--) or quoted args.
func extractCommandPath(full string) string {
	var parts []string
	for _, word := range strings.Fields(full) {
		if strings.HasPrefix(word, "-") || strings.HasPrefix(word, `"`) || strings.HasPrefix(word, "'") {
			break
		}
		parts = append(parts, word)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}
