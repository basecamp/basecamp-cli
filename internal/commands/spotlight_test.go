package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

const spotlightPath = "/99999/recordings/456/spotlight.json"

// Each argument names recording 456: a bare ID, the item's URL, and a comment's
// URL on it, which must reach the item because comments cannot be spotlighted.
var spotlightArgs = []string{
	"456",
	"https://app.basecamp.com/99999/buckets/123/messages/456",
	"https://app.basecamp.com/99999/buckets/123/messages/456#__recording_789",
}

func TestSpotlightPostsTheRecording(t *testing.T) {
	for _, arg := range spotlightArgs {
		t.Run(arg, func(t *testing.T) {
			app, transport := setupRecordingTestApp(t, stubRoute{
				method: http.MethodPost, path: spotlightPath, status: http.StatusCreated,
				body: `{"id":456,"type":"Message","title":"Kickoff"}`,
			})
			buf := captureSpotlightOutput(app)

			require.NoError(t, executeRecordingCommand(NewSpotlightCmd(), app, arg))

			calls := transport.recorded()
			require.Len(t, calls, 1)
			assert.Equal(t, http.MethodPost, calls[0].Method)
			assert.Equal(t, spotlightPath, calls[0].Path)

			env := decodeSpotlightEnvelope(t, buf)
			assert.Equal(t, float64(456), env.Data["id"])
			assert.Equal(t, "Kickoff", env.Data["title"])
			assert.Equal(t, "Spotlighted #456 on its project page", env.Summary)
			assert.Contains(t, env.Breadcrumbs, output.Breadcrumb{
				Action: "unspotlight", Cmd: "basecamp unspotlight 456", Description: "Remove the spotlight",
			})
		})
	}
}

func TestUnspotlightDeletesTheSpotlight(t *testing.T) {
	for _, arg := range spotlightArgs {
		t.Run(arg, func(t *testing.T) {
			app, transport := setupRecordingTestApp(t, stubRoute{
				method: http.MethodDelete, path: spotlightPath, status: http.StatusNoContent,
			})
			buf := captureSpotlightOutput(app)

			require.NoError(t, executeRecordingCommand(NewUnspotlightCmd(), app, arg))

			calls := transport.recorded()
			require.Len(t, calls, 1)
			assert.Equal(t, http.MethodDelete, calls[0].Method)
			assert.Equal(t, spotlightPath, calls[0].Path)

			env := decodeSpotlightEnvelope(t, buf)
			assert.Equal(t, map[string]any{"id": float64(456), "spotlighted": false}, env.Data)
			assert.Equal(t, "Spotlight removed from #456", env.Summary)
			assert.Contains(t, env.Breadcrumbs, output.Breadcrumb{
				Action: "spotlight", Cmd: "basecamp spotlight 456", Description: "Spotlight it again",
			})
		})
	}
}

// None of these names a single item, so no ID from them may go out as a
// recording's: a project, a look-alike host, a to-do list's to-dos, a person.
func TestSpotlightRefusesANonRecordingArgument(t *testing.T) {
	cmds := map[string]func() *cobra.Command{
		"spotlight":   NewSpotlightCmd,
		"unspotlight": NewUnspotlightCmd,
	}
	for name, newCmd := range cmds {
		for _, arg := range []string{
			"abc",
			"https://app.basecamp.com/99999/projects/123",
			"https://evil.example/99999/buckets/123/messages/456",
			"https://app.basecamp.com/99999/buckets/123/todolists/456/todos",
			"https://app.basecamp.com/99999/people/456",
		} {
			t.Run(name+" "+arg, func(t *testing.T) {
				app, transport := setupRecordingTestApp(t)

				err := executeRecordingCommand(newCmd(), app, arg)

				var e *output.Error
				require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
				assert.Equal(t, output.CodeUsage, e.Code)
				assert.Empty(t, transport.recorded())
			})
		}
	}
}

func TestSpotlightSurfacesARefusal(t *testing.T) {
	app, _ := setupRecordingTestApp(t, stubRoute{
		method: http.MethodPost, path: spotlightPath, status: http.StatusUnprocessableEntity,
		body: `{"error":"Recordable can't be spotlighted"}`,
	})

	err := executeRecordingCommand(NewSpotlightCmd(), app, "456")

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, output.CodeValidation, e.Code)
	assert.Contains(t, e.Hint, "not a comment")
}

func captureSpotlightOutput(app *appctx.App) *bytes.Buffer {
	buf := &bytes.Buffer{}
	app.Flags.Hints = true
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: buf})
	return buf
}

type spotlightEnvelope struct {
	Data        map[string]any      `json:"data"`
	Summary     string              `json:"summary"`
	Breadcrumbs []output.Breadcrumb `json:"breadcrumbs"`
}

func decodeSpotlightEnvelope(t *testing.T, buf *bytes.Buffer) spotlightEnvelope {
	t.Helper()
	var env spotlightEnvelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env), buf.String())
	return env
}
