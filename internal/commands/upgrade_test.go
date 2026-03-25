package commands

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/version"
)

func stubExecutablePathResolver(t *testing.T, path string, ok bool) {
	t.Helper()

	orig := executablePathResolver
	executablePathResolver = func() (string, bool) { return path, ok }
	t.Cleanup(func() { executablePathResolver = orig })
}

func stubScoopPrefixResolver(t *testing.T, resolve func(context.Context, string) (string, bool)) {
	t.Helper()

	orig := scoopPrefixResolver
	scoopPrefixResolver = resolve
	t.Cleanup(func() { scoopPrefixResolver = orig })
}

type upgradeCheckersStub struct {
	latestVersion   string
	isBrew          bool
	hasLegacyCask   bool
	isScoop         bool
	hasLegacyScoop  bool
	isGlobalScoop   bool
	homebrewUpgrade func(context.Context, io.Writer, io.Writer) error
	scoopUpgrade    func(context.Context, bool, io.Writer, io.Writer) error
}

// stubUpgradeCheckers overrides version and package manager helpers for tests.
func stubUpgradeCheckers(t *testing.T, stub upgradeCheckersStub) {
	t.Helper()

	origVC := versionChecker
	versionChecker = func() (string, error) { return stub.latestVersion, nil }
	t.Cleanup(func() { versionChecker = origVC })

	origHC := homebrewChecker
	homebrewChecker = func(context.Context) bool { return stub.isBrew }
	t.Cleanup(func() { homebrewChecker = origHC })

	origLegacy := legacyHomebrewCasker
	legacyHomebrewCasker = func(context.Context) bool { return stub.hasLegacyCask }
	t.Cleanup(func() { legacyHomebrewCasker = origLegacy })

	origHU := homebrewUpgrader
	homebrewUpgrader = stub.homebrewUpgrade
	if homebrewUpgrader == nil {
		homebrewUpgrader = func(context.Context, io.Writer, io.Writer) error { return nil }
	}
	t.Cleanup(func() { homebrewUpgrader = origHU })

	origSC := scoopChecker
	scoopChecker = func(context.Context) bool { return stub.isScoop }
	t.Cleanup(func() { scoopChecker = origSC })

	origLegacyScoop := legacyScoopChecker
	legacyScoopChecker = func(context.Context) bool { return stub.hasLegacyScoop }
	t.Cleanup(func() { legacyScoopChecker = origLegacyScoop })

	origGlobalScoop := scoopGlobalScopeChecker
	scoopGlobalScopeChecker = func(context.Context) bool { return stub.isGlobalScoop }
	t.Cleanup(func() { scoopGlobalScopeChecker = origGlobalScoop })

	origSU := scoopUpgrader
	scoopUpgrader = stub.scoopUpgrade
	if scoopUpgrader == nil {
		scoopUpgrader = func(context.Context, bool, io.Writer, io.Writer) error { return nil }
	}
	t.Cleanup(func() { scoopUpgrader = origSU })
}

// executeUpgradeCommand runs the upgrade command and returns the combined
// output captured from cmd.OutOrStdout().
func executeUpgradeCommand(t *testing.T, app *appctx.App) (cmdOut string, err error) {
	t.Helper()
	cmd := NewUpgradeCmd()
	cmd.SetArgs(nil)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)

	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(&bytes.Buffer{})

	err = cmd.Execute()
	return buf.String(), err
}

func TestUpgradeDevBuild(t *testing.T) {
	app, appBuf := setupPeopleTestApp(t)

	orig := version.Version
	version.Version = "dev"
	t.Cleanup(func() { version.Version = orig })

	_, err := executeUpgradeCommand(t, app)
	require.NoError(t, err)
	// app.OK() routes through the output writer, not cmd stdout
	assert.Contains(t, appBuf.String(), "Development build")
}

func TestUpgradeAlreadyCurrent(t *testing.T) {
	app, appBuf := setupPeopleTestApp(t)

	orig := version.Version
	version.Version = "1.2.3"
	t.Cleanup(func() { version.Version = orig })

	stubUpgradeCheckers(t, upgradeCheckersStub{latestVersion: "1.2.3"})

	cmdOut, err := executeUpgradeCommand(t, app)
	require.NoError(t, err)
	assert.Contains(t, cmdOut, "already up to date")
	assert.Contains(t, appBuf.String(), "up_to_date")
}

func TestUpgradeAvailable(t *testing.T) {
	app, appBuf := setupPeopleTestApp(t)

	orig := version.Version
	version.Version = "1.2.3"
	t.Cleanup(func() { version.Version = orig })

	stubUpgradeCheckers(t, upgradeCheckersStub{latestVersion: "1.3.0"})

	cmdOut, err := executeUpgradeCommand(t, app)
	require.NoError(t, err)
	assert.Contains(t, cmdOut, "update available: 1.3.0")
	assert.Contains(t, appBuf.String(), "releases/tag/v1.3.0")
}

func TestUpgradeSuppressesOlderLatestRelease(t *testing.T) {
	app, appBuf := setupPeopleTestApp(t)

	orig := version.Version
	version.Version = "0.4.1-0.20260313174735-243815fa23b2"
	t.Cleanup(func() { version.Version = orig })

	stubUpgradeCheckers(t, upgradeCheckersStub{latestVersion: "0.4.0"})

	cmdOut, err := executeUpgradeCommand(t, app)
	require.NoError(t, err)
	assert.Contains(t, cmdOut, "already up to date")
	assert.Contains(t, appBuf.String(), "up_to_date")
	assert.NotContains(t, cmdOut, "update available")
}

// TestUpgradeOutputGoesToWriter verifies output uses cmd.OutOrStdout(), not os.Stdout.
func TestUpgradeOutputGoesToWriter(t *testing.T) {
	app, _ := setupPeopleTestApp(t)

	orig := version.Version
	version.Version = "1.0.0"
	t.Cleanup(func() { version.Version = orig })

	stubUpgradeCheckers(t, upgradeCheckersStub{latestVersion: "1.0.0"})

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

	// Progressive output should be captured in our buffer, not leaked to os.Stdout
	assert.Contains(t, buf.String(), "Current version: 1.0.0")
	assert.Contains(t, buf.String(), "already up to date")
}

func TestUpgradePrefersRenamedHomebrewCaskOverLegacyMigration(t *testing.T) {
	app, appBuf := setupPeopleTestApp(t)

	orig := version.Version
	version.Version = "1.2.3"
	t.Cleanup(func() { version.Version = orig })

	stubUpgradeCheckers(t, upgradeCheckersStub{latestVersion: "1.3.0", isBrew: true, hasLegacyCask: true})

	cmdOut, err := executeUpgradeCommand(t, app)
	require.NoError(t, err)
	assert.Contains(t, cmdOut, "Upgrading via Homebrew…")
	assert.Contains(t, appBuf.String(), "upgraded")
	assert.NotContains(t, appBuf.String(), "migration_required")
}

func TestUpgradeLegacyCaskMigrationInstructions(t *testing.T) {
	app, appBuf := setupPeopleTestApp(t)

	orig := version.Version
	version.Version = "1.2.3"
	t.Cleanup(func() { version.Version = orig })

	stubUpgradeCheckers(t, upgradeCheckersStub{latestVersion: "1.3.0", hasLegacyCask: true})

	cmdOut, err := executeUpgradeCommand(t, app)
	require.NoError(t, err)
	assert.Contains(t, cmdOut, "The CLI cask has been renamed. To upgrade, run:")
	assert.Contains(t, cmdOut, "  brew uninstall --cask basecamp/tap/basecamp\n")
	assert.Contains(t, cmdOut, "  brew install --cask basecamp/tap/basecamp-cli\n")
	assert.Contains(t, appBuf.String(), "migration_required")
}

func TestUpgradePrefersRenamedScoopAppOverLegacyMigration(t *testing.T) {
	app, appBuf := setupPeopleTestApp(t)

	orig := version.Version
	version.Version = "1.2.3"
	t.Cleanup(func() { version.Version = orig })

	stubUpgradeCheckers(t, upgradeCheckersStub{latestVersion: "1.3.0", isScoop: true, hasLegacyScoop: true})

	cmdOut, err := executeUpgradeCommand(t, app)
	require.NoError(t, err)
	assert.Contains(t, cmdOut, "Upgrading via Scoop…")
	assert.Contains(t, appBuf.String(), "upgraded")
	assert.NotContains(t, appBuf.String(), "migration_required")
}

func TestUpgradeLegacyScoopMigrationInstructions(t *testing.T) {
	app, appBuf := setupPeopleTestApp(t)

	orig := version.Version
	version.Version = "1.2.3"
	t.Cleanup(func() { version.Version = orig })

	stubUpgradeCheckers(t, upgradeCheckersStub{latestVersion: "1.3.0", hasLegacyScoop: true})

	cmdOut, err := executeUpgradeCommand(t, app)
	require.NoError(t, err)
	assert.Contains(t, cmdOut, "The CLI Scoop manifest has been renamed. To upgrade, run:")
	assert.Contains(t, cmdOut, "  scoop uninstall basecamp\n")
	assert.Contains(t, cmdOut, "  scoop install basecamp-cli\n")
	assert.Contains(t, appBuf.String(), "migration_required")
}

func TestUpgradeGlobalScoopUsesGlobalUpdate(t *testing.T) {
	app, appBuf := setupPeopleTestApp(t)

	orig := version.Version
	version.Version = "1.2.3"
	t.Cleanup(func() { version.Version = orig })

	var gotGlobal bool
	stubUpgradeCheckers(t, upgradeCheckersStub{
		latestVersion: "1.3.0",
		isScoop:       true,
		isGlobalScoop: true,
		scoopUpgrade: func(_ context.Context, global bool, _ io.Writer, _ io.Writer) error {
			gotGlobal = global
			return nil
		},
	})

	_, err := executeUpgradeCommand(t, app)
	require.NoError(t, err)
	assert.True(t, gotGlobal)
	assert.Contains(t, appBuf.String(), "upgraded")
}

func TestUpgradeGlobalLegacyScoopMigrationInstructions(t *testing.T) {
	app, appBuf := setupPeopleTestApp(t)

	orig := version.Version
	version.Version = "1.2.3"
	t.Cleanup(func() { version.Version = orig })

	stubUpgradeCheckers(t, upgradeCheckersStub{latestVersion: "1.3.0", hasLegacyScoop: true, isGlobalScoop: true})

	cmdOut, err := executeUpgradeCommand(t, app)
	require.NoError(t, err)
	assert.Contains(t, cmdOut, "The CLI Scoop manifest has been renamed. To upgrade, run:")
	assert.Contains(t, cmdOut, "  scoop uninstall -g basecamp\n")
	assert.Contains(t, cmdOut, "  scoop install -g basecamp-cli\n")
	assert.Contains(t, appBuf.String(), "migration_required")
}

func TestIsHomebrewUsesExecutablePathProvenance(t *testing.T) {
	stubExecutablePathResolver(t, "/opt/homebrew/caskroom/basecamp-cli/1.2.3/basecamp", true)
	assert.True(t, isHomebrew(context.Background()))

	stubExecutablePathResolver(t, "/usr/local/bin/basecamp", true)
	assert.False(t, isHomebrew(context.Background()))
}

func TestIsHomebrewReturnsFalseWhenExecutablePathUnavailable(t *testing.T) {
	stubExecutablePathResolver(t, "", false)
	assert.False(t, isHomebrew(context.Background()))
}

func TestHasLegacyHomebrewCaskUsesExecutablePathProvenance(t *testing.T) {
	stubExecutablePathResolver(t, "/opt/homebrew/caskroom/basecamp/1.2.3/basecamp", true)
	assert.True(t, hasLegacyHomebrewCask(context.Background()))

	stubExecutablePathResolver(t, "/usr/local/bin/basecamp", true)
	assert.False(t, hasLegacyHomebrewCask(context.Background()))
}

func TestHasLegacyHomebrewCaskReturnsFalseWhenExecutablePathUnavailable(t *testing.T) {
	stubExecutablePathResolver(t, "", false)
	assert.False(t, hasLegacyHomebrewCask(context.Background()))
}

func TestIsScoopUsesExecutablePathProvenance(t *testing.T) {
	stubExecutablePathResolver(t, "/Users/alice/scoop/apps/basecamp-cli/current/basecamp.exe", true)
	assert.True(t, isScoop(context.Background()))

	stubExecutablePathResolver(t, "/Users/alice/bin/basecamp", true)
	assert.False(t, isScoop(context.Background()))
}

func TestIsScoopDetectsRenamedShimViaPrefix(t *testing.T) {
	stubExecutablePathResolver(t, "/Users/alice/scoop/shims/basecamp.exe", true)
	stubScoopPrefixResolver(t, func(_ context.Context, app string) (string, bool) {
		if app == scoopApp {
			return "/users/alice/scoop/apps/basecamp-cli/current", true
		}

		return "", false
	})

	assert.True(t, isScoop(context.Background()))
}

func TestIsScoopDetectsGlobalRenamedShimViaPrefix(t *testing.T) {
	stubExecutablePathResolver(t, "c:/programdata/scoop/shims/basecamp.exe", true)
	stubScoopPrefixResolver(t, func(_ context.Context, app string) (string, bool) {
		if app == scoopApp {
			return "/programdata/scoop/apps/basecamp-cli/current", true
		}

		return "", false
	})

	assert.True(t, isScoop(context.Background()))
}

func TestHasLegacyScoopUsesExecutablePathProvenance(t *testing.T) {
	stubExecutablePathResolver(t, "/Users/alice/scoop/apps/basecamp/current/basecamp.exe", true)
	assert.True(t, hasLegacyScoop(context.Background()))

	stubExecutablePathResolver(t, "/Users/alice/bin/basecamp", true)
	assert.False(t, hasLegacyScoop(context.Background()))
}

func TestHasLegacyScoopDetectsLegacyShimViaPrefix(t *testing.T) {
	stubExecutablePathResolver(t, "/Users/alice/scoop/shims/basecamp.exe", true)
	stubScoopPrefixResolver(t, func(_ context.Context, app string) (string, bool) {
		if app == legacyScoopApp {
			return "/users/alice/scoop/apps/basecamp/current", true
		}

		return "", false
	})

	assert.True(t, hasLegacyScoop(context.Background()))
}

func TestIsScoopShimIgnoresOppositeScopePrefix(t *testing.T) {
	stubExecutablePathResolver(t, "c:/programdata/scoop/shims/basecamp.exe", true)
	stubScoopPrefixResolver(t, func(_ context.Context, app string) (string, bool) {
		switch app {
		case scoopApp:
			return "/users/alice/scoop/apps/basecamp-cli/current", true
		case legacyScoopApp:
			return "/programdata/scoop/apps/basecamp/current", true
		default:
			return "", false
		}
	})

	assert.False(t, isScoop(context.Background()))
	assert.True(t, hasLegacyScoop(context.Background()))
}

func TestHasLegacyScoopShimIgnoresOppositeScopePrefix(t *testing.T) {
	stubExecutablePathResolver(t, "/users/alice/scoop/shims/basecamp.exe", true)
	stubScoopPrefixResolver(t, func(_ context.Context, app string) (string, bool) {
		switch app {
		case scoopApp:
			return "/programdata/scoop/apps/basecamp-cli/current", true
		case legacyScoopApp:
			return "/users/alice/scoop/apps/basecamp/current", true
		default:
			return "", false
		}
	})

	assert.False(t, isScoop(context.Background()))
	assert.True(t, hasLegacyScoop(context.Background()))
}

func TestIsGlobalScoopInstallUsesExecutablePathProvenance(t *testing.T) {
	stubExecutablePathResolver(t, "c:/programdata/scoop/apps/basecamp-cli/current/basecamp.exe", true)
	assert.True(t, isGlobalScoopInstall(context.Background()))

	stubExecutablePathResolver(t, "/users/alice/programdata/scoop/apps/basecamp-cli/current/basecamp.exe", true)
	assert.False(t, isGlobalScoopInstall(context.Background()))

	stubExecutablePathResolver(t, "/Users/alice/scoop/apps/basecamp-cli/current/basecamp.exe", true)
	assert.False(t, isGlobalScoopInstall(context.Background()))
}
