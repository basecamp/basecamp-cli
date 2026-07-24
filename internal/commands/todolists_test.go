package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/names"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// --- A. Guard tests (no-network transport, guards fire before any request) ---

func TestTodolistsPositionSingleRequiresTo(t *testing.T) {
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")
	app, _ := setupTestApp(t)

	project := ""
	cmd := newTodolistsPositionCmd(&project)
	err := executeCommand(cmd, app, "789")

	require.NotNil(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "--to is required (1 = top)", e.Message)
}

func TestTodolistsPositionRejectsZeroAndNegative(t *testing.T) {
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")

	cases := []struct {
		name string
		args []string
	}{
		{"single --to 0", []string{"789", "--to", "0"}},
		{"single --to -1", []string{"789", "--to", "-1"}},
		{"bulk --to 0", []string{"701", "702", "--to", "0"}},
		{"bulk --to -1", []string{"701", "702", "--to", "-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, _ := setupTestApp(t)
			project := ""
			cmd := newTodolistsPositionCmd(&project)
			err := executeCommand(cmd, app, tc.args...)

			require.NotNil(t, err)
			var e *output.Error
			require.True(t, errors.As(err, &e))
			assert.Equal(t, "--to must be at least 1 (1 = top)", e.Message)
		})
	}
}

func TestTodolistsPositionAcceptsPositionAlias(t *testing.T) {
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")
	app, _ := setupTestApp(t)

	project := ""
	cmd := newTodolistsPositionCmd(&project)
	// --position 2 satisfies the requirement → guard does not fire; the command
	// reaches the network layer and fails there, not with the usage message.
	err := executeCommand(cmd, app, "789", "--position", "2")

	require.NotNil(t, err)
	var e *output.Error
	if errors.As(err, &e) {
		assert.NotEqual(t, "--to is required (1 = top)", e.Message)
		assert.NotEqual(t, "--to must be at least 1 (1 = top)", e.Message)
	}
}

func TestTodolistsPositionBulkRejectsNonOnePosition(t *testing.T) {
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")
	app, _ := setupTestApp(t)

	project := ""
	cmd := newTodolistsPositionCmd(&project)
	err := executeCommand(cmd, app, "701", "702", "--to", "2")

	require.NotNil(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "only supported at position 1")
}

func TestTodolistsPositionBulkAllowsOmittedAndExplicitOne(t *testing.T) {
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")

	for _, args := range [][]string{
		{"701", "702"},
		{"701", "702", "--to", "1"},
	} {
		app, _ := setupTestApp(t)
		project := ""
		cmd := newTodolistsPositionCmd(&project)
		err := executeCommand(cmd, app, args...)

		// Guard must not fire: the error comes from the preflight network call.
		require.NotNil(t, err)
		var e *output.Error
		if errors.As(err, &e) {
			assert.NotContains(t, e.Message, "only supported at position 1")
			assert.NotEqual(t, "--to is required (1 = top)", e.Message)
		}
	}
}

func TestTodolistsPositionEmptyAfterExtraction(t *testing.T) {
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")
	app, _ := setupTestApp(t)

	project := ""
	cmd := newTodolistsPositionCmd(&project)
	// A bare comma extracts to zero IDs — must report the missing-arg usage error.
	err := executeCommand(cmd, app, ",", "--to", "1")

	require.NotNil(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "<id|url>... required", e.Message)
}

func TestTodolistsPositionInvalidID(t *testing.T) {
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")
	app, _ := setupTestApp(t)

	project := ""
	cmd := newTodolistsPositionCmd(&project)
	err := executeCommand(cmd, app, "abc", "--to", "1")

	require.NotNil(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "Invalid todolist ID", e.Message)
}

func TestTodolistsPositionDuplicateID(t *testing.T) {
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")
	app, _ := setupTestApp(t)

	project := ""
	cmd := newTodolistsPositionCmd(&project)
	err := executeCommand(cmd, app, "701", "701")

	require.NotNil(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Duplicate todolist ID 701")
}

func TestTodolistsPositionURLArgPassesValidation(t *testing.T) {
	t.Setenv("BASECAMP_NONINTERACTIVE", "1")
	app, _ := setupTestApp(t)

	project := ""
	cmd := newTodolistsPositionCmd(&project)
	// A todolist URL should extract its ID and get past validation, reaching the
	// network layer (which fails) — not a "Invalid todolist ID" usage error.
	err := executeCommand(cmd, app,
		"https://3.basecamp.com/99999/buckets/2/todolists/789", "--to", "1")

	require.NotNil(t, err)
	var e *output.Error
	if errors.As(err, &e) {
		assert.NotEqual(t, "Invalid todolist ID", e.Message)
	}
}

// --- B. Request-level tests (httptest server records methods/paths/bodies) ---

type recordedRequest struct {
	method string
	path   string
	body   map[string]any
}

type requestRecorder struct {
	mu       sync.Mutex
	requests []recordedRequest
}

func (r *requestRecorder) record(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var body map[string]any
	if req.Body != nil {
		data, _ := io.ReadAll(req.Body)
		if len(data) > 0 {
			_ = json.Unmarshal(data, &body)
		}
	}
	r.requests = append(r.requests, recordedRequest{
		method: req.Method,
		path:   req.URL.Path,
		body:   body,
	})
}

func (r *requestRecorder) puts() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []recordedRequest
	for _, req := range r.requests {
		if req.method == http.MethodPut {
			out = append(out, req)
		}
	}
	return out
}

// todolistGetResponse describes a canned Get response for a todolist ID.
type todolistGetResponse struct {
	status    int
	parentID  int64
	bucketID  int64
	completed bool
	nilParent bool
	nilBucket bool
}

// newTodolistsPositionServer builds an httptest server. getResponses maps a
// todolist ID to its Get response; putStatus maps a todolist ID to the status
// its reposition PUT returns (defaults to 204 when absent).
func newTodolistsPositionServer(t *testing.T, rec *requestRecorder, getResponses map[int64]todolistGetResponse, putStatus map[int64]int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/position.json") {
			id := idFromPositionPath(r.URL.Path)
			status := http.StatusNoContent
			if s, ok := putStatus[id]; ok {
				status = s
			}
			w.WriteHeader(status)
			if status >= 400 {
				fmt.Fprintf(w, `{"error":"boom"}`)
			}
			return
		}

		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/todolists/") {
			id := idFromGetPath(r.URL.Path)
			resp, ok := getResponses[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprintf(w, `{"error":"not found"}`)
				return
			}
			if resp.status != 0 && resp.status != http.StatusOK {
				w.WriteHeader(resp.status)
				fmt.Fprintf(w, `{"error":"not found"}`)
				return
			}
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, todolistJSON(id, resp))
			return
		}

		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func idFromPositionPath(path string) int64 {
	// .../todosets/todolists/{id}/position.json
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if p == "todolists" && i+1 < len(parts) {
			var id int64
			fmt.Sscanf(parts[i+1], "%d", &id)
			return id
		}
	}
	return 0
}

func idFromGetPath(path string) int64 {
	// .../todolists/{id}
	parts := strings.Split(path, "/")
	last := parts[len(parts)-1]
	last = strings.TrimSuffix(last, ".json")
	var id int64
	fmt.Sscanf(last, "%d", &id)
	return id
}

func todolistJSON(id int64, resp todolistGetResponse) string {
	parent := fmt.Sprintf(`,"parent":{"id":%d,"title":"Set","type":"Todoset"}`, resp.parentID)
	if resp.nilParent {
		parent = ""
	}
	bucket := fmt.Sprintf(`,"bucket":{"id":%d,"name":"Proj","type":"Project"}`, resp.bucketID)
	if resp.nilBucket {
		bucket = ""
	}
	return fmt.Sprintf(`{"id":%d,"name":"List %d","completed":%t%s%s}`,
		id, id, resp.completed, parent, bucket)
}

// newRequestLevelApp wires an app to a live test server URL.
func newRequestLevelApp(t *testing.T, serverURL string) (*appctx.App, *bytes.Buffer) {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	buf := &bytes.Buffer{}
	cfg := &config.Config{AccountID: "99999"}
	authMgr := auth.NewManager(cfg, nil)
	sdkClient := basecamp.NewClient(
		&basecamp.Config{BaseURL: serverURL},
		&testTokenProvider{},
		basecamp.WithMaxRetries(1),
	)
	nameResolver := names.NewResolver(sdkClient, authMgr, cfg.AccountID)

	app := &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  nameResolver,
		Output: output.New(output.Options{Format: output.FormatJSON, Writer: buf}),
	}
	app.Flags.Hints = true // emit breadcrumbs (stripped otherwise)
	return app, buf
}

func TestTodolistsPositionBulkReverseOrderAndBodies(t *testing.T) {
	rec := &requestRecorder{}
	gets := map[int64]todolistGetResponse{
		701: {parentID: 10, bucketID: 20},
		702: {parentID: 10, bucketID: 20},
		703: {parentID: 10, bucketID: 20},
	}
	server := newTodolistsPositionServer(t, rec, gets, nil)
	app, buf := newRequestLevelApp(t, server.URL)

	project := ""
	cmd := newTodolistsPositionCmd(&project)
	err := executeCommand(cmd, app, "701", "702", "703")
	require.NoError(t, err)

	puts := rec.puts()
	require.Len(t, puts, 3)
	// Reverse apply order: 703, 702, 701; each body {"position":1}.
	assert.Equal(t, int64(703), idFromPositionPath(puts[0].path))
	assert.Equal(t, int64(702), idFromPositionPath(puts[1].path))
	assert.Equal(t, int64(701), idFromPositionPath(puts[2].path))
	for _, p := range puts {
		assert.Contains(t, p.path, "/todosets/todolists/")
		assert.Equal(t, float64(1), p.body["position"])
	}

	// Output shape: typed top→bottom order, not reversed.
	env := decodeEnvelope(t, buf)
	assert.True(t, env.OK)
	assert.Equal(t, true, env.Data["repositioned"])
	assert.Equal(t, float64(1), env.Data["position"])
	assert.Equal(t, []any{float64(701), float64(702), float64(703)}, env.Data["todolist_ids"])
}

func TestTodolistsPositionSingleBody(t *testing.T) {
	rec := &requestRecorder{}
	server := newTodolistsPositionServer(t, rec, nil, nil)
	app, _ := newRequestLevelApp(t, server.URL)

	project := ""
	cmd := newTodolistsPositionCmd(&project)
	err := executeCommand(cmd, app, "789", "--to", "3")
	require.NoError(t, err)

	puts := rec.puts()
	require.Len(t, puts, 1)
	assert.Equal(t, int64(789), idFromPositionPath(puts[0].path))
	assert.Equal(t, float64(3), puts[0].body["position"])
}

func TestTodolistsPositionRejectsDifferentParents(t *testing.T) {
	rec := &requestRecorder{}
	gets := map[int64]todolistGetResponse{
		701: {parentID: 10, bucketID: 20},
		702: {parentID: 11, bucketID: 20},
	}
	server := newTodolistsPositionServer(t, rec, gets, nil)
	app, _ := newRequestLevelApp(t, server.URL)

	project := ""
	cmd := newTodolistsPositionCmd(&project)
	err := executeCommand(cmd, app, "701", "702")

	require.NotNil(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "different todoset")
	assert.Empty(t, rec.puts(), "no PUT should be issued when preflight fails")
}

func TestTodolistsPositionRejectsNilParentOrBucket(t *testing.T) {
	rec := &requestRecorder{}
	gets := map[int64]todolistGetResponse{
		701: {parentID: 10, bucketID: 20},
		702: {nilParent: true, bucketID: 20},
	}
	server := newTodolistsPositionServer(t, rec, gets, nil)
	app, _ := newRequestLevelApp(t, server.URL)

	project := ""
	cmd := newTodolistsPositionCmd(&project)
	err := executeCommand(cmd, app, "701", "702")

	require.NotNil(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "missing its todoset or project context")
	assert.Empty(t, rec.puts())
}

func TestTodolistsPositionRejectsCompletedList(t *testing.T) {
	rec := &requestRecorder{}
	gets := map[int64]todolistGetResponse{
		701: {parentID: 10, bucketID: 20},
		702: {parentID: 10, bucketID: 20, completed: true},
	}
	server := newTodolistsPositionServer(t, rec, gets, nil)
	app, _ := newRequestLevelApp(t, server.URL)

	project := ""
	cmd := newTodolistsPositionCmd(&project)
	err := executeCommand(cmd, app, "701", "702")

	require.NotNil(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "#702")
	assert.Contains(t, e.Message, "completed")
	assert.Empty(t, rec.puts())
}

func TestTodolistsPositionPreflightFailureMeansZeroMutations(t *testing.T) {
	rec := &requestRecorder{}
	gets := map[int64]todolistGetResponse{
		701: {parentID: 10, bucketID: 20},
		702: {status: http.StatusNotFound},
	}
	server := newTodolistsPositionServer(t, rec, gets, nil)
	app, _ := newRequestLevelApp(t, server.URL)

	project := ""
	cmd := newTodolistsPositionCmd(&project)
	err := executeCommand(cmd, app, "701", "702")

	require.NotNil(t, err)
	assert.Empty(t, rec.puts(), "a preflight Get failure must issue no PUTs")
}

func TestTodolistsPositionPartialFailureAccounting(t *testing.T) {
	rec := &requestRecorder{}
	gets := map[int64]todolistGetResponse{
		701: {parentID: 10, bucketID: 20},
		702: {parentID: 10, bucketID: 20},
		703: {parentID: 10, bucketID: 20},
	}
	// Apply order is 703, 702, 701. Fail on 702 (the second PUT).
	server := newTodolistsPositionServer(t, rec, gets, map[int64]int{702: http.StatusForbidden})
	app, _ := newRequestLevelApp(t, server.URL)

	project := ""
	cmd := newTodolistsPositionCmd(&project)
	err := executeCommand(cmd, app, "701", "702", "703")

	require.NotNil(t, err)
	var e *output.Error
	require.True(t, errors.As(err, &e))
	// One applied (703) before failing at 702.
	assert.Contains(t, e.Message, "Reordered 1 of 3 todolists; failed at #702")
	assert.Contains(t, e.Hint, "Rerun the whole command")
	assert.Equal(t, http.StatusForbidden, e.HTTPStatus)

	// Exactly two PUTs recorded: it stopped instead of continuing to 701.
	assert.Len(t, rec.puts(), 2)
}

func TestTodolistsPositionBulkBreadcrumbUsesPreflightData(t *testing.T) {
	rec := &requestRecorder{}
	gets := map[int64]todolistGetResponse{
		701: {parentID: 55, bucketID: 66},
		702: {parentID: 55, bucketID: 66},
	}
	server := newTodolistsPositionServer(t, rec, gets, nil)
	app, buf := newRequestLevelApp(t, server.URL)
	// Configure an unrelated project — the breadcrumb must ignore it.
	app.Config.ProjectID = "999"

	project := ""
	cmd := newTodolistsPositionCmd(&project)
	err := executeCommand(cmd, app, "701", "702")
	require.NoError(t, err)

	env := decodeEnvelope(t, buf)
	require.NotEmpty(t, env.Breadcrumbs)
	joined := breadcrumbCmds(env)
	assert.Contains(t, joined, "--in 66")
	assert.Contains(t, joined, "--todoset 55")
	assert.NotContains(t, joined, "--in 999")
}

func TestTodolistsPositionSingleBreadcrumbContextKnown(t *testing.T) {
	rec := &requestRecorder{}
	server := newTodolistsPositionServer(t, rec, nil, nil)
	app, buf := newRequestLevelApp(t, server.URL)

	project := "myproj"
	cmd := newTodolistsPositionCmd(&project)
	err := executeCommand(cmd, app, "789", "--to", "1")
	require.NoError(t, err)

	env := decodeEnvelope(t, buf)
	require.NotEmpty(t, env.Breadcrumbs)
	joined := breadcrumbCmds(env)
	assert.Contains(t, joined, "basecamp todolists show 789 --in myproj")
	assert.Contains(t, joined, "basecamp todolists list --in myproj")
}

func TestTodolistsPositionSingleBreadcrumbProjectFromURL(t *testing.T) {
	rec := &requestRecorder{}
	server := newTodolistsPositionServer(t, rec, nil, nil)
	app, buf := newRequestLevelApp(t, server.URL)

	project := ""
	cmd := newTodolistsPositionCmd(&project)
	err := executeCommand(cmd, app,
		"https://3.basecamp.com/99999/buckets/42/todolists/789", "--to", "1")
	require.NoError(t, err)

	env := decodeEnvelope(t, buf)
	require.NotEmpty(t, env.Breadcrumbs)
	joined := breadcrumbCmds(env)
	assert.Contains(t, joined, "--in 42")
}

func TestTodolistsPositionSingleBreadcrumbContextAbsent(t *testing.T) {
	rec := &requestRecorder{}
	server := newTodolistsPositionServer(t, rec, nil, nil)
	app, buf := newRequestLevelApp(t, server.URL)
	// No project anywhere.

	project := ""
	cmd := newTodolistsPositionCmd(&project)
	err := executeCommand(cmd, app, "789", "--to", "1")
	require.NoError(t, err)

	// Exactly one request: the PUT. No project-resolution GETs.
	require.Len(t, rec.requests, 1)
	assert.Equal(t, http.MethodPut, rec.requests[0].method)

	env := decodeEnvelope(t, buf)
	assert.Empty(t, env.Breadcrumbs, "no breadcrumbs when no project context is known")
}

// --- helpers ---

type testEnvelope struct {
	OK          bool             `json:"ok"`
	Data        map[string]any   `json:"data"`
	Summary     string           `json:"summary"`
	Breadcrumbs []map[string]any `json:"breadcrumbs"`
}

func decodeEnvelope(t *testing.T, buf *bytes.Buffer) testEnvelope {
	t.Helper()
	var env testEnvelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	return env
}

func breadcrumbCmds(env testEnvelope) string {
	var b strings.Builder
	for _, bc := range env.Breadcrumbs {
		if cmd, ok := bc["cmd"].(string); ok {
			b.WriteString(cmd)
			b.WriteString("\n")
		}
	}
	return b.String()
}
