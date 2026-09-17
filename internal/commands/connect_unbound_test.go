//go:build unix

package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// writeGlobalConfig puts a config file where GlobalConfigDir will find it,
// and returns nothing: the test reads the hint, not the file.
func writeGlobalConfig(t *testing.T, contents string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	dir := filepath.Join(home, "basecamp")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(contents), 0o600))
}

// hintOf is the hint an error carries for the operator to act on.
func hintOf(t *testing.T, err error) string {
	t.Helper()
	var e *output.Error
	require.ErrorAs(t, err, &e)
	return e.Hint
}

// No command binds an account on its own. Connecting an Agent writes its
// account; a bot user's browser login writes none — `auth login --account`
// binds only on the headless paths — so a bot's remedy is its config entry,
// and naming `auth login` would send the operator round the same loop.
func TestUnboundProfileHintNamesOnlyWhatBinds(t *testing.T) {
	writeGlobalConfig(t, `{"profiles":{"agent":{"base_url":"https://3.basecampapi.com"}}}`)

	hint := hintOf(t, unboundProfileError("agent"))

	assert.Contains(t, hint, "basecamp auth agent connect -P agent")
	assert.Contains(t, hint, "add account_id to the profile's entry in "+filepath.Join(config.GlobalConfigDir(), "config.json"))
	assert.Contains(t, hint, "browser login binds none", "a headless login can bind, so the claim is about the browser")
	assert.NotContains(t, hint, "auth login", "a browser login binds no account")
}

// Storing the credential again cannot bind a profile whose accountless entry
// comes from a system, repo or local config: both login paths refuse it,
// because the global entry they would write stays shadowed. Sending the
// operator there would be another command that fails on them.
func TestUnboundProfileHintNamesTheConfigFileWhenTheEntryIsNotGlobal(t *testing.T) {
	writeGlobalConfig(t, `{"profiles":{"other":{"account_id":"999"}}}`)

	hint := hintOf(t, unboundProfileError("agent"))

	assert.Contains(t, hint, "account_id to the config file that defines it")
	assert.NotContains(t, hint, "basecamp auth", "a command that would refuse this profile is no remedy")
}

// A profile that names an account nothing can parse is not an unbound one,
// and calling it unbound sends the operator to bind an account it already
// has. The account it does name is what is wrong.
func TestConnectAccountReportsAnUnusableAccountAsItself(t *testing.T) {
	app := &appctx.App{Config: &config.Config{
		Profiles: map[string]*config.ProfileConfig{"agent": {AccountID: "not-a-number"}},
	}}

	_, err := connectAccount(app, "agent")

	require.Error(t, err)
	assert.Contains(t, err.Error(), `Profile "agent" names account "not-a-number"`)
	assert.NotContains(t, err.Error(), "not bound")
	assert.Contains(t, hintOf(t, err), "Correct account_id")
}
