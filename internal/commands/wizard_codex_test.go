package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

func TestNewSetupCmdHasCodexSubcommand(t *testing.T) {
	cmd := NewSetupCmd()

	assert.NotNil(t, findSubcommand(cmd, "codex"))
}

func findSubcommand(cmd interface{ Commands() []*cobra.Command }, name string) *cobra.Command {
	for _, child := range cmd.Commands() {
		if child.Name() == name {
			return child
		}
	}
	return nil
}

func TestSetupCodexFreshInstallCommandOrder(t *testing.T) {
	logPath := installCodexStub(t, codexStubOptions{})

	envelope := runSetupCodexJSON(t)

	assert.True(t, envelope.Data.PluginInstalled)
	assert.True(t, envelope.Data.AgentDetected)
	assert.Empty(t, envelope.Data.Errors)
	calls := readCodexSetupCalls(t, logPath)
	assertCallOrder(t, calls,
		"plugin marketplace add basecamp/claude-plugins --json",
		"plugin add basecamp@37signals --json",
		"plugin list --available --json",
	)
}

func TestSetupCodexAlreadyAddedRefreshesMarketplace(t *testing.T) {
	logPath := installCodexStub(t, codexStubOptions{marketplaceAlreadyAdded: true})

	envelope := runSetupCodexJSON(t)

	assert.True(t, envelope.Data.PluginInstalled)
	calls := readCodexSetupCalls(t, logPath)
	assertCallOrder(t, calls,
		"plugin marketplace add basecamp/claude-plugins --json",
		"plugin marketplace upgrade 37signals --json",
		"plugin add basecamp@37signals --json",
	)
}

func TestSetupCodexIdempotentAddRefreshesMarketplace(t *testing.T) {
	logPath := installCodexStub(t, codexStubOptions{marketplaceAlreadyAddedSuccess: true})

	envelope := runSetupCodexJSON(t)

	assert.True(t, envelope.Data.PluginInstalled)
	calls := readCodexSetupCalls(t, logPath)
	assertCallOrder(t, calls,
		"plugin marketplace add basecamp/claude-plugins --json",
		"plugin marketplace upgrade 37signals --json",
		"plugin add basecamp@37signals --json",
	)
}

func TestSetupCodexAlreadyInstalledIsIdempotent(t *testing.T) {
	logPath := installCodexStub(t, codexStubOptions{pluginAlreadyInstalled: true})

	envelope := runSetupCodexJSON(t)

	assert.True(t, envelope.Data.PluginInstalled)
	assert.Empty(t, envelope.Data.Errors)
	assert.Contains(t, readCodexSetupCalls(t, logPath), "plugin add basecamp@37signals --json")
}

func TestSetupCodexMissingBinaryReturnsManualCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home)

	envelope := runSetupCodexJSON(t)

	assert.False(t, envelope.Data.AgentDetected)
	assert.False(t, envelope.Data.PluginInstalled)
	require.NotEmpty(t, envelope.Data.Errors)
	assert.Contains(t, envelope.Data.Errors[0], "Codex executable not found")
	assert.Equal(t, []string{
		"codex plugin marketplace add basecamp/claude-plugins",
		"codex plugin marketplace upgrade 37signals",
		"codex plugin add basecamp@37signals",
	}, envelope.Data.ManualCommands)

	breadcrumbCmds := make([]string, 0, len(envelope.Breadcrumbs))
	for _, breadcrumb := range envelope.Breadcrumbs {
		breadcrumbCmds = append(breadcrumbCmds, breadcrumb.Cmd)
	}
	assert.Contains(t, breadcrumbCmds, "basecamp doctor")
	for _, manual := range envelope.Data.ManualCommands {
		assert.Contains(t, breadcrumbCmds, manual)
	}
}

func TestSetupCodexMarketplaceFailureStopsInstall(t *testing.T) {
	logPath := installCodexStub(t, codexStubOptions{marketplaceFailure: true})

	envelope := runSetupCodexJSON(t)

	assert.False(t, envelope.Data.PluginInstalled)
	require.NotEmpty(t, envelope.Data.Errors)
	assert.Contains(t, envelope.Data.Errors[0], "marketplace add")
	assert.NotEmpty(t, envelope.Data.ManualCommands)
	assert.NotContains(t, readCodexSetupCalls(t, logPath), "plugin add basecamp@37signals --json")
}

func TestSetupCodexPluginFailureReturnsStructuredError(t *testing.T) {
	installCodexStub(t, codexStubOptions{pluginFailure: true})

	envelope := runSetupCodexJSON(t)

	assert.False(t, envelope.Data.PluginInstalled)
	require.NotEmpty(t, envelope.Data.Errors)
	assert.Contains(t, envelope.Data.Errors[0], "plugin add")
}

func TestSetupCodexFailedVerificationReturnsStructuredError(t *testing.T) {
	installCodexStub(t, codexStubOptions{verificationMissing: true})

	envelope := runSetupCodexJSON(t)

	assert.False(t, envelope.Data.PluginInstalled)
	require.NotEmpty(t, envelope.Data.Errors)
	assert.Contains(t, envelope.Data.Errors[0], "verification")
}

func TestRunCodexSetupInteractiveExplainsNextSteps(t *testing.T) {
	installCodexStub(t, codexStubOptions{})
	cmd := &cobra.Command{}
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetContext(context.Background())
	styles := tui.NewStylesWithTheme(tui.ResolveTheme(false))

	require.NoError(t, runCodexSetup(cmd, styles))

	assert.Contains(t, output.String(), "Registering 37signals marketplace")
	assert.Contains(t, output.String(), "Installing basecamp plugin")
	assert.Contains(t, output.String(), "Start a new Codex thread")
	// No hooks ship yet — setup must not point users at /hooks trust.
	assert.NotContains(t, output.String(), "/hooks")
}

func TestRunCodexSetupInteractiveFailureWarnsAndContinues(t *testing.T) {
	installCodexStub(t, codexStubOptions{marketplaceFailure: true})
	cmd := &cobra.Command{}
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetContext(context.Background())
	styles := tui.NewStylesWithTheme(tui.ResolveTheme(false))

	require.NoError(t, runCodexSetup(cmd, styles), "codex setup failure must not abort the wizard")

	assert.Contains(t, output.String(), "Codex plugin setup failed")
	assert.Contains(t, output.String(), "codex plugin marketplace add basecamp/claude-plugins")
	assert.Contains(t, output.String(), "codex plugin add basecamp@37signals")
	assert.Contains(t, output.String(), "basecamp doctor")
}

func TestInstallCodexPluginReturnsStructuredError(t *testing.T) {
	installCodexStub(t, codexStubOptions{marketplaceFailure: true})

	err := installCodexPlugin(context.Background(), nil, func(string) {})

	var setupErr *agentSetupError
	require.ErrorAs(t, err, &setupErr)
	assert.Contains(t, setupErr.Summary, "marketplace add failed")
	assert.Equal(t, []string{
		"codex plugin marketplace add basecamp/claude-plugins",
		"codex plugin marketplace upgrade 37signals",
		"codex plugin add basecamp@37signals",
	}, setupErr.Manual)
}

type setupCodexEnvelope struct {
	Summary string `json:"summary"`
	Data    struct {
		PluginInstalled bool     `json:"plugin_installed"`
		AgentDetected   bool     `json:"agent_detected"`
		Errors          []string `json:"errors"`
		ManualCommands  []string `json:"manual_commands"`
	} `json:"data"`
	Breadcrumbs []struct {
		Action string `json:"action"`
		Cmd    string `json:"cmd"`
	} `json:"breadcrumbs"`
}

func runSetupCodexJSON(t *testing.T) setupCodexEnvelope {
	t.Helper()
	app, output := setupQuickstartTestApp(t, "", "")
	app.Flags.JSON = true
	app.Flags.Hints = true
	t.Cleanup(app.Close)

	cmd := NewSetupCmd()
	cmd.SetArgs([]string{"codex"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	require.NoError(t, cmd.Execute())

	var envelope setupCodexEnvelope
	require.NoError(t, json.Unmarshal(output.Bytes(), &envelope), output.String())
	return envelope
}

type codexStubOptions struct {
	marketplaceAlreadyAdded        bool
	marketplaceAlreadyAddedSuccess bool
	marketplaceFailure             bool
	pluginAlreadyInstalled         bool
	pluginFailure                  bool
	verificationMissing            bool
}

func installCodexStub(t *testing.T, options codexStubOptions) string {
	t.Helper()
	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	statePath := filepath.Join(home, "installed")
	if options.pluginAlreadyInstalled {
		require.NoError(t, os.WriteFile(statePath, []byte("installed"), 0o644))
	}
	logPath := filepath.Join(home, "codex-calls.log")
	boolShell := func(value bool) string {
		if value {
			return "1"
		}
		return "0"
	}
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> \"" + logPath + "\"\n" +
		"case \"$*\" in\n" +
		"  \"plugin marketplace add basecamp/claude-plugins --json\")\n" +
		"    if [ " + boolShell(options.marketplaceFailure) + " = 1 ]; then echo 'network failure' >&2; exit 1; fi\n" +
		"    if [ " + boolShell(options.marketplaceAlreadyAdded) + " = 1 ]; then echo 'marketplace 37signals already registered' >&2; exit 1; fi\n" +
		"    if [ " + boolShell(options.marketplaceAlreadyAddedSuccess) + " = 1 ]; then echo '{\"marketplaceName\":\"37signals\",\"alreadyAdded\":true}'; exit 0; fi\n" +
		"    echo '{\"name\":\"37signals\"}'; exit 0 ;;\n" +
		"  \"plugin marketplace upgrade 37signals --json\") echo '{\"name\":\"37signals\"}'; exit 0 ;;\n" +
		"  \"plugin add basecamp@37signals --json\")\n" +
		"    if [ " + boolShell(options.pluginFailure) + " = 1 ]; then echo 'plugin failure' >&2; exit 1; fi\n" +
		"    : > \"" + statePath + "\"; echo '{\"installed\":true}'; exit 0 ;;\n" +
		"  \"plugin list --available --json\")\n" +
		"    if [ " + boolShell(options.verificationMissing) + " = 1 ]; then echo '{\"installed\":[],\"available\":[{\"pluginId\":\"basecamp@37signals\",\"version\":\"0.7.2\",\"installed\":false,\"enabled\":false}]}'; exit 0; fi\n" +
		"    if [ -f \"" + statePath + "\" ]; then echo '{\"installed\":[{\"pluginId\":\"basecamp@37signals\",\"version\":\"0.7.2\",\"installed\":true,\"enabled\":true}],\"available\":[]}'; else echo '{\"installed\":[],\"available\":[]}'; fi; exit 0 ;;\n" +
		"  *) echo 'unexpected command' >&2; exit 1 ;;\n" +
		"esac\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "codex"), []byte(script), 0o755)) //nolint:gosec // test executable
	t.Setenv("HOME", home)
	t.Setenv("PATH", binDir)
	return logPath
}

func readCodexSetupCalls(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func assertCallOrder(t *testing.T, calls string, expected ...string) {
	t.Helper()
	position := -1
	for _, call := range expected {
		next := strings.Index(calls[position+1:], call)
		require.NotEqual(t, -1, next, "missing call %q in:\n%s", call, calls)
		position += next + 1
	}
}
