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

const cloudFileJSON = `{"id":456,"status":"active","visible_to_clients":false,` +
	`"created_at":"2026-11-01T10:00:00Z","updated_at":"2026-11-01T10:00:00Z",` +
	`"title":"Plan","inherits_status":true,"type":"CloudFile",` +
	`"url":"https://www.dropbox.com/s/abc/plan.pdf",` +
	`"app_url":"https://3.basecamp.com/99999/buckets/123/cloud_files/456",` +
	`"bucket":{"id":123,"name":"Test Project","type":"Project"},` +
	`"creator":{"id":1,"name":"Test Person"},"description":"<div>Kept</div>",` +
	`"description_attachments":[],"service":{"name":"Dropbox","code":"dropbox"}}`

const googleDocJSON = `{"id":789,"status":"drafted","visible_to_clients":false,` +
	`"created_at":"2026-11-01T10:00:00Z","updated_at":"2026-11-01T10:00:00Z",` +
	`"title":"Spec","inherits_status":true,"type":"GoogleDocument",` +
	`"url":"https://docs.google.com/document/d/abc/edit",` +
	`"app_url":"https://3.basecamp.com/99999/buckets/123/google_documents/789",` +
	`"bucket":{"id":123,"name":"Test Project","type":"Project"},` +
	`"creator":{"id":1,"name":"Test Person"},"description":"<div>Kept</div>",` +
	`"description_attachments":[],"document_type":"doc"}`

// projectDockRoute serves project 123 with its Docs & Files folder, 777.
func projectDockRoute() stubRoute {
	return stubRoute{
		method: http.MethodGet,
		path:   "/99999/projects/123.json",
		status: http.StatusOK,
		body: `{"id":123,"name":"Test Project","dock":[` +
			`{"id":777,"name":"vault","title":"Docs & Files","enabled":true,` +
			`"url":"https://3.basecampapi.com/99999/buckets/123/vaults/777.json"}]}`,
	}
}

// peopleRoute serves the people that --subscribe names resolve against.
func peopleRoute() stubRoute {
	return stubRoute{
		method: http.MethodGet,
		path:   "/99999/people.json",
		status: http.StatusOK,
		body:   `[{"id":11,"name":"Jane Doe","email_address":"jane@example.com"},{"id":22,"name":"Bob Roe","email_address":"bob@example.com"}]`,
	}
}

// sentBody decodes the JSON body of a recorded request.
func sentBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &got), "body: %s", body)
	return got
}

func TestCloudFilesCreateInfersServiceAndChoosesSubscribers(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), projectDockRoute(), peopleRoute(), stubRoute{
		method: http.MethodPost,
		path:   "/99999/buckets/123/vaults/777/cloud_files.json",
		status: http.StatusCreated,
		body:   cloudFileJSON,
	})

	err := executeRecordingCommand(NewFilesCmd(), app, "cloud-files", "create", "Plan",
		"--url", "https://www.dropbox.com/s/abc/plan.pdf", "--subscribe", "11,22",
		"--description", "**Final**", "--in", "123")
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, http.MethodPost, last.Method)
	assert.Equal(t, "/99999/buckets/123/vaults/777/cloud_files.json", last.Path)

	body := sentBody(t, last.Body)
	assert.Equal(t, "https://www.dropbox.com/s/abc/plan.pdf", body["url"])
	assert.Equal(t, "dropbox", body["service"], "inferred from the host")
	assert.Equal(t, "Plan", body["title"], "a positional title is the title")
	assert.Equal(t, "<p><strong>Final</strong></p>", body["description"])
	assert.Equal(t, []any{float64(11), float64(22)}, body["subscriptions"])
	assert.NotContains(t, body, "visible_to_clients")
}

func TestCloudFilesCreateNoSubscribeNotifiesNobody(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), stubRoute{
		method: http.MethodPost,
		path:   "/99999/buckets/123/vaults/555/cloud_files.json",
		status: http.StatusCreated,
		body:   cloudFileJSON,
	})

	err := executeRecordingCommand(NewFilesCmd(), app, "cloud-files", "create",
		"--url", "https://example.com/plan", "--no-subscribe", "--folder", "555", "--in", "123")
	require.NoError(t, err)

	body := sentBody(t, transport.last(t).Body)
	assert.Equal(t, "other", body["service"], "an unknown host is other")
	assert.Equal(t, []any{}, body["subscriptions"], "--no-subscribe sends an empty list")
}

func TestCloudFilesCreateOmitsSubscriptionsByDefault(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), stubRoute{
		method: http.MethodPost,
		path:   "/99999/buckets/123/vaults/555/cloud_files.json",
		status: http.StatusCreated,
		body:   cloudFileJSON,
	})

	err := executeRecordingCommand(NewFilesCmd(), app, "cloud-files", "create",
		"--url", "https://www.figma.com/file/abc/Mockups", "--folder", "555", "--in", "123")
	require.NoError(t, err)

	body := sentBody(t, transport.last(t).Body)
	assert.Equal(t, "figma", body["service"])
	assert.NotContains(t, body, "subscriptions", "omitted, Basecamp's default applies")
}

func TestCloudFilesCreateRefusesBeforeAnyRequest(t *testing.T) {
	for name, args := range map[string][]string{
		"no url":               {"cloud-files", "create", "Plan", "--in", "123"},
		"subscribe and silent": {"cloud-files", "create", "--url", "https://example.com", "--subscribe", "11", "--no-subscribe", "--in", "123"},
	} {
		t.Run(name, func(t *testing.T) {
			app, transport := setupRecordingTestApp(t, projectsRoute())

			err := executeRecordingCommand(NewFilesCmd(), app, args...)
			require.Error(t, err)
			var e *output.Error
			require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
			assert.Equal(t, output.CodeUsage, e.Code)
			assert.Empty(t, transport.recorded())
		})
	}
}

// Basecamp replaces the whole cloud file on update, so the fields no flag
// names go back as they were read.
func TestCloudFilesUpdateSendsBackWhatItDoesNotChange(t *testing.T) {
	app, transport := setupRecordingTestApp(t,
		stubRoute{method: http.MethodGet, path: "/99999/cloud_files/456", status: http.StatusOK, body: cloudFileJSON},
		stubRoute{method: http.MethodPut, path: "/99999/cloud_files/456", status: http.StatusOK, body: cloudFileJSON},
	)

	err := executeRecordingCommand(NewFilesCmd(), app, "cloud-files", "update", "456", "--title", "Plan (final)")
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, http.MethodPut, last.Method)
	assert.Equal(t, map[string]any{
		"url":         "https://www.dropbox.com/s/abc/plan.pdf",
		"service":     "dropbox",
		"title":       "Plan (final)",
		"description": "<div>Kept</div>",
	}, sentBody(t, last.Body))
}

func TestCloudFilesUpdateURLInfersAKnownServiceOnly(t *testing.T) {
	for url, service := range map[string]string{
		"https://www.figma.com/file/xyz/New": "figma",
		"https://example.com/elsewhere":      "dropbox",
	} {
		app, transport := setupRecordingTestApp(t,
			stubRoute{method: http.MethodGet, path: "/99999/cloud_files/456", status: http.StatusOK, body: cloudFileJSON},
			stubRoute{method: http.MethodPut, path: "/99999/cloud_files/456", status: http.StatusOK, body: cloudFileJSON},
		)

		err := executeRecordingCommand(NewFilesCmd(), app, "cloud-files", "update", "456", "--url", url)
		require.NoError(t, err)

		body := sentBody(t, transport.last(t).Body)
		assert.Equal(t, url, body["url"])
		assert.Equal(t, service, body["service"], "url %s", url)
		assert.Equal(t, "Plan", body["title"])
	}
}

func TestCloudFilesUpdateEmptyDescriptionClearsIt(t *testing.T) {
	app, transport := setupRecordingTestApp(t,
		stubRoute{method: http.MethodGet, path: "/99999/cloud_files/456", status: http.StatusOK, body: cloudFileJSON},
		stubRoute{method: http.MethodPut, path: "/99999/cloud_files/456", status: http.StatusOK, body: cloudFileJSON},
	)

	err := executeRecordingCommand(NewFilesCmd(), app, "cloud-files", "update", "456", "--description", "")
	require.NoError(t, err)

	body := sentBody(t, transport.last(t).Body)
	assert.NotContains(t, body, "description", "left out, Basecamp clears it")
	assert.Equal(t, "Plan", body["title"])
}

func TestCloudFilesUpdateRefusesBeforeAnyRequest(t *testing.T) {
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")
	for name, args := range map[string][]string{
		"no changes":  {"cloud-files", "update", "456"},
		"empty title": {"cloud-files", "update", "456", "--title", ""},
		"empty url":   {"cloud-files", "update", "456", "--url", " "},
		"bad id":      {"cloud-files", "update", "abc", "--title", "X"},
	} {
		t.Run(name, func(t *testing.T) {
			app, transport := setupRecordingTestApp(t)

			err := executeRecordingCommand(NewFilesCmd(), app, args...)
			require.Error(t, err)
			assert.Empty(t, transport.recorded())
		})
	}
}

func TestGoogleDocsCreatePublishesAndInfersType(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), projectDockRoute(), peopleRoute(), stubRoute{
		method: http.MethodPost,
		path:   "/99999/buckets/123/vaults/777/google_documents.json",
		status: http.StatusCreated,
		body:   googleDocJSON,
	})

	err := executeRecordingCommand(NewFilesCmd(), app, "google-docs", "create", "Budget",
		"--url", "https://docs.google.com/spreadsheets/d/abc/edit", "--subscribe", "11", "--in", "123")
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, "/99999/buckets/123/vaults/777/google_documents.json", last.Path)
	body := sentBody(t, last.Body)
	assert.Equal(t, "sheet", body["document_type"], "inferred from the docs.google.com path")
	assert.Equal(t, "active", body["status"], "published unless --draft")
	assert.Equal(t, "Budget", body["title"])
	assert.Equal(t, []any{float64(11)}, body["subscriptions"])
}

func TestGoogleDocsCreateDraftOtherTypeAndNoSubscribe(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), stubRoute{
		method: http.MethodPost,
		path:   "/99999/buckets/123/vaults/555/google_documents.json",
		status: http.StatusCreated,
		body:   googleDocJSON,
	})

	err := executeRecordingCommand(NewFilesCmd(), app, "google-docs", "create",
		"--url", "https://drive.google.com/file/d/abc/view", "--draft", "--no-subscribe",
		"--folder", "555", "--in", "123")
	require.NoError(t, err)

	body := sentBody(t, transport.last(t).Body)
	assert.Equal(t, "other", body["document_type"], "a link off docs.google.com is other")
	assert.Equal(t, "drafted", body["status"])
	assert.Equal(t, []any{}, body["subscriptions"])
}

func TestGoogleDocsCreateRefusesAnUnknownType(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute())

	err := executeRecordingCommand(NewFilesCmd(), app, "google-docs", "create",
		"--url", "https://docs.google.com/document/d/abc/edit", "--type", "pdf", "--in", "123")
	require.Error(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "Invalid --type: pdf", e.Message)
	assert.Empty(t, transport.recorded())
}

func TestGoogleDocsUpdateSendsBackWhatItDoesNotChange(t *testing.T) {
	app, transport := setupRecordingTestApp(t,
		stubRoute{method: http.MethodGet, path: "/99999/google_documents/789", status: http.StatusOK, body: googleDocJSON},
		stubRoute{method: http.MethodPut, path: "/99999/google_documents/789", status: http.StatusOK, body: googleDocJSON},
	)

	err := executeRecordingCommand(NewFilesCmd(), app, "google-docs", "update", "789", "--title", "Spec (final)")
	require.NoError(t, err)

	last := transport.last(t)
	assert.Equal(t, http.MethodPut, last.Method)
	assert.Equal(t, map[string]any{
		"url":           "https://docs.google.com/document/d/abc/edit",
		"document_type": "doc",
		"title":         "Spec (final)",
		"description":   "<div>Kept</div>",
	}, sentBody(t, last.Body), "no status, so the draft stays a draft; no subscriptions, so its subscribers stay")
}

func TestGoogleDocsUpdatePublishesWithSubscribers(t *testing.T) {
	app, transport := setupRecordingTestApp(t, peopleRoute(),
		stubRoute{method: http.MethodGet, path: "/99999/google_documents/789", status: http.StatusOK, body: googleDocJSON},
		stubRoute{method: http.MethodPut, path: "/99999/google_documents/789", status: http.StatusOK, body: googleDocJSON},
	)

	err := executeRecordingCommand(NewFilesCmd(), app, "google-docs", "update", "789",
		"--publish", "--subscribe", "11,22", "--url", "https://docs.google.com/presentation/d/xyz/edit")
	require.NoError(t, err)

	body := sentBody(t, transport.last(t).Body)
	assert.Equal(t, "active", body["status"])
	assert.Equal(t, []any{float64(11), float64(22)}, body["subscriptions"])
	assert.Equal(t, "slide", body["document_type"], "a new docs.google.com url brings its type")
	assert.Equal(t, "Spec", body["title"])
}

func TestGoogleDocsUpdateExplicitTypeWinsOverTheURL(t *testing.T) {
	app, transport := setupRecordingTestApp(t,
		stubRoute{method: http.MethodGet, path: "/99999/google_documents/789", status: http.StatusOK, body: googleDocJSON},
		stubRoute{method: http.MethodPut, path: "/99999/google_documents/789", status: http.StatusOK, body: googleDocJSON},
	)

	err := executeRecordingCommand(NewFilesCmd(), app, "google-docs", "update", "789",
		"--url", "https://docs.google.com/spreadsheets/d/xyz/edit", "--type", "other")
	require.NoError(t, err)

	assert.Equal(t, "other", sentBody(t, transport.last(t).Body)["document_type"])
}

func TestGoogleDocsUpdateRefusesBeforeAnyRequest(t *testing.T) {
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")
	for name, args := range map[string][]string{
		"no changes":           {"google-docs", "update", "789"},
		"unpublish":            {"google-docs", "update", "789", "--publish=false"},
		"subscribe and silent": {"google-docs", "update", "789", "--subscribe", "11", "--no-subscribe"},
		"empty type":           {"google-docs", "update", "789", "--type", ""},
	} {
		t.Run(name, func(t *testing.T) {
			app, transport := setupRecordingTestApp(t)

			err := executeRecordingCommand(NewFilesCmd(), app, args...)
			require.Error(t, err)
			assert.Empty(t, transport.recorded())
		})
	}
}

func TestInferGoogleDocumentType(t *testing.T) {
	for raw, want := range map[string]string{
		"https://docs.google.com/document/d/abc/edit":     "doc",
		"https://docs.google.com/spreadsheets/d/abc/edit": "sheet",
		"docs.google.com/presentation/d/abc/edit":         "slide",
		"https://docs.google.com/forms/d/abc/edit":        "",
		"https://drive.google.com/file/d/abc/view":        "",
		"https://example.com/document/d/abc":              "",
	} {
		assert.Equal(t, want, inferGoogleDocumentType(raw), raw)
	}
}

func TestLinkUpdatesTakeTheirOwnBasecampURL(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		path string
	}{
		"cloud file": {
			args: []string{"cloud-files", "update", "https://3.basecamp.com/99999/buckets/123/cloud_files/456", "--title", "Plan (final)"},
			path: "/99999/cloud_files/456",
		},
		"Google document": {
			args: []string{"google-docs", "update", "https://3.basecamp.com/99999/buckets/123/vaults/777/google_documents/789", "--title", "Spec (final)"},
			path: "/99999/google_documents/789",
		},
	} {
		t.Run(name, func(t *testing.T) {
			body := cloudFileJSON
			if tc.path == "/99999/google_documents/789" {
				body = googleDocJSON
			}
			app, transport := setupRecordingTestApp(t,
				stubRoute{method: http.MethodGet, path: tc.path, status: http.StatusOK, body: body},
				stubRoute{method: http.MethodPut, path: tc.path, status: http.StatusOK, body: body},
			)

			err := executeRecordingCommand(NewFilesCmd(), app, tc.args...)
			require.NoError(t, err)
			assert.Equal(t, tc.path, transport.last(t).Path)
		})
	}
}

func TestLinkUpdatesRefuseAnotherURLBeforeAnyRequest(t *testing.T) {
	for name, args := range map[string][]string{
		"a Google document's URL to cloud-files": {"cloud-files", "update", "https://3.basecamp.com/99999/buckets/123/google_documents/789", "--title", "X"},
		"a cloud file's URL to google-docs":      {"google-docs", "update", "https://3.basecamp.com/99999/buckets/123/cloud_files/456", "--title", "X"},
		"a look-alike host":                      {"cloud-files", "update", "https://evil.example/99999/buckets/123/cloud_files/456", "--title", "X"},
		"another account":                        {"cloud-files", "update", "https://3.basecamp.com/11111/buckets/123/cloud_files/456", "--title", "X"},
		"a collection":                           {"google-docs", "update", "https://3.basecamp.com/99999/buckets/123/vaults/777", "--title", "X"},
	} {
		t.Run(name, func(t *testing.T) {
			app, transport := setupRecordingTestApp(t)

			err := executeRecordingCommand(NewFilesCmd(), app, args...)
			require.Error(t, err)
			var e *output.Error
			require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
			assert.Equal(t, output.CodeUsage, e.Code)
			assert.Empty(t, transport.recorded())
		})
	}
}

func TestCloudFilesUpdateResolvesLocalImagesInTheDescription(t *testing.T) {
	app, transport := setupRecordingTestApp(t,
		stubRoute{method: http.MethodGet, path: "/99999/cloud_files/456", status: http.StatusOK, body: cloudFileJSON},
		stubRoute{method: http.MethodPut, path: "/99999/cloud_files/456", status: http.StatusOK, body: cloudFileJSON},
	)

	// A local image that isn't there is refused rather than sent as a path.
	err := executeRecordingCommand(NewFilesCmd(), app, "cloud-files", "update", "456",
		"--description", "![preview](./no-such-preview.png)")
	require.Error(t, err)
	for _, req := range transport.recorded() {
		assert.NotEqual(t, http.MethodPut, req.Method, "nothing is written")
	}
}
