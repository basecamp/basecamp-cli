//go:build unix

package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
)

// These tests put a profile in two config files, the global one and a
// trusted local one, and run the commands over config.Load as the CLI would:
// the layering is the thing under test, so no config is built by hand.

// The operator bound the account in the global config and runs setup again.
// A trusted local config that names the same profile on the same Basecamp,
// without an account, refines the global entry rather than replacing it,
// so the binding is seen.
func TestConnectSetupSeesTheGlobalBindingThroughALocalEntry(t *testing.T) {
	s := startConnectSetupServer(t)
	connectSetupApp(t, s, "agent")
	trustedLocalConfig(t, fmt.Sprintf(`{"profiles":{"agent":{"base_url":%q,"project_id":"42"}}}`, s.srv.URL))

	app := newConnectSetupApp(t, s, "agent")
	out, err := runConnectSetupCmd(t, app, "--operator", fmt.Sprint(setupOperatorPerson), routeArg(t))
	require.NoError(t, err, out)
	f, err := setup.Load(connectSetupPath(t, "agent"))
	require.NoError(t, err)
	assert.Equal(t, "999", f.AccountID)
	assert.Equal(t, "42", app.Config.Profiles["agent"].ProjectID, "the local entry's own fields still win")
}

// A local entry for another Basecamp replaces the global one whole, and the
// account bound there with it. Setup says which file hides the binding and
// what to change, rather than sending the operator to bind it again.
func TestConnectSetupNamesTheFileThatHidesTheGlobalBinding(t *testing.T) {
	s := startConnectSetupServer(t)
	connectSetupApp(t, s, "agent")
	local := trustedLocalConfig(t, fmt.Sprintf(`{"profiles":{"agent":{"base_url":%q}}}`, elsewhereBaseURL))

	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--operator", fmt.Sprint(setupOperatorPerson), routeArg(t))
	require.Error(t, err, out)
	assert.Contains(t, err.Error(), "not bound to an account")
	hint := hintOf(t, err)
	assert.Contains(t, hint, local, "the file that hides the binding")
	assert.Contains(t, hint, filepath.Join(config.GlobalConfigDir(), "config.json"), "the file whose binding is hidden")
	assert.NotContains(t, hint, "basecamp auth", "binding the global entry again changes nothing")
	assertNotWritten(t, "agent")
}

// The operator connects the agent under a profile whose global entry has no
// account yet, while a trusted local config names the same profile on the
// same Basecamp. The account the connection binds in the global entry is
// the one the next command sees.
func TestAuthAgentConnectBindsThroughALocalEntry(t *testing.T) {
	as := startConnectAS(t)
	app := connectApp(t, as, &config.Config{})
	writeGlobalConfigHere(t, fmt.Sprintf(`{"profiles":{"agent":{"base_url":%q}}}`, as.srv.URL))
	trustedLocalConfig(t, fmt.Sprintf(`{"profiles":{"agent":{"base_url":%q,"project_id":"42"}}}`, as.srv.URL))
	reloadApp(t, app, as.srv.URL, "agent")

	out, err := runAgentConnect(t, app, "--device-name", "build-box")
	require.NoError(t, err, out)

	reloadApp(t, app, as.srv.URL, "agent")
	assert.Equal(t, "999", app.Config.Profiles["agent"].AccountID, "the binding takes effect")
	assert.Equal(t, "42", app.Config.Profiles["agent"].ProjectID)
}

// A global entry already bound, under a same-Basecamp local entry with no
// account, is a bound profile: connecting the agent in that account again
// is not refused as unbindable.
func TestAuthAgentConnectAcceptsAGlobalBindingUnderALocalEntry(t *testing.T) {
	as := startConnectAS(t)
	app := connectApp(t, as, &config.Config{})
	writeGlobalConfigHere(t, fmt.Sprintf(`{"profiles":{"agent":{"base_url":%q,"account_id":"999"}}}`, as.srv.URL))
	trustedLocalConfig(t, fmt.Sprintf(`{"profiles":{"agent":{"base_url":%q}}}`, as.srv.URL))
	reloadApp(t, app, as.srv.URL, "agent")

	out, err := runAgentConnect(t, app, "--device-name", "build-box")
	require.NoError(t, err, out)
	_, err = app.Auth.GetStore().Load("profile:agent")
	require.NoError(t, err)
}

// Where the global entry is replaced whole by a local entry for another
// Basecamp, binding it would not take effect. The connection is refused
// before the operator approves anything, naming the file that hides it.
func TestAuthAgentConnectRefusesToBindWhereTheBindingIsHidden(t *testing.T) {
	as := startConnectAS(t)
	app := connectApp(t, as, &config.Config{})
	global := fmt.Sprintf(`{"profiles":{"agent":{"base_url":%q}}}`, elsewhereBaseURL)
	writeGlobalConfigHere(t, global)
	local := trustedLocalConfig(t, fmt.Sprintf(`{"profiles":{"agent":{"base_url":%q}}}`, as.srv.URL))
	reloadApp(t, app, as.srv.URL, "agent")

	out, err := runAgentConnect(t, app, "--device-name", "build-box")
	require.Error(t, err, out)
	hint := hintOf(t, err)
	assert.Contains(t, hint, local, "the file that hides the binding")
	assert.Contains(t, hint, filepath.Join(config.GlobalConfigDir(), "config.json"))
	assert.Empty(t, as.mints(), "refused before anything is approved")
	_, loadErr := app.Auth.GetStore().Load("profile:agent")
	assert.Error(t, loadErr, "nothing stored")
	data, readErr := os.ReadFile(filepath.Join(config.GlobalConfigDir(), "config.json"))
	require.NoError(t, readErr)
	assert.JSONEq(t, global, string(data), "the global entry is left as it was")
}

// A profile only a local config defines has no global entry to bind: the
// refusal names the file it comes from.
func TestAuthAgentConnectNamesTheFileDefiningALocalOnlyProfile(t *testing.T) {
	as := startConnectAS(t)
	app := connectApp(t, as, &config.Config{})
	writeGlobalConfigHere(t, `{}`)
	local := trustedLocalConfig(t, fmt.Sprintf(`{"profiles":{"agent":{"base_url":%q}}}`, as.srv.URL))
	reloadApp(t, app, as.srv.URL, "agent")

	out, err := runAgentConnect(t, app, "--device-name", "build-box")
	require.Error(t, err, out)
	assert.Contains(t, hintOf(t, err), local)
	assert.Empty(t, as.mints())
}

// A profile bound to another account refuses the connection, naming the
// file the account is bound in — the global config, seen through the local
// entry that refines it.
func TestAuthAgentConnectNamesTheFileAProfileIsBoundIn(t *testing.T) {
	as := startConnectAS(t)
	app := connectApp(t, as, &config.Config{})
	writeGlobalConfigHere(t, fmt.Sprintf(`{"profiles":{"agent":{"base_url":%q,"account_id":"123"}}}`, as.srv.URL))
	trustedLocalConfig(t, fmt.Sprintf(`{"profiles":{"agent":{"base_url":%q,"project_id":"42"}}}`, as.srv.URL))
	reloadApp(t, app, as.srv.URL, "agent")

	_, err := runAgentConnect(t, app, "--device-name", "build-box")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "bound to account 123 (bound in "+filepath.Join(config.GlobalConfigDir(), "config.json")+")")
}

// A global config that cannot be parsed was skipped by the loader, so what
// it recorded about the profile's files proves nothing. Both binding paths
// report the unusable file itself rather than telling the operator their
// entry comes from somewhere else.
func TestUnusableGlobalConfigIsReportedAsItself(t *testing.T) {
	as := startConnectAS(t)
	app := connectApp(t, as, &config.Config{})
	writeGlobalConfigHere(t, fmt.Sprintf(`{"profiles":{"agent":{"base_url":%q,"account_id":"999"`, as.srv.URL))
	trustedLocalConfig(t, fmt.Sprintf(`{"profiles":{"agent":{"base_url":%q}}}`, as.srv.URL))
	reloadApp(t, app, as.srv.URL, "agent")

	_, err := runAgentConnect(t, app, "--device-name", "build-box")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not valid JSON, refusing to rewrite it")
	assert.Empty(t, as.mints())

	in := strings.NewReader("bc_at_secret")
	_, err = runLogin(t, app, in, "--with-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not valid JSON, refusing to rewrite it")
	assert.NotContains(t, err.Error(), "not the global config")
	assert.Equal(t, 12, in.Len(), "refused before stdin is read")
}

// Setup reads the profile's account, so it asks the same question, and a
// global config it cannot parse is reported as that rather than as a
// profile defined somewhere else.
func TestConnectSetupReportsAnUnusableGlobalConfigAsItself(t *testing.T) {
	s := startConnectSetupServer(t)
	bareSetupApp(t, s, "agent")
	trustedLocalConfig(t, fmt.Sprintf(`{"profiles":{"agent":{"base_url":%q}}}`, s.srv.URL))
	for _, global := range []string{
		fmt.Sprintf(`{"profiles":{"agent":{"base_url":%q,"account_id":"999"`, s.srv.URL), // never closed
		`{"profiles":[]}`,   // a shape every writer refuses
		`{"profiles":null}`, // and so is null
	} {
		writeGlobalConfigHere(t, global)
		assertSetupReportsTheGlobalConfig(t, s)
	}
}

func assertSetupReportsTheGlobalConfig(t *testing.T, s *connectSetupServer) {
	t.Helper()
	out, err := runConnectSetupCmd(t, newConnectSetupApp(t, s, "agent"), "--operator", fmt.Sprint(setupOperatorPerson), routeArg(t))
	require.Error(t, err, out)
	hint := hintOf(t, err)
	assert.NotContains(t, hint, "not the global config", "the global config was skipped, so it is no evidence")
	assert.Contains(t, hint, "basecamp auth agent connect -P agent", "the generic remedy, which reports the file itself")
}
