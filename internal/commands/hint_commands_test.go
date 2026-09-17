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
// tree, exactly. Leftover words past the deepest command a phrase reaches are
// allowed only where the CLI itself would not read them as a subcommand: past
// a leaf (nothing to mistake them for), or past a command that runs and whose
// own argument validator accepts the word. A word naming a command
// .surface-breaking records as removed fails whatever else it could be.
//
// This is stricter than scripts/check-skill-drift.sh can be, because it asks
// the command rather than .surface: `basecamp setup agentz` fails both, but
// only this test knows `basecamp skill anything` really runs.
//
// Scopes: hint text in this package and in internal/connector/setup, whose
// readiness checks write the hints `connect setup` shows; and every string in
// connect.go, whose help and examples are the connector's setup
// documentation and are followed the same way. Commands only — a hint's
// flags are not checked here.
func TestHintCommandsResolve(t *testing.T) {
	root := buildRootWithAllCommands()
	removed := removedCommands(t)
	fset := token.NewFileSet()

	type reference struct{ file, phrase string }
	found := map[reference]bool{}
	seen := map[token.Pos]bool{}

	for _, dir := range []string{".", "../connector/setup"} {
		files, funcs := parsePackage(t, fset, dir)
		for name, file := range files {
			everything := dir == "." && name == "connect.go"
			for _, lit := range hintLiterals(file, funcs, everything) {
				if seen[lit.Pos()] {
					continue // a helper reached from more than one hint
				}
				seen[lit.Pos()] = true

				text, err := strconv.Unquote(lit.Value)
				require.NoError(t, err, "a string literal that compiled unquotes")
				for _, phrase := range commandPhrases(text) {
					pos := fset.Position(lit.Pos())
					found[reference{filepath.Base(pos.Filename), phrase}] = true

					resolved, word, ok := resolveCommandPhrase(root, removed, phrase)
					reason := "has no subcommand"
					if removed[resolved+" "+word] {
						reason = "no longer has"
					}
					assert.Truef(t, ok, "%s: hint names %q, and %q %s %q", pos, phrase, resolved, reason, word)
				}
			}
		}
	}

	// A scan that stops seeing a kind of hint passes silently forever, so one
	// known reference per kind has to be found.
	for _, want := range []struct {
		reference
		kind string
	}{
		{reference{"connect.go", "basecamp auth agent connect"}, "every string in connect.go"},
		{reference{"auth_agent.go", "basecamp auth login"}, "an ErrUsageHint argument"},
		{reference{"connect.go", "basecamp auth login"}, "a Hint field in a composite literal"},
		{reference{"boost.go", "basecamp boost list"}, "a hint built into a variable"},
		{reference{"wizard.go", "basecamp auth status"}, "a hint returned by a helper"},
		{reference{"doctor.go", "basecamp upgrade"}, "an assignment to a Hint field"},
		{reference{"feed.go", "basecamp events poll"}, "a hint-named field"},
		{reference{"checks.go", "basecamp connect setup"}, "a readiness check's hint"},
	} {
		assert.Truef(t, found[want.reference], "no longer sees %s: %q in %s", want.kind, want.phrase, want.file)
	}
}

// parsePackage parses every non-test file in dir, and indexes the functions
// they declare by name so a hint built by a helper can be followed into it.
func parsePackage(t *testing.T, fset *token.FileSet, dir string) (map[string]*ast.File, map[string]*ast.FuncDecl) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	files := map[string]*ast.File{}
	funcs := map[string]*ast.FuncDecl{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		require.NoError(t, err)
		files[name] = file

		for _, decl := range file.Decls {
			// Methods are keyed by name too: a name collision only widens
			// what is scanned.
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				funcs[fn.Name.Name] = fn
			}
		}
	}
	require.NotEmpty(t, files, dir)
	return files, funcs
}

// isHintName reports whether an identifier names hint text: Hint,
// forbiddenHint, agentReadsRefusedHint, hint.
func isHintName(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), "hint")
}

// hintLiterals returns the string literals in a file that an operator is
// meant to read: the hint argument of the hint-carrying error constructors,
// anything written to a hint-named field, variable or constant, and — when
// everything is wanted — every string literal in the file.
//
// A hint is not always written where it is passed. Some are built into a
// variable first (boost.go builds one, then appends --event), and some are
// returned by a helper (wizard.go passes wizardEscapeHint()). So the names a
// hint expression mentions are followed to their assignments, and the
// functions it calls are followed into their bodies. Over-reaching there
// costs nothing: a literal with no command in it is scanned and passes.
func hintLiterals(file *ast.File, funcs map[string]*ast.FuncDecl, everything bool) []*ast.BasicLit {
	var lits []*ast.BasicLit
	names := map[string]bool{}
	visited := map[string]bool{}

	// collect takes every string in an expression, notes the local names
	// whose values it is built from, and follows the functions it calls.
	//
	// Only a name that is itself a value counts. `cmd` in cmd.CommandPath()
	// and `fmt` in fmt.Sprintf are not hint text, and following them would
	// pull in whole command definitions — help text included — as if they
	// were hints.
	var collect func(ast.Node)
	collect = func(n ast.Node) {
		ast.Inspect(n, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.BasicLit:
				if node.Kind == token.STRING {
					lits = append(lits, node)
				}
			case *ast.Ident:
				names[node.Name] = true
			case *ast.SelectorExpr:
				return false // a field or a package member, not a local value
			case *ast.CallExpr:
				callee := ""
				switch fn := node.Fun.(type) {
				case *ast.Ident:
					callee = fn.Name
				case *ast.SelectorExpr:
					callee = fn.Sel.Name
				}
				if decl, ok := funcs[callee]; ok && !visited[callee] {
					visited[callee] = true
					collect(decl.Body)
				}
				for _, arg := range node.Args {
					collect(arg)
				}
				return false // the callee's name is not a value
			}
			return true
		})
	}

	if everything {
		ast.Inspect(file, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				lits = append(lits, lit)
			}
			return true
		})
		return lits
	}

	// Which argument carries the hint, by constructor name.
	hintArg := map[string]int{"ErrUsageHint": 1, "ErrNotFoundHint": 2}

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok {
				if i, ok := hintArg[sel.Sel.Name]; ok && len(node.Args) > i {
					collect(node.Args[i])
				}
			}
		case *ast.KeyValueExpr:
			if key, ok := node.Key.(*ast.Ident); ok && isHintName(key.Name) {
				collect(node.Value)
			}
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				if isHintName(assignedName(lhs)) {
					for _, rhs := range node.Rhs {
						collect(rhs)
					}
					break
				}
			}
		case *ast.ValueSpec:
			for _, id := range node.Names {
				if isHintName(id.Name) {
					for _, v := range node.Values {
						collect(v)
					}
					break
				}
			}
		}
		return true
	})

	// Whatever was assigned to a name a hint is built from.
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				if names[assignedName(lhs)] {
					for _, rhs := range node.Rhs {
						collect(rhs)
					}
					break
				}
			}
		case *ast.ValueSpec:
			for _, id := range node.Names {
				if names[id.Name] {
					for _, v := range node.Values {
						collect(v)
					}
					break
				}
			}
		}
		return true
	})
	return lits
}

// assignedName is the name an assignment writes: a variable's, or a field's.
func assignedName(lhs ast.Expr) string {
	switch target := lhs.(type) {
	case *ast.Ident:
		return target.Name
	case *ast.SelectorExpr:
		return target.Sel.Name
	}
	return ""
}

// removedCommands is every command path .surface-breaking records the CLI as
// having dropped. A word past a resolved command that names one of these is
// drift whatever else it could be — without it, "basecamp recordings archive"
// reads as the [type] argument the moment `archive` is removed.
func removedCommands(t *testing.T) map[string]bool {
	t.Helper()
	data, err := os.ReadFile("../../.surface-breaking")
	require.NoError(t, err)

	removed := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if path, ok := strings.CutPrefix(strings.TrimSpace(line), "CMD "); ok {
			removed[path] = true
		}
	}
	return removed
}

// commandPhrase matches a command reference the way scripts/check-skill-drift.sh
// extracts one.
var commandPhrase = regexp.MustCompile(`basecamp( [a-z][-a-z0-9]+)+`)

// commandPhrases returns the command references in text. A last word that
// runs on into a filename or a key — "upload report.pdf", "config set
// account_id" — is an argument, not a word of the command, and is dropped.
func commandPhrases(text string) []string {
	var phrases []string
	for _, loc := range commandPhrase.FindAllStringIndex(text, -1) {
		phrase, rest := text[loc[0]:loc[1]], text[loc[1]:]
		if len(rest) > 1 && strings.ContainsRune("._/", rune(rest[0])) && isNameByte(rest[1]) {
			i := strings.LastIndexByte(phrase, ' ')
			if i <= len("basecamp") {
				continue
			}
			phrase = phrase[:i]
		}
		phrases = append(phrases, phrase)
	}
	return phrases
}

func isNameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// resolveCommandPhrase walks a command phrase down the tree. It reports the
// deepest command the phrase reaches, the first word past it that the CLI
// would not accept there, and whether the phrase resolves.
func resolveCommandPhrase(root *cobra.Command, removed map[string]bool, phrase string) (resolved, word string, ok bool) {
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
	path := cmd.CommandPath()
	if i == len(words) {
		return path, "", true
	}
	// The root takes no word it does not know: cobra answers "unknown
	// command" there whatever its validator says, since it has none.
	if !cmd.HasParent() {
		return path, words[i], false
	}
	if removed[path+" "+words[i]] {
		return path, words[i], false
	}
	// Past a leaf there is no subcommand to mistake a word for: it is an
	// argument, or prose about running the command.
	if !cmd.HasSubCommands() {
		return path, "", true
	}
	// Past a group, the word is fine only where the CLI would take it as an
	// argument: the group runs, and its own validator accepts the word.
	if cmd.Runnable() && acceptsArgument(cmd, words[i]) {
		return path, "", true
	}
	return path, words[i], false
}

// acceptsArgument asks cmd's own argument validator whether word can be its
// first argument, in an invocation of up to three.
func acceptsArgument(cmd *cobra.Command, word string) bool {
	args := []string{word}
	for len(args) <= 3 {
		if cmd.ValidateArgs(args) == nil {
			return true
		}
		args = append(args, "x")
	}
	return false
}

func childNamed(cmd *cobra.Command, name string) *cobra.Command {
	for _, sub := range cmd.Commands() {
		if sub.Name() == name || containsAlias(sub, name) {
			return sub
		}
	}
	return nil
}
