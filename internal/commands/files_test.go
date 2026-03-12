package commands

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func TestParseStorageFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://storage.3.basecamp.com/123/blobs/abc/download/file.eml", "file.eml"},
		{"https://storage.3.basecamp.com/123/blobs/abc/download/My%20Report.pdf", "My Report.pdf"},
		{"https://storage.3.basecamp.com/123/blobs/abc/download/", "download"},
		{"not-a-url\x00bad", "download"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, parseStorageFilename(tt.input))
		})
	}
}

func TestStorageToAPIURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			"https://storage.3.basecamp.com/2914079/blobs/abc/download/file.eml",
			"https://3.basecampapi.com/2914079/blobs/abc/download/file.eml",
		},
		{
			"https://storage.3.basecamp.com/123/blobs/def/download/My%20Doc.pdf",
			"https://3.basecampapi.com/123/blobs/def/download/My%20Doc.pdf",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := storageToAPIURL(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
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
