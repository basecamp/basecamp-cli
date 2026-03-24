package commands

import (
	"bytes"
	"context"
	"errors"
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
