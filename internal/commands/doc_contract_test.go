package commands_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

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
		{"basecamp todos create", []string{"content"}},
		{"basecamp todos complete", []string{"id|url"}},
		{"basecamp todos uncomplete", []string{"id|url"}},
		{"basecamp cards create", []string{"title", "body"}},
		{"basecamp comments create", []string{"id|url", "content"}},
		{"basecamp messages create", []string{"title", "body"}},
		{"basecamp show", []string{"type", "id|url"}},
		{"basecamp search", []string{"query"}},
		{"basecamp assign", []string{"id|url"}},
		{"basecamp unassign", []string{"id|url"}},
		{"basecamp completion", []string{"shell"}},
		{"basecamp timeline", []string{"me"}},
		{"basecamp schedule", nil},                    // [action] stripped
		{"basecamp people", nil},                      // not runnable
		{"basecamp chat", nil},                        // [action] stripped
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

func TestBasecampSkillStaysDiscoveryFirstAndFocused(t *testing.T) {
	data, err := os.ReadFile("../../skills/basecamp/SKILL.md")
	require.NoError(t, err)

	const maxSkillBytes = 16 * 1024
	assert.LessOrEqual(t, len(data), maxSkillBytes,
		"basecamp skill is a routing and safety guide, not a command reference; use --agent --help")

	content := string(data)
	leafHelp := "basecamp <group> <subcommand> --agent --help"
	rootHelp := "basecamp --agent --help"
	require.Contains(t, content, leafHelp)
	require.Contains(t, content, rootHelp)
	assert.Less(t, strings.Index(content, leafHelp), strings.Index(content, rootHelp),
		"leaf help must be taught before root help")
	assert.Contains(t, content, "source of truth")
	assert.NotContains(t, content, "## Resource Reference")
	assert.NotContains(t, content, "## Quick Reference")
}

// TestSkillDescriptionsFitAgentSkillsSpec keeps skill metadata portable across
// consumers. Count parsed YAML characters, not source bytes or wrapping:
// https://agentskills.io/specification#description-field
func TestSkillDescriptionsFitAgentSkillsSpec(t *testing.T) {
	paths, err := filepath.Glob("../../skills/*/SKILL.md")
	require.NoError(t, err)
	require.NotEmpty(t, paths)

	for _, path := range paths {
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			parts := strings.SplitN(string(data), "---", 3)
			require.Len(t, parts, 3)
			require.Empty(t, parts[0], "skill must start with YAML frontmatter")

			var frontmatter struct {
				Description string `yaml:"description"`
			}
			require.NoError(t, yaml.Unmarshal([]byte(parts[1]), &frontmatter))
			require.NotEmpty(t, strings.TrimSpace(frontmatter.Description))
			assert.LessOrEqual(t, utf8.RuneCountInString(frontmatter.Description), 1024,
				"keep routing cues in the description and workflow details in the body")
		})
	}
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
