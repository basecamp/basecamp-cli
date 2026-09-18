package driver

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTheRedactionRuleTakesOutEverythingItNames(t *testing.T) {
	state := t.TempDir()
	r := NewRedactor(Redaction{
		Secrets: []string{"test-token-not-real"},
		Env:     []string{"ANTHROPIC_API_KEY=test-key-not-real", "HOME=/home/operator", "TZ=UTC", "SHORT=abc"},
		Dirs:    []string{state},
	})

	assert.NotContains(t, r.Sanitize("token test-token-not-real used"), "test-token-not-real", "a named secret")
	assert.NotContains(t, r.Sanitize("key test-key-not-real used"), "test-key-not-real", "a value of the worker's environment")
	assert.Contains(t, r.Sanitize("under /home/operator/Work"), "/home/operator/Work", "BaseEnv's values are the operator's own, not the agent's")
	assert.Contains(t, r.Sanitize("abc"), "abc", "a value too short to remove safely")
	assert.NotContains(t, r.Sanitize("open "+filepath.Join(state, "ledger.db")+": denied"), state, "a path under the state directory")
	assert.Contains(t, r.Sanitize("open "+filepath.Join(state, "ledger.db")+": denied"), ": denied", "and the rest of the message stands")
	assert.NotContains(t, r.Sanitize("logged in as someone@example.com"), "someone@example.com")
	assert.NotContains(t, r.Sanitize("with Bearer abc.def-ghi"), "abc.def-ghi")
	assert.NotContains(t, r.Sanitize(strings.Repeat("x", 48)), strings.Repeat("x", 48))

	// The pattern rules hold even for a caller with no redaction of its own.
	assert.NotContains(t, (*Redactor)(nil).Sanitize("someone@example.com"), "someone@example.com")
}

func TestTheRuleFollowsADirectoryThroughItsSymlink(t *testing.T) {
	resolved := t.TempDir()
	link := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.Symlink(resolved, link))
	r := NewRedactor(Redaction{Dirs: []string{link}})
	assert.NotContains(t, r.Sanitize("open "+filepath.Join(resolved, "ledger.db")), resolved, "the resolved path is the same directory")
	assert.NotContains(t, r.Sanitize("open "+filepath.Join(link, "ledger.db")), link)
}

func TestStderrIsNeverPassedOnVerbatim(t *testing.T) {
	r := NewRedactor(Redaction{Secrets: []string{"test-token-not-real"}})
	out := r.Stderr("starting\nusing test-token-not-real\x07 now\n")
	assert.NotContains(t, out, "test-token-not-real")
	assert.NotContains(t, out, "starting", "only the last line")
	assert.NotContains(t, out, "\x07", "no control characters")
	assert.LessOrEqual(t, len(r.Stderr(strings.Repeat("y", 4000))), maxStderr)
}

func TestARedactedErrorAnswersIsAndAsWithoutCarryingTheSecret(t *testing.T) {
	r := NewRedactor(Redaction{Secrets: []string{"test-token-not-real"}})
	inner := fmt.Errorf("%w: wrote test-token-not-real", ErrUnusable)
	err := r.Err(&StartError{Process: Process{PID: 42, PGID: 42}, Err: errors.Join(ErrNotStarted, inner)})

	assert.NotContains(t, err.Error(), "test-token-not-real")
	assert.NotContains(t, fmt.Sprintf("%+v", err), "test-token-not-real", "and no verbose format reaches the original")
	assert.ErrorIs(t, err, ErrNotStarted)
	assert.ErrorIs(t, err, ErrUnusable)
	assert.Equal(t, 42, StartedProcess(err).PID, "the process a failed start left is still readable")

	var started *StartError
	require.True(t, errors.As(err, &started))
	assert.NotContains(t, started.Err.Error(), "test-token-not-real", "including the error it carries")
	assert.Nil(t, r.Err(nil))
}

func TestEveryLogRecordPassesThroughTheRule(t *testing.T) {
	var buf bytes.Buffer
	r := NewRedactor(Redaction{Secrets: []string{"test-token-not-real"}})
	log := slog.New(r.Handler(slog.NewJSONHandler(&buf, nil)))
	log = log.With("with", "test-token-not-real")
	log.WithGroup("g").Error("wrote test-token-not-real",
		"text", "test-token-not-real",
		"error", errors.New("test-token-not-real"),
		"any", []string{"test-token-not-real"},
		"count", 3)

	out := buf.String()
	assert.NotContains(t, out, "test-token-not-real")
	assert.Contains(t, out, `"count":3`, "numbers stay numbers")
}

// Card 19: a refusal an agent writes to stderr is followed by whatever it
// prints next, and the tail is only the last line. Lines keeps them all,
// bounded and sanitized.
func TestStderrLinesKeepARefusalTheDiagnosticsBury(t *testing.T) {
	r := NewRedactor(Redaction{Secrets: []string{"test-token-not-real"}})
	text := "refused: exec of /bin/rm (test-token-not-real)\nreading config\x07\n\nretrying in 2s\n"
	lines := r.Lines(text)
	require.Len(t, lines, 3, "the empty line is not one")
	assert.Contains(t, lines[0], "refused: exec of /bin/rm", "the refusal is still there, first")
	assert.NotContains(t, lines[0], "test-token-not-real", "and sanitized")
	assert.Equal(t, "reading config", lines[1], "control characters are stripped")
	assert.Equal(t, "retrying in 2s", lines[2])
	assert.Equal(t, "retrying in 2s", r.Stderr(text), "the tail is still the last line")

	many := make([]string, 0, maxStderrLines+20)
	for i := range maxStderrLines + 20 {
		many = append(many, fmt.Sprintf("line %d", i))
	}
	bounded := r.Lines(strings.Join(many, "\n"))
	assert.Len(t, bounded, maxStderrLines, "and the whole thing is bounded")
	assert.Equal(t, "line 69", bounded[len(bounded)-1], "keeping the newest")
	assert.LessOrEqual(t, len(r.Lines(strings.Repeat("z", 4000))[0]), maxStderr)
}

// Copilot on #738: Sanitize is the last thing every driver error and log
// field passes through, and the connector's stderr is a terminal. A field a
// worker chose — Claude's reported permission mode, a tool name — must not be
// able to move the cursor, repaint the screen or start an escape sequence
// there. Tab and newline stay: they are what a legitimate multi-line log
// field is made of, and neither drives a terminal.
func TestTheRuleTakesTerminalControlsOutOfEverythingItSanitizes(t *testing.T) {
	r := NewRedactor(Redaction{Secrets: []string{"test-token-not-real"}})

	out := r.Sanitize("mode \x1b[31;1mdanger\x1b[0m\a set")
	assert.NotContains(t, out, "\x1b", "an escape character never reaches a terminal")
	assert.NotContains(t, out, "\a", "nor a bell")
	assert.Contains(t, out, "danger", "and the text itself still reads")

	assert.NotContains(t, r.Sanitize("a2Kb"), "", "the C1 block is an escape sequence of its own")
	assert.NotContains(t, r.Sanitize("ab"), "", "DEL too")
	assert.NotContains(t, r.Sanitize("keep\roverwrite"), "\r", "a carriage return rewrites the line it is on")
	assert.Equal(t, "one\ttwo\nthree", r.Sanitize("one\ttwo\nthree"), "tab and newline are a log field's own")

	// A control character in the middle of a secret must not hide it from
	// the replacer, so the controls come out first.
	assert.NotContains(t, r.Sanitize("test-token\x1b-not-real"), "not-real")

	// The pattern rules hold for a caller with no redaction of its own, and
	// so does this one.
	assert.NotContains(t, (*Redactor)(nil).Sanitize("\x1b]0;title\a"), "\x1b")
}
