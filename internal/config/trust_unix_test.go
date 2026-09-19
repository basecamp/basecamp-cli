//go:build unix

package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// A path built from characters that can mean nothing to a shell is named
// bare in the trust warning. This package used to quote it anyway — its own
// copy of the quoting wrapped every value unconditionally — and the move to
// richtext.ShellQuote is what changed the message. Pinned here so the change
// is a decision and not a drift.
//
// The fixture is a relative path under a directory this test changes into,
// not a t.TempDir() path, because TMPDIR is the machine talking: a valid
// POSIX one may hold spaces, apostrophes or anything else, and on macOS /var
// resolves through a symlink. A quoting test rooted in it asserts something
// about the host rather than about the quoting, and would fail on a host
// that spells its temporary directory differently (Copilot on #769). The
// expectation below is therefore an exact string, the same on every machine
// this builds for.
//
// Unix-gated because the property is about POSIX paths: a Windows path
// carries backslashes, which are shell-active, so richtext.ShellQuote
// rightly quotes it there and there is nothing bare to assert. The
// apostrophe half of the same behavior is portable and lives next door in
// trust_test.go.
func TestTheTrustWarningLeavesAPathThatNeedsNoQuotingBare(t *testing.T) {
	t.Chdir(t.TempDir())
	const configPath = "repo/.basecamp/config.json"
	require.NoError(t, os.MkdirAll("repo/.basecamp", 0o755))
	require.NoError(t, os.WriteFile(configPath, []byte(`{"base_url": "https://evil.example.com"}`), 0o644))

	require.Equal(t, configPath, richtext.ShellQuote(configPath), "an inert path must come through unchanged")

	assert.Contains(t, captureStderr(t, func() {
		loadFromFile(Default(), configPath, SourceLocal, nil)
	}), "basecamp config trust repo/.basecamp/config.json`")
}
