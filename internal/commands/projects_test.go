package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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

type mockProjectUpdateTransport struct {
	getCount       int
	putCount       int
	putName        string
	putDescription string
	failRefetch    bool
}

func (t *mockProjectUpdateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")

	if !strings.Contains(req.URL.Path, "/projects/123") {
		return nil, fmt.Errorf("unexpected request path: %s", req.URL.Path)
	}

	switch req.Method {
	case http.MethodGet:
		t.getCount++
		if t.getCount > 1 && t.failRefetch {
			return jsonResponse(400, `{"error":"boom"}`, header), nil
		}
		description := "Old description"
		updatedAt := "2026-06-01T00:00:00.000Z"
		if t.getCount > 1 {
			description = "New description"
			updatedAt = "2026-06-02T00:00:00.000Z"
		}
		return jsonResponse(200, fmt.Sprintf(`{"id":123,"name":"Test Project","description":%q,"updated_at":%q}`, description, updatedAt), header), nil
	case http.MethodPut:
		t.putCount++
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, fmt.Errorf("decode update body: %w", err)
		}
		if name, ok := body["name"].(string); ok {
			t.putName = name
		}
		if description, ok := body["description"].(string); ok {
			t.putDescription = description
		}
		return jsonResponse(200, `{"id":123,"name":"Test Project","description":"Old description","updated_at":"2026-06-01T00:00:00.000Z"}`, header), nil
	default:
		return nil, fmt.Errorf("unexpected method: %s", req.Method)
	}
}

func jsonResponse(status int, body string, header http.Header) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
	}
}

func setupProjectsMockApp(t *testing.T, transport http.RoundTripper) (*appctx.App, *bytes.Buffer) {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	cfg := &config.Config{AccountID: "99999"}
	sdkClient := basecamp.NewClient(&basecamp.Config{}, &testTokenProvider{},
		basecamp.WithTransport(transport),
		basecamp.WithMaxRetries(1),
	)
	authMgr := auth.NewManager(cfg, nil)
	buf := &bytes.Buffer{}

	return &appctx.App{
		Config: cfg,
		Auth:   authMgr,
		SDK:    sdkClient,
		Names:  names.NewResolver(sdkClient, authMgr, cfg.AccountID),
		Output: output.New(output.Options{Format: output.FormatJSON, Writer: buf}),
	}, buf
}

func TestProjectsUpdateReturnsFreshProjectAfterDescriptionChange(t *testing.T) {
	transport := &mockProjectUpdateTransport{}
	app, out := setupProjectsMockApp(t, transport)

	cmd := NewProjectsCmd()
	err := executeCommand(cmd, app, "update", "123", "--description", "New description")
	require.NoError(t, err)

	assert.Equal(t, 1, transport.putCount)
	assert.Equal(t, "Test Project", transport.putName)
	assert.Equal(t, "New description", transport.putDescription)
	assert.Equal(t, 2, transport.getCount, "description-only update should fetch the current name, then refetch the fresh project after update")

	var envelope projectUpdateEnvelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope))
	assert.True(t, envelope.OK)
	assert.Equal(t, int64(123), envelope.Data.ID)
	assert.Equal(t, "Test Project", envelope.Data.Name)
	assert.Equal(t, "New description", envelope.Data.Description)
	assert.Equal(t, "2026-06-02T00:00:00Z", envelope.Data.UpdatedAt)
	assert.Empty(t, envelope.Notice)
}

func TestProjectsUpdateFallsBackToUpdateResponseWhenRefetchFails(t *testing.T) {
	transport := &mockProjectUpdateTransport{failRefetch: true}
	app, out := setupProjectsMockApp(t, transport)

	cmd := NewProjectsCmd()
	err := executeCommand(cmd, app, "update", "123", "--description", "New description")
	require.NoError(t, err)

	assert.Equal(t, 1, transport.putCount)
	assert.Equal(t, 2, transport.getCount)

	var envelope projectUpdateEnvelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope))
	assert.True(t, envelope.OK)
	assert.Equal(t, int64(123), envelope.Data.ID)
	assert.Equal(t, "Test Project", envelope.Data.Name)
	assert.Equal(t, "Old description", envelope.Data.Description)
	assert.Contains(t, envelope.Notice, "Project updated, but fetching the latest project state failed")
}

type projectUpdateEnvelope struct {
	OK     bool   `json:"ok"`
	Notice string `json:"notice"`
	Data   struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		UpdatedAt   string `json:"updated_at"`
	} `json:"data"`
}
