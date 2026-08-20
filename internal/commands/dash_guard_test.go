package commands

import (
	"errors"
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
	// -- doesn't escape flag values, so the hint must not claim it does.
	assert.NotContains(t, outErr.Hint, "-- separator")
	assert.Contains(t, outErr.Hint, "without piped stdin")
}

// The guard runs at Args-validation time: before the command's own Args
// check, PreRunE, and required-flag validation, so the stray-dash error is
// what the caller sees instead of a competing usage error — and no pre-run
// side effect happens first.
func TestDashGuardFiresBeforeArgsPreRunAndRequiredFlags(t *testing.T) {
	preRunRan := false
	cmd := &cobra.Command{
		Use:  "probe <name> <other>",
		Args: cobra.ExactArgs(2),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			preRunRan = true
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error { return nil },
	}
	cmd.Flags().String("title", "", "")
	require.NoError(t, cmd.MarkFlagRequired("title"))
	InstallDashGuard(cmd)
	cmd.SetIn(strings.NewReader("piped"))
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs([]string{"-"}) // one arg: ExactArgs(2) and the missing --title would both error later

	err := cmd.Execute()
	outErr := requireUsageErr(t, err)
	assert.Contains(t, outErr.Message, "<name>")
	assert.False(t, preRunRan, "guard must fire before PreRunE")
}

// Alias flags share one backing value; a value set through both spellings is
// one logical input, not two.
func TestDashGuardAliasFlagsCountOnce(t *testing.T) {
	newAliasCmd := func(probe *dashProbe) *cobra.Command {
		var description string
		cmd := &cobra.Command{
			Use: "probe",
			RunE: func(cmd *cobra.Command, args []string) error {
				probe.ran = true
				probe.args = []string{description}
				return nil
			},
		}
		cmd.Flags().StringVar(&description, "description", "", "")
		cmd.Flags().StringVar(&description, "desc", "", "")
		allowDash(cmd, "flag:description", "flag:desc")
		InstallDashGuard(cmd)
		cmd.SetOut(&strings.Builder{})
		cmd.SetErr(&strings.Builder{})
		return cmd
	}

	// Dash last: the merged value is "-", one allowed stdin input.
	probe := &dashProbe{}
	cmd := newAliasCmd(probe)
	cmd.SetIn(strings.NewReader("piped"))
	cmd.SetArgs([]string{"--description", "old", "--desc", "-"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, []string{"-"}, probe.args)

	// Dash first: the literal value wins, no dash in play at all.
	probe = &dashProbe{}
	cmd = newAliasCmd(probe)
	cmd.SetIn(strings.NewReader("piped"))
	cmd.SetArgs([]string{"--desc", "-", "--description", "old"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, []string{"old"}, probe.args)
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

// Parsed state keeps only the merged value, never which spelling the caller
// typed — so naming one alias is a coin flip that reports "--in" for a caller
// who wrote "--project -". Name the whole group instead.
func TestDashGuardNamesTheWholeAliasGroup(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Flags.JSON = true

	// --project/--in are two spellings of one persistent value on the group.
	cmd := NewCardsCmd()
	InstallDashGuard(cmd)
	cmd.SetIn(strings.NewReader("piped"))

	err := executeCommand(cmd, app, "create", "Title", "--in", "old", "--project", "-")
	outErr := requireUsageErr(t, err)
	// pflag visits flags in sorted order, so the group label is stable.
	assert.Contains(t, outErr.Message, "--in/--project")
}

// A flag with no alias still reads as a single spelling.
func TestDashGuardNamesASoloFlagPlainly(t *testing.T) {
	app, _ := setupTestApp(t)
	app.Flags.JSON = true

	cmd := NewTodosCmd()
	InstallDashGuard(cmd)
	cmd.SetIn(strings.NewReader("piped"))

	err := executeCommand(cmd, app, "update", "1", "--title", "-")
	outErr := requireUsageErr(t, err)
	assert.Contains(t, outErr.Message, "--title")
	assert.NotContains(t, outErr.Message, "/--")
}

// The root keeps nil Args so cobra's legacyArgs still rejects unknown
// subcommands, so its guard hangs off RunE instead. All four root behaviors
// have to survive together.
func TestDashGuardOnRootPreservesUnknownCommandHandling(t *testing.T) {
	newRoot := func(probe *dashProbe) *cobra.Command {
		root := &cobra.Command{
			Use: "basecamp",
			RunE: func(cmd *cobra.Command, args []string) error {
				probe.ran = true
				probe.args = args
				return nil
			},
		}
		root.AddCommand(&cobra.Command{
			Use:  "todos",
			RunE: func(cmd *cobra.Command, args []string) error { return nil },
		})
		InstallDashGuard(root)
		require.Nil(t, root.Args, "root Args must stay nil for legacyArgs")
		root.SetOut(&strings.Builder{})
		root.SetErr(&strings.Builder{})
		return root
	}

	t.Run("piped dash is a usage error", func(t *testing.T) {
		probe := &dashProbe{}
		root := newRoot(probe)
		root.SetIn(strings.NewReader("piped"))
		root.SetArgs([]string{"-"})

		outErr := requireUsageErr(t, root.Execute())
		assert.Contains(t, outErr.Message, "does not read stdin")
		assert.False(t, probe.ran, "quick-start must not run on a stray dash")
	})

	t.Run("separator keeps the dash literal", func(t *testing.T) {
		probe := &dashProbe{}
		root := newRoot(probe)
		root.SetIn(strings.NewReader("piped"))
		root.SetArgs([]string{"--", "-"})

		require.NoError(t, root.Execute())
		assert.True(t, probe.ran)
		assert.Equal(t, []string{"-"}, probe.args)
	})

	t.Run("unknown subcommand still errors", func(t *testing.T) {
		probe := &dashProbe{}
		root := newRoot(probe)
		root.SetIn(strings.NewReader("piped"))
		root.SetArgs([]string{"unknowncmd"})

		err := root.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown command")
		assert.False(t, probe.ran)
	})

	t.Run("bare invocation still runs", func(t *testing.T) {
		probe := &dashProbe{}
		root := newRoot(probe)
		root.SetIn(strings.NewReader("piped"))
		root.SetArgs(nil)

		require.NoError(t, root.Execute())
		assert.True(t, probe.ran)
	})
}

// The root guard runs at the front of the persistent pre-run, so nothing the
// caller did not ask about — config loading, profile resolution, --jq
// validation — can answer ahead of the stray dash.
func TestDashGuardOnRootPrecedesPersistentPreRun(t *testing.T) {
	preRunRan := false
	root := &cobra.Command{
		Use: "basecamp",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			preRunRan = true
			return errors.New("pre-run would have answered first")
		},
		RunE: func(cmd *cobra.Command, args []string) error { return nil },
	}
	root.AddCommand(&cobra.Command{
		Use:  "todos",
		RunE: func(cmd *cobra.Command, args []string) error { return nil },
	})
	InstallDashGuard(root)
	root.SetIn(strings.NewReader("piped"))
	root.SetOut(&strings.Builder{})
	root.SetErr(&strings.Builder{})
	root.SetArgs([]string{"-"})

	outErr := requireUsageErr(t, root.Execute())
	assert.Contains(t, outErr.Message, "does not read stdin")
	assert.False(t, preRunRan, "the guard must precede the root's pre-run work")
}

// Subcommands inherit the root's persistent pre-run; the guard there must not
// double-fire for them, since they are already guarded at Args-validation time.
func TestDashGuardOnRootDoesNotAffectSubcommands(t *testing.T) {
	preRunRan := false
	var got []string
	root := &cobra.Command{
		Use: "basecamp",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			preRunRan = true
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error { return nil },
	}
	sub := &cobra.Command{
		Use: "notes",
		RunE: func(cmd *cobra.Command, args []string) error {
			got = args
			return nil
		},
	}
	allowDash(sub, "arg:0")
	root.AddCommand(sub)
	InstallDashGuard(root)
	root.SetIn(strings.NewReader("piped"))
	root.SetOut(&strings.Builder{})
	root.SetErr(&strings.Builder{})
	root.SetArgs([]string{"notes", "-"})

	require.NoError(t, root.Execute())
	assert.True(t, preRunRan, "the inherited pre-run still runs for subcommands")
	assert.Equal(t, []string{"-"}, got, "an allowed dash reaches the subcommand")
}
