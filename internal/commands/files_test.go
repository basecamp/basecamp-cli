package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
)

func TestIsStorageURL(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"https://storage.3.basecamp.com/123/blobs/abc/download/file.eml", true},
		{"https://storage.3.basecamp.com/99/blobs/def-ghi/download/My%20Doc.pdf", true},
		{"https://3.basecamp.com/123/buckets/456/uploads/789", false},
		{"789", false},
		{"", false},
		{"https://storage.3.basecamp.com/123/blobs/abc", false},                  // no /download/
		{"https://evil.com/blobs/abc/download/file.eml", false},                  // wrong host
		{"https://storage.3.basecamp.com/123/uploads/789", false},                // no /blobs/
		{"https://storage.evil.basecamp.com.evil.com/blobs/x/download/y", false}, // wrong TLD
		{"http://storage.3.basecamp.com/123/blobs/abc/download/file.eml", false}, // http not allowed
		{"ftp://storage.3.basecamp.com/123/blobs/abc/download/file.eml", false},  // wrong scheme
		{"storage.3.basecamp.com/123/blobs/abc/download/file.eml", false},        // no scheme
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, isStorageURL(tt.input))
		})
	}
}

// TestDocsCreateHasSubscribeFlags tests that docs create has --subscribe and --no-subscribe flags.
func TestDocsCreateHasSubscribeFlags(t *testing.T) {
	cmd := NewFilesCmd()

	// Navigate: files -> documents -> create
	docsCmd, _, err := cmd.Find([]string{"documents", "create"})
	require.NoError(t, err)

	flag := docsCmd.Flags().Lookup("subscribe")
	require.NotNil(t, flag, "expected --subscribe flag on docs create")

	flag = docsCmd.Flags().Lookup("no-subscribe")
	require.NotNil(t, flag, "expected --no-subscribe flag on docs create")
}

// TestDocsCreateSubscribeEmptyIsError tests that --subscribe "" is rejected on docs create.
func TestDocsCreateSubscribeEmptyIsError(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewFilesCmd()

	err := executeMessagesCommand(cmd, app, "documents", "create", "Test", "--subscribe", "")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "at least one person")
}

// TestDocsCreateSubscribeMutualExclusion tests that --subscribe and --no-subscribe are mutually exclusive.
func TestDocsCreateSubscribeMutualExclusion(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewFilesCmd()

	err := executeMessagesCommand(cmd, app, "documents", "create", "Test", "--subscribe", "me", "--no-subscribe")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "mutually exclusive")
}

// TestFilesDownloadStdoutStreamsStorageURL verifies that `files download --out -`
// with a storage URL streams the response body to stdout without writing files.
func TestFilesDownloadStdoutStreamsStorageURL(t *testing.T) {
	fileContent := "PDF-binary-content-here"
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			// DownloadURL rewrites the storage URL to the API host.
			// The path is preserved from the original storage URL.
			if strings.Contains(path, "/blobs/") {
				return 200, fileContent
			}
			return 200, `{}`
		},
	}
	app := showTestApp(t, transport)

	stdout := &bytes.Buffer{}
	cmd := NewFilesCmd()
	cmd.SetArgs([]string{
		"download",
		"https://storage.3.basecamp.com/123/blobs/abc/download/report.pdf",
		"--out", "-",
	})
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(stdout)
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	assert.Equal(t, fileContent, stdout.String(),
		"storage URL body should be streamed directly to stdout")
}

// TestFilesDownloadStdoutStreamsUploadID verifies that `files download --out -`
// with an upload ID streams the response body to stdout.
func TestFilesDownloadStdoutStreamsUploadID(t *testing.T) {
	fileContent := "spreadsheet-data"
	transport := &showTrackingTransport{
		responder: func(path string) (int, string) {
			if strings.Contains(path, "/projects.json") {
				return 200, `[{"id": 456, "name": "Test Project"}]`
			}
			// Uploads.Get fetches metadata at /{accountId}/uploads/{id}.json
			if strings.Contains(path, "/uploads/789") {
				return 200, `{"id": 789, "filename": "report.xlsx", "download_url": "https://signed.example.com/report.xlsx"}`
			}
			// fetchSignedDownload fetches the signed URL
			if strings.Contains(path, "/report.xlsx") {
				return 200, fileContent
			}
			return 200, `{}`
		},
	}
	app := showTestApp(t, transport)
	app.Config.ProjectID = "456"

	stdout := &bytes.Buffer{}
	cmd := NewFilesCmd()
	cmd.SetArgs([]string{"download", "789", "--out", "-"})
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(stdout)
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)

	assert.Equal(t, fileContent, stdout.String(),
		"upload body should be streamed directly to stdout")
}

type mockFilesUpdateTransport struct {
	capturedBody []byte
	requests     []string
}

func (t *mockFilesUpdateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.requests = append(t.requests, req.Method+" "+req.URL.Path)

	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	switch {
	case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/projects.json"):
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`[{"id":456,"name":"Test Project"}]`)),
			Header:     header,
		}, nil
	case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/documents/999"):
		return &http.Response{
			StatusCode: 200,
			Body: io.NopCloser(strings.NewReader(
				`{"id":999,"title":"Existing title","content":"<div>Existing body</div>","status":"active","bucket":{"id":456,"name":"Test Project","type":"Project"}}`,
			)),
			Header: header,
		}, nil
	case req.Method == http.MethodPut && strings.Contains(req.URL.Path, "/documents/999"):
		if req.Body != nil {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, fmt.Errorf("reading request body: %w", err)
			}
			t.capturedBody = body
			_ = req.Body.Close()
		}
		return &http.Response{
			StatusCode: 200,
			Body: io.NopCloser(strings.NewReader(
				`{"id":999,"title":"Updated title","content":"<div>Existing body</div>","status":"active","bucket":{"id":456,"name":"Test Project","type":"Project"}}`,
			)),
			Header: header,
		}, nil
	default:
		return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
	}
}

func TestFilesUpdateDocumentTitlePreservesExistingContent(t *testing.T) {
	transport := &mockFilesUpdateTransport{}
	app := showTestApp(t, transport)
	app.Config.ProjectID = "456"

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "update", "999", "--type", "document", "--title", "Updated title")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	err = json.Unmarshal(transport.capturedBody, &body)
	require.NoError(t, err)

	assert.Equal(t, "Updated title", body["title"])
	assert.Equal(t, "<div>Existing body</div>", body["content"])
}

func TestFilesUpdateDocumentContentPreservesExistingTitle(t *testing.T) {
	transport := &mockFilesUpdateTransport{}
	app := showTestApp(t, transport)
	app.Config.ProjectID = "456"

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "update", "999", "--content", "Updated **body**")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	err = json.Unmarshal(transport.capturedBody, &body)
	require.NoError(t, err)

	assert.Equal(t, "Existing title", body["title"])
	assert.Equal(t, "<p>Updated <strong>body</strong></p>", body["content"])
}

func TestFilesUpdateDocumentEmptyTitleClearsWhilePreservingContent(t *testing.T) {
	transport := &mockFilesUpdateTransport{}
	app := showTestApp(t, transport)
	app.Config.ProjectID = "456"

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "update", "999", "--type", "document", "--title", "")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	err = json.Unmarshal(transport.capturedBody, &body)
	require.NoError(t, err)

	_, hasTitle := body["title"]
	assert.False(t, hasTitle)
	assert.Equal(t, "<div>Existing body</div>", body["content"])
}

func TestFilesUpdateDocumentEmptyContentClearsWhilePreservingTitle(t *testing.T) {
	transport := &mockFilesUpdateTransport{}
	app := showTestApp(t, transport)
	app.Config.ProjectID = "456"

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "update", "999", "--content", "")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	err = json.Unmarshal(transport.capturedBody, &body)
	require.NoError(t, err)

	assert.Equal(t, "Existing title", body["title"])
	_, hasContent := body["content"]
	assert.False(t, hasContent)
}

func TestFilesUpdateTypeWithoutChangesShowsHelp(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "456"

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "update", "999", "--type", "document")
	assert.NoError(t, err)
}

func TestFilesUpdateVaultRejectsContentFlag(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "456"

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "update", "999", "--type", "vault", "--content", "desc")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "--content can only be used with --type document or upload")
}

func TestFilesUpdateVaultWithoutTitleShowsHelp(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "456"

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "update", "999", "--type", "vault")
	assert.NoError(t, err)
}

type mockFilesAutodetectVaultTransport struct{}

func (t *mockFilesAutodetectVaultTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	switch {
	case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/projects.json"):
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`[{"id":456,"name":"Test Project"}]`)),
			Header:     header,
		}, nil
	case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/documents/999"):
		return &http.Response{
			StatusCode: 404,
			Body:       io.NopCloser(strings.NewReader(`{"error":"not found"}`)),
			Header:     header,
		}, nil
	case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/vaults/999"):
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{"id":999,"title":"Existing folder"}`)),
			Header:     header,
		}, nil
	case req.Method == http.MethodPut:
		return nil, fmt.Errorf("unexpected update request: %s", req.URL.Path)
	default:
		return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
	}
}

func TestFilesUpdateAutodetectVaultRejectsContentOnly(t *testing.T) {
	app := showTestApp(t, &mockFilesAutodetectVaultTransport{})
	app.Config.ProjectID = "456"

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "update", "999", "--content", "desc")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "detected a folder/vault; use --title to rename it")
}

func TestFilesUpdateTypedVaultEmptyTitleNoChanges(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "456"

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "update", "999", "--type", "vault", "--title", "")
	assert.NoError(t, err)
}

func TestFilesUpdateTypedUploadEmptyTitleNoChanges(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "456"

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "update", "999", "--type", "upload", "--title", "")
	assert.NoError(t, err)
}

func TestFilesUpdateTypedUploadEmptyContentNoChanges(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "456"

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "update", "999", "--type", "upload", "--content", "")
	assert.NoError(t, err)
}

func TestFilesUpdateAutodetectVaultEmptyTitleNoChanges(t *testing.T) {
	app := showTestApp(t, &mockFilesAutodetectVaultTransport{})
	app.Config.ProjectID = "456"

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "update", "999", "--title", "")
	assert.NoError(t, err)
}

type mockFilesAutodetectUploadTransport struct{}

func (t *mockFilesAutodetectUploadTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	switch {
	case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/projects.json"):
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`[{"id":456,"name":"Test Project"}]`)),
			Header:     header,
		}, nil
	case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/documents/999"):
		return &http.Response{
			StatusCode: 404,
			Body:       io.NopCloser(strings.NewReader(`{"error":"not found"}`)),
			Header:     header,
		}, nil
	case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/vaults/999"):
		return &http.Response{
			StatusCode: 404,
			Body:       io.NopCloser(strings.NewReader(`{"error":"not found"}`)),
			Header:     header,
		}, nil
	case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/uploads/999"):
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{"id":999,"filename":"report.pdf"}`)),
			Header:     header,
		}, nil
	case req.Method == http.MethodPut:
		return nil, fmt.Errorf("unexpected update request: %s", req.URL.Path)
	default:
		return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
	}
}

func TestFilesUpdateAutodetectUploadEmptyContentNoChanges(t *testing.T) {
	app := showTestApp(t, &mockFilesAutodetectUploadTransport{})
	app.Config.ProjectID = "456"

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "update", "999", "--content", "")
	assert.NoError(t, err)
}

func TestFilesUpdateTypedVaultWhitespaceTitleNoChanges(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "456"

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "update", "999", "--type", "vault", "--title", "   ")
	assert.NoError(t, err)
}

func TestFilesUpdateTypedUploadWhitespaceContentNoChanges(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "456"

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "update", "999", "--type", "upload", "--content", "   ")
	assert.NoError(t, err)
}

func TestFilesUpdateTypedDocumentWhitespaceTitleNoChanges(t *testing.T) {
	app, _ := setupMessagesTestApp(t)
	app.Config.ProjectID = "456"

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "update", "999", "--type", "document", "--title", "   ")
	assert.NoError(t, err)
}
