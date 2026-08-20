package commands_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Reading stdin is the one step in a RunE that cannot be undone and cannot be
// hurried: it blocks until the producer is done. Everything that can reject the
// invocation from the arguments alone — parsing the target ID out of args[0],
// validating a file path — has to happen first, or a typo'd ID waits on a slow
// pipe and a blank one reports "stdin is empty" instead of "Invalid card ID".
//
// This was fixed at thirteen call sites by hand; the ordering is a property of
// every future one too, which is what this test holds. It reads the AST rather
// than running the commands, so it covers call sites no test exercises — but
// only syntactic forms it can recognize, listed in syntacticArgUse below.
func TestSyntacticArgChecksPrecedeStdinReads(t *testing.T) {
	dir := "."
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	fset := token.NewFileSet()
	var checked int

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		require.NoError(t, err)

		ast.Inspect(file, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			if key, ok := kv.Key.(*ast.Ident); !ok || (key.Name != "RunE" && key.Name != "PreRunE") {
				return true
			}

			firstStdinRead := token.NoPos
			var lateChecks []token.Pos
			ast.Inspect(kv.Value, func(inner ast.Node) bool {
				call, ok := inner.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch {
				case stdinResolver(call):
					if !firstStdinRead.IsValid() {
						firstStdinRead = call.Pos()
					}
				case deterministicCheck(call):
					// Every recognized check must precede the read, not just
					// the earliest one: comparing first-to-first lets a single
					// early check hide every later one.
					if firstStdinRead.IsValid() && call.Pos() > firstStdinRead {
						lateChecks = append(lateChecks, call.Pos())
					}
				}
				return true
			})

			if !firstStdinRead.IsValid() {
				return true
			}
			checked++
			for _, late := range lateChecks {
				assert.Fail(t, "stdin is read before a check that decides the invocation",
					"%s: reads stdin at %s but checks its arguments at %s — hoist the check above the resolver",
					name, fset.Position(firstStdinRead), fset.Position(late))
			}
			return true
		})
	}

	require.Greater(t, checked, 10, "expected many commands to both read stdin and check args")
}

// stdinResolver matches the two functions that can read from stdin.
func stdinResolver(call *ast.CallExpr) bool {
	name, ok := call.Fun.(*ast.Ident)
	return ok && (name.Name == "resolveContentValue" || name.Name == "resolveContentArg")
}

// deterministicCheck matches a call that can decide the invocation without the
// network: a syntactic use of args, or one of the local validation helpers.
// Recognizing a form by name is exactly as wide as the names listed; a new
// helper needs adding here, which is why the coverage claim above is bounded.
//
// Normalizers are deliberately absent. dateparse.Parse and isNumericID cannot
// fail, so they decide nothing on their own — listing them would report call
// sites that are branch selection rather than validation, and the noise would
// be answered by suppressions instead of fixes.
func deterministicCheck(call *ast.CallExpr) bool {
	if id, ok := call.Fun.(*ast.Ident); ok {
		switch id.Name {
		case "validateAttachPaths", "validateUploadPath", "validateScheduleTimestamp",
			"rejectSubscribeConflict", "rejectForeignAPIPath":
			return true
		}
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if pkg, ok := sel.X.(*ast.Ident); ok {
			switch pkg.Name + "." + sel.Sel.Name {
			case "hostutil.IsTrustedBasecampHost", "urlarg.Parse", "urlarg.IsURL":
				return true
			}
		}
	}
	return syntacticArgUse(call)
}

// syntacticArgUse matches a call that derives something from args without any
// account, config, or network dependency.
func syntacticArgUse(call *ast.CallExpr) bool {
	name, ok := call.Fun.(*ast.Ident)
	if !ok {
		if sel, isSel := call.Fun.(*ast.SelectorExpr); isSel {
			pkg, isPkg := sel.X.(*ast.Ident)
			if !isPkg || pkg.Name != "strconv" || sel.Sel.Name != "ParseInt" {
				return false
			}
		} else {
			return false
		}
	} else {
		switch name.Name {
		case "extractID", "extractWithProject", "extractCommentWithProject":
		default:
			return false
		}
	}

	// Only when it reads the positional arguments directly.
	for _, arg := range call.Args {
		if index, ok := arg.(*ast.IndexExpr); ok {
			if ident, ok := index.X.(*ast.Ident); ok && ident.Name == "args" {
				return true
			}
		}
	}
	return false
}
