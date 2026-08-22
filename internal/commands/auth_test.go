package commands

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// nudgeOutput runs printAgentNudge against a non-styled renderer and returns
// the plain text written for the login hint.
func nudgeOutput(t *testing.T) string {
	t.Helper()
	buf := &bytes.Buffer{}
	printAgentNudge(buf, output.NewRenderer(io.Discard, false))
	return buf.String()
}

// TestPrintAgentNudgeNoneDetected: no detected agent → no nudge.
func TestPrintAgentNudgeNoneDetected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", filepath.Join(home, "empty-bin"))

	assert.Empty(t, nudgeOutput(t))
}

// TestPrintAgentNudgeSingle: one detected-unhealthy agent → its `setup <id>`.
func TestPrintAgentNudgeSingle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", filepath.Join(home, "empty-bin"))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".codex"), 0o755))

	out := nudgeOutput(t)

	assert.Contains(t, out, "Codex detected")
	assert.Contains(t, out, "basecamp setup codex")
	assert.NotContains(t, out, "basecamp setup claude")
}

// TestPrintAgentNudgeMultiple: ≥2 detected-unhealthy → every choice printed
// directly (never Claude-first, never routed to `setup agents`).
func TestPrintAgentNudgeMultiple(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", filepath.Join(home, "empty-bin"))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".codex"), 0o755))

	out := nudgeOutput(t)

	assert.Contains(t, out, "Multiple coding agents detected")
	assert.Contains(t, out, "basecamp setup claude")
	assert.Contains(t, out, "basecamp setup codex")
	assert.NotContains(t, out, "basecamp setup agents")
}
