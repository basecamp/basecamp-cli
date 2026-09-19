//go:build unix

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// A POSIX path is built from characters that can mean nothing to a shell, so
// the trust warning names it bare. This package used to quote it anyway —
// its own copy of the quoting wrapped every value unconditionally — and the
// move to richtext.ShellQuote is what changed the message. Pinned here so
// the change is a decision and not a drift.
//
// Unix-gated because the property is about POSIX paths: a Windows path
// carries backslashes, which are shell-active, so `richtext.ShellQuote`
// rightly quotes it there and there is nothing bare to assert. The
// apostrophe half of the same behavior is portable and lives next door in
// trust_test.go.
func TestTheTrustWarningLeavesAPathThatNeedsNoQuotingBare(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plain")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	configPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"base_url": "https://evil.example.com"}`), 0o644))
	require.NotContains(t, configPath, "'", "the temp path itself must need no quoting")

	require.Equal(t, configPath, richtext.ShellQuote(configPath), "an inert path must come through unchanged")

	want := "basecamp config trust " + configPath + "`"
	require.NotContains(t, want, "'", "and so the warning names it unquoted")

	assert.Contains(t, captureStderr(t, func() {
		loadFromFile(Default(), configPath, SourceLocal, nil)
	}), want)
}
