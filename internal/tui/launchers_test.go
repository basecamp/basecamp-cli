package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A TUI launcher reads keystrokes, so every one of them needs the
// non-interactive floor. The floor is only worth anything if launchers cannot
// appear outside the places that apply it — which is the lesson of the
// `basecamp setup` hang: huh calls tea.NewProgram inside its own package, so an
// audit that greps for the launcher never saw the prompt at all.
//
// So bound where a launcher can be written rather than trying to recognize
// every spelling of one. Two layers:
//
//   - a file may launch only if it is listed here, and
//   - inside a listed file, the launch may sit only in the named runner, so a
//     second unguarded one cannot ride in on the file's exemption.
//
// A file whose runner is "" is exempt wholesale, which needs its own argument.
// Both lists are deliberately short; adding to either is a decision to apply
// the floor by hand, and belongs in review rather than waved through.
var (
	// bubbleteaLaunchers maps a file that may call tea.NewProgram to the sole
	// function within it that may do so.
	bubbleteaLaunchers = map[string]string{
		"internal/tui/picker.go":           "runPicker",
		"internal/tui/spinner.go":          "", // no callers; slated for deletion
		"internal/tui/paginated_picker.go": "", // no callers; slated for deletion
		"internal/commands/tui.go":         "", // dev build tag; not in a shipped binary
	}

	// huhImporters may import huh at all. huh launches bubbletea internally, so
	// importing it is importing a launcher. forms.go funnels every form through
	// runForm — asserted separately by TestFormsRunsOnlyThroughRunForm, which
	// looks for .Run() rather than NewProgram because the launch is inside huh.
	huhImporters = map[string]string{
		"internal/tui/forms.go": "the sanctioned prompt layer",
	}

	// launcherModules are the modules whose NewProgram starts a program, each
	// paired with the package name it declares. Both of these live at
	// .../bubbletea (or .../bubbletea/v2) and declare `package tea`, so the
	// declared name is recorded here rather than derived from the path — an
	// unaliased `import "charm.land/bubbletea/v2"` binds tea, not bubbletea,
	// and a resolver that guessed from the path would look up the wrong
	// identifier and wave the launcher through.
	launcherModules = []struct{ path, pkg string }{
		{"charm.land/bubbletea", "tea"},
		{"github.com/charmbracelet/bubbletea", "tea"},
	}

	huhPaths = []string{
		"charm.land/huh",
		"github.com/charmbracelet/huh",
	}
)

// matchesModule reports whether an import path is a module or one of its
// subpackages, under any major-version suffix.
func matchesModule(importPath string, modules []string) bool {
	for _, m := range modules {
		if importPath == m || strings.HasPrefix(importPath, m+"/") {
			return true
		}
	}
	return false
}

// launcherIdents maps the identifiers a file binds to a launcher module back to
// that module's path. An explicit alias wins; otherwise the import binds the
// package name the module declares, which launcherModules records. Both forms
// are the same launcher and both have to be recognized:
//
//	import tea "charm.land/bubbletea/v2"   // alias
//	import "charm.land/bubbletea/v2"       // also binds tea
//
// Only the modules in launcherModules are resolved. That is the check's
// boundary: a new bubbletea-alike would need adding here, which go.mod makes
// visible in the same review.
func launcherIdents(file *ast.File) map[string]string {
	idents := make(map[string]string, len(file.Imports))
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		for _, mod := range launcherModules {
			if !matchesModule(importPath, []string{mod.path}) {
				continue
			}
			name := mod.pkg
			if spec.Name != nil {
				name = spec.Name.Name
			}
			idents[name] = importPath
		}
	}
	return idents
}

// importPaths returns every path a file imports, for checks that do not care
// what the import is called locally.
func importPaths(file *ast.File) []string {
	paths := make([]string, 0, len(file.Imports))
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		paths = append(paths, importPath)
	}
	return paths
}

// repoRoot returns the module root, two levels up from internal/tui.
func repoRoot(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)

	root := filepath.Join(wd, "..", "..")
	_, err = os.Stat(filepath.Join(root, "go.mod"))
	require.NoError(t, err, "expected the module root at %s", root)
	return root
}

// TestNoUnsanctionedLaunchers walks every non-test .go file in the repo and
// fails on a TUI launcher outside the lists above — or inside a listed file but
// outside its named runner.
func TestNoUnsanctionedLaunchers(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "bin", "dist":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}

		rel, relErr := filepath.Rel(root, p)
		require.NoError(t, relErr)
		rel = filepath.ToSlash(rel)

		// A file this walk cannot parse is not this check's business — the
		// compiler and the linter both fail on it first.
		file, parseErr := parser.ParseFile(fset, p, nil, 0)
		if parseErr != nil {
			return nil //nolint:nilerr // unparseable files are out of scope here
		}

		for _, importPath := range importPaths(file) {
			if matchesModule(importPath, huhPaths) {
				_, allowed := huhImporters[rel]
				assert.True(t, allowed,
					"%s imports %s. huh launches a bubbletea program against the real stdin; "+
						"route prompts through internal/tui instead of importing it here",
					rel, importPath)
			}
		}

		idents := launcherIdents(file)

		runner, fileAllowed := bubbleteaLaunchers[rel]
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "NewProgram" {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				modulePath, isLauncher := idents[ident.Name]
				if !isLauncher {
					return true
				}
				if !fileAllowed {
					assert.Fail(t, "unsanctioned bubbletea launcher",
						"%s:%s calls NewProgram on %s. A bubbletea program reached without an "+
							"interactivity floor waits on /dev/tty rather than failing; gate it, "+
							"or route it through internal/tui",
						rel, fn.Name.Name, modulePath)
					return true
				}
				if runner != "" {
					assert.Equal(t, runner, fn.Name.Name,
						"%s may launch bubbletea only in %s, where the floor is applied — "+
							"%s starts a second, unguarded program", rel, runner, fn.Name.Name)
				}
				return true
			})
		}
		return nil
	})
	require.NoError(t, err)
}

// TestFormsRunsOnlyThroughRunForm keeps forms.go funneled. Every prompt there
// builds a form and hands it to runForm, which is where the floor lives — a
// .Run() anywhere else in the file is a prompt that skipped it. NewProgram is
// the wrong thing to look for here: huh makes that call inside its own package,
// which is exactly how this family stayed invisible.
func TestFormsRunsOnlyThroughRunForm(t *testing.T) {
	assert.Equal(t, map[string]bool{"runForm": true}, functionsCallingRun(t, "forms.go"),
		"forms.go must execute forms only in runForm, which is where the non-interactive floor is applied")
}

// functionsCallingRun returns the names of functions in a file that call
// something's .Run() method.
func functionsCallingRun(t *testing.T, filename string) map[string]bool {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	require.NoError(t, err)

	callers := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Run" {
				callers[fn.Name.Name] = true
			}
			return true
		})
	}
	return callers
}

// TestLauncherIdentsResolvesEverySpelling guards the resolver the backstop
// depends on. It got this wrong once in a way nothing caught: it derived the
// identifier from the import path, so an unaliased bubbletea import registered
// as "bubbletea" while the file actually binds "tea", and every unaliased
// launcher walked past the check. Nothing in the repo noticed, because
// picker.go happens to alias its import.
//
// Both modules declare `package tea` at a path ending in bubbletea (or
// bubbletea/v2), so path and package name genuinely disagree — the exact shape
// a path-derived guess gets wrong.
func TestLauncherIdentsResolvesEverySpelling(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
		ident  string
		path   string
	}{
		{
			name:   "unaliased v2 binds tea, not bubbletea",
			source: `package p; import "charm.land/bubbletea/v2"`,
			ident:  "tea",
			path:   "charm.land/bubbletea/v2",
		},
		{
			name:   "unaliased v1 binds tea, not bubbletea",
			source: `package p; import "github.com/charmbracelet/bubbletea"`,
			ident:  "tea",
			path:   "github.com/charmbracelet/bubbletea",
		},
		{
			name:   "explicit alias wins",
			source: `package p; import bt "charm.land/bubbletea/v2"`,
			ident:  "bt",
			path:   "charm.land/bubbletea/v2",
		},
		{
			name:   "an alias may even be a misleading one",
			source: `package p; import bubbletea "github.com/charmbracelet/bubbletea"`,
			ident:  "bubbletea",
			path:   "github.com/charmbracelet/bubbletea",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "p.go", tc.source, 0)
			require.NoError(t, err)

			assert.Equal(t, map[string]string{tc.ident: tc.path}, launcherIdents(file))
		})
	}
}

// TestLauncherIdentsIgnoresUnrelatedImports keeps the resolver from claiming
// identifiers it has no business claiming — a false positive here would block
// an unrelated NewProgram on some other package.
func TestLauncherIdentsIgnoresUnrelatedImports(t *testing.T) {
	source := `package p

import (
	"os"

	"github.com/charmbracelet/huh"
	tea "example.com/not/bubbletea-alike"
)`

	file, err := parser.ParseFile(token.NewFileSet(), "p.go", source, 0)
	require.NoError(t, err)

	assert.Empty(t, launcherIdents(file))
}
