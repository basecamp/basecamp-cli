package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"
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

// TestFilesUpdateDocumentRealTitleWhitespaceContentPreservesExistingContent verifies
// that a real --title paired with whitespace-only --content writes only the title
// and preserves existing content (whitespace doesn't reach the wire).
func TestFilesUpdateDocumentRealTitleWhitespaceContentPreservesExistingContent(t *testing.T) {
	transport := &mockFilesUpdateTransport{}
	app := showTestApp(t, transport)
	app.Config.ProjectID = "456"

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "update", "999", "--type", "document", "--title", "Updated title", "--content", "   ")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	err = json.Unmarshal(transport.capturedBody, &body)
	require.NoError(t, err)

	assert.Equal(t, "Updated title", body["title"])
	assert.Equal(t, "<div>Existing body</div>", body["content"])
}

// mockFilesUploadUpdateTransport supports typed --type upload PUTs; captures the body.
type mockFilesUploadUpdateTransport struct {
	capturedBody []byte
}

func (t *mockFilesUploadUpdateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	switch {
	case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/projects.json"):
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`[{"id":456,"name":"Test Project"}]`)),
			Header:     header,
		}, nil
	case req.Method == http.MethodPut && strings.Contains(req.URL.Path, "/uploads/999"):
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
			Body:       io.NopCloser(strings.NewReader(`{"id":999,"filename":"report.pdf"}`)),
			Header:     header,
		}, nil
	default:
		return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
	}
}

// TestFilesUpdateUploadRealTitleWhitespaceContentSendsOnlyBaseName verifies that a
// real --title paired with whitespace-only --content sends only base_name on the
// wire (no description), so whitespace doesn't overwrite the existing description.
func TestFilesUpdateUploadRealTitleWhitespaceContentSendsOnlyBaseName(t *testing.T) {
	transport := &mockFilesUploadUpdateTransport{}
	app := showTestApp(t, transport)
	app.Config.ProjectID = "456"

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "update", "999", "--type", "upload", "--title", "report.pdf", "--content", "   ")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	err = json.Unmarshal(transport.capturedBody, &body)
	require.NoError(t, err)

	assert.Equal(t, "report.pdf", body["base_name"])
	_, hasDescription := body["description"]
	assert.False(t, hasDescription, "description must not be sent for whitespace-only --content")
}

// TestFilesUpdateUploadWhitespaceTitleRealContentSendsOnlyDescription verifies the
// inverse: whitespace --title paired with real --content sends only description.
func TestFilesUpdateUploadWhitespaceTitleRealContentSendsOnlyDescription(t *testing.T) {
	transport := &mockFilesUploadUpdateTransport{}
	app := showTestApp(t, transport)
	app.Config.ProjectID = "456"

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "update", "999", "--type", "upload", "--title", "   ", "--content", "Quarterly report")
	require.NoError(t, err)
	require.NotEmpty(t, transport.capturedBody)

	var body map[string]any
	err = json.Unmarshal(transport.capturedBody, &body)
	require.NoError(t, err)

	assert.Equal(t, "Quarterly report", body["description"])
	_, hasBaseName := body["base_name"]
	assert.False(t, hasBaseName, "base_name must not be sent for whitespace-only --title")
}

// mockFilesCreateTransport backs the docs/uploads create client-visibility tests.
// It switches on method+path, counts every POST (so tests can prove a rejection
// stages nothing), and records whether the vault-inspection GET fired.
type mockFilesCreateTransport struct {
	vaultParent    bool     // GET /vaults/<id> returns a parent (nested) when true
	vaultGetCalled bool     // set when GET /vaults/<id> is hit
	postPaths      []string // every POST path, in order
	uploadBody     []byte   // captured POST /vaults/<id>/uploads.json body
	docBody        []byte   // captured POST /vaults/<id>/documents.json body
}

func (t *mockFilesCreateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	respond := func(code int, body string) (*http.Response, error) {
		return &http.Response{
			StatusCode: code,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}
	path := req.URL.Path

	switch {
	case req.Method == http.MethodGet && strings.HasSuffix(path, "/projects.json"):
		return respond(200, `[{"id":123,"name":"Test Project"}]`)
	case req.Method == http.MethodGet && strings.Contains(path, "/projects/"):
		// Dock payload with a single enabled vault tool, consumed by getVaultID
		// on the root/default (no --vault) path.
		return respond(200, `{"id":123,"name":"Test Project","dock":[{"id":555,"name":"vault","title":"Docs & Files","enabled":true}]}`)
	case req.Method == http.MethodGet && strings.Contains(path, "/vaults/"):
		t.vaultGetCalled = true
		if t.vaultParent {
			return respond(200, `{"id":600,"title":"Sub folder","parent":{"id":555,"title":"Docs & Files"}}`)
		}
		return respond(200, `{"id":555,"title":"Docs & Files"}`)
	case req.Method == http.MethodPost && strings.Contains(path, "/attachments.json"):
		t.postPaths = append(t.postPaths, path)
		return respond(201, `{"attachable_sgid":"sgid-abc"}`)
	case req.Method == http.MethodPost && strings.Contains(path, "/uploads.json"):
		t.postPaths = append(t.postPaths, path)
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("reading upload body: %w", err)
		}
		t.uploadBody = body
		return respond(201, `{"id":789,"title":"report","filename":"report.pdf"}`)
	case req.Method == http.MethodPost && strings.Contains(path, "/documents.json"):
		t.postPaths = append(t.postPaths, path)
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("reading document body: %w", err)
		}
		t.docBody = body
		return respond(201, `{"id":999,"title":"For client"}`)
	default:
		return nil, fmt.Errorf("unexpected request: %s %s", req.Method, path)
	}
}

// writeTempUpload creates a real file so richtext.ValidateFile (which runs before
// the visibility gate in runUploadFile) passes.
func writeTempUpload(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "report.pdf")
	require.NoError(t, os.WriteFile(path, []byte("PDF-bytes"), 0o644))
	return path
}

// --- docs create ------------------------------------------------------------

func TestDocsCreateHasVisibleToClientsFlag(t *testing.T) {
	cmd := NewFilesCmd()

	docsCmd, _, err := cmd.Find([]string{"documents", "create"})
	require.NoError(t, err)

	assert.NotNil(t, docsCmd.Flags().Lookup("visible-to-clients"),
		"expected --visible-to-clients flag on docs create")
}

func TestDocsCreateDefaultOmitsVisibleToClients(t *testing.T) {
	transport := &mockFilesCreateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "documents", "create", "For client", "Body")
	require.NoError(t, err)
	require.NotEmpty(t, transport.docBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.docBody, &body))
	_, present := body["visible_to_clients"]
	assert.False(t, present, "visible_to_clients must be omitted when the flag is not passed")
}

func TestDocsCreateRootVisibleToClientsTrue(t *testing.T) {
	transport := &mockFilesCreateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "documents", "create", "For client", "Body", "--visible-to-clients")
	require.NoError(t, err)
	require.NotEmpty(t, transport.docBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.docBody, &body))
	assert.Equal(t, true, body["visible_to_clients"])
	assert.False(t, transport.vaultGetCalled, "root target must not inspect the vault")
}

func TestDocsCreateRootVisibleToClientsFalse(t *testing.T) {
	transport := &mockFilesCreateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "documents", "create", "For client", "Body", "--visible-to-clients=false")
	require.NoError(t, err)
	require.NotEmpty(t, transport.docBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.docBody, &body))
	assert.Equal(t, false, body["visible_to_clients"], "explicit false must reach the wire")
}

func TestDocsCreateExplicitRootVaultVisibleToClientsTrue(t *testing.T) {
	transport := &mockFilesCreateTransport{vaultParent: false}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "documents", "create", "For client", "Body", "--vault", "555", "--visible-to-clients")
	require.NoError(t, err)
	require.True(t, transport.vaultGetCalled, "explicit vault must be inspected")
	require.NotEmpty(t, transport.docBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.docBody, &body))
	assert.Equal(t, true, body["visible_to_clients"])
}

func TestDocsCreateNestedVaultVisibleToClientsRejected(t *testing.T) {
	transport := &mockFilesCreateTransport{vaultParent: true}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "documents", "create", "For client", "Body", "--vault", "600", "--visible-to-clients")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "root Docs & Files folder")
	assert.Empty(t, transport.postPaths, "a rejected nested target must stage no POSTs")
}

func TestDocsCreateNestedVaultVisibleToClientsFalseRejected(t *testing.T) {
	transport := &mockFilesCreateTransport{vaultParent: true}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "documents", "create", "For client", "Body", "--vault", "600", "--visible-to-clients=false")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Empty(t, transport.postPaths, "a nested target is rejected even for false")
}

func TestDocsCreateNestedVaultWithoutFlagSucceeds(t *testing.T) {
	transport := &mockFilesCreateTransport{vaultParent: true}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "documents", "create", "For client", "Body", "--vault", "600")
	require.NoError(t, err)
	assert.False(t, transport.vaultGetCalled, "resolver must short-circuit before inspecting the vault")
	require.NotEmpty(t, transport.docBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.docBody, &body))
	_, present := body["visible_to_clients"]
	assert.False(t, present, "unchanged nested path must omit visible_to_clients")
}

// --- uploads create ---------------------------------------------------------

func TestUploadsCreateHasVisibleToClientsFlag(t *testing.T) {
	cmd := NewFilesCmd()

	upCmd, _, err := cmd.Find([]string{"uploads", "create"})
	require.NoError(t, err)

	assert.NotNil(t, upCmd.Flags().Lookup("visible-to-clients"),
		"expected --visible-to-clients flag on uploads create")
}

func TestUploadsCreateDefaultOmitsVisibleToClients(t *testing.T) {
	transport := &mockFilesCreateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "uploads", "create", writeTempUpload(t))
	require.NoError(t, err)
	require.NotEmpty(t, transport.uploadBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.uploadBody, &body))
	_, present := body["visible_to_clients"]
	assert.False(t, present, "visible_to_clients must be omitted when the flag is not passed")
}

func TestUploadsCreateRootVisibleToClientsTrue(t *testing.T) {
	transport := &mockFilesCreateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "uploads", "create", writeTempUpload(t), "--visible-to-clients")
	require.NoError(t, err)
	require.NotEmpty(t, transport.uploadBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.uploadBody, &body))
	assert.Equal(t, true, body["visible_to_clients"])
	assert.False(t, transport.vaultGetCalled, "root target must not inspect the vault")
}

func TestUploadsCreateRootVisibleToClientsFalse(t *testing.T) {
	transport := &mockFilesCreateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "uploads", "create", writeTempUpload(t), "--visible-to-clients=false")
	require.NoError(t, err)
	require.NotEmpty(t, transport.uploadBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.uploadBody, &body))
	assert.Equal(t, false, body["visible_to_clients"], "explicit false must reach the wire")
}

func TestUploadsCreateExplicitRootVaultVisibleToClientsTrue(t *testing.T) {
	transport := &mockFilesCreateTransport{vaultParent: false}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "uploads", "create", writeTempUpload(t), "--vault", "555", "--visible-to-clients")
	require.NoError(t, err)
	require.True(t, transport.vaultGetCalled, "explicit vault must be inspected")
	require.NotEmpty(t, transport.uploadBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.uploadBody, &body))
	assert.Equal(t, true, body["visible_to_clients"])
}

func TestUploadsCreateNestedVaultVisibleToClientsRejected(t *testing.T) {
	transport := &mockFilesCreateTransport{vaultParent: true}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "uploads", "create", writeTempUpload(t), "--vault", "600", "--visible-to-clients")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "root Docs & Files folder")
	assert.Empty(t, transport.postPaths, "a rejected nested target must stage no attachment or upload POST")
}

func TestUploadsCreateNestedVaultVisibleToClientsFalseRejected(t *testing.T) {
	transport := &mockFilesCreateTransport{vaultParent: true}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "uploads", "create", writeTempUpload(t), "--vault", "600", "--visible-to-clients=false")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Empty(t, transport.postPaths, "a nested target is rejected even for false")
}

func TestUploadsCreateNestedVaultWithoutFlagSucceeds(t *testing.T) {
	transport := &mockFilesCreateTransport{vaultParent: true}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := NewFilesCmd()
	err := executeMessagesCommand(cmd, app, "uploads", "create", writeTempUpload(t), "--vault", "600")
	require.NoError(t, err)
	assert.False(t, transport.vaultGetCalled, "resolver must short-circuit before inspecting the vault")
	require.NotEmpty(t, transport.uploadBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.uploadBody, &body))
	_, present := body["visible_to_clients"]
	assert.False(t, present, "unchanged nested path must omit visible_to_clients")
}

// --- top-level upload shortcut ----------------------------------------------

func TestUploadShortcutHasVisibleToClientsFlag(t *testing.T) {
	cmd := NewUploadCmd()
	assert.NotNil(t, cmd.Flags().Lookup("visible-to-clients"),
		"expected --visible-to-clients flag on the upload shortcut")
}

// newUploadShortcutRoot mounts the top-level `upload` shortcut under a root
// command so its breadcrumb logic (which dereferences cmd.Parent()) matches real
// invocation.
func newUploadShortcutRoot() *cobra.Command {
	root := &cobra.Command{Use: "basecamp"}
	root.AddCommand(NewUploadCmd())
	return root
}

func TestUploadShortcutRootVisibleToClientsTrue(t *testing.T) {
	transport := &mockFilesCreateTransport{}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := newUploadShortcutRoot()
	err := executeMessagesCommand(cmd, app, "upload", writeTempUpload(t), "--visible-to-clients")
	require.NoError(t, err)
	require.NotEmpty(t, transport.uploadBody)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.uploadBody, &body))
	assert.Equal(t, true, body["visible_to_clients"])
}

func TestUploadShortcutNestedVaultVisibleToClientsRejected(t *testing.T) {
	transport := &mockFilesCreateTransport{vaultParent: true}
	app, _ := setupMessagesMockApp(t, transport)

	cmd := newUploadShortcutRoot()
	err := executeMessagesCommand(cmd, app, "upload", writeTempUpload(t), "--vault", "600", "--visible-to-clients")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Empty(t, transport.postPaths, "the shortcut must also stage no POSTs on rejection")
}

// --- files list: account-wide -----------------------------------------------

// filesAccountWideRoute serves the account-wide files feed.
func filesAccountWideRoute(body string) stubRoute {
	return stubRoute{
		method: http.MethodGet,
		path:   "/99999/files.json",
		status: http.StatusOK,
		body:   body,
	}
}

// filesProjectScopedRoutes serves everything the project-scoped folder listing
// touches: the dock lookup that finds the root vault, then its three children.
func filesProjectScopedRoutes() []stubRoute {
	return []stubRoute{
		projectsRoute(),
		{http.MethodGet, "/99999/projects/123.json", http.StatusOK,
			`{"id":123,"name":"Test Project","dock":[{"id":555,"name":"vault","title":"Docs & Files","enabled":true}]}`},
		{http.MethodGet, "/99999/vaults/555", http.StatusOK, `{"id":555,"title":"Docs & Files"}`},
		{http.MethodGet, "/99999/vaults/555/vaults.json", http.StatusOK, `[]`},
		{http.MethodGet, "/99999/vaults/555/uploads.json", http.StatusOK, `[]`},
		{http.MethodGet, "/99999/vaults/555/documents.json", http.StatusOK, `[]`},
	}
}

func runFilesListCmd(t *testing.T, app *appctx.App, args ...string) error {
	t.Helper()
	return executeRecordingCommand(NewFilesCmd(), app, append([]string{"list"}, args...)...)
}

func requireFilesUsageError(t *testing.T, err error) *output.Error {
	t.Helper()
	require.Error(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, output.CodeUsage, e.Code)
	return e
}

func filesRequestPaths(calls []recordedCall) []string {
	paths := make([]string, 0, len(calls))
	for _, c := range calls {
		paths = append(paths, c.Path)
	}
	return paths
}

func TestFilesListWithConfiguredProjectStaysProjectScoped(t *testing.T) {
	app, transport := setupRecordingTestApp(t, filesProjectScopedRoutes()...)
	app.Config.ProjectID = "123"

	require.NoError(t, runFilesListCmd(t, app))
	paths := filesRequestPaths(transport.recorded())
	assert.NotContains(t, paths, "/99999/files.json")
	assert.Contains(t, paths, "/99999/vaults/555/documents.json")
}

func TestFilesListWithoutProjectListsAccountWide(t *testing.T) {
	app, transport := setupRecordingTestApp(t, filesAccountWideRoute(`[]`))

	require.NoError(t, runFilesListCmd(t, app))
	assert.Equal(t, "/99999/files.json", transport.last(t).Path)
}

func TestFilesListAllProjectsOverridesConfiguredProject(t *testing.T) {
	app, transport := setupRecordingTestApp(t, filesAccountWideRoute(`[]`))
	app.Config.ProjectID = "123"

	require.NoError(t, runFilesListCmd(t, app, "--all-projects"))
	assert.Equal(t, "/99999/files.json", transport.last(t).Path)
}

func TestFilesListAllProjectsConflictsWithExplicitProject(t *testing.T) {
	app, transport := setupRecordingTestApp(t, filesAccountWideRoute(`[]`))

	e := requireFilesUsageError(t, runFilesListCmd(t, app, "--all-projects", "--in", "123"))
	assert.Contains(t, e.Message, "--all-projects conflicts with --project/--in")
	assert.Empty(t, transport.recorded(), "a scope conflict must not reach the network")
}

func TestFilesListAllProjectsConflictsWithRootLevelProject(t *testing.T) {
	app, transport := setupRecordingTestApp(t, filesAccountWideRoute(`[]`))
	app.Flags.Project = "123"

	requireFilesUsageError(t, runFilesListCmd(t, app, "--all-projects"))
	assert.Empty(t, transport.recorded())
}

func TestFilesListAccountWideRejectsVault(t *testing.T) {
	app, transport := setupRecordingTestApp(t, filesAccountWideRoute(`[]`))

	e := requireFilesUsageError(t, runFilesListCmd(t, app, "--vault", "555"))
	assert.Contains(t, e.Message, "--vault/--folder")
	assert.Empty(t, filesRequestPaths(transport.recorded()), "a rejected scope-child flag lists nothing")
}

func TestFilesListAccountWideRejectsFolderAlias(t *testing.T) {
	app, _ := setupRecordingTestApp(t, filesAccountWideRoute(`[]`))

	e := requireFilesUsageError(t, runFilesListCmd(t, app, "--folder", "555"))
	assert.Contains(t, e.Message, "--vault/--folder")
}

func TestFilesListKindRejectedWithExplicitProject(t *testing.T) {
	app, transport := setupRecordingTestApp(t, filesProjectScopedRoutes()...)

	e := requireFilesUsageError(t, runFilesListCmd(t, app, "--in", "123", "--kind", "images"))
	assert.Contains(t, e.Message, "--kind only applies to the account-wide file listing")
	assert.Empty(t, filesRequestPaths(transport.recorded()), "a rejected filter must not list the project")
}

func TestFilesListKindRejectedWithRootLevelProject(t *testing.T) {
	app, _ := setupRecordingTestApp(t, filesProjectScopedRoutes()...)
	app.Flags.Project = "123"

	e := requireFilesUsageError(t, runFilesListCmd(t, app, "--kind", "images"))
	assert.Contains(t, e.Message, "--kind")
}

func TestFilesListKindRejectedWithConfiguredProject(t *testing.T) {
	app, _ := setupRecordingTestApp(t, filesProjectScopedRoutes()...)
	app.Config.ProjectID = "123"

	e := requireFilesUsageError(t, runFilesListCmd(t, app, "--kind", "images"))
	assert.Contains(t, e.Message, "--kind")
}

func TestFilesListPersonRejectedWithConfiguredProject(t *testing.T) {
	app, _ := setupRecordingTestApp(t, filesProjectScopedRoutes()...)
	app.Config.ProjectID = "123"

	e := requireFilesUsageError(t, runFilesListCmd(t, app, "--person", "me"))
	assert.Contains(t, e.Message, "--person only applies to the account-wide file listing")
}

func TestFilesListInvalidKindListsAcceptedValues(t *testing.T) {
	app, transport := setupRecordingTestApp(t, filesAccountWideRoute(`[]`))

	e := requireFilesUsageError(t, runFilesListCmd(t, app, "--kind", "spreadsheets"))
	assert.Contains(t, e.Message, "spreadsheets")
	assert.Contains(t, e.Hint, "all, images, pdfs, documents, videos")
	assert.Empty(t, filesRequestPaths(transport.recorded()))
}

func TestFilesListKindReachesTheQuery(t *testing.T) {
	app, transport := setupRecordingTestApp(t, filesAccountWideRoute(`[]`))

	require.NoError(t, runFilesListCmd(t, app, "--kind", "IMAGES"))
	assert.Contains(t, transport.last(t).Query, "kind=images")
}

func TestFilesListPersonResolvesAndReachesTheQuery(t *testing.T) {
	app, transport := setupRecordingTestApp(t,
		filesAccountWideRoute(`[]`),
		stubRoute{http.MethodGet, "/99999/people.json", http.StatusOK,
			`[{"id":77,"name":"Ann Perkins"},{"id":88,"name":"Bo Diaz"}]`},
	)

	require.NoError(t, runFilesListCmd(t, app, "--person", "Ann Perkins", "--person", "88"))
	query := transport.last(t).Query
	assert.Contains(t, query, "77")
	assert.Contains(t, query, "88")
}

func TestFilesListAccountWideFollowsEveryPage(t *testing.T) {
	app, transport := setupRecordingTestApp(t, filesAccountWideRoute(`[]`))

	require.NoError(t, runFilesListCmd(t, app))
	assert.NotContains(t, transport.last(t).Query, "page=",
		"bare files list is pinned at every page, so it sends no page argument")
}

func TestFilesListGainsNoPaginationFlags(t *testing.T) {
	listCmd, _, err := NewFilesCmd().Find([]string{"list"})
	require.NoError(t, err)

	for _, name := range []string{"limit", "page", "all"} {
		assert.Nil(t, listCmd.Flags().Lookup(name), "files list must not grow --%s", name)
	}
}

func TestFilesListMachineFormatKeepsRawPayload(t *testing.T) {
	buf := &bytes.Buffer{}
	app, _ := setupRecordingTestApp(t, filesAccountWideRoute(filesAccountWideFixture))
	app.Output = output.New(output.Options{Format: output.FormatJSON, Writer: buf})

	require.NoError(t, runFilesListCmd(t, app))
	assert.Contains(t, buf.String(), "attachable_sgid", "machine output keeps the SDK payload")
}

func TestFilesListStyledFormatFlattensRows(t *testing.T) {
	buf := &bytes.Buffer{}
	app, _ := setupRecordingTestApp(t, filesAccountWideRoute(filesAccountWideFixture))
	app.Output = output.New(output.Options{Format: output.FormatStyled, Writer: buf})

	require.NoError(t, runFilesListCmd(t, app))
	rendered := buf.String()
	assert.NotContains(t, rendered, "attachable_sgid", "styled output renders flattened rows")
	assert.Contains(t, rendered, "Test Project")
	assert.Contains(t, rendered, "report.pdf")
}

// filesAccountWideFixture carries one file per wire variant: a full upload, a
// document with no blob metadata, and a bare attachment with no title.
const filesAccountWideFixture = `[
  {"id":1,"title":"report.pdf","type":"Upload","byte_size":2048,"filename":"report.pdf","created_at":"2026-07-01T10:00:00.000Z","bucket":{"id":123,"name":"Test Project","type":"Project"}},
  {"id":2,"title":"Spec","type":"Document","content":"<div>hi</div>","bucket":{"id":123,"name":"Test Project","type":"Project"}},
  {"type":"Attachment","attachable_sgid":"sgid-abc","filename":"pasted.png","byte_size":17}
]`

func TestFlattenAccountWideFilesNilChecksEveryField(t *testing.T) {
	rows := flattenAccountWideFiles([]basecamp.EverythingFile{{}})

	require.Len(t, rows, 1)
	assert.Empty(t, rows[0], "an all-nil file must fabricate no columns")
}

func TestFlattenAccountWideFilesFallsBackToFilename(t *testing.T) {
	name := "pasted.png"
	size := int64(17)
	rows := flattenAccountWideFiles([]basecamp.EverythingFile{{Filename: &name, ByteSize: &size}})

	require.Len(t, rows, 1)
	assert.Equal(t, "pasted.png", rows[0]["name"])
	assert.Equal(t, "17b", rows[0]["size"])
}

// vaults/folders and docs/documents share files list's leaf, but the
// account-wide feed carries no folder variant, so the folder spellings must
// say so rather than return a folder-less listing under a folder name.
func TestFilesGroupSpellingsGetHonestAccountWideSemantics(t *testing.T) {
	t.Run("folders refuse account-wide", func(t *testing.T) {
		for _, args := range [][]string{{"list", "--all-projects"}, {"list"}} {
			app, transport := setupRecordingTestApp(t)
			err := executeRecordingCommand(NewVaultsCmd(), app, args...)
			require.Error(t, err)

			var e *output.Error
			require.True(t, errors.As(err, &e), "expected *output.Error, got %T", err)
			assert.Contains(t, e.Message, "folders have no account-wide listing")
			assert.Empty(t, transport.recorded(), "must refuse before any request")
		}
	})

	t.Run("docs pin the documents kind", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t, stubRoute{
			method: http.MethodGet, path: "/99999/files.json", status: http.StatusOK, body: `[]`,
		})
		require.NoError(t, executeRecordingCommand(NewDocsCmd(), app, "list", "--all-projects"))
		assert.Contains(t, transport.last(t).Query, "kind=documents")
	})

	t.Run("docs reject a contradicting kind", func(t *testing.T) {
		app, _ := setupRecordingTestApp(t)
		err := executeRecordingCommand(NewDocsCmd(), app, "list", "--all-projects", "--kind", "images")
		require.Error(t, err)

		var e *output.Error
		require.True(t, errors.As(err, &e), "expected *output.Error, got %T", err)
		assert.Contains(t, e.Message, "--kind cannot narrow an account-wide document listing")
	})

	t.Run("files stays unfiltered", func(t *testing.T) {
		app, transport := setupRecordingTestApp(t, stubRoute{
			method: http.MethodGet, path: "/99999/files.json", status: http.StatusOK, body: `[]`,
		})
		require.NoError(t, executeRecordingCommand(NewFilesCmd(), app, "list", "--all-projects"))
		assert.NotContains(t, transport.last(t).Query, "kind=")
	})
}
