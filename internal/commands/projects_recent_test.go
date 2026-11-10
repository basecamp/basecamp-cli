package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

func TestProjectsRecentPreservesVisitOrder(t *testing.T) {
	var requests []string
	app, out := setupProjectsMockApp(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.Method+" "+req.URL.RequestURI())
		return jsonResponse(http.StatusOK, `[{"id":2,"name":"Zebra"},{"id":1,"name":"Alpha"}]`, http.Header{"Content-Type": {"application/json"}}), nil
	}))

	err := executeCommand(NewProjectsCmd(), app, "recent")
	require.NoError(t, err)
	assert.Equal(t, []string{"GET /99999/my/recent_projects.json"}, requests)

	var envelope struct {
		OK      bool               `json:"ok"`
		Data    []basecamp.Project `json:"data"`
		Summary string             `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope))
	assert.True(t, envelope.OK)
	require.Len(t, envelope.Data, 2)
	assert.Equal(t, int64(2), envelope.Data[0].ID)
	assert.Equal(t, int64(1), envelope.Data[1].ID)
	assert.Equal(t, "2 recent projects", envelope.Summary)
}

func TestProjectsRecentEmptyList(t *testing.T) {
	app, out := setupProjectsMockApp(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `[]`, http.Header{"Content-Type": {"application/json"}}), nil
	}))
	require.NoError(t, executeCommand(NewProjectsCmd(), app, "recent"))
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope))
	assert.Equal(t, true, envelope["ok"])
	assert.Equal(t, []any{}, envelope["data"])
	assert.Equal(t, "0 recent projects", envelope["summary"])
}

func TestProjectsRecentReportsAPIRefusal(t *testing.T) {
	app, out := setupProjectsMockApp(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusForbidden, `{}`, http.Header{"Content-Type": {"application/json"}}), nil
	}))
	require.Error(t, executeCommand(NewProjectsCmd(), app, "recent"))
	assert.Empty(t, out.String())
}

func TestProjectsRecentRejectsArgumentsBeforeRequest(t *testing.T) {
	requests := 0
	app, _ := setupProjectsMockApp(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		return jsonResponse(http.StatusOK, `[]`, http.Header{"Content-Type": {"application/json"}}), nil
	}))
	require.Error(t, executeCommand(NewProjectsCmd(), app, "recent", "unexpected"))
	assert.Zero(t, requests)
}

func TestProjectsRecentWithoutApp(t *testing.T) {
	cmd := newProjectsRecentCmd()
	cmd.SetContext(context.Background())
	require.ErrorContains(t, cmd.RunE(cmd, nil), "app not initialized")
}

func TestProjectsRecentShowBreadcrumb(t *testing.T) {
	app, out := setupProjectsMockApp(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `[{"id":2,"name":"Zebra"}]`, http.Header{"Content-Type": {"application/json"}}), nil
	}))
	app.Flags.Hints = true

	require.NoError(t, executeCommand(NewProjectsCmd(), app, "recent"))
	var envelope struct {
		Breadcrumbs []struct {
			Action string `json:"action"`
			Cmd    string `json:"cmd"`
		} `json:"breadcrumbs"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope))
	require.Len(t, envelope.Breadcrumbs, 1)
	assert.Equal(t, "show", envelope.Breadcrumbs[0].Action)
	assert.Equal(t, "basecamp projects show <id>", envelope.Breadcrumbs[0].Cmd)
}

func TestProjectsRecentShowBreadcrumbKeepsScope(t *testing.T) {
	var requested string
	app, out := setupProjectsMockApp(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requested = req.URL.RequestURI()
		return jsonResponse(http.StatusOK, `[{"id":2,"name":"Zebra"}]`, http.Header{"Content-Type": {"application/json"}}), nil
	}))
	app.Config.ActiveProfile = "staging"
	app.Config.AccountID = "77777"
	app.Config.Sources = map[string]string{"account_id": "flag"}
	app.Flags.Hints = true

	require.NoError(t, executeCommand(NewProjectsCmd(), app, "recent"))
	assert.Equal(t, "/77777/my/recent_projects.json", requested)
	var envelope struct {
		Breadcrumbs []struct {
			Cmd string `json:"cmd"`
		} `json:"breadcrumbs"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope))
	require.Len(t, envelope.Breadcrumbs, 1)
	assert.Equal(t, "basecamp projects show <id> --profile staging --account 77777", envelope.Breadcrumbs[0].Cmd)
}
