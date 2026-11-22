package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

const gaugesListPath = "/99999/reports/gauges.json"

func gaugesListRoute() stubRoute {
	return stubRoute{
		method: http.MethodGet,
		path:   gaugesListPath,
		status: http.StatusOK,
		body:   `[{"id":1,"title":"Gauge","enabled":true}]`,
	}
}

func setupGaugesApp(t *testing.T, routes ...stubRoute) (*recordingTransport, *bytes.Buffer, func(args ...string) error) {
	t.Helper()
	app, transport := setupRecordingTestApp(t, routes...)
	out := &bytes.Buffer{}
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: out})
	run := func(args ...string) error {
		return executeRecordingCommand(NewGaugesCmd(), app, args...)
	}
	return transport, out, run
}

func gaugesListQuery(t *testing.T, transport *recordingTransport) url.Values {
	t.Helper()
	queries := transport.queriesFor(gaugesListPath)
	require.Len(t, queries, 1)
	query, err := url.ParseQuery(queries[0])
	require.NoError(t, err)
	return query
}

func TestGaugesListWithoutProjectsStaysAccountWide(t *testing.T) {
	transport, out, run := setupGaugesApp(t, gaugesListRoute())

	require.NoError(t, run("list"))

	assert.False(t, gaugesListQuery(t, transport).Has("bucket_ids"))
	var envelope struct {
		Summary string `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope))
	assert.Equal(t, "1 gauge(s)", envelope.Summary)
}

// A configured default project scopes project commands, but must not quietly
// narrow the account-wide report.
func TestGaugesListIgnoresTheConfiguredProject(t *testing.T) {
	app, transport := setupRecordingTestApp(t, gaugesListRoute())
	app.Config.ProjectID = "123"
	app.Flags.Project = "123"

	require.NoError(t, executeRecordingCommand(NewGaugesCmd(), app, "list"))

	assert.False(t, gaugesListQuery(t, transport).Has("bucket_ids"))
}

func TestGaugesListSendsTheNamedProjectsInOrder(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"--in", []string{"list", "--in", "456"}, "456"},
		{"--project", []string{"list", "--project", "456"}, "456"},
		{"--projects comma-separated", []string{"list", "--projects", "456,123"}, "456,123"},
		{"--projects repeated", []string{"list", "--projects", "456", "--projects", "123"}, "456,123"},
		{"repeats dropped", []string{"list", "--projects", "456,123,456"}, "456,123"},
		{"leading zeros canonical", []string{"list", "--projects", "00456"}, "456"},
		{"by name", []string{"list", "--projects", "Test Project,456"}, "123,456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport, out, run := setupGaugesApp(t, projectsRoute(), gaugesListRoute())

			require.NoError(t, run(tt.args...))

			assert.Equal(t, tt.want, gaugesListQuery(t, transport).Get("bucket_ids"))
			var envelope struct {
				Summary string `json:"summary"`
			}
			require.NoError(t, json.Unmarshal(out.Bytes(), &envelope))
			assert.Contains(t, envelope.Summary, "1 gauge(s) in ")
		})
	}
}

func TestGaugesListRefusesBadProjectsBeforeAnyRequest(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"empty entry", []string{"list", "--projects", "123,,456"}},
		{"empty --in", []string{"list", "--in", ""}},
		{"empty --projects", []string{"list", "--projects", ""}},
		{"empty repeated --projects", []string{"list", "--projects", "123", "--projects", ""}},
		{"--in with --projects", []string{"list", "--in", "789", "--projects", "456,123"}},
		{"--projects with --in", []string{"list", "--projects", "456,123", "--in", "789"}},
		{"zero", []string{"list", "--projects", "0"}},
		{"negative", []string{"list", "--projects=-5"}},
		{"unknown name", []string{"list", "--projects", "No Such Project"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport, _, run := setupGaugesApp(t, projectsRoute(), gaugesListRoute())

			require.Error(t, run(tt.args...))

			assert.Empty(t, transport.queriesFor(gaugesListPath))
		})
	}
}

func TestGaugesListTakesNoArguments(t *testing.T) {
	transport, _, run := setupGaugesApp(t, gaugesListRoute())

	require.Error(t, run("list", "123"))

	assert.Empty(t, transport.queriesFor(gaugesListPath))
}
