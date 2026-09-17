package commands_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A hint is a command an operator is about to run. `basecamp profile set
// <name> account_id <id>` sat in connect.go's account-binding hint and was
// not a command at all — `profile` has no `set` — so anyone who followed it
// got "unknown command" at the moment they were already stuck.
//
// So every command named in a hint is resolved here against the real command
// tree. Resolving to the nearest existing ancestor is what let the invented
// subcommand through the skill drift check; this test resolves exactly, and
// allows leftover words only where they cannot be a subcommand: after a
// command that takes positional arguments (they are argument values), or
// after a leaf that has no subcommands (they are prose).
//
// Two scopes, for two reasons. Hint text everywhere in the package, because
// a hint is an instruction. Every string in connect.go, because its help and
// examples are the connector's setup documentation and are followed the same
// way.
func TestHintCommandsResolve(t *testing.T) {
	root := buildRootWithAllCommands()
	fset := token.NewFileSet()

	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	var checked int
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		require.NoError(t, err)

		for _, lit := range hintLiterals(file, name == "connect.go") {
			text, err := strconv.Unquote(lit.Value)
			if err != nil {
				continue // a raw string with an unquotable escape; nothing to read
			}
			for _, phrase := range commandPhrases(text) {
				checked++
				resolved, word, ok := resolveCommandPhrase(root, phrase)
				assert.Truef(t, ok, "%s: hint names %q, and %q has no subcommand %q",
					fset.Position(lit.Pos()), phrase, resolved, word)
			}
		}
	}

	// A scan that stopped finding hints would pass silently forever.
	assert.Greaterf(t, checked, 40, "only %d command references scanned — the scan has stopped seeing hints", checked)
}

// hintLiterals returns the string literals in a file that an operator is
// meant to read: the hint argument of the hint-carrying error constructors,
// the Hint field of an output.Error, and — when everything is wanted — every
// string literal in the file.
func hintLiterals(file *ast.File, everything bool) []*ast.BasicLit {
	var lits []*ast.BasicLit
	collect := func(n ast.Node) {
		ast.Inspect(n, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				lits = append(lits, lit)
			}
			return true
		})
	}

	if everything {
		collect(file)
		return lits
	}

	// Which argument carries the hint, by constructor name.
	hintArg := map[string]int{"ErrUsageHint": 1, "ErrNotFoundHint": 2}

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if i, ok := hintArg[sel.Sel.Name]; ok && len(node.Args) > i {
				collect(node.Args[i])
			}
		case *ast.KeyValueExpr:
			if key, ok := node.Key.(*ast.Ident); ok && key.Name == "Hint" {
				collect(node.Value)
			}
		}
		return true
	})
	return lits
}

// commandPhrase matches a command reference the way scripts/check-skill-drift.sh
// extracts one, so a hint and a skill are read by the same rule.
var commandPhrase = regexp.MustCompile(`basecamp( [a-z][-a-z0-9]+)+`)

func commandPhrases(text string) []string {
	return commandPhrase.FindAllString(text, -1)
}

// resolveCommandPhrase walks a command phrase down the tree. It reports the
// deepest command the phrase reaches, the first word past it that command
// does not have, and whether the phrase resolves.
func resolveCommandPhrase(root *cobra.Command, phrase string) (resolved, word string, ok bool) {
	words := strings.Fields(phrase)
	cmd := root
	i := 1 // words[0] is the binary name
	for ; i < len(words); i++ {
		sub := childNamed(cmd, words[i])
		if sub == nil {
			break
		}
		cmd = sub
	}
	if i == len(words) {
		return cmd.CommandPath(), "", true
	}
	// Leftover words cannot be an invented subcommand when the command runs
	// as it stands — they are its arguments, or prose about running it — nor
	// when it has no subcommands they could be mistaken for. What is left is
	// a group that only dispatches, where the next word has to be one of the
	// subcommands it dispatches to.
	if cmd.Runnable() || !cmd.HasSubCommands() {
		return cmd.CommandPath(), "", true
	}
	return cmd.CommandPath(), words[i], false
}

func childNamed(cmd *cobra.Command, name string) *cobra.Command {
	for _, sub := range cmd.Commands() {
		if sub.Name() == name || containsAlias(sub, name) {
			return sub
		}
	}
	return nil
}
