//go:build unix

package setup

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

// A launcher is written and read back word for word, and admission reads
// the same policy from a file that has one: the key is not one it decides
// from.
func TestLauncherRoundTrips(t *testing.T) {
	path, err := Path(configDir(t), "agent")
	require.NoError(t, err)
	f := validFile(t)
	f.Launcher = []string{"/opt/box/bin/sandbox", "--profile", "a word with spaces"}
	require.NoError(t, save(path, f))

	loaded, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, f.Launcher, loaded.Launcher)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	p, err := admission.ParsePolicy(data)
	require.NoError(t, err)
	p.AgentID = agentID
	require.NoError(t, p.Validate())
	assert.Equal(t, f.Projects, p.Projects)
}

// No launcher writes no key, so a file set up without one reads exactly as
// it did before launchers existed.
func TestNoLauncherWritesNoKey(t *testing.T) {
	path, err := Path(configDir(t), "agent")
	require.NoError(t, err)
	require.NoError(t, save(path, validFile(t)))
	var raw map[string]any
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.NotContains(t, raw, "launcher")
}

func TestValidLauncher(t *testing.T) {
	for name, argv := range map[string][]string{
		"a name on PATH":           {"sandbox"},
		"an absolute path":         {"/usr/local/bin/sandbox"},
		"words after the program":  {"sandbox", "--profile", "work"},
		"a word with a space":      {"env", "A=b c"},
		"a word that looks shelly": {"sandbox", "$(id)", "; rm -rf ~"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.NoError(t, ValidLauncher(argv))
		})
	}
	long := make([]string, MaxLauncherWords+1)
	for i := range long {
		long[i] = "x"
	}
	for name, argv := range map[string][]string{
		"nothing":              {},
		"an empty program":     {""},
		"an empty word":        {"sandbox", ""},
		"a relative path":      {"bin/sandbox"},
		"a dot path":           {"./sandbox"},
		"an option first":      {"--sandbox"},
		"a newline":            {"sandbox", "a\nb"},
		"an escape":            {"sandbox\x1b[2J"},
		"a NUL":                {"sand\x00box"},
		"too many words":       long,
		"a word over the cap":  {"sandbox", strings.Repeat("a", MaxLauncherWordBytes+1)},
		"not UTF-8":            {"sandbox", "\xff"},
		"a C1 control":         {"sandbox", "\u009b"},
		"a tab in the program": {"sand\tbox"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, ValidLauncher(argv))
		})
	}
}

// A launcher setup would refuse is refused when read too, by everything
// that loads connect.json: one written by hand, or by a newer setup with
// other rules, is not run as it stands.
func TestParseRefusesABadLauncher(t *testing.T) {
	for name, launcher := range map[string]any{
		"an empty list":   []string{},
		"a relative path": []string{"bin/sandbox"},
		"a string":        "sandbox claude",
	} {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(validFile(t))
			require.NoError(t, err)
			var raw map[string]any
			require.NoError(t, json.Unmarshal(data, &raw))
			raw["launcher"] = launcher
			data, err = json.Marshal(raw)
			require.NoError(t, err)
			_, err = Parse(data)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "launcher")
		})
	}
}

func TestApplyLauncher(t *testing.T) {
	base := validFile(t)

	t.Run("set", func(t *testing.T) {
		out, err := Apply(base, Changes{Launcher: []string{"sandbox"}})
		require.NoError(t, err)
		assert.Equal(t, []string{"sandbox"}, out.Launcher)
		require.NoError(t, out.Validate())
	})
	t.Run("kept when not passed", func(t *testing.T) {
		f := base
		f.Launcher = []string{"sandbox"}
		out, err := Apply(f, Changes{Serve: []int64{777}})
		require.NoError(t, err)
		assert.Equal(t, []string{"sandbox"}, out.Launcher)
	})
	t.Run("replaced whole, not appended to", func(t *testing.T) {
		f := base
		f.Launcher = []string{"sandbox", "--old"}
		out, err := Apply(f, Changes{Launcher: []string{"/opt/box"}})
		require.NoError(t, err)
		assert.Equal(t, []string{"/opt/box"}, out.Launcher)
	})
	t.Run("cleared", func(t *testing.T) {
		f := base
		f.Launcher = []string{"sandbox"}
		out, err := Apply(f, Changes{ClearLauncher: true})
		require.NoError(t, err)
		assert.Nil(t, out.Launcher)
		require.NoError(t, out.Validate())
	})
	t.Run("set and cleared is refused", func(t *testing.T) {
		_, err := Apply(base, Changes{Launcher: []string{"sandbox"}, ClearLauncher: true})
		assert.Error(t, err)
	})
	t.Run("an invalid one is refused", func(t *testing.T) {
		_, err := Apply(base, Changes{Launcher: []string{"./sandbox"}})
		assert.Error(t, err)
	})
	t.Run("the input is not modified", func(t *testing.T) {
		f := base
		f.Launcher = []string{"sandbox"}
		out, err := Apply(f, Changes{})
		require.NoError(t, err)
		out.Launcher[0] = "changed"
		assert.Equal(t, []string{"sandbox"}, f.Launcher)
	})
}
