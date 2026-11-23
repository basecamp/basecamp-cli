package commands

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const gaugesPath = "/99999/reports/gauges.json"

func TestGaugesListProjectFilters(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"account-wide", nil, ""},
		{"project", []string{"--project", "30"}, "30"},
		{"in", []string{"--in", "30"}, "30"},
		{"short", []string{"-p", "30"}, "30"},
		{"name", []string{"--in", "Test Project"}, "123"},
		{"ordered", []string{"--projects", "30,10", "--projects", "20"}, "30,10,20"},
		{"mixed names and IDs", []string{"--projects", "30,Test Project,20"}, "30,123,20"},
		{"parent flag", []string{"--in", "30", "list"}, "30"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, transport := setupRecordingTestApp(t, projectsRoute(), stubRoute{
				method: http.MethodGet, path: gaugesPath, status: http.StatusOK,
				body: "[]",
			})
			app.Config.ProjectID = "999"
			args := append([]string{"list"}, tc.args...)
			if tc.name == "parent flag" {
				args = tc.args
			}
			require.NoError(t, executeRecordingCommand(NewGaugesCmd(), app, args...))
			queries := transport.queriesFor(gaugesPath)
			require.Len(t, queries, 1)
			values := queryValues(t, queries[0])
			assert.Equal(t, tc.want, values.Get("bucket_ids"))
			if tc.want == "" {
				assert.NotContains(t, values, "bucket_ids")
			} else {
				assert.Len(t, values["bucket_ids"], 1)
			}
		})
	}
}

func TestGaugesListInvalidProjectFilters(t *testing.T) {
	cases := [][]string{
		{"--projects", "30", "--in", "10"},
		{"--projects", "30", "--project", "10"},
		{"--projects", ""},
		{"--projects", "30,,10"},
		{"--in", ""},
		{"--projects", "0"},
		{"--projects", "-1"},
		{"--projects", "9223372036854775808"},
		{"--in", "Missing"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			app, transport := setupRecordingTestApp(t, projectsRoute())
			err := executeRecordingCommand(NewGaugesCmd(), app, append([]string{"list"}, args...)...)
			require.Error(t, err)
			assert.Empty(t, transport.queriesFor(gaugesPath))
		})
	}
}

func TestGaugesListGlobalProjectFilter(t *testing.T) {
	app, transport := setupRecordingTestApp(t, stubRoute{
		method: http.MethodGet, path: gaugesPath, status: http.StatusOK, body: "[]",
	})
	app.Flags.Project = "42"
	require.NoError(t, executeRecordingCommand(NewGaugesCmd(), app, "list"))
	assert.Equal(t, "42", queryValues(t, transport.queriesFor(gaugesPath)[0]).Get("bucket_ids"))
}
