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
//
// It also serves the name/person resolution endpoints (/projects.json,
// /people.json, /my/profile.json) so --project/--creator resolution works.
// requests/lastParams capture ONLY /search.json so those resolution fetches do
// not overwrite the captured search query.
type searchTransport struct {
	resultCount int
	totalCount  int

	perPage int // page size when paginating; 0 = single page of resultCount
	pool    int // total results available across pages (paginated mode)

	requests   *int        // captures number of /search.json requests served
	lastParams *url.Values // captures the last /search.json request's query params
}

func (s searchTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	// Serve resolution endpoints before the /search.json-only guard so
	// --project and --creator can resolve to IDs. Single page, no Link.
	if body, ok := resolutionResponse(req.URL.Path); ok {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewReader(body)),
			Header:     header,
			Request:    req,
		}, nil
	}

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

// resolutionResponse returns a canned JSON body for the name/person resolution
// endpoints the search command hits when --project or --creator is set. The
// second return is false for any other path so callers fall through to their
// primary handler. Single page, no Link header.
func resolutionResponse(path string) ([]byte, bool) {
	switch {
	case strings.Contains(path, "/my/profile.json"):
		return []byte(`{"id":555,"name":"Me"}`), true
	case strings.Contains(path, "/projects.json"):
		return []byte(`[{"id":123,"name":"Test Project"}]`), true
	case strings.Contains(path, "/people.json"):
		return []byte(`[{"id":987,"name":"Ann"}]`), true
	default:
		return nil, false
	}
}

// searchMetadataTransport serves /searches/metadata.json with the real BC3
// response shape: recording/file search types as key/value pairs (each list led
// by a key:null default) plus the default_* filter labels. See bc3
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
			{"key": "Image", "value": "Images"}
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

// TestSearchRejectsExtraArgs proves an unquoted multi-word query is a usage
// error rather than silently searching only the first word.
func TestSearchRejectsExtraArgs(t *testing.T) {
	app, _ := setupSearchTestApp(t, todosNoNetworkTransport{})

	cmd := NewSearchCmd()
	err := executeSearchCommand(cmd, app, "foo", "bar")
	require.Error(t, err)
}

// TestSearchMetadataRejectsExtraArgs proves the metadata subcommand rejects
// stray positional args (cobra.NoArgs) before RunE, rather than ignoring them
// and reaching account validation. Asserting the cobra arg-rejection message
// (not just any error) rules out a downstream network/account failure.
func TestSearchMetadataRejectsExtraArgs(t *testing.T) {
	app, _ := setupSearchTestApp(t, todosNoNetworkTransport{})

	cmd := NewSearchCmd()
	err := executeSearchCommand(cmd, app, "metadata", "junk")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown command "junk"`)
}

// TestSearchProjectByID proves an explicit --project (surfaced via
// app.Flags.Project, since the harness can't parse root globals) resolves to a
// bucket ID sent in both the plural bucket_ids[] and singular bucket_id forms.
func TestSearchProjectByID(t *testing.T) {
	var params url.Values
	app, _ := setupSearchTestApp(t, searchTransport{resultCount: 1, totalCount: 1, lastParams: &params})
	app.Flags.Project = "123"

	cmd := NewSearchCmd()
	require.NoError(t, executeSearchCommand(cmd, app, "query"))

	assert.Equal(t, "123", params.Get("bucket_ids[]"))
	assert.Equal(t, "123", params.Get("bucket_id"))
}

func TestSearchProjectByName(t *testing.T) {
	var params url.Values
	app, _ := setupSearchTestApp(t, searchTransport{resultCount: 1, totalCount: 1, lastParams: &params})
	app.Flags.Project = "Test Project"

	cmd := NewSearchCmd()
	require.NoError(t, executeSearchCommand(cmd, app, "query"))

	assert.Equal(t, "123", params.Get("bucket_ids[]"))
	assert.Equal(t, "123", params.Get("bucket_id"))
}

// TestSearchProjectNotFound proves an unresolvable project NAME errors. Unknown
// numeric IDs intentionally pass through (resolver.go:99), so a bad name is the
// case that surfaces the error.
func TestSearchProjectNotFound(t *testing.T) {
	app, _ := setupSearchTestApp(t, searchTransport{resultCount: 1, totalCount: 1})
	app.Flags.Project = "No Such Project"

	cmd := NewSearchCmd()
	err := executeSearchCommand(cmd, app, "query")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
}

// TestSearchAmbientProjectIgnored proves an ambient config project is never
// used to scope the search — only an explicit --project flag scopes.
func TestSearchAmbientProjectIgnored(t *testing.T) {
	var params url.Values
	app, buf := setupSearchTestApp(t, searchTransport{resultCount: 1, totalCount: 1, lastParams: &params})
	app.Config.ProjectID = "456" // ambient, not an explicit flag
	require.Empty(t, app.Flags.Project)

	cmd := NewSearchCmd()
	require.NoError(t, executeSearchCommand(cmd, app, "query"))

	assert.Empty(t, params.Get("bucket_ids[]"))
	assert.Empty(t, params.Get("bucket_id"))

	var envelope output.Response
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	assert.True(t, envelope.OK)
}

// TestSearchTypeMappings proves each friendly --type alias maps onto its
// canonical Search::Type Key, sent in both type_names[] and singular type.
func TestSearchTypeMappings(t *testing.T) {
	for input, want := range map[string]string{
		"todo":     "Todo",
		"upload":   "Attachment",
		"ping":     "Circle",
		"check-in": "Question",
		"event":    "Schedule::Entry",
		"folder":   "Vault",
		"chat":     "Chat::Transcript",
		"card":     "Kanban::Card",
	} {
		var params url.Values
		app, _ := setupSearchTestApp(t, searchTransport{resultCount: 1, totalCount: 1, lastParams: &params})

		cmd := NewSearchCmd()
		require.NoError(t, executeSearchCommand(cmd, app, "query", "--type", input), "type %q", input)
		assert.Equal(t, want, params.Get("type_names[]"), "type %q", input)
		assert.Equal(t, want, params.Get("type"), "type %q", input)
	}
}

// TestSearchInvalidTypeRejected proves an unknown --type errors before any
// /search.json request — BC3 would silently drop it and return unfiltered.
func TestSearchInvalidTypeRejected(t *testing.T) {
	var requests int
	app, _ := setupSearchTestApp(t, searchTransport{resultCount: 1, totalCount: 1, requests: &requests})

	cmd := NewSearchCmd()
	err := executeSearchCommand(cmd, app, "query", "--type", "foo")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "invalid --type value")
	assert.Equal(t, 0, requests, "invalid --type must not reach the search API")
}

func TestSearchCreatorByID(t *testing.T) {
	var params url.Values
	app, _ := setupSearchTestApp(t, searchTransport{resultCount: 1, totalCount: 1, lastParams: &params})

	cmd := NewSearchCmd()
	require.NoError(t, executeSearchCommand(cmd, app, "query", "--creator", "987"))

	assert.Equal(t, "987", params.Get("creator_ids[]"))
	assert.Equal(t, "987", params.Get("creator_id"))
}

// TestSearchCreatorMe proves --creator me resolves via /my/profile.json.
func TestSearchCreatorMe(t *testing.T) {
	var params url.Values
	app, _ := setupSearchTestApp(t, searchTransport{resultCount: 1, totalCount: 1, lastParams: &params})

	cmd := NewSearchCmd()
	require.NoError(t, executeSearchCommand(cmd, app, "query", "--creator", "me"))

	assert.Equal(t, "555", params.Get("creator_ids[]"))
	assert.Equal(t, "555", params.Get("creator_id"))
}

// TestSearchFileTypeCasing proves --file-type image is capitalized to the
// case-sensitive Blob::TYPES value BC3 requires.
func TestSearchFileTypeCasing(t *testing.T) {
	var params url.Values
	app, _ := setupSearchTestApp(t, searchTransport{resultCount: 1, totalCount: 1, lastParams: &params})

	cmd := NewSearchCmd()
	require.NoError(t, executeSearchCommand(cmd, app, "query", "--file-type", "image"))

	assert.Equal(t, "Image", params.Get("file_type"))
}

func TestSearchInvalidFileTypeRejected(t *testing.T) {
	var requests int
	app, _ := setupSearchTestApp(t, searchTransport{resultCount: 1, totalCount: 1, requests: &requests})

	cmd := NewSearchCmd()
	err := executeSearchCommand(cmd, app, "query", "--file-type", "img")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "invalid --file-type value")
	assert.Equal(t, 0, requests, "invalid --file-type must not reach the search API")
}

func TestSearchExcludeChat(t *testing.T) {
	var params url.Values
	app, _ := setupSearchTestApp(t, searchTransport{resultCount: 1, totalCount: 1, lastParams: &params})

	cmd := NewSearchCmd()
	require.NoError(t, executeSearchCommand(cmd, app, "query", "--exclude-chat"))

	assert.Equal(t, "true", params.Get("exclude_chat"))
}

func TestSearchSince(t *testing.T) {
	var params url.Values
	app, _ := setupSearchTestApp(t, searchTransport{resultCount: 1, totalCount: 1, lastParams: &params})

	cmd := NewSearchCmd()
	require.NoError(t, executeSearchCommand(cmd, app, "query", "--since", "last_30_days"))

	assert.Equal(t, "last_30_days", params.Get("since"))
}

func TestSearchInvalidSinceRejected(t *testing.T) {
	var requests int
	app, _ := setupSearchTestApp(t, searchTransport{resultCount: 1, totalCount: 1, requests: &requests})

	cmd := NewSearchCmd()
	err := executeSearchCommand(cmd, app, "query", "--since", "yesterday")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e), "expected *output.Error, got %T: %v", err, err)
	assert.Contains(t, e.Message, "invalid --since value")
	assert.Equal(t, 0, requests, "invalid --since must not reach the search API")
}

// TestSearchMetadataRealFields proves metadata presents the real recording/file
// search types with no drift notice, and the summary counts exclude the
// key:null defaults.
func TestSearchMetadataRealFields(t *testing.T) {
	app, buf := setupSearchTestApp(t, searchMetadataTransport{})

	cmd := NewSearchCmd()
	require.NoError(t, executeSearchCommand(cmd, app, "metadata"))

	var envelope output.Response
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope))
	assert.True(t, envelope.OK)
	assert.Empty(t, envelope.Notice)

	// One selectable recording type (Todo) and one file type (Image); the
	// key:null defaults must not be counted, and the count-1 labels are
	// singular.
	assert.Equal(t, "Search filters: 1 recording type, 1 file type", envelope.Summary)

	data, ok := envelope.Data.(map[string]any)
	require.True(t, ok, "expected metadata object, got %T", envelope.Data)
	assert.Contains(t, data, "recording_search_types")
	assert.Contains(t, data, "file_search_types")
}
