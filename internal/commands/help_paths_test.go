package commands_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A help example that names a path cobra cannot resolve exits 0 showing group
// help, so nothing fails and the wrong path keeps getting taught. That is how
// "basecamp docs create" survived: Find() landed on the `docs` group with
// "create" left over.
//
// Coverage is bounded and deliberately so: only the leading run of bare
// lowercase words after "basecamp" is resolved (stopping at the first flag,
// placeholder, quote, or shell metacharacter), and a leftover token is only
// reported when it is the name or alias of some command in the tree — which is
// what distinguishes a mistyped subcommand from a literal argument like a
// project name.
func TestHelpExampleCommandPathsResolveExactly(t *testing.T) {
	root := buildRootWithAllCommands()

	commandWords := map[string]bool{}
	var collect func(*cobra.Command)
	collect = func(cmd *cobra.Command) {
		for _, sub := range cmd.Commands() {
			commandWords[sub.Name()] = true
			for _, alias := range sub.Aliases {
				commandWords[alias] = true
			}
			collect(sub)
		}
	}
	collect(root)

	word := regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

	var checked int
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		for _, text := range []string{cmd.Long, cmd.Example} {
			for _, line := range strings.Split(text, "\n") {
				_, after, found := strings.Cut(strings.TrimSpace(line), "basecamp ")
				if !found {
					continue
				}
				var path []string
				for _, token := range strings.Fields(after) {
					if !word.MatchString(token) {
						break
					}
					path = append(path, token)
				}
				// Prose ("basecamp is a CLI tool ...") is not an invocation:
				// require the first word to be a real top-level command.
				if len(path) == 0 || root.Commands() == nil || !isTopLevel(root, path[0]) {
					continue
				}

				target, remaining, err := root.Find(path)
				require.NoError(t, err, "line %q", line)
				checked++

				// Only a *group* can swallow a mistyped subcommand: it shows
				// its help and exits 0. A leaf's leftovers are its arguments
				// ("assignments due overdue"), even when the word happens to
				// name a command elsewhere in the tree.
				if len(remaining) > 0 && target.HasSubCommands() && commandWords[remaining[0]] {
					assert.Fail(t,
						"example names a path that does not resolve",
						"%s: %q resolves to the %q group with %q left over — it exits 0 showing group help",
						cmd.CommandPath(), strings.TrimSpace(line), target.CommandPath(), remaining[0])
				}
			}
		}
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(root)

	require.Greater(t, checked, 100, "expected the help corpus to yield many command paths")
}

func isTopLevel(root *cobra.Command, name string) bool {
	for _, sub := range root.Commands() {
		if sub.Name() == name {
			return true
		}
		for _, alias := range sub.Aliases {
			if alias == name {
				return true
			}
		}
	}
	return false
}
