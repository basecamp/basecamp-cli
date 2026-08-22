package tui

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stdinKinds are the two ways stdin arrives without a human behind it. Both
// have to refuse, and /dev/null is the one that used to slip through: it is a
// character device, so the older interactivity test called it a terminal.
var stdinKinds = []string{"pipe", "devnull"}

// The two launchers in this package draw to different streams, so the tests
// have to hold different things constant. A huh form renders to stderr
// (huh form.go:112), a bare bubbletea program to stdout. Point the launcher's
// own output stream at a pty so stdin is the only thing left disqualifying it —
// otherwise go test's piped stdout and stderr fail the check on their own, and
// the stdin cases below prove nothing at all.
//
// Best-effort: where no pty is available the test still runs, less specifically.
func terminalStdout(t *testing.T) {
	t.Helper()
	swapForPTY(t, &os.Stdout)
}

// terminalStderr is terminalStdout's counterpart for huh, which draws there.
func terminalStderr(t *testing.T) {
	t.Helper()
	swapForPTY(t, &os.Stderr)
}

func swapForPTY(t *testing.T, stream **os.File) {
	t.Helper()

	if runtime.GOOS == "windows" {
		return
	}
	pty, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return
	}

	orig := *stream
	*stream = pty
	t.Cleanup(func() {
		*stream = orig
		_ = pty.Close()
	})
}

// nonInteractiveStdin points os.Stdin at the named non-terminal for the
// duration of the test, so the assertions hold no matter how the test binary
// was invoked — running it straight from a terminal would otherwise leave
// stdin a TTY and prove nothing. The caller points the relevant output stream
// at a pty first.
func nonInteractiveStdin(t *testing.T, kind string) {
	t.Helper()

	var replacement *os.File
	switch kind {
	case "pipe":
		r, w, err := os.Pipe()
		require.NoError(t, err)
		t.Cleanup(func() { _ = w.Close() })
		replacement = r
	case "devnull":
		f, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
		require.NoError(t, err)
		replacement = f
	default:
		t.Fatalf("unknown stdin kind %q", kind)
	}

	orig := os.Stdin
	os.Stdin = replacement
	t.Cleanup(func() {
		os.Stdin = orig
		_ = replacement.Close()
	})
}

// promptFloor is one exported prompt in forms.go and a call that must refuse.
// The name has to match the function's declared name: TestPromptFloorCovers
// walks forms.go and fails if an exported function is missing from this table,
// so a prompt added without the floor cannot pass unnoticed.
type promptFloor struct {
	name string
	call func() error
}

func promptFloors() []promptFloor {
	opts := []SelectOption{{Value: "a", Label: "A"}}

	return []promptFloor{
		{"Confirm", func() error { _, err := Confirm("q", true); return err }},
		{"ConfirmDangerous", func() error { _, err := ConfirmDangerous("q"); return err }},
		{"Input", func() error { _, err := Input("t", "p"); return err }},
		{"InputRequired", func() error { _, err := InputRequired("t", "p"); return err }},
		{"TextArea", func() error { _, err := TextArea("t", "p"); return err }},
		{"Select", func() error { _, err := Select("t", opts); return err }},
		{"SelectWithDescription", func() error { _, err := SelectWithDescription("t", "d", opts); return err }},
		{"MultiSelect", func() error { _, err := MultiSelect("t", opts); return err }},
		{"Form", func() error { _, err := Form("t", []FormField{{Key: "k", Title: "T"}}); return err }},
		{"Note", func() error { return Note("t", "b") }},
		{"ConfirmSetDefault", func() error { _, err := ConfirmSetDefault("account_id"); return err }},
		{"SelectScope", func() error { _, err := SelectScope(); return err }},
	}
}

// TestPromptFloorRefusesNonInteractiveStdio is the anti-hang assertion: every
// prompt returns ErrNotInteractive rather than launching a bubbletea program.
// A prompt that skips the floor fails one of the two assertions depending on
// the environment — it blocks on /dev/tty where there is a controlling
// terminal (the timeout catches that), and returns a huh TTY error where there
// is not (the errors.Is catches that).
func TestPromptFloorRefusesNonInteractiveStdio(t *testing.T) {
	for _, floor := range promptFloors() {
		for _, kind := range stdinKinds {
			t.Run(floor.name+"/"+kind, func(t *testing.T) {
				terminalStderr(t) // huh draws here; stdin is the variable under test
				nonInteractiveStdin(t, kind)

				done := make(chan error, 1)
				go func() { done <- floor.call() }()

				select {
				case err := <-done:
					assert.True(t, errors.Is(err, ErrNotInteractive),
						"%s should return ErrNotInteractive on %s stdin, got %v", floor.name, kind, err)
				case <-time.After(5 * time.Second):
					t.Fatalf("%s blocked on %s stdin instead of refusing", floor.name, kind)
				}
			})
		}
	}
}

// TestPickerFloorRefusesNonInteractiveStdio covers the other bubbletea
// launcher in this package. Every current picker call site gates on
// resolve.Resolver.IsInteractive first; this makes the next one safe anyway.
//
// The loader case also asserts the loader never ran. A refusal that still
// fetched would have paid for a round trip nobody can act on, and would leave
// the caller unable to tell a refusal from a failed fetch.
func TestPickerFloorRefusesNonInteractiveStdio(t *testing.T) {
	items := []PickerItem{{ID: "1", Title: "One"}}

	var loaderCalls int
	loader := func() ([]PickerItem, error) {
		loaderCalls++
		return items, nil
	}

	for _, tc := range []struct {
		name   string
		picker *Picker
	}{
		{"items", NewPicker(items)},
		{"loader", NewPickerWithLoader(loader)},
	} {
		for _, kind := range stdinKinds {
			t.Run(tc.name+"/"+kind, func(t *testing.T) {
				terminalStdout(t) // the picker draws here; stdin is the variable under test
				nonInteractiveStdin(t, kind)

				done := make(chan error, 1)
				go func() {
					_, err := tc.picker.Run()
					done <- err
				}()

				select {
				case err := <-done:
					assert.True(t, errors.Is(err, ErrNotInteractive),
						"picker should return ErrNotInteractive on %s stdin, got %v", kind, err)
				case <-time.After(5 * time.Second):
					t.Fatalf("picker blocked on %s stdin instead of refusing", kind)
				}
			})
		}
	}

	assert.Zero(t, loaderCalls, "a refused picker must not fetch items it cannot show")
}

// TestPromptFloorCovers keeps the table honest. Every exported function in
// forms.go launches a huh form — directly or through one that does — so every
// one of them has to be exercised above. A new prompt fails here first.
func TestPromptFloorCovers(t *testing.T) {
	covered := make(map[string]bool)
	for _, floor := range promptFloors() {
		covered[floor.name] = true
	}

	file, err := parser.ParseFile(token.NewFileSet(), "forms.go", nil, 0)
	require.NoError(t, err)

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !fn.Name.IsExported() {
			continue
		}
		assert.True(t, covered[fn.Name.Name],
			"forms.go exports %s but promptFloors() does not exercise its non-interactive floor", fn.Name.Name)
	}
}
