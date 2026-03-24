package commands

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockGaugesTransport struct {
	capturedPath   string
	capturedMethod string
	capturedBody   []byte
}

func (t *mockGaugesTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	t.capturedPath = req.URL.Path
	t.capturedMethod = req.Method
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		t.capturedBody = body
		_ = req.Body.Close()
	}

	switch {
	case req.Method == http.MethodGet && req.URL.Path == "/99999/reports/gauges.json":
		return &http.Response{
			StatusCode: 200,
			Header:     header,
			Body: io.NopCloser(strings.NewReader(`[
				{"id":1,"title":"Delivery","created_at":"2026-03-24T00:00:00Z","updated_at":"2026-03-24T00:00:00Z"}
			]`)),
		}, nil
	case req.Method == http.MethodGet && req.URL.Path == "/99999/projects/123/gauge/needles.json":
		return &http.Response{
			StatusCode: 200,
			Header:     header,
			Body: io.NopCloser(strings.NewReader(`[
				{"id":99,"position":75,"created_at":"2026-03-24T00:00:00Z","updated_at":"2026-03-24T00:00:00Z"}
			]`)),
		}, nil
	case req.Method == http.MethodGet && req.URL.Path == "/99999/gauge_needles/99":
		return &http.Response{
			StatusCode: 200,
			Header:     header,
			Body: io.NopCloser(strings.NewReader(`{
				"id":99,"position":75,"created_at":"2026-03-24T00:00:00Z","updated_at":"2026-03-24T00:00:00Z"
			}`)),
		}, nil
	case req.Method == http.MethodPost && req.URL.Path == "/99999/projects/123/gauge/needles.json":
		return &http.Response{StatusCode: 201, Header: header, Body: io.NopCloser(strings.NewReader(`{
			"id":99,"position":75,"created_at":"2026-03-24T00:00:00Z","updated_at":"2026-03-24T00:00:00Z"
		}`))}, nil
	case req.Method == http.MethodPut && req.URL.Path == "/99999/gauge_needles/99":
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(`{
			"id":99,"position":75,"created_at":"2026-03-24T00:00:00Z","updated_at":"2026-03-24T00:00:00Z"
		}`))}, nil
	case req.Method == http.MethodDelete && req.URL.Path == "/99999/gauge_needles/99":
		return &http.Response{StatusCode: 204, Header: header, Body: io.NopCloser(strings.NewReader(""))}, nil
	case req.Method == http.MethodPut && req.URL.Path == "/99999/projects/123/gauge.json":
		return &http.Response{StatusCode: 204, Header: header, Body: io.NopCloser(strings.NewReader(""))}, nil
	case req.Method == http.MethodGet && req.URL.Path == "/99999/projects/123.json":
		return &http.Response{
			StatusCode: 200,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`{"id":123,"name":"Project"}`)),
		}, nil
	case req.Method == http.MethodGet && req.URL.Path == "/99999/projects.json":
		return &http.Response{
			StatusCode: 200,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`[{"id":123,"name":"Project"}]`)),
		}, nil
	case req.Method == http.MethodGet && req.URL.Path == "/99999/people.json":
		return &http.Response{
			StatusCode: 200,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`[{"id":1001,"name":"Alice"}]`)),
		}, nil
	default:
		return nil, errors.New("unexpected request")
	}
}

func TestGaugesList(t *testing.T) {
	transport := &mockGaugesTransport{}
	app, buf := newTestAppWithTransport(t, transport)

	cmd := NewGaugesCmd()
	err := executeCommand(cmd, app, "list")
	require.NoError(t, err)

	assert.Equal(t, http.MethodGet, transport.capturedMethod)
	assert.Equal(t, "/99999/reports/gauges.json", transport.capturedPath)
	assert.Contains(t, buf.String(), `"title": "Delivery"`)
}

func TestGaugesNeedles(t *testing.T) {
	transport := &mockGaugesTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	cmd := NewGaugesCmd()
	err := executeCommand(cmd, app, "needles", "--in", "123")
	require.NoError(t, err)

	assert.Equal(t, http.MethodGet, transport.capturedMethod)
	assert.Equal(t, "/99999/projects/123/gauge/needles.json", transport.capturedPath)
}

func TestGaugesCreate(t *testing.T) {
	transport := &mockGaugesTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	cmd := NewGaugesCmd()
	err := executeCommand(cmd, app, "create", "--in", "123", "--position", "75", "--color", "yellow", "--description", "On track")
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, transport.capturedMethod)
	assert.Equal(t, "/99999/projects/123/gauge/needles.json", transport.capturedPath)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))
	needle := body["gauge_needle"].(map[string]any)
	assert.Equal(t, "yellow", needle["color"])
}

func TestGaugesUpdate(t *testing.T) {
	transport := &mockGaugesTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	cmd := NewGaugesCmd()
	err := executeCommand(cmd, app, "update", "99", "--description", "Updated")
	require.NoError(t, err)

	assert.Equal(t, http.MethodPut, transport.capturedMethod)
	assert.Equal(t, "/99999/gauge_needles/99", transport.capturedPath)
}

func TestGaugesDelete(t *testing.T) {
	transport := &mockGaugesTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	cmd := NewGaugesCmd()
	err := executeCommand(cmd, app, "delete", "99")
	require.NoError(t, err)

	assert.Equal(t, http.MethodDelete, transport.capturedMethod)
	assert.Equal(t, "/99999/gauge_needles/99", transport.capturedPath)
}

func TestGaugesEnable(t *testing.T) {
	transport := &mockGaugesTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	cmd := NewGaugesCmd()
	err := executeCommand(cmd, app, "enable", "--in", "123")
	require.NoError(t, err)

	assert.Equal(t, http.MethodPut, transport.capturedMethod)
	assert.Equal(t, "/99999/projects/123/gauge.json", transport.capturedPath)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))
	gauge := body["gauge"].(map[string]any)
	assert.Equal(t, true, gauge["enabled"])
}
