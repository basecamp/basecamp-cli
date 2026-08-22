package tui

import (
	"errors"
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
		"internal/tui/picker.go": "runPicker",

		// Zero callers today and slated for deletion, but "dead" is not a gate:
		// adding a caller would not add a NewProgram, so the check would still
		// pass while the hang came back. Both launch from their own Run, and
		// both apply the floor there.
		"internal/tui/spinner.go":          "Run",
		"internal/tui/paginated_picker.go": "Run",

		"internal/commands/tui.go": "", // dev build tag; not in a shipped binary
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
		for _, ref := range referencesTo(file, "NewProgram", idents) {
			if !fileAllowed {
				assert.Fail(t, "unsanctioned bubbletea launcher",
					"%s (%s) references NewProgram on %s. A bubbletea program reached without an "+
						"interactivity floor waits on /dev/tty rather than failing; gate it, "+
						"or route it through internal/tui",
					rel, ref.where, ref.module)
				continue
			}
			if runner != "" {
				assert.Equal(t, runner, ref.enclosing,
					"%s may reach bubbletea only in %s, where the floor is applied — %s "+
						"reaches it outside that runner", rel, runner, ref.where)
			}
		}
		return nil
	})
	require.NoError(t, err)
}

// TestRunFormPinsItsOutputStream keeps the floor and huh talking about the same
// stream. huh has two render paths with two different defaults — stderr
// normally, stdout under accessible mode, which it enables on its own when
// TERM=dumb — so the stream is only knowable if we set it. canPrompt asks about
// stderr; runForm must therefore say stderr, not inherit a default that changes
// under an environment variable.
func TestRunFormPinsItsOutputStream(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "forms.go", nil, 0)
	require.NoError(t, err)

	var pinned bool
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "runForm" {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "WithOutput" {
				return true
			}
			arg, ok := call.Args[0].(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := arg.X.(*ast.Ident)
			if ok && pkg.Name == "os" && arg.Sel.Name == "Stderr" {
				pinned = true
			}
			return true
		})
	}

	assert.True(t, pinned,
		"runForm must call WithOutput(os.Stderr): huh renders to stdout in accessible mode "+
			"(auto-enabled when TERM=dumb), which would disagree with canPrompt's stderr check")
}

// launcherRef is one mention of a guarded symbol, and the top-level declaration
// it sits in.
type launcherRef struct {
	enclosing string // the func's name, or "" for a package-level declaration
	where     string // human-readable, for the failure message
	module    string
}

// referencesTo finds every mention of sel on a launcher package anywhere in the
// file, and attributes each to its enclosing top-level declaration.
//
// It walks the whole file rather than only *ast.FuncDecl bodies, because a
// launcher does not have to live in a function to run:
//
//	var launch = func() { tea.NewProgram(m).Run() }   // package-level func value
//	var newProgram = tea.NewProgram                   // package-level alias
//
// Both are declarations, not FuncDecls, and a FuncDecl-only walk never sees
// them. Matching the mention rather than the call also catches the alias, where
// the call happens somewhere else entirely.
func referencesTo(file *ast.File, sel string, idents map[string]string) []launcherRef {
	var refs []launcherRef

	for _, decl := range file.Decls {
		enclosing, where := "", "package-level declaration"
		if fn, ok := decl.(*ast.FuncDecl); ok {
			enclosing = fn.Name.Name
			where = "func " + fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				where = "method " + fn.Name.Name
			}
		}

		ast.Inspect(decl, func(n ast.Node) bool {
			s, ok := n.(*ast.SelectorExpr)
			if !ok || s.Sel.Name != sel {
				return true
			}
			ident, ok := s.X.(*ast.Ident)
			if !ok {
				return true
			}
			if module, isLauncher := idents[ident.Name]; isLauncher {
				refs = append(refs, launcherRef{enclosing: enclosing, where: where, module: module})
			}
			return true
		})
	}
	return refs
}

// formLaunchers are huh.Form's exported entry points. Both start the same
// bubbletea program, so both have to be funneled — checking only Run leaves
// RunWithContext as an open door.
var formLaunchers = []string{"Run", "RunWithContext"}

// TestFormsRunsOnlyThroughRunForm keeps forms.go funneled. Every prompt there
// builds a form and hands it to runForm, which is where the floor lives — a
// launch anywhere else in the file is a prompt that skipped it. NewProgram is
// the wrong thing to look for here: huh makes that call inside its own package,
// which is exactly how this family stayed invisible.
func TestFormsRunsOnlyThroughRunForm(t *testing.T) {
	assert.Equal(t, map[string]bool{"runForm": true},
		functionsReferencing(t, "forms.go", formLaunchers...),
		"forms.go must launch forms only in runForm, which is where the non-interactive floor is applied")
}

// functionsReferencing returns the enclosing top-level declaration of every
// mention of the named methods in a file.
//
// Two deliberate choices, each closing a way past an earlier version:
//
//   - It matches a selector *reference*, not a call. `run := form.Run` is a
//     method value: the launch happens wherever run is invoked, possibly in
//     another file, and a CallExpr-only scan sees nothing here.
//   - A package-level declaration reports as "", which no allowlist entry can
//     match, so a launcher outside any function fails rather than slipping past
//     a FuncDecl-only walk.
//
// It does not check the receiver's type — that would need type information this
// walk does not have. For a single-purpose file like forms.go that errs toward
// strictness, which is the right direction for a backstop: a false positive is
// a conversation, a false negative is the bug shipping.
func functionsReferencing(t *testing.T, filename string, names ...string) map[string]bool {
	t.Helper()

	wanted := make(map[string]bool, len(names))
	for _, n := range names {
		wanted[n] = true
	}

	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	require.NoError(t, err)

	callers := map[string]bool{}
	for _, decl := range file.Decls {
		enclosing := ""
		if fn, ok := decl.(*ast.FuncDecl); ok {
			enclosing = fn.Name.Name
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && wanted[sel.Sel.Name] {
				callers[enclosing] = true
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

// TestReferencesToSeesPackageLevelLaunchers guards the walk itself. An earlier
// revision iterated only *ast.FuncDecl bodies, so a launcher that is not inside
// a function — a package-level func value, or an alias whose call happens
// elsewhere — was invisible to the backstop while looking perfectly normal in
// the diff.
//
// The enclosing name for a package-level declaration is "", which no allowlist
// entry can match, so such a launcher fails rather than slipping through.
func TestReferencesToSeesPackageLevelLaunchers(t *testing.T) {
	const mod = "charm.land/bubbletea/v2"

	for _, tc := range []struct {
		name      string
		source    string
		enclosing string
	}{
		{
			name:      "inside a function",
			source:    `func run() { tea.NewProgram(nil) }`,
			enclosing: "run",
		},
		{
			name:      "inside a method",
			source:    `func (p *picker) run() { tea.NewProgram(nil) }`,
			enclosing: "run",
		},
		{
			name:      "package-level func value",
			source:    `var launch = func() { tea.NewProgram(nil) }`,
			enclosing: "",
		},
		{
			name:      "package-level alias, called elsewhere entirely",
			source:    `var newProgram = tea.NewProgram`,
			enclosing: "",
		},
		{
			name:      "inside a package-level composite literal",
			source:    `var launchers = []func(){func() { tea.NewProgram(nil) }}`,
			enclosing: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "package p\n\nimport \"" + mod + "\"\n\ntype picker struct{}\n\n" + tc.source
			file, err := parser.ParseFile(token.NewFileSet(), "p.go", src, 0)
			require.NoError(t, err)

			refs := referencesTo(file, "NewProgram", launcherIdents(file))
			require.Len(t, refs, 1, "the walk must see this launcher")
			assert.Equal(t, tc.enclosing, refs[0].enclosing)
			assert.Equal(t, mod, refs[0].module)
		})
	}
}

// TestReferencesToIgnoresUnrelatedSelectors keeps the walk from firing on
// something that merely shares a method name.
func TestReferencesToIgnoresUnrelatedSelectors(t *testing.T) {
	src := `package p

import "charm.land/bubbletea/v2"

type other struct{}

func (o other) NewProgram() {}

func run() {
	var o other
	o.NewProgram()
	_ = tea.Quit
}`

	file, err := parser.ParseFile(token.NewFileSet(), "p.go", src, 0)
	require.NoError(t, err)

	assert.Empty(t, referencesTo(file, "NewProgram", launcherIdents(file)),
		"only NewProgram on a launcher package counts")
}

// TestRunFormTranslatesCancellation guards a chain that five call sites now
// depend on and none of them can see.
//
// huh reports a dismissal — Escape or Ctrl+C, both bound to Quit by escKeyMap —
// by setting f.aborted, which Run turns into ErrUserAborted (form.go:558, 690).
// Everything else (a timeout, a wrapped bubbletea failure) arrives as an
// ordinary error. runForm is the single place that knows this, translating the
// dismissal into ErrCanceled so callers can swallow exactly that one value.
//
// Drop the translation and every caller silently reverts to reporting a
// cancellation nobody performed — which is the bug this replaced, and it would
// come back without a single test failing anywhere else.
func TestRunFormTranslatesCancellation(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "forms.go", nil, 0)
	require.NoError(t, err)

	var sawAborted, sawCanceled bool
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "runForm" {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			switch e := n.(type) {
			case *ast.SelectorExpr:
				if id, ok := e.X.(*ast.Ident); ok && id.Name == "huh" && e.Sel.Name == "ErrUserAborted" {
					sawAborted = true
				}
			case *ast.Ident:
				if e.Name == "ErrCanceled" {
					sawCanceled = true
				}
			}
			return true
		})
	}

	assert.True(t, sawAborted && sawCanceled,
		"runForm must translate huh.ErrUserAborted into ErrCanceled: it is the only place that "+
			"knows huh's error values, and its callers swallow ErrCanceled alone")
}

// TestPromptSentinelsAreDistinct keeps the two outcomes from collapsing into
// each other. "Nobody could be asked" and "the user said no" lead to opposite
// handling — a usage error versus exit 0 — so a caller matching on one must
// never match the other.
func TestPromptSentinelsAreDistinct(t *testing.T) {
	assert.False(t, errors.Is(ErrCanceled, ErrNotInteractive))
	assert.False(t, errors.Is(ErrNotInteractive, ErrCanceled))
	assert.NotEqual(t, ErrCanceled.Error(), ErrNotInteractive.Error())
}
