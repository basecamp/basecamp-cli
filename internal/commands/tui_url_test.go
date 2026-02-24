package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
)

func TestParseBasecampURL_ProjectOnly(t *testing.T) {
	target, scope, err := parseBasecampURL("https://3.basecamp.com/12345/buckets/67890")
	require.NoError(t, err)
	assert.Equal(t, workspace.ViewDock, target)
	assert.Equal(t, "12345", scope.AccountID)
	assert.Equal(t, int64(67890), scope.ProjectID)
	assert.Zero(t, scope.RecordingID)
}

func TestParseBasecampURL_WithRecording(t *testing.T) {
	target, scope, err := parseBasecampURL("https://3.basecamp.com/12345/buckets/67890/todos/11111")
	require.NoError(t, err)
	assert.Equal(t, workspace.ViewDetail, target)
	assert.Equal(t, "12345", scope.AccountID)
	assert.Equal(t, int64(67890), scope.ProjectID)
	assert.Equal(t, int64(11111), scope.RecordingID)
	assert.Equal(t, "Todo", scope.RecordingType, "should canonicalize todos → Todo")
}

func TestParseBasecampURL_Messages(t *testing.T) {
	target, scope, err := parseBasecampURL("https://3.basecamp.com/99/buckets/42/messages/7")
	require.NoError(t, err)
	assert.Equal(t, workspace.ViewDetail, target)
	assert.Equal(t, "99", scope.AccountID)
	assert.Equal(t, int64(42), scope.ProjectID)
	assert.Equal(t, int64(7), scope.RecordingID)
	assert.Equal(t, "Message", scope.RecordingType, "should canonicalize messages → Message")
}

func TestParseBasecampURL_Cards(t *testing.T) {
	target, scope, err := parseBasecampURL("https://3.basecamp.com/99/buckets/42/card_tables/cards/7")
	require.NoError(t, err)
	assert.Equal(t, workspace.ViewDetail, target)
	assert.Equal(t, "Card", scope.RecordingType, "should canonicalize cards → Card")
	assert.Equal(t, int64(7), scope.RecordingID)
	_ = target
}

func TestParseBasecampURL_InvalidURL(t *testing.T) {
	_, _, err := parseBasecampURL("not-a-url")
	assert.Error(t, err)
}

func TestParseBasecampURL_NonBasecampURL(t *testing.T) {
	_, _, err := parseBasecampURL("https://example.com/projects/123")
	assert.Error(t, err)
}

func TestParseBasecampURL_WithoutSubdomain(t *testing.T) {
	target, scope, err := parseBasecampURL("https://basecamp.com/12345/buckets/67890")
	require.NoError(t, err)
	assert.Equal(t, workspace.ViewDock, target)
	assert.Equal(t, "12345", scope.AccountID)
	assert.Equal(t, int64(67890), scope.ProjectID)
}

func TestParseBasecampURL_ProjectsPath(t *testing.T) {
	// /projects/{id} is the canonical project URL handled by the SDK router.
	target, scope, err := parseBasecampURL("https://3.basecamp.com/99/projects/42")
	require.NoError(t, err)
	assert.Equal(t, workspace.ViewDock, target)
	assert.Equal(t, "99", scope.AccountID)
	assert.Equal(t, int64(42), scope.ProjectID)
}

func TestParseBasecampURL_BucketWithExtraPath_UsesRouter(t *testing.T) {
	// /buckets/{id}/messages is a recording URL, not a bare bucket URL.
	// The SDK router should handle it (recording with type "messages").
	// If the router doesn't match, the bucket-only regex must NOT accept it.
	target, scope, err := parseBasecampURL("https://3.basecamp.com/99/buckets/42/messages/7")
	require.NoError(t, err)
	// The SDK router handles this as a recording URL
	assert.Equal(t, workspace.ViewDetail, target)
	assert.Equal(t, int64(7), scope.RecordingID)
}

func TestParseBasecampURL_BucketWithTrailingSlash(t *testing.T) {
	target, scope, err := parseBasecampURL("https://3.basecamp.com/12345/buckets/67890/")
	require.NoError(t, err)
	assert.Equal(t, workspace.ViewDock, target)
	assert.Equal(t, int64(67890), scope.ProjectID)
}

func TestParseBasecampURL_BucketWithQueryString(t *testing.T) {
	target, scope, err := parseBasecampURL("https://3.basecamp.com/12345/buckets/67890?foo=bar")
	require.NoError(t, err)
	assert.Equal(t, workspace.ViewDock, target)
	assert.Equal(t, int64(67890), scope.ProjectID)
}

func TestParseBasecampURL_BucketWithUnknownSegment_Rejected(t *testing.T) {
	// A URL with an unknown path segment after /buckets/{id} should be rejected,
	// not silently treated as a project URL.
	_, _, err := parseBasecampURL("https://3.basecamp.com/99/buckets/42/foobar")
	assert.Error(t, err)
}

func TestParseBasecampURL_CanonicalizesUpload(t *testing.T) {
	target, scope, err := parseBasecampURL("https://3.basecamp.com/99/buckets/42/uploads/7")
	require.NoError(t, err)
	assert.Equal(t, workspace.ViewDetail, target)
	assert.Equal(t, "Upload", scope.RecordingType, "should canonicalize uploads → Upload")
}

func TestParseBasecampURL_UnknownType_PassesThrough(t *testing.T) {
	// URL types not in the canonicalization map pass through as-is
	target, scope, err := parseBasecampURL("https://3.basecamp.com/99/buckets/42/chats/7")
	require.NoError(t, err)
	assert.Equal(t, workspace.ViewDetail, target)
	assert.Equal(t, "chats", scope.RecordingType, "unknown types pass through verbatim")
}
