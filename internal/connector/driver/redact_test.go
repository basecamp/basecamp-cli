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
