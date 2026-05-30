package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/commands"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/version"
)

// TestLLMEndpointValidation exercises the production validateLLMEndpoint helper
// in root.go: the scheme/host check is unconditional, while the HTTPS gate is
// only enforced for credentialed/ambiguous providers when a key is present.
// Credential-less providers (ollama, apple, none) never send the key, so a
// remote http endpoint is allowed even when a key exists for a different provider.
func TestLLMEndpointValidation(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		provider string
		apiKey   string
		wantOK   bool
	}{
		{"file scheme rejected", "file:///etc/passwd", "", "", false},
		{"ssh scheme rejected", "ssh://host", "", "", false},
		{"hostless https rejected", "https:example.com", "", "", false},
		{"http remote with credential rejected", "http://remote-host:1234", "", "secret", false},
		{"http remote without credential accepted", "http://remote-host:1234", "", "", true},
		{"https remote accepted", "https://remote-host", "", "", true},
		{"https remote with credential accepted", "https://remote-host", "", "secret", true},
		{"localhost with credential accepted", "http://localhost:11434", "", "secret", true},
		{"empty endpoint no-op", "", "", "", true},
		{"empty endpoint with key no-op", "", "", "secret", true},
		// Credential-less provider: remote http allowed even with a stray key.
		{"ollama remote http with key accepted", "http://192.168.1.10:11434", "ollama", "secret", true},
		// Credentialed/ambiguous providers: key still gates remote http.
		{"openai remote http with key rejected", "http://remote:1234", "openai", "secret", false},
		{"auto remote http with key rejected", "http://remote:1234", "auto", "secret", false},
		{"anthropic remote http with key rejected", "http://remote:1234", "anthropic", "secret", false},
		// Unconditional scheme check fires before the credential-less exemption.
		{"ollama file scheme rejected", "file:///etc/passwd", "ollama", "secret", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateLLMEndpoint(tt.endpoint, tt.provider, tt.apiKey)
			if tt.wantOK {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

// TestHasWritableAncestor exercises the TOCTOU guard that gates the best-effort
// chmod of the global config dir in PersistentPreRunE. The chmod is skipped when
// any ancestor — not just the immediate parent — is group/world-writable, since a
// writable ancestor anywhere up the path lets an attacker substitute a path
// component and win the Lstat->Chmod race.
//
// hasWritableAncestor walks the filesystem all the way to root, so a clean
// ("proceeds") case needs a base whose entire ancestry is non-writable. t.TempDir()
// lives under a world-writable /tmp (1777), which would always trip the guard, so
// the tree is built under the user's home dir instead and the proceeds assertion is
// skipped on environments whose HOME itself sits under a writable ancestor.
//
// The helper walks BOTH the lexical ancestor chain of the original path AND the
// chain of the symlink-resolved real path; either chain being writable is unsafe.
// EvalSymlinks requires the path to exist, so cfgDir leaves are created (not just
// their parents) in these cases. It also treats relative and unresolvable paths as
// unsafe.
func TestHasWritableAncestor(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	// base is a freshly created, 0755 dir under HOME; only its ancestry (HOME and
	// above) plus whatever we loosen below can be writable.
	base, err := os.MkdirTemp(home, "hwa-test-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	require.NoError(t, os.Chmod(base, 0o755))

	t.Run("relative path => unsafe", func(t *testing.T) {
		// A relative config dir can't be reasoned about (real ancestry depends on
		// cwd), so it must be treated as unsafe regardless of the filesystem.
		assert.True(t, hasWritableAncestor(filepath.Join("foo", "basecamp")))
	})

	t.Run("non-writable ancestors => chmod proceeds", func(t *testing.T) {
		if hasWritableAncestor(base) {
			t.Skip("HOME has a writable ancestor; cannot demonstrate the proceeds case here")
		}
		grandparent := filepath.Join(base, "ok-gp")
		parent := filepath.Join(grandparent, "parent")
		cfgDir := filepath.Join(parent, "basecamp")
		require.NoError(t, os.MkdirAll(cfgDir, 0o755))

		assert.False(t, hasWritableAncestor(cfgDir))
	})

	t.Run("writable immediate parent => chmod skipped", func(t *testing.T) {
		parent := filepath.Join(base, "wp-parent")
		cfgDir := filepath.Join(parent, "basecamp")
		require.NoError(t, os.MkdirAll(cfgDir, 0o755))
		require.NoError(t, os.Chmod(parent, 0o777))

		assert.True(t, hasWritableAncestor(cfgDir))
	})

	t.Run("non-writable parent but writable grandparent => chmod skipped", func(t *testing.T) {
		grandparent := filepath.Join(base, "wgp-gp")
		parent := filepath.Join(grandparent, "parent")
		cfgDir := filepath.Join(parent, "basecamp")
		require.NoError(t, os.MkdirAll(cfgDir, 0o755))
		// Loosen the grandparent only; the immediate parent stays 0755.
		require.NoError(t, os.Chmod(grandparent, 0o777))

		assert.True(t, hasWritableAncestor(cfgDir))
	})

	t.Run("symlink into writable real ancestor => chmod skipped", func(t *testing.T) {
		if hasWritableAncestor(base) {
			t.Skip("HOME has a writable ancestor; cannot isolate the symlinked-ancestor case here")
		}
		// Real target lives under a world-writable ancestor (writable/real-cfg),
		// but the lexical path to the symlink (safe/link) has only non-writable
		// ancestors. Lexical walking would miss the writable ancestor; symlink
		// resolution must catch it.
		writable := filepath.Join(base, "writable")
		realCfg := filepath.Join(writable, "real-cfg")
		require.NoError(t, os.MkdirAll(realCfg, 0o755))
		require.NoError(t, os.Chmod(writable, 0o777))

		safe := filepath.Join(base, "safe")
		require.NoError(t, os.MkdirAll(safe, 0o755))
		link := filepath.Join(safe, "link")
		if err := os.Symlink(realCfg, link); err != nil {
			t.Skipf("symlinks unavailable in this environment: %v", err)
		}

		assert.True(t, hasWritableAncestor(link))
	})

	t.Run("symlink whose lexical parent is writable but target tree is private => chmod skipped", func(t *testing.T) {
		if hasWritableAncestor(base) {
			t.Skip("HOME has a writable ancestor; cannot isolate the writable-symlink-parent case here")
		}
		// The dual to the prior case: here the symlink's TARGET tree is entirely
		// private (0755) but the world-writable dir holding the symlink is in the
		// LEXICAL chain only — EvalSymlinks jumps to the target and skips it, so a
		// resolved-only walk returns "safe". A local user with write access to the
		// writable dir can swap the symlink between our Lstat and Chmod, so the
		// lexical chain must catch it.
		realTree := filepath.Join(base, "private-real", "sub")
		require.NoError(t, os.MkdirAll(realTree, 0o755))

		writable := filepath.Join(base, "world-writable")
		require.NoError(t, os.MkdirAll(writable, 0o755))
		require.NoError(t, os.Chmod(writable, 0o777))
		link := filepath.Join(writable, "link")
		if err := os.Symlink(realTree, link); err != nil {
			t.Skipf("symlinks unavailable in this environment: %v", err)
		}

		// link/leaf resolves into the private tree, but its lexical parent chain
		// runs through the world-writable dir.
		assert.True(t, hasWritableAncestor(filepath.Join(link, "leaf")))
	})
}

// TestIsForeignOwnerWritable exercises the foreign-owner half of the TOCTOU guard
// directly. The helper only flags ancestors owned by a DIFFERENT non-root user
// that still carry the owner-write bit; root-owned and self-owned dirs are trusted
// regardless of their owner-write bit. Most cases run as any user; the genuine
// "true" case requires chown'ing to a foreign uid, which only root can do, so it is
// skipped otherwise.
func TestIsForeignOwnerWritable(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	base, err := os.MkdirTemp(home, "ifow-test-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	t.Run("self-owned 0700 => false", func(t *testing.T) {
		dir := filepath.Join(base, "self-0700")
		require.NoError(t, os.Mkdir(dir, 0o700))
		fi, err := os.Stat(dir)
		require.NoError(t, err)
		assert.False(t, isForeignOwnerWritable(fi))
	})

	t.Run("self-owned 0755 => false (uid==self; group/world bits handled elsewhere)", func(t *testing.T) {
		dir := filepath.Join(base, "self-0755")
		require.NoError(t, os.Mkdir(dir, 0o755))
		require.NoError(t, os.Chmod(dir, 0o755))
		fi, err := os.Stat(dir)
		require.NoError(t, err)
		// Owned by us, so the foreign-owner check is false even with the
		// owner-write bit set; group/world-writability is a separate concern
		// handled by hasWritableChain, not this helper.
		assert.False(t, isForeignOwnerWritable(fi))
	})

	t.Run("foreign non-root owner with owner-write => true", func(t *testing.T) {
		// A genuine foreign-owned, owner-writable dir can only be produced by
		// chown'ing to another uid, which requires root. Skip otherwise.
		if os.Geteuid() != 0 {
			t.Skip("need root to chown a dir to a foreign uid")
		}
		dir := filepath.Join(base, "foreign-0755")
		require.NoError(t, os.Mkdir(dir, 0o755))
		// Chown to a non-root, non-self uid (nobody-ish). Skip if it fails.
		const foreignUID = 65534 // conventionally "nobody"
		if err := os.Chown(dir, foreignUID, foreignUID); err != nil {
			t.Skipf("cannot chown to foreign uid %d: %v", foreignUID, err)
		}
		fi, err := os.Stat(dir)
		require.NoError(t, err)
		assert.True(t, isForeignOwnerWritable(fi))
	})
}

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
