package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
)

// runAgentHookWithConfig runs the plugin's SessionStart hook on cfg.
func runAgentHookWithConfig(t *testing.T, cfg *config.Config, input string) string {
	t.Helper()
	t.Setenv("CLAUDE_PLUGIN_ROOT", "/home/u/.claude/plugins/cache/37signals/basecamp-cli/0.12.0")
	t.Setenv("CLAUDE_PLUGIN_DATA", t.TempDir())
	t.Setenv("PLUGIN_ROOT", "")
	if cfg.CacheDir == config.Default().CacheDir {
		cfg.CacheDir = t.TempDir() // never the developer's own cached copy
	}
	app := appctx.NewApp(cfg)
	t.Cleanup(app.Close)
	cmd := NewAgentHookCmd()
	var stdout bytes.Buffer
	cmd.SetIn(strings.NewReader(input))
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"pre-commit-snapshot"})
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	require.NoError(t, cmd.Execute())
	return strings.TrimSpace(stdout.String())
}

func TestAgentHookPointsAtInstructionsWhenSet(t *testing.T) {
	cfg := config.Default()
	cfg.Instructions = "https://github.com/basecamp/coworker/blob/main/docs/basecamp-style.md"

	for _, source := range []string{"startup", "resume", "clear", "compact"} {
		output := runAgentHookWithConfig(t, cfg, `{"hook_event_name":"SessionStart","session_id":"s","source":"`+source+`"}`)
		require.NotEmpty(t, output, source)
		event, context := hookSpecificOutput(t, output)
		assert.Equal(t, "SessionStart", event)
		assert.Contains(t, context, "`basecamp instructions`", source)
		assert.Contains(t, context, "script or tool that posts it", source)
		var reply map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(output), &reply), source)
		assert.NotContains(t, reply, "systemMessage", "the pointer is for the model, not the user")
	}
}

func TestAgentHookPointsAtProfileInstructions(t *testing.T) {
	cfg := config.Default()
	cfg.ProfileInstructions = "/home/u/agent-voice.md"

	_, context := hookSpecificOutput(t, runAgentHookWithConfig(t, cfg, sessionStartPayload))
	assert.Contains(t, context, "`basecamp instructions`")
}

// A session that starts on a profile with no instructions can still post as
// one that has some, so instructions set on any configured profile count.
func TestAgentHookPointsAtAnotherProfilesInstructions(t *testing.T) {
	cfg := config.Default()
	cfg.Profiles = map[string]*config.ProfileConfig{
		"human": {BaseURL: "https://3.basecampapi.com"},
		"agent": {BaseURL: "https://3.basecampapi.com", Instructions: "/home/u/agent-voice.md"},
	}
	require.Empty(t, cfg.ProfileInstructions, "the active profile has none of its own")

	_, context := hookSpecificOutput(t, runAgentHookWithConfig(t, cfg, sessionStartPayload))
	assert.Contains(t, context, "`basecamp instructions`")
	assert.Contains(t, context, "-P <profile>")
}

func TestAgentHookSilentWhenNoProfileHasInstructions(t *testing.T) {
	cfg := config.Default()
	cfg.Profiles = map[string]*config.ProfileConfig{
		"human": {BaseURL: "https://3.basecampapi.com"},
		"agent": {BaseURL: "https://3.basecampapi.com"},
	}
	assert.Empty(t, runAgentHookWithConfig(t, cfg, sessionStartPayload))
}

func TestAgentHookSilentWithoutInstructions(t *testing.T) {
	assert.Empty(t, runAgentHookWithConfig(t, config.Default(), sessionStartPayload))
}

func TestAgentHookInstructionsPointerOnlyAtSessionStart(t *testing.T) {
	cfg := config.Default()
	cfg.Instructions = "/home/u/style.md"

	output := runAgentHookWithConfig(t, cfg,
		`{"hook_event_name":"PreToolUse","session_id":"s","tool_use_id":"t","tool_input":{"command":"basecamp comments create 1 hi"}}`)
	assert.Empty(t, output)
}

// failInstructionsFetches makes any fetch of an instructions URL fail the
// test: the hook has a 5-second budget and must never wait on the network.
func failInstructionsFetches(t *testing.T) {
	t.Helper()
	client, gh := instructionsHTTPClient, instructionsGH
	t.Cleanup(func() { instructionsHTTPClient, instructionsGH = client, gh })
	instructionsHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("the hook fetched %s", r.URL)
		return nil, errors.New("no fetching in the hook")
	})}
	instructionsGH = func(context.Context, instructionsSource) ([]byte, error) {
		t.Error("the hook ran gh")
		return nil, errors.New("no fetching in the hook")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// cacheInstructions stores content as the cached copy of source, fetched at.
func cacheInstructions(t *testing.T, cfg *config.Config, source, content string, at time.Time) {
	t.Helper()
	src, err := parseInstructionsSource(source)
	require.NoError(t, err)
	path := instructionsCachePath(&appctx.App{Config: cfg}, src)
	require.NotEmpty(t, path)
	writeInstructionsCache(path, instructionsCacheEntry{Source: src.Raw, Content: content, FetchedAt: at})
	_, err = os.Stat(path)
	require.NoError(t, err)
}

// With the source cached, however long ago, the pointer carries the gist,
// read from the cache with no fetch.
func TestAgentHookCarriesTheGistFromTheCache(t *testing.T) {
	failInstructionsFetches(t)
	cfg := config.Default()
	cfg.CacheDir = t.TempDir()
	cfg.Instructions = "https://github.com/basecamp/coworker/blob/main/docs/basecamp-style.md"
	cacheInstructions(t, cfg, cfg.Instructions, instructionsStyleFixture, time.Now().Add(-30*24*time.Hour))

	output := runAgentHookWithConfig(t, cfg, sessionStartPayload)
	_, context := hookSpecificOutput(t, output)
	pointer, gist, found := strings.Cut(context, "\n\nThe gist (run the command for the full text):\n")
	require.True(t, found, context)
	assert.Contains(t, pointer, "`basecamp instructions`")
	assert.True(t, strings.HasPrefix(gist, "1. Speak only when it helps someone.\n2. Keep private context private.\n"), gist)
	assert.True(t, strings.HasSuffix(gist, "11. Show what is visual, and check what readers see."), gist)
	assert.NotContains(t, gist, "Post when asked", "the gist, not the text")
	assert.Less(t, len(context), 1500, "the whole notice stays short:\n%s", context)
}

// A source never fetched has no copy to read, and the hook won't fetch it,
// so it points at the command alone.
func TestAgentHookPointerAloneWithoutACache(t *testing.T) {
	failInstructionsFetches(t)
	cfg := config.Default()
	cfg.CacheDir = t.TempDir()
	cfg.Instructions = "https://github.com/basecamp/coworker/blob/main/docs/basecamp-style.md"

	_, context := hookSpecificOutput(t, runAgentHookWithConfig(t, cfg, sessionStartPayload))
	assert.Contains(t, context, "`basecamp instructions`")
	assert.NotContains(t, context, "The gist")
}

// One layer cached and the other not is still a gist that would leave
// rules out: the pointer goes alone.
func TestAgentHookPointerAloneWhenOneLayerIsUncached(t *testing.T) {
	failInstructionsFetches(t)
	cfg := config.Default()
	cfg.CacheDir = t.TempDir()
	cfg.Instructions = writeInstructions(t, "house.md", instructionsStyleFixture)
	cfg.ActiveProfile = "marie"
	cfg.ProfileInstructions = "https://example.com/marie.md"

	_, context := hookSpecificOutput(t, runAgentHookWithConfig(t, cfg, sessionStartPayload))
	assert.NotContains(t, context, "The gist")
}

// A local file is read as the command reads it, and with two layers each is
// named, baseline first.
func TestAgentHookGistNamesEachLayer(t *testing.T) {
	failInstructionsFetches(t)
	cfg := config.Default()
	cfg.Instructions = writeInstructions(t, "house.md", "- **Lead with the point.** Then the rest.")
	cfg.Sources = map[string]string{"instructions": "global"}
	cfg.ActiveProfile = "marie"
	cfg.ProfileInstructions = writeInstructions(t, "marie.md", "- Sign off as Marie. Always.")

	_, context := hookSpecificOutput(t, runAgentHookWithConfig(t, cfg, sessionStartPayload))
	assert.Contains(t, context, "The gist (run the command for the full text):\n"+
		"From global:\n- Lead with the point.\nFrom profile:marie:\n- Sign off as Marie.")
}

// Sources with no list have no gist to give.
func TestAgentHookPointerAloneWhenNothingIsAList(t *testing.T) {
	cfg := config.Default()
	cfg.Instructions = writeInstructions(t, "house.md", "Write plainly. Lead with the point.")

	_, context := hookSpecificOutput(t, runAgentHookWithConfig(t, cfg, sessionStartPayload))
	assert.Contains(t, context, "`basecamp instructions`")
	assert.NotContains(t, context, "The gist")
}

// One source with no list would leave its rules out of a gist built from
// the others, the later, winning source included: the pointer goes alone.
func TestAgentHookPointerAloneWhenOneSourceIsNotAList(t *testing.T) {
	cfg := config.Default()
	cfg.Instructions = writeInstructions(t, "house.md", "- **Lead with the point.** Then the rest.")
	cfg.ActiveProfile = "marie"
	cfg.ProfileInstructions = writeInstructions(t, "marie.md", "Sign off as Marie. Always.")

	_, context := hookSpecificOutput(t, runAgentHookWithConfig(t, cfg, sessionStartPayload))
	assert.Contains(t, context, "`basecamp instructions`")
	assert.NotContains(t, context, "The gist")
}

// A gist too long for the hook's context isn't cut off, which would drop the
// later sources: the pointer goes alone.
func TestAgentHookPointerAloneWhenTheGistIsTooLong(t *testing.T) {
	var rules strings.Builder
	for range 60 {
		rules.WriteString("- " + strings.Repeat("word ", 39) + "end.\n")
	}
	cfg := config.Default()
	cfg.Instructions = writeInstructions(t, "house.md", rules.String())

	_, context := hookSpecificOutput(t, runAgentHookWithConfig(t, cfg, sessionStartPayload))
	assert.Contains(t, context, "`basecamp instructions`")
	assert.NotContains(t, context, "The gist")
	assert.Less(t, len(context), 10000)
}

// The command keeps a profile's cached copy under profiles/<name>, so that
// is where the hook looks for it, while its own state stays where it was.
func TestAgentHookReadsTheProfilesCache(t *testing.T) {
	failInstructionsFetches(t)
	t.Setenv("BASECAMP_CACHE_DIR", "")
	cfg := config.Default()
	cfg.CacheDir = t.TempDir()
	cfg.ActiveProfile = "chef"
	cfg.Instructions = "https://github.com/basecamp/coworker/blob/main/docs/basecamp-style.md"
	scoped := *cfg
	scoped.CacheDir = filepath.Join(cfg.CacheDir, "profiles", "chef")
	cacheInstructions(t, &scoped, cfg.Instructions, instructionsStyleFixture, time.Now())

	_, context := hookSpecificOutput(t, runAgentHookWithConfig(t, cfg, sessionStartPayload))
	assert.Contains(t, context, "The gist (run the command for the full text):\n1. Speak only when it helps someone.")
}
