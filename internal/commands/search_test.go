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

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/names"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// searchTransport serves mock search API responses with configurable
// result count and total count (X-Total-Count header).
type searchTransport struct {
	resultCount int
	totalCount  int
}

func (s searchTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if !strings.Contains(req.URL.Path, "/search.json") {
		return nil, errors.New("unexpected request: " + req.URL.Path)
	}

	// Build N search results
	var results []map[string]any
	for i := range s.resultCount {
		results = append(results, map[string]any{
			"id":                 i + 1,
			"status":             "active",
			"visible_to_clients": true,
			"created_at":         "2026-01-15T10:00:00Z",
			"updated_at":         "2026-01-15T10:00:00Z",
			"title":              fmt.Sprintf("Result %d", i+1),
			"inherits_status":    false,
			"type":               "Todo",
			"url":                fmt.Sprintf("https://3.basecampapi.com/1/buckets/1/todos/%d.json", i+1),
			"app_url":            fmt.Sprintf("https://3.basecamp.com/1/buckets/1/todos/%d", i+1),
			"bookmark_url":       "",
			"parent":             map[string]any{"id": 0, "title": "", "type": "", "url": "", "app_url": ""},
			"bucket":             map[string]any{"id": 100, "name": "Test Project", "type": "Project"},
			"creator":            map[string]any{"id": 0, "name": "", "email_address": "", "avatar_url": "", "admin": false, "owner": false},
		})
	}

	body, _ := json.Marshal(results)
	header.Set("X-Total-Count", fmt.Sprintf("%d", s.totalCount))

	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     header,
		Request:    req,
	}, nil
}

func setupSearchTestApp(t *testing.T, transport http.RoundTripper) (*appctx.App, *bytes.Buffer) {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
	}

	authMgr := auth.NewManager(cfg, nil)
	sdkClient := basecamp.NewClient(&basecamp.Config{BaseURL: "https://3.basecampapi.com"}, &todosTestTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	nameResolver := names.NewResolver(sdkClient, authMgr, cfg.AccountID)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{
			Format: output.FormatJSON,
			Writer: buf,
		}),
	}
	return app, buf
}

func executeSearchCommand(cmd *cobra.Command, app *appctx.App, args ...string) error {
	cmd.SetArgs(args)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd.Execute()
}

// searchMetadataTransport serves the search metadata endpoint. An empty body
// simulates an empty (no-filters) response.
type searchMetadataTransport struct {
	body string
}

func (s searchMetadataTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if !strings.Contains(req.URL.Path, "/searches/metadata") {
		return nil, errors.New("unexpected request: " + req.URL.Path)
	}
	body := s.body
	if body == "" {
		body = `{"recording_search_types":[],"file_search_types":[]}`
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
		Request:    req,
	}, nil
}

const searchMetadataBody = `{
	"recording_search_types": [
		{"key": null, "value": "Everything"},
		{"key": "Message", "value": "Messages"},
		{"key": "Todo", "value": "To-dos"}
	],
	"file_search_types": [
		{"key": null, "value": "All files"},
		{"key": "image", "value": "Images"}
	],
	"default_creator_label": "Anyone",
	"default_bucket_label": "All projects",
	"default_circle_label": "All pings",
	"default_file_type_label": "All files",
	"default_type_label": "Everything"
}`

// TestSearchMetadataReturnsFilterTypes verifies the metadata command reports the
// recording/file filter counts from the reshaped SearchMetadata response.
func TestSearchMetadataReturnsFilterTypes(t *testing.T) {
	app, buf := setupSearchTestApp(t, searchMetadataTransport{body: searchMetadataBody})

	cmd := NewSearchCmd()
	err := executeSearchCommand(cmd, app, "metadata")
	require.NoError(t, err)

	var envelope output.Response
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	assert.Contains(t, envelope.Summary, "3 recording types")
	assert.Contains(t, envelope.Summary, "2 file types")
}

// TestSearchMetadataEmptyIsUsageError verifies an empty metadata response
// surfaces a usage hint rather than an empty success envelope.
func TestSearchMetadataEmptyIsUsageError(t *testing.T) {
	app, _ := setupSearchTestApp(t, searchMetadataTransport{})

	cmd := NewSearchCmd()
	err := executeSearchCommand(cmd, app, "metadata")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, "Search metadata not available", e.Message)
}

func TestSearchTruncationNoticePresent(t *testing.T) {
	app, buf := setupSearchTestApp(t, searchTransport{resultCount: 5, totalCount: 20})

	cmd := NewSearchCmd()
	err := executeSearchCommand(cmd, app, "query", "--limit", "5")
	require.NoError(t, err)

	var envelope output.Response
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	assert.Contains(t, envelope.Notice, "Showing 5 of 20")
}

func TestSearchNoTruncationNotice(t *testing.T) {
	app, buf := setupSearchTestApp(t, searchTransport{resultCount: 5, totalCount: 5})

	cmd := NewSearchCmd()
	err := executeSearchCommand(cmd, app, "query")
	require.NoError(t, err)

	var envelope output.Response
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	assert.Empty(t, envelope.Notice)
}

func TestSearchAllAndLimitMutuallyExclusive(t *testing.T) {
	app, _ := setupSearchTestApp(t, todosNoNetworkTransport{})

	cmd := NewSearchCmd()
	err := executeSearchCommand(cmd, app, "query", "--all", "--limit", "5")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "--all and --limit are mutually exclusive")
}
