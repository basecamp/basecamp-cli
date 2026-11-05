package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

const (
	instructionsTestURL  = "https://3.basecamp.com/99999/buckets/555/documents/42"
	instructionsTestPath = "/99999/documents/42"
)

func instructionsDocBody(title, html string) string {
	body, _ := json.Marshal(map[string]any{
		"id":                  42,
		"status":              "active",
		"title":               title,
		"type":                "Document",
		"content":             html,
		"created_at":          "2026-11-01T10:00:00.000Z",
		"updated_at":          "2026-11-05T10:00:00.000Z",
		"content_attachments": []any{},
	})
	return string(body)
}

func TestParseInstructionsDoc(t *testing.T) {
	ref, err := parseInstructionsDoc(instructionsTestURL)
	require.NoError(t, err)
	assert.Equal(t, "99999", ref.AccountID)
	assert.Equal(t, int64(42), ref.DocumentID)

	for _, bad := range []string{
		"",
		"not a url",
		"https://3.basecamp.com/99999/buckets/555/messages/42",
		"https://3.basecamp.com/99999/buckets/555/documents/42#__recording_7",
		"https://3.basecamp.com/99999/projects/555",
	} {
		_, err := parseInstructionsDoc(bad)
		var outErr *output.Error
		require.Truef(t, errors.As(err, &outErr), "%q must be refused", bad)
		assert.Equal(t, output.CodeUsage, outErr.Code)
	}
}

// fakeInstructionsFetcher counts reads and answers with content or err.
type fakeInstructionsFetcher struct {
	calls   int
	content string
	err     error
}

func (f *fakeInstructionsFetcher) fetch(context.Context, instructionsDocRef) (instructionsFetched, error) {
	f.calls++
	if f.err != nil {
		return instructionsFetched{}, f.err
	}
	return instructionsFetched{Title: "House style", Content: f.content}, nil
}

func instructionsTestApp(t *testing.T) *appctx.App {
	t.Helper()
	app, _ := setupConfigTestApp(t)
	app.Config.AccountID = "99999"
	app.Config.CacheEnabled = true
	app.Config.InstructionsDoc = instructionsTestURL
	app.Config.Sources["instructions_doc"] = "global"
	return app
}

func TestLoadInstructionsNotConfiguredReadsNothing(t *testing.T) {
	app := instructionsTestApp(t)
	app.Config.InstructionsDoc = ""
	f := &fakeInstructionsFetcher{}

	result, err := loadInstructions(context.Background(), app, false, time.Now, f.fetch)
	require.NoError(t, err)
	assert.False(t, result.Configured)
	assert.False(t, result.Applies)
	assert.Empty(t, result.Documents)
	assert.Zero(t, f.calls)
}

// A document in one account never shapes posts in another.
func TestLoadInstructionsOtherAccountDoesNotApply(t *testing.T) {
	app := instructionsTestApp(t)
	app.Config.AccountID = "11111"
	f := &fakeInstructionsFetcher{content: "Be brief."}

	result, err := loadInstructions(context.Background(), app, false, time.Now, f.fetch)
	require.NoError(t, err)
	assert.True(t, result.Configured)
	assert.False(t, result.Applies)
	assert.Contains(t, result.Reason, "account 99999")
	assert.Empty(t, result.Documents)
	assert.Zero(t, f.calls, "a document that does not apply is not read")
}

// The cache serves a copy younger than instructionsMaxAge, reads the document
// again once it is older, and --refresh always reads it.
func TestLoadInstructionsCacheFreshness(t *testing.T) {
	app := instructionsTestApp(t)
	f := &fakeInstructionsFetcher{content: "v1"}
	clock := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }

	result, err := loadInstructions(context.Background(), app, false, now, f.fetch)
	require.NoError(t, err)
	require.True(t, result.Applies)
	assert.Equal(t, "v1", result.Documents[0].Content)
	assert.Equal(t, 1, f.calls)

	f.content = "v2"
	clock = clock.Add(instructionsMaxAge - time.Second)
	result, err = loadInstructions(context.Background(), app, false, now, f.fetch)
	require.NoError(t, err)
	assert.Equal(t, "v1", result.Documents[0].Content, "a fresh copy comes from the cache")
	assert.Equal(t, 1, f.calls)

	result, err = loadInstructions(context.Background(), app, true, now, f.fetch)
	require.NoError(t, err)
	assert.Equal(t, "v2", result.Documents[0].Content, "--refresh reads the document")
	assert.Equal(t, 2, f.calls)

	f.content = "v3"
	clock = clock.Add(instructionsMaxAge)
	result, err = loadInstructions(context.Background(), app, false, now, f.fetch)
	require.NoError(t, err)
	assert.Equal(t, "v3", result.Documents[0].Content, "an expired copy is read again")
	assert.Equal(t, 3, f.calls)
	assert.False(t, result.Documents[0].Stale)
}

// When Basecamp cannot be read, the last copy is served and marked stale;
// with no copy at all, the error is the answer.
func TestLoadInstructionsFallsBackToStaleCopy(t *testing.T) {
	app := instructionsTestApp(t)
	f := &fakeInstructionsFetcher{content: "v1"}
	clock := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }

	_, err := loadInstructions(context.Background(), app, false, now, f.fetch)
	require.NoError(t, err)

	f.err = errors.New("offline")
	clock = clock.Add(2 * instructionsMaxAge)
	result, err := loadInstructions(context.Background(), app, false, now, f.fetch)
	require.NoError(t, err)
	require.True(t, result.Applies)
	assert.Equal(t, "v1", result.Documents[0].Content)
	assert.True(t, result.Documents[0].Stale)
	assert.Equal(t, "offline", result.Documents[0].StaleReason)

	empty := instructionsTestApp(t)
	_, err = loadInstructions(context.Background(), empty, false, now, f.fetch)
	require.Error(t, err)
}

func TestLoadInstructionsWithoutCacheAlwaysReads(t *testing.T) {
	app := instructionsTestApp(t)
	app.Config.CacheEnabled = false
	f := &fakeInstructionsFetcher{content: "v1"}

	for range 2 {
		_, err := loadInstructions(context.Background(), app, false, time.Now, f.fetch)
		require.NoError(t, err)
	}
	assert.Equal(t, 2, f.calls)
	entries, _ := os.ReadDir(filepath.Join(app.Config.CacheDir, "instructions"))
	assert.Empty(t, entries, "nothing is written with the cache off")
}

func instructionsCommandApp(t *testing.T, machine bool) (*appctx.App, *recordingTransport, *bytes.Buffer) {
	t.Helper()
	app, transport := setupRecordingTestApp(t, stubRoute{
		method: http.MethodGet,
		path:   instructionsTestPath,
		status: http.StatusOK,
		body:   instructionsDocBody("House style", "<p>Lead with the answer.</p><ul><li>Link every reference.</li></ul>"),
	})
	app.Config.InstructionsDoc = instructionsTestURL
	app.Config.CacheEnabled = true
	app.Config.CacheDir = t.TempDir()
	app.Config.Sources = map[string]string{"instructions_doc": "global"}
	out := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: out})
	app.Flags.JSON = machine
	return app, transport, out
}

// Run plainly, the command prints the document as Markdown under the
// preface that bounds its authority.
func TestInstructionsCommandPrintsMarkdown(t *testing.T) {
	app, transport, _ := instructionsCommandApp(t, false)

	cmd := NewInstructionsCmd()
	stdout := &bytes.Buffer{}
	cmd.SetArgs(nil)
	cmd.SetContext(appctx.WithApp(context.Background(), app))
	cmd.SetOut(stdout)
	cmd.SetErr(&bytes.Buffer{})
	require.NoError(t, cmd.Execute())

	text := stdout.String()
	assert.Contains(t, text, "# House style")
	assert.Contains(t, text, instructionsPreface)
	assert.Contains(t, text, "Lead with the answer.")
	assert.Contains(t, text, "- Link every reference.")
	assert.NotContains(t, text, "<p>")
	assert.Len(t, transport.queriesFor(instructionsTestPath), 1)
}

func TestInstructionsCommandJSON(t *testing.T) {
	app, _, out := instructionsCommandApp(t, true)

	require.NoError(t, executeRecordingCommand(NewInstructionsCmd(), app))

	var env struct {
		Summary string             `json:"summary"`
		Data    instructionsResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &env), out.String())
	assert.Equal(t, instructionsPreface, env.Summary)
	assert.True(t, env.Data.Applies)
	require.Len(t, env.Data.Documents, 1)
	doc := env.Data.Documents[0]
	assert.Equal(t, "global", doc.Scope)
	assert.Equal(t, "99999", doc.AccountID)
	assert.Contains(t, doc.Content, "Lead with the answer.")
}

func TestConfigSetInstructionsDoc(t *testing.T) {
	app, _ := setupConfigTestApp(t)
	err := executeConfigCommand(app, "set", "instructions_doc", instructionsTestURL)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "a local instructions_doc is refused")
	assert.Contains(t, outErr.Message, "--global")

	app, _ = setupConfigTestApp(t)
	err = executeConfigCommand(app, "set", "--global", "instructions_doc", "https://3.basecamp.com/99999/buckets/555/messages/42")
	require.True(t, errors.As(err, &outErr), "a URL that is not a document is refused")
	assert.Equal(t, output.CodeUsage, outErr.Code)

	app, buf := setupConfigTestApp(t)
	require.NoError(t, executeConfigCommand(app, "set", "--global", "instructions_doc", instructionsTestURL))
	var data map[string]any
	parseEnvelopeData(t, buf, &data)
	assert.Equal(t, instructionsTestURL, data["value"])
	assert.Equal(t, "global", data["scope"])
}

// layeredFetcher answers each document id with its own content.
type layeredFetcher map[int64]string

func (f layeredFetcher) fetch(_ context.Context, ref instructionsDocRef) (instructionsFetched, error) {
	return instructionsFetched{Title: fmt.Sprintf("doc %d", ref.DocumentID), Content: f[ref.DocumentID]}, nil
}

// The shared baseline comes first and the active profile's own document
// after it, so the more specific one is read last and wins.
func TestLoadInstructionsLayersProfileOverBaseline(t *testing.T) {
	app := instructionsTestApp(t)
	app.Config.ActiveProfile = "marie"
	app.Config.ProfileInstructionsDoc = "https://3.basecamp.com/99999/buckets/555/documents/43"
	f := layeredFetcher{42: "Lead with the answer.", 43: "Sign off as Marie."}

	result, err := loadInstructions(context.Background(), app, false, time.Now, f.fetch)
	require.NoError(t, err)
	require.True(t, result.Applies)
	require.Len(t, result.Documents, 2)
	assert.Equal(t, "global", result.Documents[0].Scope)
	assert.Equal(t, "Lead with the answer.", result.Documents[0].Content)
	assert.Equal(t, "profile:marie", result.Documents[1].Scope)
	assert.Equal(t, "Sign off as Marie.", result.Documents[1].Content)
}

// Each layer is checked against the account on its own: a profile document
// in another account is skipped while the baseline still applies.
func TestLoadInstructionsSkipsALayerInAnotherAccount(t *testing.T) {
	app := instructionsTestApp(t)
	app.Config.ActiveProfile = "marie"
	app.Config.ProfileInstructionsDoc = "https://3.basecamp.com/11111/buckets/555/documents/43"
	f := layeredFetcher{42: "Lead with the answer.", 43: "Sign off as Marie."}

	result, err := loadInstructions(context.Background(), app, false, time.Now, f.fetch)
	require.NoError(t, err)
	require.True(t, result.Applies)
	require.Len(t, result.Documents, 1)
	assert.Equal(t, "global", result.Documents[0].Scope)
	assert.Contains(t, result.Reason, "profile:marie document is in account 11111")
}

// A profile's document alone applies too: the baseline is not required.
func TestLoadInstructionsProfileLayerAlone(t *testing.T) {
	app := instructionsTestApp(t)
	app.Config.InstructionsDoc = ""
	app.Config.ActiveProfile = "marie"
	app.Config.ProfileInstructionsDoc = "https://3.basecamp.com/99999/buckets/555/documents/43"

	result, err := loadInstructions(context.Background(), app, false, time.Now, layeredFetcher{43: "Sign off as Marie."}.fetch)
	require.NoError(t, err)
	require.True(t, result.Applies)
	require.Len(t, result.Documents, 1)
	assert.Equal(t, "profile:marie", result.Documents[0].Scope)
}

func TestConfigSetInstructionsDocForProfile(t *testing.T) {
	const profileDoc = "https://3.basecamp.com/99999/buckets/555/documents/43"
	app, buf := setupConfigTestApp(t)
	globalPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "basecamp", "config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(globalPath), 0o700))
	require.NoError(t, os.WriteFile(globalPath, []byte(`{"profiles": {"marie": {"base_url": "https://3.basecampapi.com"}}}`), 0o600))

	require.NoError(t, executeConfigCommand(app, "set", "--global", "--for-profile", "marie", "instructions_doc", profileDoc))
	var data map[string]any
	parseEnvelopeData(t, buf, &data)
	assert.Equal(t, "global, profile marie", data["scope"])

	var written map[string]any
	raw, err := os.ReadFile(globalPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &written))
	assert.Nil(t, written["instructions_doc"], "the shared key is untouched")
	profile := written["profiles"].(map[string]any)["marie"].(map[string]any)
	assert.Equal(t, profileDoc, profile["instructions_doc"])
	assert.Equal(t, "https://3.basecampapi.com", profile["base_url"], "the rest of the profile stays")

	err = executeConfigCommand(app, "set", "--global", "--for-profile", "nobody", "instructions_doc", profileDoc)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "a profile not in the global config is refused")
	assert.Contains(t, outErr.Message, "nobody")

	err = executeConfigCommand(app, "set", "--global", "--for-profile", "marie", "format", "json")
	require.True(t, errors.As(err, &outErr), "--for-profile takes only instructions_doc")

	buf.Reset()
	require.NoError(t, executeConfigCommand(app, "unset", "--global", "--for-profile", "marie", "instructions_doc"))
	raw, err = os.ReadFile(globalPath)
	require.NoError(t, err)
	written = nil
	require.NoError(t, json.Unmarshal(raw, &written))
	profile = written["profiles"].(map[string]any)["marie"].(map[string]any)
	assert.NotContains(t, profile, "instructions_doc")
	assert.Equal(t, "https://3.basecampapi.com", profile["base_url"])
}
