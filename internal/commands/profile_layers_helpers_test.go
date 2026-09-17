package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// elsewhereBaseURL is a Basecamp other than the mock servers'.
const elsewhereBaseURL = "https://elsewhere.example"

// trustedLocalConfig makes a fresh directory the working directory, with a
// local config holding contents that `basecamp config trust` has trusted.
// HOME (USERPROFILE on Windows) points away from it, so no repo config is found above it. It
// returns the local config's path.
func trustedLocalConfig(t *testing.T, contents string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := t.TempDir()
	path := filepath.Join(dir, ".basecamp", "config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	require.NoError(t, config.NewTrustStore(config.GlobalConfigDir()).Trust(path))
	t.Chdir(dir)
	return path
}

// hintOf is the hint an error carries for the operator to act on.
func hintOf(t *testing.T, err error) string {
	t.Helper()
	var e *output.Error
	require.ErrorAs(t, err, &e)
	return e.Hint
}

// writeGlobalConfigHere writes the global config file under the
// XDG_CONFIG_HOME already set.
func writeGlobalConfigHere(t *testing.T, contents string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(config.GlobalConfigDir(), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(config.GlobalConfigDir(), "config.json"), []byte(contents), 0o600))
}

// reloadApp replaces app's config with what config.Load reads
// now, the way the next invocation of the CLI would see it.
func reloadApp(t *testing.T, app *appctx.App, baseURL, profile string) {
	t.Helper()
	cfg, err := config.Load(config.FlagOverrides{})
	require.NoError(t, err)
	cfg.BaseURL = baseURL
	if _, ok := cfg.Profiles[profile]; ok {
		require.NoError(t, cfg.ApplyProfile(profile))
	} else {
		cfg.ActiveProfile = profile
	}
	*app.Config = *cfg
}
