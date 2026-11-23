package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// writeInstructions writes content to a file in a fresh temp dir and
// returns its absolute path.
func writeInstructions(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func instructionsTestApp(t *testing.T, path string) *appctx.App {
	t.Helper()
	app, _ := setupConfigTestApp(t)
	app.Config.Instructions = path
	if path != "" {
		app.Config.Sources["instructions"] = "global"
	}
	return app
}

func runInstructionsCommand(t *testing.T, app *appctx.App, args ...string) (string, string, error) {
	t.Helper()
	cmd := NewInstructionsCmd()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetArgs(args)
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func TestLoadInstructionsNotConfigured(t *testing.T) {
	result, err := loadLocal(instructionsTestApp(t, ""))
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
	assert.Contains(t, stderr, "none are set")
}

// The file is read on every run, so an edit applies to the next write.
func TestLoadInstructionsReadsTheFileEachTime(t *testing.T) {
	path := writeInstructions(t, "style.md", "Lead with the answer.\n")
	app := instructionsTestApp(t, path)

	result, err := loadLocal(app)
	require.NoError(t, err)
	require.Len(t, result.Documents, 1)
	assert.Equal(t, "Lead with the answer.", result.Documents[0].Content)
	assert.Equal(t, path, result.Documents[0].Source)
	assert.Equal(t, "global", result.Documents[0].Scope)
	assert.Equal(t, instructionsPreface, result.Guidance)

	require.NoError(t, os.WriteFile(path, []byte("Link every reference."), 0o600))
	result, err = loadLocal(app)
	require.NoError(t, err)
	assert.Equal(t, "Link every reference.", result.Documents[0].Content)
}

// The shared baseline comes first and the active profile's own file after
// it, so the more specific one is read last and wins.
func TestLoadInstructionsLayersProfileOverBaseline(t *testing.T) {
	app := instructionsTestApp(t, writeInstructions(t, "house.md", "Lead with the answer."))
	app.Config.ActiveProfile = "marie"
	app.Config.ProfileInstructions = writeInstructions(t, "marie.md", "Sign off as Marie.")

	result, err := loadLocal(app)
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
	app.Config.ProfileInstructions = writeInstructions(t, "marie.md", "Sign off as Marie.")

	result, err := loadLocal(app)
	require.NoError(t, err)
	assert.True(t, result.Configured)
	require.Len(t, result.Documents, 1)
	assert.Equal(t, "profile:marie", result.Documents[0].Scope)
}

// A file that is set but can't be read is an error: the person chose it, and
// an agent should say it is missing rather than write as if none were set.
func TestLoadInstructionsRefusesWhatCannotBeASource(t *testing.T) {
	dir := t.TempDir()
	big := writeInstructions(t, "big.md", strings.Repeat("a", instructionsMaxBytes+1))
	binary := writeInstructions(t, "bin.md", "\xff\xfe\x00")

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
		"empty":     {writeInstructions(t, "empty.md", " \n\t\n"), output.CodeUsage, "is empty"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadLocal(instructionsTestApp(t, tc.path))
			var outErr *output.Error
			require.True(t, errors.As(err, &outErr), "%v", err)
			assert.Equal(t, tc.code, outErr.Code)
			assert.Contains(t, outErr.Message, tc.says)
		})
	}
}

// Run plainly, the command prints each file as Markdown with where it came
// from, then the preface that bounds the files' authority.
func TestInstructionsCommandPrintsMarkdown(t *testing.T) {
	path := writeInstructions(t, "style.md", "# House style\n\n- Lead with the answer.\n")
	app := instructionsTestApp(t, path)
	app.Flags.JSON = false

	stdout, _, err := runInstructionsCommand(t, app)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(stdout, "Source: "+path+" (global)\n\n# House style\n\n- Lead with the answer."), stdout)
	assert.True(t, strings.HasSuffix(stdout, "\n---\n\n"+instructionsPreface+"\n"), stdout)
}

// The rules come before the frame around them, baseline then profile, so an
// agent that reads only the first lines (through head, say) reads rules.
func TestInstructionsCommandPrintsRulesBeforeThePreface(t *testing.T) {
	house := writeInstructions(t, "house.md", "1. **Lead with the point.** Then the rest.")
	app := instructionsTestApp(t, house)
	app.Config.ActiveProfile = "marie"
	marie := writeInstructions(t, "marie.md", "Sign off as Marie.")
	app.Config.ProfileInstructions = marie
	app.Flags.JSON = false

	stdout, _, err := runInstructionsCommand(t, app)
	require.NoError(t, err)
	lines := strings.Split(stdout, "\n")
	assert.Equal(t, []string{"Source: " + house + " (global)", "", "1. **Lead with the point.** Then the rest."}, lines[:3])
	houseAt := strings.Index(stdout, "Lead with the point.")
	marieAt := strings.Index(stdout, "Sign off as Marie.")
	prefaceAt := strings.Index(stdout, instructionsPreface)
	assert.True(t, houseAt < marieAt && marieAt < prefaceAt, stdout)
	assert.Contains(t, stdout, "Source: "+marie+" (profile:marie)")
}

// --brief prints each source's brief in place of its text, and says so when
// a source has no list to cut down.
func TestInstructionsCommandBrief(t *testing.T) {
	house := writeInstructions(t, "house.md", instructionsStyleFixture)
	app := instructionsTestApp(t, house)
	app.Config.ActiveProfile = "marie"
	app.Config.ProfileInstructions = writeInstructions(t, "marie.md", "Sign off as Marie.")
	app.Flags.JSON = false

	stdout, _, err := runInstructionsCommand(t, app, "--brief")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(stdout, "Source: "+house+" (global)\n\n1. Speak only when it helps someone.\n2. Keep private context private.\n"), stdout)
	assert.Contains(t, stdout, "11. Show what is visual, and check what readers see.\n")
	assert.NotContains(t, stdout, "Writing in Basecamp")
	assert.NotContains(t, stdout, "Post when asked")
	assert.Contains(t, stdout, "(No list to cut down: run `basecamp instructions` for the full text.)")
	assert.True(t, strings.HasSuffix(stdout, instructionsPreface+"\n"), stdout)
}

func TestInstructionsCommandStripsTerminalControls(t *testing.T) {
	app := instructionsTestApp(t, writeInstructions(t, "style.md", "Be brief.\u001b]0;owned\u0007\u009b31m\u001b[2J"))
	app.Flags.JSON = false

	stdout, _, err := runInstructionsCommand(t, app)
	require.NoError(t, err)
	assert.Contains(t, stdout, "Be brief.")
	assert.NotContains(t, stdout, "\x1b")
	assert.NotContains(t, stdout, "\u009b")
	assert.NotContains(t, stdout, "\a")
}

func TestInstructionsCommandJSON(t *testing.T) {
	path := writeInstructions(t, "style.md", "Lead with the answer.")
	app, buf := setupConfigTestApp(t)
	app.Config.Instructions = path

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
	assert.Equal(t, path, env.Data.Documents[0].Source)
	assert.Equal(t, "Lead with the answer.", env.Data.Documents[0].Content)
	assert.Equal(t, []string{}, env.Data.Documents[0].Brief, "no list: an empty brief, not null")
	assert.NotNil(t, env.Data.Documents[0].ModifiedAt)
}

func TestConfigSetInstructionsFile(t *testing.T) {
	path := writeInstructions(t, "style.md", "Lead with the answer.")

	app, _ := setupConfigTestApp(t)
	err := executeConfigCommand(app, "set", "instructions", path)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "a local instructions is refused")
	assert.Contains(t, outErr.Message, "--global")

	app, _ = setupConfigTestApp(t)
	err = executeConfigCommand(app, "set", "--global", "instructions", filepath.Join(t.TempDir(), "gone.md"))
	require.True(t, errors.As(err, &outErr), "a file that does not exist is refused")
	assert.Equal(t, output.CodeNotFound, outErr.Code)

	app, buf := setupConfigTestApp(t)
	require.NoError(t, executeConfigCommand(app, "set", "--global", "instructions", path))
	var data map[string]any
	parseEnvelopeData(t, buf, &data)
	assert.Equal(t, path, data["value"])
	assert.Equal(t, "global", data["scope"])
}

// A relative path or one under ~ is saved absolute: the agent reads it from
// whatever directory it stands in, so the setting can't depend on it.
func TestConfigSetInstructionsSavesAnAbsolutePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(home, "style.md"), []byte("x"), 0o600))

	app, _ := setupConfigTestApp(t)
	saved := func() any {
		raw, err := os.ReadFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "basecamp", "config.json"))
		require.NoError(t, err)
		var written map[string]any
		require.NoError(t, json.Unmarshal(raw, &written))
		return written["instructions"]
	}

	require.NoError(t, executeConfigCommand(app, "set", "--global", "instructions", "~/style.md"))
	assert.Equal(t, filepath.Join(home, "style.md"), saved())

	work := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(work, "rel.md"), []byte("x"), 0o600))
	t.Chdir(work)
	require.NoError(t, executeConfigCommand(app, "set", "--global", "instructions", "rel.md"))
	assert.Equal(t, filepath.Join(work, "rel.md"), saved())
}

func TestConfigSetInstructionsForProfile(t *testing.T) {
	profileFile := writeInstructions(t, "marie.md", "Sign off as Marie.")
	app, buf := setupConfigTestApp(t)
	globalPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "basecamp", "config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(globalPath), 0o700))
	require.NoError(t, os.WriteFile(globalPath, []byte(`{"profiles": {"marie": {"base_url": "https://3.basecampapi.com"}}}`), 0o600))

	require.NoError(t, executeConfigCommand(app, "set", "--global", "--for-profile", "marie", "instructions", profileFile))
	var data map[string]any
	parseEnvelopeData(t, buf, &data)
	assert.Equal(t, "global, profile marie", data["scope"])

	var written map[string]any
	raw, err := os.ReadFile(globalPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &written))
	assert.Nil(t, written["instructions"], "the shared key is untouched")
	profile := written["profiles"].(map[string]any)["marie"].(map[string]any)
	assert.Equal(t, profileFile, profile["instructions"])
	assert.Equal(t, "https://3.basecampapi.com", profile["base_url"], "the rest of the profile stays")

	err = executeConfigCommand(app, "set", "--global", "--for-profile", "nobody", "instructions", profileFile)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "a profile not in the global config is refused")
	assert.Contains(t, outErr.Message, "nobody")

	err = executeConfigCommand(app, "set", "--global", "--for-profile", "marie", "format", "json")
	require.True(t, errors.As(err, &outErr), "--for-profile takes only instructions")

	require.NoError(t, os.WriteFile(globalPath, []byte(`{"profiles": {"marie": {"base_url": "https://3.basecampapi.com"}, "bare": {"account_id": "1"}}, "experimental": {"foo": true}}`), 0o600))
	require.NoError(t, executeConfigCommand(app, "set", "--global", "--for-profile", "marie", "instructions", profileFile))
	err = executeConfigCommand(app, "set", "--global", "--for-profile", "bare", "instructions", profileFile)
	require.True(t, errors.As(err, &outErr), "a profile without its base_url is not loaded, so it is refused")
	assert.Contains(t, outErr.Message, "base_url")

	err = executeConfigCommand(app, "unset", "--for-profile", "marie", "instructions")
	require.True(t, errors.As(err, &outErr), "unset --for-profile needs --global")
	assert.Contains(t, outErr.Message, "--global")
	err = executeConfigCommand(app, "unset", "--global", "--for-profile", "marie", "experimental.foo")
	require.True(t, errors.As(err, &outErr), "unset --for-profile takes only instructions")
	raw, err = os.ReadFile(globalPath)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"foo"`, "the experimental key stays")

	buf.Reset()
	require.NoError(t, executeConfigCommand(app, "unset", "--global", "--for-profile", "marie", "instructions"))
	raw, err = os.ReadFile(globalPath)
	require.NoError(t, err)
	written = nil
	require.NoError(t, json.Unmarshal(raw, &written))
	profile = written["profiles"].(map[string]any)["marie"].(map[string]any)
	assert.NotContains(t, profile, "instructions")
	assert.Equal(t, "https://3.basecampapi.com", profile["base_url"])
}

// loadLocal loads instructions with a fetcher that fails the test if any
// URL is fetched: these sources are local files.
func loadLocal(app *appctx.App) (instructionsResult, error) {
	return loadInstructions(context.Background(), app, false, time.Now, func(context.Context, instructionsSource) (instructionsFetched, error) {
		panic("a local file must not be fetched")
	})
}

func TestParseInstructionsSource(t *testing.T) {
	src, err := parseInstructionsSource("https://github.com/basecamp/coworker/blob/main/docs/basecamp%20style.md")
	require.NoError(t, err)
	assert.Equal(t, instructionsKindGitHub, src.Kind)
	assert.Equal(t, "basecamp", src.Owner)
	assert.Equal(t, "coworker", src.Repo)
	assert.Equal(t, "main", src.Ref)
	assert.Equal(t, "docs/basecamp style.md", src.File)
	assert.Equal(t, "https://raw.githubusercontent.com/basecamp/coworker/main/docs/basecamp%20style.md", src.URL)

	src, err = parseInstructionsSource("https://example.com/style.md")
	require.NoError(t, err)
	assert.Equal(t, instructionsKindURL, src.Kind)

	src, err = parseInstructionsSource("/home/someone/style.md")
	require.NoError(t, err)
	assert.Equal(t, instructionsKindFile, src.Kind)

	for _, bad := range []string{
		"",
		"style.md",                               // relative: would follow the working directory
		"http://example.com/style.md",            // plain http off localhost
		"ftp://example.com/style.md",             // not https
		"https://user:pass@example.com/style.md", // credentials in the URL
		"https://github.com/basecamp/coworker",   // a repo, not a file
		"https://github.com/basecamp/coworker/tree/main/docs", // a directory
		"https://github.com/basecamp/coworker/blob/main",      // no file
		"https://github.com/basecamp/coworker/blob/main/../x.md",
	} {
		_, err := parseInstructionsSource(bad)
		var outErr *output.Error
		require.Truef(t, errors.As(err, &outErr), "%q must be refused", bad)
		assert.Equal(t, output.CodeUsage, outErr.Code, bad)
	}
}

// fakeInstructionsFetcher counts fetches and answers with content or err.
type fakeInstructionsFetcher struct {
	calls   int
	content string
	err     error
}

func (f *fakeInstructionsFetcher) fetch(context.Context, instructionsSource) (instructionsFetched, error) {
	f.calls++
	if f.err != nil {
		return instructionsFetched{}, f.err
	}
	return instructionsFetched{Content: []byte(f.content), Via: "https"}, nil
}

const instructionsTestURL = "https://example.com/style.md"

func instructionsURLApp(t *testing.T) *appctx.App {
	t.Helper()
	app := instructionsTestApp(t, instructionsTestURL)
	app.Config.CacheEnabled = true
	return app
}

// A URL is fetched once and served from the cache for instructionsMaxAge;
// after that, or with --refresh, it is fetched again.
func TestLoadInstructionsURLCacheFreshness(t *testing.T) {
	app := instructionsURLApp(t)
	f := &fakeInstructionsFetcher{content: "v1"}
	clock := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }

	result, err := loadInstructions(context.Background(), app, false, now, f.fetch)
	require.NoError(t, err)
	assert.Equal(t, "v1", result.Documents[0].Content)
	assert.Equal(t, "https", result.Documents[0].Via)
	assert.Equal(t, instructionsKindURL, result.Documents[0].Kind)
	assert.Equal(t, 1, f.calls)

	f.content = "v2"
	clock = clock.Add(instructionsMaxAge - time.Second)
	result, err = loadInstructions(context.Background(), app, false, now, f.fetch)
	require.NoError(t, err)
	assert.Equal(t, "v1", result.Documents[0].Content, "still fresh: served from the cache")
	assert.Equal(t, "cache", result.Documents[0].Via)
	assert.Equal(t, 1, f.calls)

	result, err = loadInstructions(context.Background(), app, true, now, f.fetch)
	require.NoError(t, err)
	assert.Equal(t, "v2", result.Documents[0].Content, "--refresh fetches now")
	assert.Equal(t, 2, f.calls)

	f.content = "v3"
	clock = clock.Add(instructionsMaxAge)
	result, err = loadInstructions(context.Background(), app, false, now, f.fetch)
	require.NoError(t, err)
	assert.Equal(t, "v3", result.Documents[0].Content, "expired: fetched again")
	assert.Equal(t, 3, f.calls)

	clock = clock.Add(-time.Hour)
	_, err = loadInstructions(context.Background(), app, false, now, f.fetch)
	require.NoError(t, err)
	assert.Equal(t, 4, f.calls, "a copy dated in the future is not trusted as fresh")
}

// A failed fetch serves the last copy, marked stale, whatever made it fail;
// with no copy, it is an error, never silence.
func TestLoadInstructionsURLFallsBackToTheLastCopy(t *testing.T) {
	app := instructionsURLApp(t)
	f := &fakeInstructionsFetcher{err: errors.New("offline")}

	_, err := loadInstructions(context.Background(), app, false, time.Now, f.fetch)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "no copy: the command fails")
	assert.Equal(t, output.CodeNetwork, outErr.Code)
	assert.Contains(t, outErr.Message, "offline")

	f.err = nil
	f.content = "v1"
	_, err = loadInstructions(context.Background(), app, false, time.Now, f.fetch)
	require.NoError(t, err)

	for _, failure := range []*fakeInstructionsFetcher{
		{err: errors.New("offline")},
		{content: strings.Repeat("a", instructionsMaxBytes+1)},
		{content: "\xff\xfe"},
	} {
		result, err := loadInstructions(context.Background(), app, true, time.Now, failure.fetch)
		require.NoError(t, err)
		doc := result.Documents[0]
		assert.Equal(t, "v1", doc.Content)
		assert.True(t, doc.Stale)
		assert.NotEmpty(t, doc.StaleReason)
	}
}

// With the cache off, every run fetches, and a failure has nothing to fall
// back on.
func TestLoadInstructionsURLWithoutCache(t *testing.T) {
	app := instructionsURLApp(t)
	app.Config.CacheEnabled = false
	f := &fakeInstructionsFetcher{content: "v1"}
	for range 2 {
		_, err := loadInstructions(context.Background(), app, false, time.Now, f.fetch)
		require.NoError(t, err)
	}
	assert.Equal(t, 2, f.calls)

	f.err = errors.New("offline")
	_, err := loadInstructions(context.Background(), app, false, time.Now, f.fetch)
	require.Error(t, err)
}

// A cached copy is followed like the source, so a cache_dir set by a local
// or repo config is not where it is kept.
func TestInstructionsCacheIgnoresARepoCacheDir(t *testing.T) {
	app := instructionsURLApp(t)
	repoDir := t.TempDir()
	app.Config.CacheDir = repoDir
	app.Config.Sources["cache_dir"] = "repo"
	src, err := parseInstructionsSource(instructionsTestURL)
	require.NoError(t, err)
	path := instructionsCachePath(app, src)
	assert.NotEmpty(t, path)
	assert.False(t, strings.HasPrefix(path, repoDir), path)
}

// A GitHub file goes through gh, and falls back to plain HTTPS when gh fails.
func TestFetchInstructionsGitHubPrefersGH(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/basecamp/coworker/main/docs/style.md" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("from https"))
	}))
	defer server.Close()
	origBase, origGH := instructionsRawGitHubBase, instructionsGH
	t.Cleanup(func() { instructionsRawGitHubBase, instructionsGH = origBase, origGH })
	instructionsRawGitHubBase = server.URL

	src, err := parseInstructionsSource("https://github.com/basecamp/coworker/blob/main/docs/style.md")
	require.NoError(t, err)

	var asked instructionsSource
	instructionsGH = func(_ context.Context, s instructionsSource) ([]byte, error) {
		asked = s
		return []byte("from gh"), nil
	}
	got, err := fetchInstructions(context.Background(), src)
	require.NoError(t, err)
	assert.Equal(t, "from gh", string(got.Content))
	assert.Equal(t, "gh", got.Via)
	assert.Equal(t, "docs/style.md", asked.File)

	instructionsGH = func(context.Context, instructionsSource) ([]byte, error) {
		return nil, errors.New("gh is not installed")
	}
	got, err = fetchInstructions(context.Background(), src)
	require.NoError(t, err)
	assert.Equal(t, "from https", string(got.Content))
	assert.Equal(t, "https", got.Via)

	missing, err := parseInstructionsSource("https://github.com/basecamp/coworker/blob/main/gone.md")
	require.NoError(t, err)
	_, err = fetchInstructions(context.Background(), missing)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gh is not installed")
	assert.Contains(t, err.Error(), "404")
}

// The command fetches a URL source and prints it like a file.
func TestInstructionsCommandFetchesAURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("Lead with the answer.\n"))
	}))
	defer server.Close()
	app := instructionsTestApp(t, server.URL+"/style.md")
	app.Config.CacheEnabled = true
	app.Flags.JSON = false

	stdout, _, err := runInstructionsCommand(t, app)
	require.NoError(t, err)
	assert.Contains(t, stdout, "Source: "+server.URL+"/style.md (global)")
	assert.Contains(t, stdout, "Lead with the answer.")
}

// The stale warning goes to stderr in machine modes too, where a --jq filter
// may keep the content and drop the flag.
func TestInstructionsStaleWarningInMachineOutput(t *testing.T) {
	app := instructionsURLApp(t)
	fetched := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	result := instructionsResult{Configured: true, Documents: []instructionsDocument{
		{Scope: "global", Source: instructionsTestURL, Content: "v1", Stale: true, StaleReason: "offline", FetchedAt: &fetched},
	}}
	cmd := NewInstructionsCmd()
	stderr := &bytes.Buffer{}
	cmd.SetErr(stderr)

	require.NoError(t, renderInstructions(cmd, app, result, false))
	assert.Contains(t, stderr.String(), "could not fetch the instructions at "+instructionsTestURL+" (offline)")
}

// A URL is saved as given, without a fetch; one that can't be a source is
// refused.
func TestConfigSetInstructionsURL(t *testing.T) {
	const blob = "https://github.com/basecamp/coworker/blob/main/docs/basecamp-style.md"
	app, buf := setupConfigTestApp(t)
	require.NoError(t, executeConfigCommand(app, "set", "--global", "instructions", blob))
	var data map[string]any
	parseEnvelopeData(t, buf, &data)
	assert.Equal(t, blob, data["value"])

	err := executeConfigCommand(app, "set", "--global", "instructions", "https://github.com/basecamp/coworker")
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "a repo, not a file, is refused")
	assert.Contains(t, outErr.Message, "does not name a file")
}

// Only blank lines and trailing space are trimmed: an indented first line is
// a Markdown code block and keeps its indentation.
func TestInstructionsKeepLeadingIndentation(t *testing.T) {
	result, err := loadLocal(instructionsTestApp(t, writeInstructions(t, "style.md", "\n\n    basecamp instructions\n\nRun it first.  \n\n")))
	require.NoError(t, err)
	assert.Equal(t, "    basecamp instructions\n\nRun it first.", result.Documents[0].Content)
}

// A profile name is not validated on load, so the scope it lands in is
// stripped of terminal controls like everything else printed.
func TestInstructionsCommandSanitizesTheScope(t *testing.T) {
	app := instructionsTestApp(t, "")
	app.Config.ActiveProfile = "evil\u001b[2J\u009b31m"
	app.Config.ProfileInstructions = writeInstructions(t, "p.md", "Be brief.")
	app.Flags.JSON = false

	stdout, _, err := runInstructionsCommand(t, app)
	require.NoError(t, err)
	assert.Contains(t, stdout, "profile:evil")
	assert.NotContains(t, stdout, "\x1b")
	assert.NotContains(t, stdout, "\u009b")
}

// instructions lives in the global config alone, so unsetting it anywhere
// else would report success and leave agents following it.
func TestConfigUnsetInstructionsNeedsGlobal(t *testing.T) {
	app, _ := setupConfigTestApp(t)
	err := executeConfigCommand(app, "unset", "instructions")
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Contains(t, outErr.Message, "--global")
}

func TestReadInstructionsFileStopsAtTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.md")
	require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte("a"), 10*instructionsMaxBytes), 0o600))

	data, err := readInstructionsBounded(path)

	require.NoError(t, err)
	assert.Len(t, data, instructionsMaxBytes+1)
}

func TestInstructionsRedirectsKeepToHTTPS(t *testing.T) {
	via := []*http.Request{{}}
	redirect := func(raw string) error {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		return checkInstructionsRedirect(&http.Request{URL: u}, via)
	}

	assert.NoError(t, redirect("https://example.com/style.md"))
	assert.NoError(t, redirect("http://localhost:3000/style.md"))
	assert.Error(t, redirect("http://example.com/style.md"))
	assert.Error(t, redirect("https://user:secret@example.com/style.md"))
	assert.Error(t, redirect("ftp://example.com/style.md"))

	via = make([]*http.Request, 10)
	assert.Error(t, redirect("https://example.com/style.md"))
}

func TestInstructionsHTTPClientRefusesACleartextRedirect(t *testing.T) {
	require.NotNil(t, instructionsHTTPClient.CheckRedirect)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/style.md", http.StatusFound)
	}))
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = instructionsHTTPClient.CheckRedirect
	orig := instructionsHTTPClient
	instructionsHTTPClient = client
	t.Cleanup(func() { instructionsHTTPClient = orig })

	_, err := fetchInstructionsHTTPS(context.Background(), server.URL+"/style.md")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be https")
}

// fakeGH puts a gh on PATH that records its arguments and runs body.
func fakeGH(t *testing.T, body string) (argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n" + body + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o700)) //nolint:gosec // G306: a test executable
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

func TestInstructionsGHIsPinnedToGitHub(t *testing.T) {
	args := fakeGH(t, "echo hello")
	src, err := parseInstructionsSource("https://github.com/o/r/blob/main/style.md")
	require.NoError(t, err)

	out, err := instructionsGH(context.Background(), src)

	require.NoError(t, err)
	assert.Equal(t, "hello\n", string(out))
	got, err := os.ReadFile(args)
	require.NoError(t, err)
	assert.Contains(t, string(got), "--hostname\ngithub.com\n")
}

func TestInstructionsGHStopsAtTheLimit(t *testing.T) {
	// Finite output, then a hang: reading past the limit fails at the
	// deadline instead of filling memory.
	fakeGH(t, "head -c 200000 /dev/zero | tr '\\0' a; exec sleep 30")
	src, err := parseInstructionsSource("https://github.com/o/r/blob/main/style.md")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := instructionsGH(ctx, src)

	require.NoError(t, err)
	assert.Len(t, out, instructionsMaxBytes+1)
	assert.NoError(t, ctx.Err(), "the read stopped at the limit, not at the deadline")
}

func TestInstructionsGHReportsItsError(t *testing.T) {
	fakeGH(t, "echo 'HTTP 404: Not Found' >&2; exit 1")
	src, err := parseInstructionsSource("https://github.com/o/r/blob/main/style.md")
	require.NoError(t, err)

	_, err = instructionsGH(context.Background(), src)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 404")
}

func TestFetchInstructionsGivesTheFallbackItsOwnDeadline(t *testing.T) {
	origGH, origTimeout := instructionsGH, instructionsFetchTimeout
	t.Cleanup(func() { instructionsGH, instructionsFetchTimeout = origGH, origTimeout })
	instructionsFetchTimeout = 20 * time.Millisecond
	instructionsGH = func(ctx context.Context, _ instructionsSource) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("from https"))
	}))
	defer server.Close()
	src := instructionsSource{Kind: instructionsKindGitHub, URL: server.URL}

	fetched, err := fetchInstructions(context.Background(), src)

	require.NoError(t, err)
	assert.Equal(t, "https", fetched.Via)
}
