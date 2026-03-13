package cli

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/commands"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/version"
)

func TestResolvePreferences(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }
	intPtr := func(i int) *int { return &i }
	isDev := version.IsDev()

	tests := []struct {
		name        string
		cfg         *config.Config
		setFlags    map[string]string // flags to Set (marks Changed)
		flags       appctx.GlobalFlags
		wantStats   bool
		wantHints   bool
		wantVerbose int
	}{
		{
			name:      "empty config falls back to IsDev",
			cfg:       &config.Config{},
			wantStats: isDev,
			wantHints: isDev,
		},
		{
			name:      "config true overrides dev default",
			cfg:       &config.Config{Stats: boolPtr(true), Hints: boolPtr(true)},
			wantStats: true,
			wantHints: true,
		},
		{
			name:      "config false overrides dev default",
			cfg:       &config.Config{Stats: boolPtr(false), Hints: boolPtr(false)},
			wantStats: false,
			wantHints: false,
		},
		{
			name:      "explicit --stats flag overrides config false",
			cfg:       &config.Config{Stats: boolPtr(false), Hints: boolPtr(false)},
			setFlags:  map[string]string{"stats": "true"},
			flags:     appctx.GlobalFlags{Stats: true},
			wantStats: true,
			wantHints: false,
		},
		{
			name:      "explicit --no-stats overrides config true",
			cfg:       &config.Config{Stats: boolPtr(true), Hints: boolPtr(true)},
			setFlags:  map[string]string{"no-stats": "true"},
			flags:     appctx.GlobalFlags{NoStats: true},
			wantStats: false, // no-stats Changed and true, skip config
			wantHints: true,
		},
		{
			name:      "--no-stats=false does NOT suppress config fallback",
			cfg:       &config.Config{Stats: boolPtr(true), Hints: boolPtr(true)},
			setFlags:  map[string]string{"no-stats": "false"},
			flags:     appctx.GlobalFlags{NoStats: false},
			wantStats: true, // no-stats Changed but value is false, config applies
			wantHints: true,
		},
		{
			name:      "--no-hints=false does NOT suppress config fallback",
			cfg:       &config.Config{Stats: boolPtr(true), Hints: boolPtr(true)},
			setFlags:  map[string]string{"no-hints": "false"},
			flags:     appctx.GlobalFlags{NoHints: false},
			wantStats: true,
			wantHints: true, // no-hints Changed but value is false, config applies
		},
		{
			name:      "explicit --hints overrides config false",
			cfg:       &config.Config{Stats: boolPtr(true), Hints: boolPtr(false)},
			setFlags:  map[string]string{"hints": "true"},
			flags:     appctx.GlobalFlags{Hints: true},
			wantStats: true,
			wantHints: true,
		},
		{
			name:        "config verbose overrides default",
			cfg:         &config.Config{Stats: boolPtr(false), Hints: boolPtr(false), Verbose: intPtr(2)},
			wantStats:   false,
			wantHints:   false,
			wantVerbose: 2,
		},
		{
			name:        "explicit --verbose overrides config",
			cfg:         &config.Config{Stats: boolPtr(false), Hints: boolPtr(false), Verbose: intPtr(2)},
			setFlags:    map[string]string{"verbose": "1"},
			flags:       appctx.GlobalFlags{Verbose: 1},
			wantStats:   false,
			wantHints:   false,
			wantVerbose: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			var stats, noStats, hints, noHints bool
			var verbose int
			cmd.PersistentFlags().BoolVar(&stats, "stats", false, "")
			cmd.PersistentFlags().BoolVar(&noStats, "no-stats", false, "")
			cmd.PersistentFlags().BoolVar(&hints, "hints", false, "")
			cmd.PersistentFlags().BoolVar(&noHints, "no-hints", false, "")
			cmd.PersistentFlags().IntVar(&verbose, "verbose", 0, "")

			for f, v := range tt.setFlags {
				_ = cmd.PersistentFlags().Set(f, v)
			}

			flags := &tt.flags

			resolvePreferences(cmd, tt.cfg, flags)

			assert.Equal(t, tt.wantStats, flags.Stats, "Stats")
			assert.Equal(t, tt.wantHints, flags.Hints, "Hints")
			assert.Equal(t, tt.wantVerbose, flags.Verbose, "Verbose")
		})
	}
}

// isolateRootTest sets env vars for hermetic root tests.
func isolateRootTest(t *testing.T) {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func TestJQInvalidExpressionRejectedBeforeRunE(t *testing.T) {
	isolateRootTest(t)

	root := NewRootCmd()
	root.AddCommand(commands.NewConfigCmd())
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"config", "show", "--jq", ".[invalid"})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --jq expression")
}

func TestJQCompileErrorRejectedBeforeRunE(t *testing.T) {
	isolateRootTest(t)

	root := NewRootCmd()
	root.AddCommand(commands.NewConfigCmd())
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"config", "show", "--jq", "$__loc__"})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --jq expression")
}

func TestJQWithIDsOnlyConflict(t *testing.T) {
	isolateRootTest(t)

	root := NewRootCmd()
	root.AddCommand(commands.NewConfigCmd())
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"config", "show", "--jq", ".data", "--ids-only"})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot use --jq with --ids-only")
}

func TestJQWithCountConflict(t *testing.T) {
	isolateRootTest(t)

	root := NewRootCmd()
	root.AddCommand(commands.NewConfigCmd())
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"config", "show", "--jq", ".data", "--count"})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot use --jq with --count")
}

func TestIsMachineConsumerWithJQ(t *testing.T) {
	root := NewRootCmd()
	_ = root.PersistentFlags().Set("jq", ".data")

	assert.True(t, isMachineConsumer(root))
}

func TestIsMachineConsumerWithoutJQ(t *testing.T) {
	// Without any flags and with stdout as a terminal (in tests it's not a terminal),
	// the piped stdout should make this return true in test context.
	root := NewRootCmd()

	// isMachineConsumer checks stdout which in tests is not a TTY.
	// This is fine — it returns true because stdout is piped.
	assert.True(t, isMachineConsumer(root))
}

func TestVersionSubcommand(t *testing.T) {
	orig := version.Version
	version.Version = "1.2.3"
	t.Cleanup(func() { version.Version = orig })

	root := NewRootCmd()
	root.AddCommand(commands.NewVersionCmd())

	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"version"})

	err := root.Execute()
	require.NoError(t, err)
	assert.Equal(t, "basecamp version 1.2.3\n", buf.String())
}

func TestVersionWithJQReturnsUsageError(t *testing.T) {
	root := NewRootCmd()
	root.AddCommand(commands.NewVersionCmd())
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"version", "--jq", ".x"})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--jq is not supported by the version command")
}
