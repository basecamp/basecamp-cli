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
	"sync"
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

func setupAssignTestApp(t *testing.T) (*appctx.App, *bytes.Buffer) {
	t.Helper()
	return setupTodosTestApp(t)
}

func executeAssignCommand(cmd *cobra.Command, app *appctx.App, args ...string) error {
	cmd.SetArgs(args)
	ctx := appctx.WithApp(context.Background(), app)
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd.Execute()
}

func TestAssignRequiresID(t *testing.T) {
	app := setupAssignGuardTestApp(t)

	cmd := NewAssignCmd()
	err := executeAssignCommand(cmd, app)
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "required")
}

func TestUnassignRequiresID(t *testing.T) {
	app := setupAssignGuardTestApp(t)

	cmd := NewUnassignCmd()
	err := executeAssignCommand(cmd, app)
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "required")
}

func TestAssignCardAndStepMutuallyExclusive(t *testing.T) {
	app, _ := setupAssignTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewAssignCmd()
	err := executeAssignCommand(cmd, app, "456", "--card", "--step", "--to", "me")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Cannot use --card and --step together")
}

func TestUnassignCardAndStepMutuallyExclusive(t *testing.T) {
	app, _ := setupAssignTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewUnassignCmd()
	err := executeAssignCommand(cmd, app, "456", "--card", "--step", "--from", "me")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Cannot use --card and --step together")
}

func TestAssignRequiresProject(t *testing.T) {
	app, _ := setupAssignTestApp(t)
	// No project configured — should fail before reaching assignee check

	cmd := NewAssignCmd()
	err := executeAssignCommand(cmd, app, "456", "--to", "me")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "Project ID required", e.Message)
}

func TestUnassignRequiresProject(t *testing.T) {
	app, _ := setupAssignTestApp(t)

	cmd := NewUnassignCmd()
	err := executeAssignCommand(cmd, app, "456", "--from", "me")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "Project ID required", e.Message)
}

func TestAssignHasCardFlag(t *testing.T) {
	cmd := NewAssignCmd()
	flag := cmd.Flags().Lookup("card")
	require.NotNil(t, flag)
	assert.Equal(t, "false", flag.DefValue)
}

func TestAssignHasStepFlag(t *testing.T) {
	cmd := NewAssignCmd()
	flag := cmd.Flags().Lookup("step")
	require.NotNil(t, flag)
	assert.Equal(t, "false", flag.DefValue)
}

func TestUnassignHasCardFlag(t *testing.T) {
	cmd := NewUnassignCmd()
	flag := cmd.Flags().Lookup("card")
	require.NotNil(t, flag)
	assert.Equal(t, "false", flag.DefValue)
}

func TestUnassignHasStepFlag(t *testing.T) {
	cmd := NewUnassignCmd()
	flag := cmd.Flags().Lookup("step")
	require.NotNil(t, flag)
	assert.Equal(t, "false", flag.DefValue)
}

func TestAssignDefaultsTodoWithProjectConfig(t *testing.T) {
	app, _ := setupAssignTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewAssignCmd()
	err := executeAssignCommand(cmd, app, "456", "--to", "me")
	require.Error(t, err)

	// Should proceed past input validation and fail on network (not input validation)
	var e *output.Error
	if errors.As(err, &e) {
		assert.NotContains(t, e.Message, "Cannot use --card and --step")
		assert.NotContains(t, e.Message, "Person to assign is required")
	}
}

func TestAssignCardWithProjectConfig(t *testing.T) {
	app, _ := setupAssignTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewAssignCmd()
	err := executeAssignCommand(cmd, app, "456", "--card", "--to", "me")
	require.Error(t, err)

	// Should proceed past input validation and fail on network
	var e *output.Error
	if errors.As(err, &e) {
		assert.NotContains(t, e.Message, "Cannot use --card and --step")
		assert.NotContains(t, e.Message, "Person to assign is required")
	}
}

func TestAssignStepWithProjectConfig(t *testing.T) {
	app, _ := setupAssignTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewAssignCmd()
	err := executeAssignCommand(cmd, app, "456", "--step", "--to", "me")
	require.Error(t, err)

	// Should proceed past input validation and fail on network
	var e *output.Error
	if errors.As(err, &e) {
		assert.NotContains(t, e.Message, "Cannot use --card and --step")
		assert.NotContains(t, e.Message, "Person to assign is required")
	}
}

func TestAssignAcceptsMultipleIDs(t *testing.T) {
	app, _ := setupAssignTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewAssignCmd()
	err := executeAssignCommand(cmd, app, "111", "222", "--to", "me")
	require.Error(t, err)

	// Should not fail on arg count — should proceed into the batch loop
	assert.NotContains(t, err.Error(), "accepts")
	assert.NotContains(t, err.Error(), "required")
}

func TestUnassignAcceptsMultipleIDs(t *testing.T) {
	app, _ := setupAssignTestApp(t)
	app.Config.ProjectID = "123"

	cmd := NewUnassignCmd()
	err := executeAssignCommand(cmd, app, "111", "222", "--from", "me")
	require.Error(t, err)

	assert.NotContains(t, err.Error(), "accepts")
	assert.NotContains(t, err.Error(), "required")
}

func TestAssignHelpMentionsCardAndStep(t *testing.T) {
	cmd := NewAssignCmd()
	assert.Contains(t, cmd.Long, "--card")
	assert.Contains(t, cmd.Long, "--step")
	assert.Contains(t, cmd.Long, "card step")
}

func TestUnassignHelpMentionsCardAndStep(t *testing.T) {
	cmd := NewUnassignCmd()
	assert.Contains(t, cmd.Long, "--card")
	assert.Contains(t, cmd.Long, "--step")
	assert.Contains(t, cmd.Long, "card step")
}

func TestExistingAssigneeIDs(t *testing.T) {
	ids := existingAssigneeIDs(nil)
	assert.Empty(t, ids)
}

func TestContainsID(t *testing.T) {
	assert.True(t, containsID([]int64{1, 2, 3}, 2))
	assert.False(t, containsID([]int64{1, 2, 3}, 4))
	assert.False(t, containsID(nil, 1))
}

func TestRemoveID(t *testing.T) {
	assert.Equal(t, []int64{1, 3}, removeID([]int64{1, 2, 3}, 2))
	assert.Equal(t, []int64{1, 2, 3}, removeID([]int64{1, 2, 3}, 4))
}

func TestFindAssigneeName(t *testing.T) {
	// Uses basecamp.Person from SDK, tested indirectly through the helper
	assert.Equal(t, "Unknown", findAssigneeName(nil, 1))
}

func TestNotFoundOrConvertReturnsTypedNotFound(t *testing.T) {
	tests := []struct {
		typeName string
		itemID   string
	}{
		{"to-do", "99999"},
		{"card", "88888"},
		{"step", "77777"},
	}

	for _, tt := range tests {
		t.Run(tt.typeName, func(t *testing.T) {
			sdkErr := basecamp.ErrNotFound(tt.typeName, tt.itemID)
			err := notFoundOrConvert(sdkErr, tt.typeName, tt.itemID)

			var e *output.Error
			require.True(t, errors.As(err, &e))
			assert.Equal(t, basecamp.CodeNotFound, e.Code)
			assert.Contains(t, e.Message, fmt.Sprintf("%s not found: %s", tt.typeName, tt.itemID))
		})
	}
}

func TestNotFoundOrConvertPassesThroughOtherErrors(t *testing.T) {
	sdkErr := basecamp.ErrForbidden("access denied")
	err := notFoundOrConvert(sdkErr, "to-do", "123")

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.NotEqual(t, basecamp.CodeNotFound, e.Code)
}

func TestBatchFailErrorPreservesTypedError(t *testing.T) {
	firstErr := output.ErrNotFound("to-do", "111")
	err := batchFailError("assign", []string{"111", "222"}, firstErr)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, basecamp.CodeNotFound, e.Code)
	assert.Contains(t, e.Message, "111, 222")
	assert.Contains(t, e.Message, "Failed to assign")
}

func TestBatchFailErrorWrapsPlainError(t *testing.T) {
	firstErr := fmt.Errorf("network down")
	err := batchFailError("unassign", []string{"111"}, firstErr)

	assert.Contains(t, err.Error(), "failed to unassign")
	assert.Contains(t, err.Error(), "111")
	assert.ErrorIs(t, err, firstErr)
}

func TestBatchFailErrorFallsBackToUsage(t *testing.T) {
	err := batchFailError("assign", []string{"abc", "def"}, nil)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Invalid item ID(s): abc, def")
}

// assignGuardTransport serves project resolution but errors on item-fetch endpoints.
// This proves the non-interactive guard short-circuits before any item lookup.
type assignGuardTransport struct{}

func (assignGuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	path := req.URL.Path
	if req.Method == "GET" && strings.Contains(path, "/projects.json") {
		body := `[{"id": 123, "name": "Test Project"}]`
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	// Any other request means the guard didn't fire — the command tried to
	// fetch a todo/card/step or resolve a person.
	return nil, fmt.Errorf("unexpected HTTP request: %s %s — guard should have short-circuited", req.Method, path)
}

// setupAssignGuardTestApp creates an app whose transport errors on item-fetch calls.
func setupAssignGuardTestApp(t *testing.T) *appctx.App {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
	}

	authMgr := auth.NewManager(cfg, nil)
	sdkClient := basecamp.NewClient(&basecamp.Config{BaseURL: "https://3.basecampapi.com"}, &todosTestTokenProvider{},
		basecamp.WithTransport(assignGuardTransport{}),
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
			Writer: &bytes.Buffer{},
		}),
	}
	app.Flags.JSON = true
	return app
}

func TestAssignRequiresAssigneeNonInteractive(t *testing.T) {
	app := setupAssignGuardTestApp(t)

	cmd := NewAssignCmd()
	err := executeAssignCommand(cmd, app, "456", "-p", "123")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Person to assign is required")
	assert.Contains(t, e.Hint, "Use --to")
}

func TestUnassignRequiresAssigneeNonInteractive(t *testing.T) {
	app := setupAssignGuardTestApp(t)

	cmd := NewUnassignCmd()
	err := executeAssignCommand(cmd, app, "456", "-p", "123")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Person to unassign is required")
	assert.Contains(t, e.Hint, "Use --from")
}

func TestAssignCardRequiresAssigneeNonInteractive(t *testing.T) {
	app := setupAssignGuardTestApp(t)

	cmd := NewAssignCmd()
	err := executeAssignCommand(cmd, app, "456", "--card", "-p", "123")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Person to assign is required")
	assert.Contains(t, e.Hint, "Use --to")
}

// stepPathTransport captures the request path for step-fetch calls.
type stepPathTransport struct {
	capturedPath string
}

func (s *stepPathTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	path := req.URL.Path
	if req.Method == "GET" && strings.Contains(path, "/projects.json") {
		body := `[{"id": 123, "name": "Test Project"}]`
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	if req.Method == "GET" && strings.Contains(path, "card_tables/steps/") {
		s.capturedPath = path
		body := `{"id": 456, "title": "Test Step", "assignees": []}`
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	if req.Method == "GET" && strings.Contains(path, "/my/profile.json") {
		body := `{"id": 999, "name": "Test User"}`
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}, nil
	}

	return nil, fmt.Errorf("unexpected HTTP request: %s %s", req.Method, path)
}

func setupStepPathTestApp(t *testing.T, transport *stepPathTransport) *appctx.App {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
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
			Writer: &bytes.Buffer{},
		}),
	}
	app.Flags.JSON = true
	return app
}

func TestValidateStepUsesSDKGet(t *testing.T) {
	t.Run("assign", func(t *testing.T) {
		transport := &stepPathTransport{}
		app := setupStepPathTestApp(t, transport)

		cmd := NewAssignCmd()
		_ = executeAssignCommand(cmd, app, "456", "--step", "--to", "me", "-p", "123")

		require.NotEmpty(t, transport.capturedPath, "step-fetch request was never made")
		assert.Contains(t, transport.capturedPath, "card_tables/steps/456")
	})

	t.Run("unassign", func(t *testing.T) {
		transport := &stepPathTransport{}
		app := setupStepPathTestApp(t, transport)

		cmd := NewUnassignCmd()
		_ = executeAssignCommand(cmd, app, "456", "--step", "--from", "me", "-p", "123")

		require.NotEmpty(t, transport.capturedPath, "step-fetch request was never made")
		assert.Contains(t, transport.capturedPath, "card_tables/steps/456")
	})
}

func TestUnassignStepRequiresAssigneeNonInteractive(t *testing.T) {
	app := setupAssignGuardTestApp(t)

	cmd := NewUnassignCmd()
	err := executeAssignCommand(cmd, app, "456", "--step", "-p", "123")
	require.Error(t, err)

	var e *output.Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Message, "Person to unassign is required")
	assert.Contains(t, e.Hint, "Use --from")
}

// assignBatchTransport serves controlled responses for batch assign tests.
// It tracks request order to verify lazy assignee resolution.
type assignBatchTransport struct {
	mu           sync.Mutex
	validTodoIDs map[string]bool // true = 200 with todo, false = 404
	requestLog   []string        // ordered log of request paths
}

func (t *assignBatchTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	path := req.URL.Path
	t.mu.Lock()
	t.requestLog = append(t.requestLog, req.Method+" "+path)
	t.mu.Unlock()

	// Project resolution
	if req.Method == "GET" && strings.Contains(path, "/projects.json") {
		body := `[{"id": 123, "name": "Test Project"}]`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: header}, nil
	}

	// Person "me" resolution
	if req.Method == "GET" && strings.Contains(path, "/my/profile.json") {
		body := `{"id": 42, "name": "Test User"}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: header}, nil
	}

	// Todo GET
	if req.Method == "GET" && strings.Contains(path, "/todos/") {
		for id, valid := range t.validTodoIDs {
			if strings.Contains(path, "/todos/"+id) {
				if valid {
					body := fmt.Sprintf(`{"id": %s, "title": "Test Todo %s", "content": "Test Todo %s", "assignees": []}`, id, id, id)
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: header}, nil
				}
				break
			}
		}
		return &http.Response{
			StatusCode: 404,
			Body:       io.NopCloser(strings.NewReader(`{"status": 404, "error": "The resource was not found."}`)),
			Header:     header,
		}, nil
	}

	// Todo UPDATE
	if req.Method == "PUT" && strings.Contains(path, "/todos/") {
		body := `{"id": 222, "title": "Updated Todo", "assignees": [{"id": 42, "name": "Test User"}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: header}, nil
	}

	return nil, fmt.Errorf("unexpected request: %s %s", req.Method, path)
}

func setupAssignBatchTestApp(t *testing.T, transport *assignBatchTransport) (*appctx.App, *bytes.Buffer) {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	buf := &bytes.Buffer{}
	cfg := &config.Config{
		AccountID: "99999",
		ProjectID: "123",
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
	app.Flags.JSON = true
	return app, buf
}

func TestAssignBatchPartialSuccess(t *testing.T) {
	transport := &assignBatchTransport{
		validTodoIDs: map[string]bool{
			"111": false, // 404
			"222": true,  // exists
		},
	}
	app, buf := setupAssignBatchTestApp(t, transport)

	cmd := NewAssignCmd()
	err := executeAssignCommand(cmd, app, "111", "222", "--to", "me", "-p", "123")
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Assigned 1, failed 1")
}

func TestUnassignBatchPartialSuccess(t *testing.T) {
	transport := &assignBatchTransport{
		validTodoIDs: map[string]bool{
			"111": false,
			"222": true,
		},
	}
	app, buf := setupAssignBatchTestApp(t, transport)

	cmd := NewUnassignCmd()
	err := executeAssignCommand(cmd, app, "111", "222", "--from", "me", "-p", "123")
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Unassigned 1, failed 1")
}

func TestAssignBatchAllFail(t *testing.T) {
	transport := &assignBatchTransport{
		validTodoIDs: map[string]bool{
			"111": false,
			"222": false,
		},
	}
	app, _ := setupAssignBatchTestApp(t, transport)

	cmd := NewAssignCmd()
	err := executeAssignCommand(cmd, app, "111", "222", "--to", "me", "-p", "123")
	require.Error(t, err)

	// Should contain both failed IDs
	assert.Contains(t, err.Error(), "111")
	assert.Contains(t, err.Error(), "222")
	assert.Contains(t, err.Error(), "Failed to assign")
}

func TestAssignBatchLazyResolution(t *testing.T) {
	// First ID fails validation (404), second succeeds.
	// Person resolution should only happen AFTER the first valid item.
	transport := &assignBatchTransport{
		validTodoIDs: map[string]bool{
			"111": false, // 404
			"222": true,  // exists
		},
	}
	app, _ := setupAssignBatchTestApp(t, transport)

	cmd := NewAssignCmd()
	err := executeAssignCommand(cmd, app, "111", "222", "--to", "me", "-p", "123")
	require.NoError(t, err)

	// Verify request ordering: todo/111 (404), todo/222 validation GET,
	// THEN profile (me resolution), THEN the SDK's merge GET, THEN update.
	transport.mu.Lock()
	log := transport.requestLog
	transport.mu.Unlock()

	var todoGets []int
	profileIdx, putIdx := -1, -1
	for i, entry := range log {
		switch {
		case strings.HasPrefix(entry, "GET") && strings.Contains(entry, "/todos/222"):
			todoGets = append(todoGets, i)
		case strings.Contains(entry, "/my/profile.json"):
			profileIdx = i
		case strings.HasPrefix(entry, "PUT") && strings.Contains(entry, "/todos/222"):
			putIdx = i
		}
	}
	require.Len(t, todoGets, 2, "expected a validation GET and an SDK merge GET for /todos/222, got: %v", log)
	require.NotEqual(t, -1, profileIdx, "expected GET /my/profile.json in request log")
	require.NotEqual(t, -1, putIdx, "expected PUT for /todos/222 in request log")
	assert.Greater(t, profileIdx, todoGets[0], "person resolution should happen after the valid todo is fetched")
	assert.Greater(t, todoGets[1], profileIdx, "SDK merge GET should follow person resolution")
	assert.Greater(t, putIdx, todoGets[1], "update PUT should follow the SDK merge GET")
}

func TestAssignBatchAllFailNeverResolvesAssignee(t *testing.T) {
	// If all items fail validation, assignee resolution should never be attempted.
	transport := &assignBatchTransport{
		validTodoIDs: map[string]bool{
			"111": false,
			"222": false,
		},
	}
	app, _ := setupAssignBatchTestApp(t, transport)

	cmd := NewAssignCmd()
	_ = executeAssignCommand(cmd, app, "111", "222", "--to", "me", "-p", "123")

	transport.mu.Lock()
	log := transport.requestLog
	transport.mu.Unlock()

	for _, entry := range log {
		assert.NotContains(t, entry, "/my/profile.json",
			"person resolution should not be attempted when all items fail validation")
	}
}

// mockStepAssignTransport serves the current step on GET and captures the
// PUT body so the assign/unassign step paths can be checked for the
// carried-over title the API requires.
type mockStepAssignTransport struct {
	capturedPut []byte
}

func (m *mockStepAssignTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	stepJSON := `{"id": 456, "title": "Existing step", "completed": false, "assignees": [{"id": 11, "name": "Existing Person"}]}`

	switch req.Method {
	case "GET":
		var body string
		switch {
		case strings.Contains(req.URL.Path, "/card_tables/steps/"):
			body = stepJSON
		case strings.Contains(req.URL.Path, "/projects.json"):
			body = `[{"id": 123, "name": "Test Project"}]`
		case strings.Contains(req.URL.Path, "/projects/"):
			body = `{"id": 123, "dock": [{"name": "kanban_board", "id": 789, "title": "Card Table"}]}`
		case strings.Contains(req.URL.Path, "/people.json"):
			body = `[{"id": 11, "name": "Existing Person"}, {"id": 99, "name": "New Person"}]`
		default:
			return nil, fmt.Errorf("unexpected GET path: %s", req.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: header}, nil
	case "PUT":
		if !strings.HasSuffix(req.URL.Path, "/card_tables/steps/456") {
			return nil, fmt.Errorf("unexpected PUT path: %s", req.URL.Path)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		m.capturedPut = body
		if err := req.Body.Close(); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(stepJSON)), Header: header}, nil
	default:
		return nil, fmt.Errorf("unexpected request method: %s", req.Method)
	}
}

// TestAssignStepCarriesTitle verifies that assigning a person to a step sends
// the current title in the update, which the API requires.
func TestAssignStepCarriesTitle(t *testing.T) {
	transport := &mockStepAssignTransport{}
	app := setupCardsMockApp(t, transport)

	cmd := NewAssignCmd()
	err := executeAssignCommand(cmd, app, "456", "--step", "--to", "99")
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedPut, &body))
	assert.Equal(t, "Existing step", body["title"])
	assert.Equal(t, []any{float64(11), float64(99)}, body["assignee_ids"])
}

// TestUnassignStepCarriesTitle verifies that removing a person from a step
// also sends the current title in the update.
func TestUnassignStepCarriesTitle(t *testing.T) {
	transport := &mockStepAssignTransport{}
	app := setupCardsMockApp(t, transport)

	cmd := NewUnassignCmd()
	err := executeAssignCommand(cmd, app, "456", "--step", "--from", "11")
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(transport.capturedPut, &body))
	assert.Equal(t, "Existing step", body["title"])
	assert.Equal(t, []any{}, body["assignee_ids"])
}
