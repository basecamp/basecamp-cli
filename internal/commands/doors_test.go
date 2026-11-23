package commands

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

const doorJSON = `{"id":456,"status":"active","visible_to_clients":false,` +
	`"created_at":"2026-07-22T15:51:54.872Z","updated_at":"2026-07-22T15:51:54.886Z",` +
	`"title":"Design system","inherits_status":true,"type":"Door",` +
	`"url":"https://www.figma.com/file/abc123/Design-system",` +
	`"app_url":"https://3.basecamp.com/99999/buckets/123/dock/doors/456",` +
	`"position":8,"bucket":{"id":123,"name":"Test Project","type":"Project"},` +
	`"creator":{"id":1,"name":"Test Person"},` +
	`"service":{"name":"Figma","code":"figma"},"description":"<div>Shared</div>"}`

// decodeDoorBody parses the request body a door create or update sent.
func decodeDoorBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &got), "body: %s", body)
	return got
}

func TestDoorsListHitsBucketScopedPath(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), stubRoute{
		method: http.MethodGet,
		path:   "/99999/buckets/123/dock/doors.json",
		status: http.StatusOK,
		body:   "[" + doorJSON + "]",
	})

	err := executeRecordingCommand(NewDoorsCmd(), app, "list", "--in", "123")
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, http.MethodGet, last.Method)
	assert.Equal(t, "/99999/buckets/123/dock/doors.json", last.Path)
}

func TestDoorsShowHitsFlatPath(t *testing.T) {
	app, transport := setupRecordingTestApp(t, stubRoute{
		method: http.MethodGet,
		path:   "/99999/dock/doors/456",
		status: http.StatusOK,
		body:   doorJSON,
	})

	err := executeRecordingCommand(NewDoorsCmd(), app, "show", "456")
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, http.MethodGet, last.Method)
	assert.Equal(t, "/99999/dock/doors/456", last.Path)
}

// The web app's address for a door is a URL the CLI must read the ID from.
func TestDoorsShowAcceptsAppURL(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), stubRoute{
		method: http.MethodGet,
		path:   "/99999/dock/doors/456",
		status: http.StatusOK,
		body:   doorJSON,
	})

	err := executeRecordingCommand(NewDoorsCmd(), app, "show",
		"https://3.basecamp.com/99999/buckets/123/dock/doors/456")
	require.NoError(t, err)
	assert.Equal(t, "/99999/dock/doors/456", transport.last(t).Path)
}

func TestDoorsCreateSendsWrappedDoorAndInfersService(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), stubRoute{
		method: http.MethodPost,
		path:   "/99999/buckets/123/dock/doors.json",
		status: http.StatusCreated,
		body:   doorJSON,
	})

	err := executeRecordingCommand(NewDoorsCmd(), app, "create",
		"--url", "https://www.figma.com/file/abc123/Design-system",
		"--title", "Design system", "--in", "123")
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, http.MethodPost, last.Method)
	assert.Equal(t, "/99999/buckets/123/dock/doors.json", last.Path)

	body := decodeDoorBody(t, last.Body)
	door, ok := body["door"].(map[string]any)
	require.True(t, ok, "door wrapper missing: %s", last.Body)
	assert.Equal(t, "figma", door["service"])
	assert.Equal(t, "https://www.figma.com/file/abc123/Design-system", door["url"])
	assert.Equal(t, "Design system", door["title"])
	assert.NotContains(t, door, "description")
	// Omitted, so the server keeps the new link private to the team.
	assert.NotContains(t, body, "visible_to_clients")
}

func TestDoorsCreateUnknownHostIsOther(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), stubRoute{
		method: http.MethodPost,
		path:   "/99999/buckets/123/dock/doors.json",
		status: http.StatusCreated,
		body:   doorJSON,
	})

	err := executeRecordingCommand(NewDoorsCmd(), app, "create", "Roadmap",
		"--url", "https://example.com/roadmap", "--visible-to-clients", "--in", "123")
	require.NoError(t, err)

	body := decodeDoorBody(t, transport.last(t).Body)
	door := body["door"].(map[string]any)
	assert.Equal(t, "other", door["service"])
	assert.Equal(t, "Roadmap", door["title"], "a positional title is the title")
	assert.Equal(t, true, body["visible_to_clients"])
}

func TestDoorsCreateExplicitServiceAndMarkdownDescription(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), stubRoute{
		method: http.MethodPost,
		path:   "/99999/buckets/123/dock/doors.json",
		status: http.StatusCreated,
		body:   doorJSON,
	})

	err := executeRecordingCommand(NewDoorsCmd(), app, "create",
		"--url", "https://example.atlassian.net/browse/X-1", "--service", "jira",
		"--description", "**Board**", "--in", "123")
	require.NoError(t, err)

	door := decodeDoorBody(t, transport.last(t).Body)["door"].(map[string]any)
	assert.Equal(t, "jira", door["service"])
	assert.Equal(t, "<p><strong>Board</strong></p>", door["description"])
}

func TestDoorsCreateRequiresURLBeforeAnyRequest(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute())

	err := executeRecordingCommand(NewDoorsCmd(), app, "create", "--title", "X", "--in", "123")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "--url is required", e.Message)
	assert.Empty(t, transport.recorded())
}

func TestDoorsUpdateIsPartial(t *testing.T) {
	app, transport := setupRecordingTestApp(t, stubRoute{
		method: http.MethodPut,
		path:   "/99999/dock/doors/456",
		status: http.StatusOK,
		body:   doorJSON,
	})

	err := executeRecordingCommand(NewDoorsCmd(), app, "update", "456", "--title", "Renamed")
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, http.MethodPut, last.Method)
	assert.Equal(t, "/99999/dock/doors/456", last.Path)

	body := decodeDoorBody(t, last.Body)
	assert.Equal(t, map[string]any{"title": "Renamed"}, body["door"])
	assert.NotContains(t, body, "visible_to_clients")
}

func TestDoorsUpdateVisibilityOnlySendsNoDoorFields(t *testing.T) {
	app, transport := setupRecordingTestApp(t, stubRoute{
		method: http.MethodPut,
		path:   "/99999/dock/doors/456",
		status: http.StatusOK,
		body:   doorJSON,
	})

	err := executeRecordingCommand(NewDoorsCmd(), app, "update", "456", "--visible-to-clients=false")
	require.NoError(t, err)

	body := decodeDoorBody(t, transport.last(t).Body)
	assert.NotContains(t, body, "door")
	assert.Equal(t, false, body["visible_to_clients"])
}

func TestDoorsUpdateURLInfersKnownServiceOnly(t *testing.T) {
	route := stubRoute{method: http.MethodPut, path: "/99999/dock/doors/456", status: http.StatusOK, body: doorJSON}

	app, transport := setupRecordingTestApp(t, route)
	require.NoError(t, executeRecordingCommand(NewDoorsCmd(), app, "update", "456",
		"--url", "https://github.com/basecamp/basecamp-cli"))
	door := decodeDoorBody(t, transport.last(t).Body)["door"].(map[string]any)
	assert.Equal(t, "github", door["service"])

	// An unknown host leaves the door's service as it is.
	app, transport = setupRecordingTestApp(t, route)
	require.NoError(t, executeRecordingCommand(NewDoorsCmd(), app, "update", "456",
		"--url", "https://example.com/elsewhere"))
	door = decodeDoorBody(t, transport.last(t).Body)["door"].(map[string]any)
	assert.NotContains(t, door, "service")
}

func TestDoorsUpdateEmptyDescriptionClearsIt(t *testing.T) {
	app, transport := setupRecordingTestApp(t, stubRoute{
		method: http.MethodPut,
		path:   "/99999/dock/doors/456",
		status: http.StatusOK,
		body:   doorJSON,
	})

	err := executeRecordingCommand(NewDoorsCmd(), app, "update", "456", "--description", "")
	require.NoError(t, err)

	door := decodeDoorBody(t, transport.last(t).Body)["door"].(map[string]any)
	assert.Equal(t, map[string]any{"description": ""}, door)
}

func TestDoorsUpdateNoChangesPrecedesRequests(t *testing.T) {
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")
	app, transport := setupRecordingTestApp(t)

	err := executeRecordingCommand(NewDoorsCmd(), app, "update", "456")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "No update fields specified", e.Message)
	assert.Empty(t, transport.recorded())
}

func TestDoorsDeleteHitsFlatPath(t *testing.T) {
	app, transport := setupRecordingTestApp(t, stubRoute{
		method: http.MethodDelete,
		path:   "/99999/dock/doors/456",
		status: http.StatusNoContent,
	})

	err := executeRecordingCommand(NewDoorsCmd(), app, "delete", "456")
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, http.MethodDelete, last.Method)
	assert.Equal(t, "/99999/dock/doors/456", last.Path)
}

func TestDoorsShowInvalidID(t *testing.T) {
	app, transport := setupRecordingTestApp(t)

	err := executeRecordingCommand(NewDoorsCmd(), app, "show", "abc")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "Invalid door ID", e.Message)
	assert.Empty(t, transport.recorded())
}

func TestInferDoorService(t *testing.T) {
	cases := map[string]string{
		"https://www.figma.com/file/abc":                  "figma",
		"figma.com/file/abc":                              "figma",
		"https://github.com/basecamp/basecamp-cli":        "github",
		"https://basecamp.github.io/docs":                 "github",
		"https://www.dropbox.com/s/x":                     "dropbox",
		"https://drive.google.com/drive/folders/x":        "google_drive",
		"https://docs.google.com/document/d/x/edit":       "google_doc",
		"https://docs.google.com/spreadsheets/d/x":        "google_sheet",
		"https://docs.google.com/presentation/d/x":        "google_slide",
		"https://docs.google.com/forms/d/x":               "",
		"https://www.notion.so/page":                      "notion",
		"https://us02web.zoom.us/j/1":                     "zoom",
		"https://example.com/path":                        "",
		"https://notfigma.com/file":                       "",
		"https://figma.com.example.com/file":              "",
		"":                                                "",
		"https://trello.com/b/abc/board":                  "trello",
		"https://acme.slack.com/archives/C1":              "slack",
		"https://miro.com/app/board/x":                    "miro",
		"https://airtable.com/app123":                     "airtable",
		"https://app.box.com/folder/1":                    "box",
		"HTTPS://WWW.FIGMA.COM/file/abc":                  "figma",
		"https://www.notion.site/public":                  "notion",
		"https://acme.notion.com/x":                       "notion",
		"javascript:alert(1)":                             "",
		"https://github.com.evil.example/basecamp/x":      "",
		"https://docs.google.com.evil.example/document/x": "",
		"figma.com/file/abc?redirect=https://example.com": "figma",
		"figma.com:443/file/abc":                          "figma",
	}
	for in, want := range cases {
		assert.Equal(t, want, inferDoorService(in), "inferDoorService(%q)", in)
	}
}

// A door URL only supplies an ID when it is a Basecamp URL for one door in the
// configured account: anything else is refused before any request, so update
// and delete never act on a same-numbered door the URL did not name.
func TestDoorsRefuseURLsThatDoNotNameADoorHere(t *testing.T) {
	cases := map[string]struct {
		url  string
		want string
	}{
		"untrusted host": {"https://3.basecamp.com.evil.example/99999/buckets/123/dock/doors/456", "untrusted host"},
		"another kind":   {"https://3.basecamp.com/99999/buckets/123/todos/456", "does not name one external link"},
		"a collection":   {"https://3.basecamp.com/99999/buckets/123/dock/doors", "does not name one external link"},
		"other account":  {"https://3.basecamp.com/11111/buckets/123/dock/doors/456", "does not match the configured account"},
	}
	for name, tc := range cases {
		for _, action := range [][]string{{"show"}, {"update", "--title", "X"}, {"delete"}} {
			t.Run(name+"/"+action[0], func(t *testing.T) {
				app, transport := setupRecordingTestApp(t)
				args := append([]string{action[0], tc.url}, action[1:]...)

				err := executeRecordingCommand(NewDoorsCmd(), app, args...)
				require.Error(t, err)

				var e *output.Error
				require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
				assert.Equal(t, output.CodeUsage, e.Code)
				assert.Contains(t, e.Message, tc.want)
				assert.Empty(t, transport.recorded(), "a refused URL must send no request")
			})
		}
	}
}

// A door URL names its account, so it serves where none is configured: the
// account is read from the URL before any account is required.
func TestDoorsURLSuppliesTheAccount(t *testing.T) {
	for _, action := range [][]string{{"show"}, {"update", "--title", "X"}, {"delete"}} {
		t.Run(action[0], func(t *testing.T) {
			app, transport := setupRecordingTestApp(t,
				stubRoute{method: http.MethodGet, path: "/99999/dock/doors/456", status: http.StatusOK, body: doorJSON},
				stubRoute{method: http.MethodPut, path: "/99999/dock/doors/456", status: http.StatusOK, body: doorJSON},
				stubRoute{method: http.MethodDelete, path: "/99999/dock/doors/456", status: http.StatusNoContent},
				projectsRoute(),
			)
			app.Config.AccountID = ""
			args := append([]string{action[0], "https://3.basecamp.com/99999/buckets/123/dock/doors/456"}, action[1:]...)

			require.NoError(t, executeRecordingCommand(NewDoorsCmd(), app, args...))
			assert.Equal(t, "99999", app.Config.AccountID)
			assert.Equal(t, "/99999/dock/doors/456", transport.last(t).Path)
		})
	}
}

// A follow-up copied into a fresh shell must act where this command did, so
// it names the active profile and an account that came from the URL.
func TestDoorsFollowUpsCarryTheProfileAndAccount(t *testing.T) {
	const doorURL = "https://3.basecamp.com/99999/buckets/123/dock/doors/456"
	cases := map[string]struct {
		arg      string
		profile  string
		inherit  bool
		followUp string
	}{
		"account from config":       {arg: "456", inherit: true, followUp: "basecamp doors delete 456"},
		"profile, config account":   {arg: "456", profile: "staging", inherit: true, followUp: "basecamp doors delete 456 --profile staging"},
		"profile, account from URL": {arg: doorURL, profile: "staging", followUp: "basecamp doors delete 456 --in 123 --profile staging --account 99999"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			app, _ := setupRecordingTestApp(t, projectsRoute(),
				stubRoute{method: http.MethodGet, path: "/99999/dock/doors/456", status: http.StatusOK, body: doorJSON})
			app.Config.ActiveProfile = c.profile
			app.Config.Sources = map[string]string{}
			if !c.inherit {
				app.Config.AccountID = ""
			}
			buf := captureSpotlightOutput(app)

			require.NoError(t, executeRecordingCommand(NewDoorsCmd(), app, "show", c.arg))

			assert.Contains(t, decodeSpotlightEnvelope(t, buf).Breadcrumbs, output.Breadcrumb{
				Action: "delete", Cmd: c.followUp, Description: "Delete external link",
			})
		})
	}
}
