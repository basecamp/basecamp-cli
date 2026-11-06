package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// writeInstructionsFile writes content to a file in a fresh temp dir and
// returns its absolute path.
func writeInstructionsFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func instructionsTestApp(t *testing.T, path string) *appctx.App {
	t.Helper()
	app, _ := setupConfigTestApp(t)
	app.Config.InstructionsFile = path
	if path != "" {
		app.Config.Sources["instructions_file"] = "global"
	}
	return app
}

func runInstructionsCommand(t *testing.T, app *appctx.App) (string, string, error) {
	t.Helper()
	cmd := NewInstructionsCmd()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetArgs(nil)
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func TestLoadInstructionsNotConfigured(t *testing.T) {
	result, err := loadInstructions(instructionsTestApp(t, ""))
	require.NoError(t, err)
	assert.False(t, result.Configured)
	assert.Empty(t, result.Guidance)
	assert.Empty(t, result.Documents)
}

// With nothing set, the plain command prints nothing on stdout and succeeds,
// so "run basecamp instructions before writing" is safe everywhere.
func TestInstructionsCommandNotConfiguredPrintsNothing(t *testing.T) {
	app := instructionsTestApp(t, "")
	app.Flags.JSON = false
	stdout, stderr, err := runInstructionsCommand(t, app)
	require.NoError(t, err)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "no instructions_file is set")
}

// The file is read on every run, so an edit applies to the next write.
func TestLoadInstructionsReadsTheFileEachTime(t *testing.T) {
	path := writeInstructionsFile(t, "style.md", "Lead with the answer.\n")
	app := instructionsTestApp(t, path)

	result, err := loadInstructions(app)
	require.NoError(t, err)
	require.Len(t, result.Documents, 1)
	assert.Equal(t, "Lead with the answer.", result.Documents[0].Content)
	assert.Equal(t, path, result.Documents[0].Path)
	assert.Equal(t, "global", result.Documents[0].Scope)
	assert.Equal(t, instructionsPreface, result.Guidance)

	require.NoError(t, os.WriteFile(path, []byte("Link every reference."), 0o600))
	result, err = loadInstructions(app)
	require.NoError(t, err)
	assert.Equal(t, "Link every reference.", result.Documents[0].Content)
}

// The shared baseline comes first and the active profile's own file after
// it, so the more specific one is read last and wins.
func TestLoadInstructionsLayersProfileOverBaseline(t *testing.T) {
	app := instructionsTestApp(t, writeInstructionsFile(t, "house.md", "Lead with the answer."))
	app.Config.ActiveProfile = "marie"
	app.Config.ProfileInstructionsFile = writeInstructionsFile(t, "marie.md", "Sign off as Marie.")

	result, err := loadInstructions(app)
	require.NoError(t, err)
	require.Len(t, result.Documents, 2)
	assert.Equal(t, "global", result.Documents[0].Scope)
	assert.Equal(t, "Lead with the answer.", result.Documents[0].Content)
	assert.Equal(t, "profile:marie", result.Documents[1].Scope)
	assert.Equal(t, "Sign off as Marie.", result.Documents[1].Content)
}

// A profile's file alone applies too: the baseline is not required.
func TestLoadInstructionsProfileLayerAlone(t *testing.T) {
	app := instructionsTestApp(t, "")
	app.Config.ActiveProfile = "marie"
	app.Config.ProfileInstructionsFile = writeInstructionsFile(t, "marie.md", "Sign off as Marie.")

	result, err := loadInstructions(app)
	require.NoError(t, err)
	assert.True(t, result.Configured)
	require.Len(t, result.Documents, 1)
	assert.Equal(t, "profile:marie", result.Documents[0].Scope)
}

// A file that is set but can't be read is an error: the person chose it, and
// an agent should say it is missing rather than write as if none were set.
func TestLoadInstructionsRefusesWhatCannotBeAnInstructionsFile(t *testing.T) {
	dir := t.TempDir()
	big := writeInstructionsFile(t, "big.md", strings.Repeat("a", instructionsMaxBytes+1))
	binary := writeInstructionsFile(t, "bin.md", "\xff\xfe\x00")

	cases := map[string]struct {
		path string
		code string
		says string
	}{
		"missing":   {filepath.Join(dir, "gone.md"), output.CodeNotFound, "does not exist"},
		"relative":  {"docs/style.md", output.CodeUsage, "not an absolute path"},
		"directory": {dir, output.CodeUsage, "not a regular file"},
		"too big":   {big, output.CodeUsage, "over the 65536-byte limit"},
		"not text":  {binary, output.CodeUsage, "not UTF-8"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadInstructions(instructionsTestApp(t, tc.path))
			var outErr *output.Error
			require.True(t, errors.As(err, &outErr), "%v", err)
			assert.Equal(t, tc.code, outErr.Code)
			assert.Contains(t, outErr.Message, tc.says)
		})
	}
}

// Run plainly, the command prints the preface that bounds the files'
// authority, then each file as Markdown with where it came from.
func TestInstructionsCommandPrintsMarkdown(t *testing.T) {
	path := writeInstructionsFile(t, "style.md", "# House style\n\n- Lead with the answer.\n")
	app := instructionsTestApp(t, path)
	app.Flags.JSON = false

	stdout, _, err := runInstructionsCommand(t, app)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(stdout, instructionsPreface), stdout)
	assert.Contains(t, stdout, "Source: "+path+" (global)")
	assert.Contains(t, stdout, "# House style\n\n- Lead with the answer.")
}

func TestInstructionsCommandStripsTerminalControls(t *testing.T) {
	app := instructionsTestApp(t, writeInstructionsFile(t, "style.md", "Be brief.\u001b]0;owned\u0007\u009b31m\u001b[2J"))
	app.Flags.JSON = false

	stdout, _, err := runInstructionsCommand(t, app)
	require.NoError(t, err)
	assert.Contains(t, stdout, "Be brief.")
	assert.NotContains(t, stdout, "\x1b")
	assert.NotContains(t, stdout, "\u009b")
	assert.NotContains(t, stdout, "\a")
}

func TestInstructionsCommandJSON(t *testing.T) {
	path := writeInstructionsFile(t, "style.md", "Lead with the answer.")
	app, buf := setupConfigTestApp(t)
	app.Config.InstructionsFile = path

	_, _, err := runInstructionsCommand(t, app)
	require.NoError(t, err)

	var env struct {
		Summary string             `json:"summary"`
		Data    instructionsResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env), buf.String())
	assert.Equal(t, instructionsPreface, env.Summary)
	assert.True(t, env.Data.Configured)
	require.Len(t, env.Data.Documents, 1)
	assert.Equal(t, path, env.Data.Documents[0].Path)
	assert.Equal(t, "Lead with the answer.", env.Data.Documents[0].Content)
	assert.False(t, env.Data.Documents[0].ModifiedAt.IsZero())
}

func TestConfigSetInstructionsFile(t *testing.T) {
	path := writeInstructionsFile(t, "style.md", "Lead with the answer.")

	app, _ := setupConfigTestApp(t)
	err := executeConfigCommand(app, "set", "instructions_file", path)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "a local instructions_file is refused")
	assert.Contains(t, outErr.Message, "--global")

	app, _ = setupConfigTestApp(t)
	err = executeConfigCommand(app, "set", "--global", "instructions_file", filepath.Join(t.TempDir(), "gone.md"))
	require.True(t, errors.As(err, &outErr), "a file that does not exist is refused")
	assert.Equal(t, output.CodeNotFound, outErr.Code)

	app, buf := setupConfigTestApp(t)
	require.NoError(t, executeConfigCommand(app, "set", "--global", "instructions_file", path))
	var data map[string]any
	parseEnvelopeData(t, buf, &data)
	assert.Equal(t, path, data["value"])
	assert.Equal(t, "global", data["scope"])
}

// A relative path or one under ~ is saved absolute: the agent reads it from
// whatever directory it stands in, so the setting can't depend on it.
func TestConfigSetInstructionsFileSavesAnAbsolutePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(home, "style.md"), []byte("x"), 0o600))

	app, _ := setupConfigTestApp(t)
	saved := func() any {
		raw, err := os.ReadFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "basecamp", "config.json"))
		require.NoError(t, err)
		var written map[string]any
		require.NoError(t, json.Unmarshal(raw, &written))
		return written["instructions_file"]
	}

	require.NoError(t, executeConfigCommand(app, "set", "--global", "instructions_file", "~/style.md"))
	assert.Equal(t, filepath.Join(home, "style.md"), saved())

	work := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(work, "rel.md"), []byte("x"), 0o600))
	t.Chdir(work)
	require.NoError(t, executeConfigCommand(app, "set", "--global", "instructions_file", "rel.md"))
	assert.Equal(t, filepath.Join(work, "rel.md"), saved())
}

func TestConfigSetInstructionsFileForProfile(t *testing.T) {
	profileFile := writeInstructionsFile(t, "marie.md", "Sign off as Marie.")
	app, buf := setupConfigTestApp(t)
	globalPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "basecamp", "config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(globalPath), 0o700))
	require.NoError(t, os.WriteFile(globalPath, []byte(`{"profiles": {"marie": {"base_url": "https://3.basecampapi.com"}}}`), 0o600))

	require.NoError(t, executeConfigCommand(app, "set", "--global", "--for-profile", "marie", "instructions_file", profileFile))
	var data map[string]any
	parseEnvelopeData(t, buf, &data)
	assert.Equal(t, "global, profile marie", data["scope"])

	var written map[string]any
	raw, err := os.ReadFile(globalPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &written))
	assert.Nil(t, written["instructions_file"], "the shared key is untouched")
	profile := written["profiles"].(map[string]any)["marie"].(map[string]any)
	assert.Equal(t, profileFile, profile["instructions_file"])
	assert.Equal(t, "https://3.basecampapi.com", profile["base_url"], "the rest of the profile stays")

	err = executeConfigCommand(app, "set", "--global", "--for-profile", "nobody", "instructions_file", profileFile)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "a profile not in the global config is refused")
	assert.Contains(t, outErr.Message, "nobody")

	err = executeConfigCommand(app, "set", "--global", "--for-profile", "marie", "format", "json")
	require.True(t, errors.As(err, &outErr), "--for-profile takes only instructions_file")

	require.NoError(t, os.WriteFile(globalPath, []byte(`{"profiles": {"marie": {"base_url": "https://3.basecampapi.com"}, "bare": {"account_id": "1"}}, "experimental": {"foo": true}}`), 0o600))
	require.NoError(t, executeConfigCommand(app, "set", "--global", "--for-profile", "marie", "instructions_file", profileFile))
	err = executeConfigCommand(app, "set", "--global", "--for-profile", "bare", "instructions_file", profileFile)
	require.True(t, errors.As(err, &outErr), "a profile without its base_url is not loaded, so it is refused")
	assert.Contains(t, outErr.Message, "base_url")

	err = executeConfigCommand(app, "unset", "--for-profile", "marie", "instructions_file")
	require.True(t, errors.As(err, &outErr), "unset --for-profile needs --global")
	assert.Contains(t, outErr.Message, "--global")
	err = executeConfigCommand(app, "unset", "--global", "--for-profile", "marie", "experimental.foo")
	require.True(t, errors.As(err, &outErr), "unset --for-profile takes only instructions_file")
	raw, err = os.ReadFile(globalPath)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"foo"`, "the experimental key stays")

	buf.Reset()
	require.NoError(t, executeConfigCommand(app, "unset", "--global", "--for-profile", "marie", "instructions_file"))
	raw, err = os.ReadFile(globalPath)
	require.NoError(t, err)
	written = nil
	require.NoError(t, json.Unmarshal(raw, &written))
	profile = written["profiles"].(map[string]any)["marie"].(map[string]any)
	assert.NotContains(t, profile, "instructions_file")
	assert.Equal(t, "https://3.basecampapi.com", profile["base_url"])
}
