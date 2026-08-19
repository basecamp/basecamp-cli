package commands

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/stdinarg"
)

// dashProbe records whether a guarded command's original RunE ran, and with
// which args — the passthrough half of the guard's contract.
type dashProbe struct {
	ran  bool
	args []string
}

func newDashProbeCmd(probe *dashProbe, tokens ...string) *cobra.Command {
	cmd := &cobra.Command{
		Use: "probe <name>",
		RunE: func(cmd *cobra.Command, args []string) error {
			probe.ran = true
			probe.args = args
			return nil
		},
	}
	cmd.Flags().String("title", "", "")
	cmd.Flags().String("out", "", "")
	cmd.Flags().StringArray("attach", nil, "")
	if len(tokens) > 0 {
		allowDash(cmd, tokens...)
	}
	InstallDashGuard(cmd)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	return cmd
}

func TestDashGuardRejectsUnlistedPositionalWhenPiped(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Flags.JSON = true

	cmd := NewProjectsCmd()
	InstallDashGuard(cmd)
	cmd.SetIn(strings.NewReader("piped"))

	err := executeCommand(cmd, app, "create", "-")
	outErr := requireUsageErr(t, err)
	assert.Contains(t, outErr.Message, "<name>")
	assert.Contains(t, outErr.Hint, "--")
	assert.Contains(t, outErr.Hint, "--description", "hint should point at where stdin is accepted")
}

func TestDashGuardRejectsUnlistedFlagWhenPiped(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Flags.JSON = true

	cmd := NewTodosCmd()
	InstallDashGuard(cmd)
	cmd.SetIn(strings.NewReader("piped"))

	err := executeCommand(cmd, app, "update", "1", "--title", "-")
	outErr := requireUsageErr(t, err)
	assert.Contains(t, outErr.Message, "--title")
}

func TestDashGuardPassesLiteralDashOnTTY(t *testing.T) {
	probe := &dashProbe{}
	cmd := newDashProbeCmd(probe)
	devNullStdin(t, cmd)
	cmd.SetArgs([]string{"-", "--title", "-"})

	require.NoError(t, cmd.Execute())
	assert.True(t, probe.ran)
	assert.Equal(t, []string{"-"}, probe.args)
}

func TestDashGuardExemptsOutFlag(t *testing.T) {
	probe := &dashProbe{}
	cmd := newDashProbeCmd(probe, "flag:out")
	cmd.SetIn(strings.NewReader("piped"))
	cmd.SetArgs([]string{"name", "--out", "-"})

	require.NoError(t, cmd.Execute())
	assert.True(t, probe.ran)
}

func TestDashGuardRejectsTwoAllowedDashes(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Flags.JSON = true

	cmd := NewChatCmd()
	InstallDashGuard(cmd)
	cmd.SetIn(strings.NewReader("piped"))

	err := executeCommand(cmd, app, "post", "-", "--content", "-")
	outErr := requireUsageErr(t, err)
	assert.Contains(t, outErr.Message, "only one input")
}

// Two allowed dashes can never both be satisfied, even on a TTY.
func TestDashGuardRejectsTwoAllowedDashesOnTTY(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Flags.JSON = true

	cmd := NewChatCmd()
	InstallDashGuard(cmd)
	devNullStdin(t, cmd)

	err := executeCommand(cmd, app, "post", "-", "--content", "-")
	outErr := requireUsageErr(t, err)
	assert.Contains(t, outErr.Message, "only one input")
}

func TestDashGuardRejectsUnlistedAttachAlongsideAllowedBody(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Flags.JSON = true

	cmd := NewMessagesCmd()
	InstallDashGuard(cmd)
	cmd.SetIn(strings.NewReader("piped"))

	err := executeCommand(cmd, app, "create", "title", "-", "--attach", "-")
	outErr := requireUsageErr(t, err)
	assert.Contains(t, outErr.Message, "--attach")
}

func TestDashGuardSeparatorEscapesLiteralDash(t *testing.T) {
	probe := &dashProbe{}
	cmd := newDashProbeCmd(probe)
	cmd.SetIn(strings.NewReader("piped"))
	cmd.SetArgs([]string{"--", "-"})

	require.NoError(t, cmd.Execute())
	assert.True(t, probe.ran)
	assert.Equal(t, []string{"-"}, probe.args)
}

// The download commands carry the --out exemption so "-" (stdout) never trips
// the stdin guard.
func TestDownloadCommandsExemptOutFlag(t *testing.T) {
	for _, cmd := range []*cobra.Command{NewAttachmentsCmd(), NewFilesCmd()} {
		download := findSubcommand(cmd, "download")
		require.NotNil(t, download, "%s download not found", cmd.Name())
		allow := stdinarg.ParseAllow(download.Annotations[stdinarg.AnnotationAllowDash])
		assert.True(t, allow.Flag("out"), "%s download should exempt --out", cmd.Name())
	}
}
