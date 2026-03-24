package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
)

type mockAccountsTransport struct {
	capturedPath        string
	capturedMethod      string
	capturedBody        []byte
	capturedContentType string
}

func (t *mockAccountsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.capturedMethod = req.Method
	t.capturedPath = req.URL.Path
	t.capturedContentType = req.Header.Get("Content-Type")

	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		t.capturedBody = body
		_ = req.Body.Close()
	}

	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	switch {
	case req.Method == http.MethodGet && req.URL.Path == "/99999/account.json":
		return &http.Response{
			StatusCode: 200,
			Header:     header,
			Body: io.NopCloser(strings.NewReader(`{
				"id": 99999,
				"name": "Acme",
				"created_at": "2026-03-01T00:00:00Z",
				"updated_at": "2026-03-01T00:00:00Z"
			}`)),
		}, nil
	case req.Method == http.MethodPut && req.URL.Path == "/99999/account/name.json":
		return &http.Response{
			StatusCode: 200,
			Header:     header,
			Body: io.NopCloser(strings.NewReader(`{
				"id": 99999,
				"name": "Renamed",
				"created_at": "2026-03-01T00:00:00Z",
				"updated_at": "2026-03-24T00:00:00Z"
			}`)),
		}, nil
	case req.Method == http.MethodPut && req.URL.Path == "/99999/account/logo.json":
		return &http.Response{
			StatusCode: 204,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	case req.Method == http.MethodDelete && req.URL.Path == "/99999/account/logo.json":
		return &http.Response{
			StatusCode: 204,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	default:
		return &http.Response{
			StatusCode: 404,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`{"error":"not found"}`)),
		}, nil
	}
}

func executeAccountsCommand(cmd *cobra.Command, app *appctx.App, args ...string) error {
	cmd.SetArgs(args)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd.Execute()
}

func TestAccountsShow(t *testing.T) {
	transport := &mockAccountsTransport{}
	app, buf := newTestAppWithTransport(t, transport)

	cmd := NewAccountsCmd()
	err := executeAccountsCommand(cmd, app, "show")
	require.NoError(t, err)

	assert.Equal(t, http.MethodGet, transport.capturedMethod)
	assert.Equal(t, "/99999/account.json", transport.capturedPath)

	var envelope struct {
		Data struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	assert.Equal(t, "Acme", envelope.Data.Name)
}

func TestAccountsRename(t *testing.T) {
	transport := &mockAccountsTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	cmd := NewAccountsCmd()
	err := executeAccountsCommand(cmd, app, "rename", "Renamed")
	require.NoError(t, err)

	assert.Equal(t, http.MethodPut, transport.capturedMethod)
	assert.Equal(t, "/99999/account/name.json", transport.capturedPath)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedBody, &body))
	assert.Equal(t, "Renamed", body["name"])
}

func TestAccountsLogoSet(t *testing.T) {
	transport := &mockAccountsTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	logo := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	path := filepath.Join(t.TempDir(), "logo.png")
	require.NoError(t, os.WriteFile(path, logo, 0600))

	cmd := NewAccountsCmd()
	err := executeAccountsCommand(cmd, app, "logo", "set", path)
	require.NoError(t, err)

	assert.Equal(t, http.MethodPut, transport.capturedMethod)
	assert.Equal(t, "/99999/account/logo.json", transport.capturedPath)
	assert.Contains(t, transport.capturedContentType, "multipart/form-data")
	assert.Contains(t, string(transport.capturedBody), `filename="logo.png"`)
}

func TestAccountsLogoRemove(t *testing.T) {
	transport := &mockAccountsTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	cmd := NewAccountsCmd()
	err := executeAccountsCommand(cmd, app, "logo", "remove")
	require.NoError(t, err)

	assert.Equal(t, http.MethodDelete, transport.capturedMethod)
	assert.Equal(t, "/99999/account/logo.json", transport.capturedPath)
}

func TestAccountsLogoSetRejectsUnsupportedType(t *testing.T) {
	transport := &mockAccountsTransport{}
	app, _ := newTestAppWithTransport(t, transport)

	path := filepath.Join(t.TempDir(), "logo.txt")
	require.NoError(t, os.WriteFile(path, []byte("not an image"), 0600))

	cmd := NewAccountsCmd()
	err := executeAccountsCommand(cmd, app, "logo", "set", path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be PNG, JPEG, GIF, WebP, AVIF, or HEIC")
}
