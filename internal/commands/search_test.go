package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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

// searchTransport serves mock search API responses.
//
// In single-page mode (perPage == 0) it returns resultCount results with no
// pagination Link — the shape most tests need. In paginated mode (perPage > 0)
// it serves ?page= pages of perPage results drawn from a pool of `pool` total
// results, advertising a `Link: …; rel="next"` header whenever more results
// remain. Optional pointers capture the number of HTTP requests served and the
// last request's query parameters so tests can assert pagination short-circuits
// and query wiring.
type searchTransport struct {
	resultCount int
	totalCount  int

	perPage int // page size when paginating; 0 = single page of resultCount
	pool    int // total results available across pages (paginated mode)

	requests   *int        // captures number of HTTP requests served
	lastParams *url.Values // captures the last request's query params
}

func (s searchTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if !strings.Contains(req.URL.Path, "/search.json") {
		return nil, errors.New("unexpected request: " + req.URL.Path)
	}

	query := req.URL.Query()
	if s.requests != nil {
		*s.requests++
	}
	if s.lastParams != nil {
		*s.lastParams = query
	}

	// Determine which ids this page carries.
	start, end := 0, s.resultCount
	if s.perPage > 0 {
		page := 1
		if p := query.Get("page"); p != "" {
			if n, err := strconv.Atoi(p); err == nil && n > 0 {
				page = n
			}
		}
		start = (page - 1) * s.perPage
		end = start + s.perPage
		if end > s.pool {
			end = s.pool
		}
		if end < s.pool {
			next := *req.URL
			q := next.Query()
			q.Set("page", strconv.Itoa(page+1))
			next.RawQuery = q.Encode()
			header.Set("Link", fmt.Sprintf("<%s>; rel=\"next\"", next.String()))
		}
	}

	var results []map[string]any
	for i := start; i < end; i++ {
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

// searchMetadataTransport serves /searches/metadata.json with the current BC3
// response shape: recording/file search types as key/value pairs plus the
// default_* filter labels, but no `projects` key — so the pinned SDK
// deserializes an empty Projects slice. See bc3
// app/views/api/searches/metadata/index.json.jbuilder.
type searchMetadataTransport struct{}

func (searchMetadataTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if !strings.Contains(req.URL.Path, "/searches/metadata.json") {
		return nil, errors.New("unexpected request: " + req.URL.Path)
	}

	body := []byte(`{
		"recording_search_types": [
			{"key": null, "value": "Everything"},
			{"key": "Todo", "value": "To-dos"}
		],
		"file_search_types": [
			{"key": null, "value": "All files"},
			{"key": "image", "value": "images"}
		],
		"default_creator_label": "Anyone",
		"default_bucket_label": "All projects",
		"default_circle_label": "All pings",
		"default_file_type_label": "All files",
		"default_type_label": "Everything"
	}`)

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

// TestSearchBoundedDefault is the regression for #470: a bare search must apply
// the default cap and short-circuit pagination in a single request, even when
// the first page already advertises a next Link.
func TestSearchBoundedDefault(t *testing.T) {
	var requests int
	// Page 1 carries 25 results and a next Link; pool of 50 guarantees the Link
	// is present so we prove the default cap stops us, not an exhausted pool.
	app, buf := setupSearchTestApp(t, searchTransport{
		perPage:    25,
		pool:       50,
		totalCount: 50,
		requests:   &requests,
	})

	cmd := NewSearchCmd()
	require.NoError(t, executeSearchCommand(cmd, app, "query"))

	var envelope output.Response
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))

	results, ok := envelope.Data.([]any)
	require.True(t, ok, "expected results array, got %T", envelope.Data)
	assert.Len(t, results, defaultSearchLimit)
	assert.Equal(t, 1, requests, "default cap must short-circuit pagination in one request")
}

// TestSearchAllTraversesPages proves --all bypasses the default cap and follows
// pagination to completion.
func TestSearchAllTraversesPages(t *testing.T) {
	var requests int
	app, buf := setupSearchTestApp(t, searchTransport{
		perPage:    20,
		pool:       25,
		totalCount: 25,
		requests:   &requests,
	})

	cmd := NewSearchCmd()
	require.NoError(t, executeSearchCommand(cmd, app, "query", "--all"))

	var envelope output.Response
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))

	results, ok := envelope.Data.([]any)
	require.True(t, ok, "expected results array, got %T", envelope.Data)
	assert.Len(t, results, 25)
	assert.Equal(t, 2, requests, "--all must traverse every page")
}

func TestSearchLimitMustBePositive(t *testing.T) {
	for _, value := range []string{"0", "-1"} {
		app, _ := setupSearchTestApp(t, todosNoNetworkTransport{})
		cmd := NewSearchCmd()
		err := executeSearchCommand(cmd, app, "query", "--limit", value)
		require.Error(t, err, "--limit %s should be rejected", value)

		var e *output.Error
		require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
		assert.Contains(t, e.Message, "must be a positive number")
	}
}

// TestSearchDefaultQueryAndSort proves a bare search sends q=<query> and the
// pinned best_match default sort.
func TestSearchDefaultQueryAndSort(t *testing.T) {
	var params url.Values
	app, _ := setupSearchTestApp(t, searchTransport{
		resultCount: 3,
		totalCount:  3,
		lastParams:  &params,
	})

	cmd := NewSearchCmd()
	require.NoError(t, executeSearchCommand(cmd, app, "meeting notes"))

	assert.Equal(t, "meeting notes", params.Get("q"))
	assert.Equal(t, "best_match", params.Get("sort"))
}

func TestSearchSortMappings(t *testing.T) {
	for input, want := range map[string]string{
		"relevance":  "best_match",
		"best_match": "best_match",
		"recency":    "recency",
		"newest":     "recency",
		"created_at": "recency",
		"updated_at": "recency",
	} {
		var params url.Values
		app, _ := setupSearchTestApp(t, searchTransport{
			resultCount: 1,
			totalCount:  1,
			lastParams:  &params,
		})

		cmd := NewSearchCmd()
		require.NoError(t, executeSearchCommand(cmd, app, "query", "--sort", input), "sort %q", input)
		assert.Equal(t, want, params.Get("sort"), "sort %q", input)
	}
}

func TestSearchInvalidSortRejected(t *testing.T) {
	app, _ := setupSearchTestApp(t, todosNoNetworkTransport{})

	cmd := NewSearchCmd()
	err := executeSearchCommand(cmd, app, "query", "--sort", "bogus")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "invalid --sort value")
}

// TestSearchProjectRejected proves an explicit --project (surfaced via
// app.Flags.Project, since the harness can't parse root globals) is rejected
// with the SDK-gap message, while an ambient config project is never rejected.
func TestSearchProjectRejected(t *testing.T) {
	app, _ := setupSearchTestApp(t, todosNoNetworkTransport{})
	app.Flags.Project = "123"

	cmd := NewSearchCmd()
	err := executeSearchCommand(cmd, app, "query")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "project-scoped search is not supported yet")
}

func TestSearchAmbientProjectNotRejected(t *testing.T) {
	app, buf := setupSearchTestApp(t, searchTransport{resultCount: 1, totalCount: 1})
	app.Config.ProjectID = "456" // ambient, not an explicit flag
	require.Empty(t, app.Flags.Project)

	cmd := NewSearchCmd()
	require.NoError(t, executeSearchCommand(cmd, app, "query"))

	var envelope output.Response
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	assert.True(t, envelope.OK)
}

// TestSearchMetadataEmptyIsGraceful proves empty metadata (SDK schema drift) is
// a successful envelope with a notice, not the former fatal error (#546).
func TestSearchMetadataEmptyIsGraceful(t *testing.T) {
	app, buf := setupSearchTestApp(t, searchMetadataTransport{})

	cmd := NewSearchCmd()
	require.NoError(t, executeSearchCommand(cmd, app, "metadata"))

	var envelope output.Response
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	assert.True(t, envelope.OK)
	assert.NotContains(t, envelope.Notice, "Search metadata not available")
	assert.Contains(t, envelope.Notice, "not yet modeled")
}
