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

// No command binds an account on its own. Connecting an Agent writes its
// account; a bot user's browser login writes none — `auth login --account`
// binds only on the headless paths — so a bot's remedy is its config entry,
// and naming `auth login` would send the operator round the same loop.
func TestUnboundProfileHintNamesOnlyWhatBinds(t *testing.T) {
	writeGlobalConfig(t, `{"profiles":{"agent":{"base_url":"https://3.basecampapi.com"}}}`)
	trustedLocalConfig(t, `{}`)

	hint := hintOf(t, unboundProfileError(loadConfig(t), "agent"))

	assert.Contains(t, hint, "basecamp auth agent connect -P agent")
	assert.Contains(t, hint, "add account_id to the profile's entry in "+filepath.Join(config.GlobalConfigDir(), "config.json"))
	assert.Contains(t, hint, "browser login binds none", "a headless login can bind, so the claim is about the browser")
	assert.NotContains(t, hint, "auth login", "a browser login binds no account")
}

// Storing the credential again cannot bind a profile whose entry comes from
// a system, repo or local config and not the global one: both login paths
// refuse it, because the global entry they would write is not part of the
// profile. Sending the operator there would be another command that fails
// on them, so the hint names the file the entry does come from.
func TestUnboundProfileHintNamesTheConfigFileWhenTheEntryIsNotGlobal(t *testing.T) {
	writeGlobalConfig(t, `{"profiles":{"other":{"account_id":"999"}}}`)
	local := trustedLocalConfig(t, `{"profiles":{"agent":{"base_url":"https://3.basecampapi.com"}}}`)

	hint := hintOf(t, unboundProfileError(loadConfig(t), "agent"))

	assert.Contains(t, hint, "Its entry comes from "+local+", not the global config")
	assert.Contains(t, hint, "Add account_id to the profile's entry there")
	assert.NotContains(t, hint, "basecamp auth", "a command that would refuse this profile is no remedy")
}

// loadConfig is the configuration the CLI would load from where the test
// stands.
func loadConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(config.FlagOverrides{})
	require.NoError(t, err)
	return cfg
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

// The file an unusable account comes from is the one to correct, and a
// global entry's account is not where a local entry's comes from.
func TestConnectAccountNamesTheFileOfAnUnusableAccount(t *testing.T) {
	writeGlobalConfig(t, `{"profiles":{"agent":{"base_url":"https://3.basecampapi.com","account_id":"999"}}}`)
	local := trustedLocalConfig(t, `{"profiles":{"agent":{"base_url":"https://3.basecampapi.com","account_id":"not-a-number"}}}`)

	_, err := connectAccount(&appctx.App{Config: loadConfig(t)}, "agent")

	require.Error(t, err)
	assert.Equal(t, "Correct account_id in the profile's entry in "+local+".", hintOf(t, err))
}

// The file that hid the global entry is the first after it on another
// Basecamp, even when a closer entry is back on the global entry's Basecamp
// and replaced that file in turn.
func TestGlobalBindingBlockerNamesTheFileThatReplacedTheGlobalEntry(t *testing.T) {
	global := config.ProfileLayer{Source: config.SourceGlobal, Path: "/g/config.json", BaseURL: "https://3.basecampapi.com"}
	repo := config.ProfileLayer{Source: config.SourceRepo, Path: "/r/config.json", BaseURL: "http://localhost:3000"}
	local := config.ProfileLayer{Source: config.SourceLocal, Path: "/l/config.json", BaseURL: "https://3.basecampapi.com/"}
	cfg := &config.Config{ProfileOrigins: map[string]*config.ProfileOrigin{
		"agent": {Replaced: []config.ProfileLayer{global, repo}, Layers: []config.ProfileLayer{local}},
	}}

	assert.Equal(t,
		"Its entry in /r/config.json is for http://localhost:3000, not https://3.basecampapi.com, so it replaces the global config's entry in /g/config.json and any account bound there. Add account_id to the profile's entry in /l/config.json",
		globalBindingBlocker(cfg, "agent"))
}
