package commands

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// projectStatusServer answers one status change with an empty body, and fails
// the test on any other request.
func projectStatusServer(t *testing.T, path string, status int, body string) (*httptest.Server, *[]string) {
	t.Helper()
	calls := &[]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, r.Method+" "+r.URL.Path)
		if r.Method != http.MethodPut || r.URL.Path != path {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server, calls
}

func TestProjectsArchiveAndUnarchive(t *testing.T) {
	for _, tc := range []struct {
		action, path, status, summary, inverse string
	}{
		{"archive", "/99999/projects/123/status/archived.json", "archived", "Project archived", "unarchive"},
		{"unarchive", "/99999/projects/123/status/active.json", "active", "Project restored", "archive"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			server, calls := projectStatusServer(t, tc.path, http.StatusNoContent, "")
			app, buf := newRequestLevelApp(t, server.URL)

			require.NoError(t, executeCommand(NewProjectsCmd(), app, tc.action, "123"))

			assert.Equal(t, []string{"PUT " + tc.path}, *calls)
			env := decodeWallpaperEnvelope(t, buf)
			assert.True(t, env.OK)
			assert.Equal(t, tc.summary, env.Summary)
			assert.Equal(t, tc.status, env.Data["status"])
			assert.EqualValues(t, 123, env.Data["id"])
			got := map[string]string{}
			for _, crumb := range env.Breadcrumbs {
				got[crumb.Action] = crumb.Cmd
			}
			assert.Equal(t, map[string]string{
				"show":     "basecamp projects show 123",
				tc.inverse: "basecamp projects " + tc.inverse + " 123",
			}, got)
		})
	}
}

func TestProjectsArchiveFollowUpsPreserveScope(t *testing.T) {
	server, _ := projectStatusServer(t, "/99999/projects/123/status/archived.json", http.StatusNoContent, "")
	app, buf := newRequestLevelApp(t, server.URL)
	app.Config.ActiveProfile = "staging"
	app.Config.Sources = map[string]string{"account_id": string(config.SourceFlag)}

	require.NoError(t, executeCommand(NewProjectsCmd(), app, "archive", "123"))

	env := decodeWallpaperEnvelope(t, buf)
	got := map[string]string{}
	for _, crumb := range env.Breadcrumbs {
		got[crumb.Action] = crumb.Cmd
	}
	assert.Equal(t, map[string]string{
		"show":      "basecamp projects show 123 --profile staging --account 99999",
		"unarchive": "basecamp projects unarchive 123 --profile staging --account 99999",
	}, got)
}

func TestProjectsArchiveRefusalIsReported(t *testing.T) {
	server, _ := projectStatusServer(t, "/99999/projects/123/status/archived.json", http.StatusForbidden, `{"error":"Only admins can archive"}`)
	app, buf := newRequestLevelApp(t, server.URL)

	err := executeCommand(NewProjectsCmd(), app, "archive", "123")

	var outErr *output.Error
	require.ErrorAs(t, err, &outErr)
	assert.Equal(t, http.StatusForbidden, outErr.HTTPStatus)
	assert.Equal(t, output.CodeForbidden, outErr.Code)
	assert.Empty(t, buf.String(), "a refusal must not report the project as archived")
}

func TestProjectsUnarchiveAtTheProjectLimitIsReported(t *testing.T) {
	server, _ := projectStatusServer(t, "/99999/projects/123/status/active.json", http.StatusInsufficientStorage, `{"error":"Project limit reached"}`)
	app, buf := newRequestLevelApp(t, server.URL)

	err := executeCommand(NewProjectsCmd(), app, "unarchive", "123")

	var outErr *output.Error
	require.ErrorAs(t, err, &outErr)
	assert.Equal(t, output.CodeLimitExceeded, outErr.Code)
	assert.Empty(t, buf.String(), "a refusal must not report the project as restored")
}

func TestProjectsArchiveCommandsRefuseABadIDBeforeAnyRequest(t *testing.T) {
	for _, action := range []string{"archive", "unarchive"} {
		t.Run(action, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			}))
			t.Cleanup(server.Close)
			app, _ := newRequestLevelApp(t, server.URL)

			err := executeCommand(NewProjectsCmd(), app, action, "launch")

			var outErr *output.Error
			require.ErrorAs(t, err, &outErr)
			assert.Equal(t, "Invalid project ID", outErr.Message)
			assert.Equal(t, output.CodeUsage, outErr.Code)
		})
	}
}
