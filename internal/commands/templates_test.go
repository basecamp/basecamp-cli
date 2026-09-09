package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

const templateLibraryJSON = `{
	"bucket":{"id":1,"name":"To-do List Templates","type":"TemplateLibrary"},
	"todoset":{"id":2,"title":"To-do List Templates","type":"Todoset","url":"https://example.test/todoset.json","app_url":"https://example.test/todoset"},
	"todolists":[{"id":3,"status":"active","visible_to_clients":false,"created_at":"2026-08-27T12:00:00Z","updated_at":"2026-08-27T12:00:00Z","title":"Project kickoff","inherits_status":true,"type":"Todolist","url":"https://example.test/list.json","app_url":"https://example.test/list","bubble_up_url":"https://example.test/bubble.json","parent":{"id":2,"title":"To-do List Templates","type":"Todoset","url":"https://example.test/todoset.json","app_url":"https://example.test/todoset"},"bucket":{"id":1,"name":"To-do List Templates","type":"TemplateLibrary"},"creator":{"id":4,"name":"Victor"},"description":"","description_attachments":[],"name":"Project kickoff","color":null,"comments_app_url":"https://example.test/comments"}]
}`

const templateCopyPendingJSON = `{"id":5,"status":"pending","source_recording_id":3,"destination_parent_id":9,"url":"https://example.test/copies/5.json"}`

func templateCopyProjectRoute() stubRoute {
	return stubRoute{
		method: http.MethodGet,
		path:   "/99999/projects/123.json",
		status: http.StatusOK,
		body:   `{"id":123,"name":"Test Project","dock":[{"name":"todoset","id":9,"title":"To-dos","enabled":true}]}`,
	}
}

func templateCopyTodosetRoute(bucketID int64) stubRoute {
	return stubRoute{
		method: http.MethodGet,
		path:   "/99999/todosets/9",
		status: http.StatusOK,
		body:   fmt.Sprintf(`{"id":9,"bucket":{"id":%d,"name":"Project"}}`, bucketID),
	}
}

func captureTemplateOutput(app *appctx.App) *bytes.Buffer {
	buf := &bytes.Buffer{}
	app.Flags.Hints = true
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: buf})
	return buf
}

func decodeTemplateEnvelope(t *testing.T, buf *bytes.Buffer) struct {
	Summary     string              `json:"summary"`
	Breadcrumbs []output.Breadcrumb `json:"breadcrumbs"`
} {
	t.Helper()
	var envelope struct {
		Summary     string              `json:"summary"`
		Breadcrumbs []output.Breadcrumb `json:"breadcrumbs"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	return envelope
}

func TestTemplatesLibraryUsesSDKLibraryEndpoint(t *testing.T) {
	app, transport := setupRecordingTestApp(t, stubRoute{
		method: http.MethodGet,
		path:   "/99999/template_library.json",
		status: http.StatusOK,
		body:   templateLibraryJSON,
	})
	buf := captureTemplateOutput(app)
	app.Config.ActiveProfile = "work profile"
	app.Config.Sources = map[string]string{"account_id": string(config.SourceFlag)}

	err := executeRecordingCommand(NewTemplatesCmd(), app, "library")
	require.NoError(t, err)

	request := transport.last(t)
	assert.Equal(t, http.MethodGet, request.Method)
	assert.Equal(t, "/99999/template_library.json", request.Path)
	envelope := decodeTemplateEnvelope(t, buf)
	assert.Equal(t, "1 active to-do list templates", envelope.Summary)
	require.Len(t, envelope.Breadcrumbs, 1)
	assert.Equal(t, "basecamp templates copy <template-id> --in <project> --profile 'work profile' --account 99999", envelope.Breadcrumbs[0].Cmd)
}

func TestTemplatesLibraryHumanOutputShowsCopyableID(t *testing.T) {
	app, _ := setupRecordingTestApp(t, stubRoute{
		method: http.MethodGet,
		path:   "/99999/template_library.json",
		status: http.StatusOK,
		body:   templateLibraryJSON,
	})
	buf := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatStyled, Writer: buf})

	err := executeRecordingCommand(NewTemplatesCmd(), app, "library")
	require.NoError(t, err)

	assert.Contains(t, buf.String(), "Project kickoff")
	assert.Contains(t, buf.String(), "3")
}

func TestTemplatesCopyResolvesProjectAndTodosTool(t *testing.T) {
	app, transport := setupRecordingTestApp(t,
		projectsRoute(),
		templateCopyProjectRoute(),
		stubRoute{
			method: http.MethodPost,
			path:   "/99999/template_library/copies.json",
			status: http.StatusCreated,
			body:   templateCopyPendingJSON,
		},
	)
	buf := captureTemplateOutput(app)
	app.Config.ActiveProfile = "work profile"
	app.Config.Sources = map[string]string{"account_id": string(config.SourceFlag)}

	err := executeRecordingCommand(NewTemplatesCmd(), app, "copy", "3", "--in", "Test Project")
	require.NoError(t, err)

	requests := transport.recorded()
	require.Len(t, requests, 3)
	assert.Equal(t, "/99999/projects.json", requests[0].Path)
	assert.Equal(t, "/99999/projects/123.json", requests[1].Path)
	assert.Equal(t, http.MethodPost, requests[2].Method)
	assert.Equal(t, "/99999/template_library/copies.json", requests[2].Path)
	assert.JSONEq(t, `{"template_recording_id":3,"destination_parent_id":9}`, requests[2].Body)

	envelope := decodeTemplateEnvelope(t, buf)
	assert.Equal(t, "Started template copy #5 (pending)", envelope.Summary)
	require.Len(t, envelope.Breadcrumbs, 1)
	assert.Equal(t, "basecamp templates copy-status 5 --profile 'work profile' --account 99999", envelope.Breadcrumbs[0].Cmd)
}

func TestTemplatesCopySendsExplicitPeopleConfirmation(t *testing.T) {
	app, transport := setupRecordingTestApp(t,
		projectsRoute(),
		templateCopyTodosetRoute(123),
		templateCopyProjectRoute(),
		stubRoute{
			method: http.MethodPost,
			path:   "/99999/template_library/copies.json",
			status: http.StatusCreated,
			body:   templateCopyPendingJSON,
		},
	)

	err := executeRecordingCommand(NewTemplatesCmd(), app,
		"copy", "3", "--project", "123", "--todoset", "9", "--confirm-adding-people")
	require.NoError(t, err)

	requests := transport.recorded()
	require.Len(t, requests, 4)
	assert.Equal(t, "/99999/projects.json", requests[0].Path)
	assert.Equal(t, "/99999/todosets/9", requests[1].Path)
	assert.Equal(t, "/99999/projects/123.json", requests[2].Path)
	assert.JSONEq(t,
		`{"template_recording_id":3,"destination_parent_id":9,"adding_people_confirmed":true}`,
		requests[3].Body,
	)
}

func TestTemplatesCopyRejectsTodosetFromAnotherProject(t *testing.T) {
	app, transport := setupRecordingTestApp(t, projectsRoute(), templateCopyTodosetRoute(456))

	err := executeRecordingCommand(NewTemplatesCmd(), app,
		"copy", "3", "--project", "123", "--todoset", "9")
	require.Error(t, err)

	var outputErr *output.Error
	require.True(t, errors.As(err, &outputErr))
	assert.Contains(t, outputErr.Message, "--todoset 9 belongs to project 456, not 123")
	require.Len(t, transport.recorded(), 2, "copy request must not be sent")
}

func TestTemplatesCopyRejectsDisabledTodoset(t *testing.T) {
	app, transport := setupRecordingTestApp(t,
		projectsRoute(),
		templateCopyTodosetRoute(123),
		stubRoute{
			method: http.MethodGet,
			path:   "/99999/projects/123.json",
			status: http.StatusOK,
			body:   `{"id":123,"name":"Test Project","dock":[{"name":"todoset","id":9,"title":"To-dos","enabled":false}]}`,
		},
	)

	err := executeRecordingCommand(NewTemplatesCmd(), app,
		"copy", "3", "--project", "123", "--todoset", "9")
	require.Error(t, err)

	var outputErr *output.Error
	require.True(t, errors.As(err, &outputErr))
	assert.Contains(t, outputErr.Message, "--todoset 9 is disabled for project 123")
	require.Len(t, transport.recorded(), 3, "copy request must not be sent")
}

func TestTemplatesCopyExplainsPeopleConfirmation(t *testing.T) {
	app, _ := setupRecordingTestApp(t,
		projectsRoute(),
		templateCopyProjectRoute(),
		stubRoute{
			method: http.MethodPost,
			path:   "/99999/template_library/copies.json",
			status: http.StatusUnprocessableEntity,
			body:   `{"error":"Adding people requires confirmation","people":[{"id":4,"name":"Victor","avatar_url":"https://example.test/avatar.png"},{"id":7,"name":"Georgia","avatar_url":"https://example.test/georgia.png"}]}`,
		},
	)

	app.Config.ActiveProfile = "work profile; echo unsafe"
	app.Config.Sources = map[string]string{"account_id": string(config.SourceFlag)}

	err := executeRecordingCommand(NewTemplatesCmd(), app, "copy", "3", "--in", "123")
	require.Error(t, err)

	var outputErr *output.Error
	require.True(t, errors.As(err, &outputErr), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, output.CodeValidation, outputErr.Code)
	assert.Contains(t, outputErr.Message, "Victor (#4)")
	assert.Contains(t, outputErr.Message, "Georgia (#7)")
	assert.Contains(t, outputErr.Hint, "basecamp templates copy 3 --in 123 --todoset 9 --profile 'work profile; echo unsafe' --account 99999 --confirm-adding-people")

	var confirmationErr *basecamp.PeopleConfirmationRequiredError
	assert.True(t, errors.As(err, &confirmationErr), "typed SDK error should remain available")
}

func TestTemplatesCopyStatusStates(t *testing.T) {
	completed := `{
		"id":5,"status":"completed","source_recording_id":3,"destination_parent_id":9,"url":"https://example.test/copies/5.json",
		"destination_todolist":{"id":10,"status":"active","visible_to_clients":false,"created_at":"2026-08-27T12:00:00Z","updated_at":"2026-08-27T12:00:00Z","title":"Project kickoff","inherits_status":true,"type":"Todolist","url":"https://example.test/list.json","app_url":"https://example.test/list","bubble_up_url":"https://example.test/bubble.json","parent":{"id":9,"title":"To-dos","type":"Todoset","url":"https://example.test/todoset.json","app_url":"https://example.test/todoset"},"bucket":{"id":123,"name":"Test Project","type":"Project"},"creator":{"id":4,"name":"Victor"},"description":"","description_attachments":[],"name":"Project kickoff","color":null,"comments_app_url":"https://example.test/comments"}
	}`

	tests := []struct {
		name       string
		body       string
		summary    string
		breadcrumb string
	}{
		{name: "pending", body: templateCopyPendingJSON, summary: "Template copy #5 is pending", breadcrumb: "basecamp templates copy-status 5"},
		{name: "processing", body: `{"id":5,"status":"processing","source_recording_id":3,"destination_parent_id":9,"url":"https://example.test/copies/5.json"}`, summary: "Template copy #5 is processing", breadcrumb: "basecamp templates copy-status 5"},
		{name: "completed", body: completed, summary: "Template copy complete: Project kickoff (to-do list #10)", breadcrumb: "basecamp todolists show 10 --in 123"},
		{name: "failed", body: `{"id":5,"status":"failed","source_recording_id":3,"destination_parent_id":9,"url":"https://example.test/copies/5.json"}`, summary: "Template copy #5 failed", breadcrumb: "basecamp templates library"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, transport := setupRecordingTestApp(t, stubRoute{
				method: http.MethodGet,
				path:   "/99999/template_library/copies/5",
				status: http.StatusOK,
				body:   test.body,
			})
			buf := captureTemplateOutput(app)
			app.Config.ActiveProfile = "work profile"
			app.Config.Sources = map[string]string{"account_id": string(config.SourceFlag)}

			err := executeRecordingCommand(NewTemplatesCmd(), app, "copy-status", "5")
			require.NoError(t, err)

			request := transport.last(t)
			assert.Equal(t, http.MethodGet, request.Method)
			assert.Equal(t, "/99999/template_library/copies/5", request.Path)
			envelope := decodeTemplateEnvelope(t, buf)
			assert.Equal(t, test.summary, envelope.Summary)
			require.Len(t, envelope.Breadcrumbs, 1)
			assert.Equal(t, test.breadcrumb+" --profile 'work profile' --account 99999", envelope.Breadcrumbs[0].Cmd)
		})
	}
}
