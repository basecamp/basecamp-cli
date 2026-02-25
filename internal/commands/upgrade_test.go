package commands

import (
	"bytes"
	"context"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/version"
)

// stubUpgradeCheckers overrides versionChecker and homebrewChecker for tests.
func stubUpgradeCheckers(t *testing.T, latestVersion string, isBrew bool) {
	t.Helper()

	origVC := versionChecker
	versionChecker = func() (string, error) { return latestVersion, nil }
	t.Cleanup(func() { versionChecker = origVC })

	origHC := homebrewChecker
	homebrewChecker = func(context.Context) bool { return isBrew }
	t.Cleanup(func() { homebrewChecker = origHC })
}

func executeUpgradeCommand(t *testing.T, app *appctx.App) (string, error) {
	t.Helper()
	cmd := NewUpgradeCmd()
	cmd.SetArgs(nil)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)

	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	return buf.String(), err
}

func TestUpgradeDevBuild(t *testing.T) {
	app, _ := setupPeopleTestApp(t)

	orig := version.Version
	version.Version = "dev"
	t.Cleanup(func() { version.Version = orig })

	out, err := executeUpgradeCommand(t, app)
	require.NoError(t, err)
	assert.Contains(t, out, "Development build")
}

func TestUpgradeAlreadyCurrent(t *testing.T) {
	app, _ := setupPeopleTestApp(t)

	orig := version.Version
	version.Version = "1.2.3"
	t.Cleanup(func() { version.Version = orig })

	stubUpgradeCheckers(t, "1.2.3", false)

	out, err := executeUpgradeCommand(t, app)
	require.NoError(t, err)
	assert.Contains(t, out, "already up to date")
}

func TestUpgradeAvailable(t *testing.T) {
	app, _ := setupPeopleTestApp(t)

	orig := version.Version
	version.Version = "1.2.3"
	t.Cleanup(func() { version.Version = orig })

	stubUpgradeCheckers(t, "1.3.0", false)

	out, err := executeUpgradeCommand(t, app)
	require.NoError(t, err)
	assert.Contains(t, out, "update available: 1.3.0")
	assert.Contains(t, out, "releases/tag/v1.3.0")
}

// TestUpgradeOutputGoesToWriter verifies output uses cmd.OutOrStdout(), not os.Stdout.
func TestUpgradeOutputGoesToWriter(t *testing.T) {
	app, _ := setupPeopleTestApp(t)

	orig := version.Version
	version.Version = "1.0.0"
	t.Cleanup(func() { version.Version = orig })

	stubUpgradeCheckers(t, "1.0.0", false)

	cmd := NewUpgradeCmd()
	cmd.SetArgs(nil)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)

	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(&bytes.Buffer{})

	// Wrap in a parent to ensure OutOrStdout resolves to our buffer
	root := &cobra.Command{Use: "test"}
	root.AddCommand(cmd)
	root.SetOut(buf)
	root.SetArgs([]string{"upgrade"})
	root.SetContext(ctx)

	err := root.Execute()
	require.NoError(t, err)

	// All output should be captured in our buffer, not leaked to os.Stdout
	assert.Contains(t, buf.String(), "Current version: 1.0.0")
	assert.Contains(t, buf.String(), "already up to date")
}
