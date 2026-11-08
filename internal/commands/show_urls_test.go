package commands

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

func TestShowNativeItemAliases(t *testing.T) {
	for _, tc := range []struct {
		kind     string
		wireType string
		endpoint string
	}{
		{"inbox_replies", "Inbox::Reply", "/inbox_forwards/123/replies/789.json"},
		{"subtasks", "Kanban::Step", "/card_tables/steps/789.json"},
		{"google_documents", "GoogleDocument", "/google_documents/789.json"},
		{"cloud_files", "CloudFile", "/cloud_files/789.json"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			transport := &showTrackingTransport{responder: func(path string) (int, string) {
				if strings.HasSuffix(path, tc.endpoint) {
					return 200, `{"id":789,"type":"` + tc.wireType + `","content":"Full item content","parent":{"id":123},"bucket":{"id":456}}`
				}
				if strings.HasSuffix(path, "/recordings/789.json") {
					return 200, `{"id":789,"type":"` + tc.wireType + `","parent":{"id":123},"bucket":{"id":456}}`
				}
				return 404, `{}`
			}}
			reqs, stdout, _, err := runShowCmdCapture(t, transport, output.FormatJSON, "https://3.basecamp.com/99999/buckets/456/"+tc.kind+"/789", "--no-comments")
			require.NoError(t, err)
			require.NotEmpty(t, reqs)
			assert.True(t, strings.HasSuffix(reqs[len(reqs)-1], tc.endpoint))
			data := decodeShowJSONDataMap(t, decodeShowJSONEnvelope(t, stdout).Data)
			assert.JSONEq(t, `"Full item content"`, string(data["content"]))
		})
	}
}

func TestRecordingTypeEndpointOccurrence(t *testing.T) {
	data := map[string]any{"type": "Schedule::Entry::Occurrence", "parent": map[string]any{"id": json.Number("123")}, "url": "https://untrusted.example/99999/buckets/456/schedule_entries/123/occurrences/20261109.json"}
	assert.Equal(t, "/schedule_entries/123/occurrences/20261109.json", recordingTypeEndpoint(data, "789"))
	data["parent"] = map[string]any{"id": json.Number("999")}
	assert.Empty(t, recordingTypeEndpoint(data, "789"))
}

func TestRecordingTypeEndpointColumnSubtypes(t *testing.T) {
	for _, kind := range []string{"Kanban::Triage", "Kanban::DoneColumn", "Kanban::NotNowColumn"} {
		assert.Equal(t, "/card_tables/columns/42.json", recordingTypeEndpoint(map[string]any{"type": kind}, "42"))
	}
}

func TestRecordingTypeEndpointOnHoldIsNotAColumn(t *testing.T) {
	// On hold is a section inside a column, not a column recording itself.
	assert.Empty(t, recordingTypeEndpoint(map[string]any{"type": "Kanban::OnHold"}, "42"))
}

func TestShowRefusesInvalidItemArguments(t *testing.T) {
	for _, arg := range []string{
		"https://example.com/99999/buckets/456/todos/789",
		"https://3.basecamp.com/99999/buckets/456/uploads/789/versions/123",
		"https://3.basecamp.com/99999/buckets/456/questions/789/answers/by/123",
		"https://3.basecamp.com/99999/buckets/456/embeds/signed-id",
		"not-an-id", "-1", "+1", "0",
	} {
		t.Run(arg, func(t *testing.T) {
			transport := &showTrackingTransport{}
			reqs, err := runShowCmd(t, transport, arg)
			require.Error(t, err)
			assert.Empty(t, reqs)
		})
	}
}

func TestShowRejectsDifferentURLAccount(t *testing.T) {
	transport := &showTrackingTransport{}
	reqs, err := runShowCmd(t, transport, "https://3.basecamp.com/12345/buckets/456/todos/789")
	require.Error(t, err)
	assert.Empty(t, reqs)
}

func TestShowRejectsWrongURLProject(t *testing.T) {
	transport := &showTrackingTransport{responder: func(path string) (int, string) {
		return 200, `{"id":789,"type":"Todo","bucket":{"id":999}}`
	}}
	_, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/456/todos/789")
	require.Error(t, err)
}

func TestShowRefetchCannotEraseWrongURLProject(t *testing.T) {
	for _, route := range []string{"chats/123/lines/789", "inbox_forwards/123/replies/789"} {
		t.Run(route, func(t *testing.T) {
			wireType := "Chat::Lines::Text"
			if strings.HasPrefix(route, "inbox_forwards/") {
				wireType = "Inbox::Reply"
			}
			transport := &showTrackingTransport{responder: func(path string) (int, string) {
				if strings.HasSuffix(path, "/recordings/789.json") {
					return 200, `{"id":789,"type":"` + wireType + `","parent":{"id":123},"bucket":{"id":999}}`
				}
				if strings.HasSuffix(path, "/"+route+".json") {
					return 200, `{"id":789,"type":"` + wireType + `","parent":{"id":123},"content":"Full content"}`
				}
				return 404, `{}`
			}}
			_, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/456/"+route, "--no-comments")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "does not belong to the URL project")
		})
	}
}

func TestShowNestedURLRetainsSparseParent(t *testing.T) {
	for _, route := range []string{"chats/123/lines/789", "inbox_forwards/123/replies/789"} {
		for _, parentField := range []string{"", `,"parent":{"id":999}`, `,"parent":null`} {
			t.Run(route+parentField, func(t *testing.T) {
				wireType := "Chat::Lines::Text"
				if strings.HasPrefix(route, "inbox_forwards/") {
					wireType = "Inbox::Reply"
				}
				transport := &showTrackingTransport{responder: func(path string) (int, string) {
					if strings.HasSuffix(path, "/recordings/789.json") {
						return 200, `{"id":789,"type":"` + wireType + `","parent":{"id":123},"bucket":{"id":456}}`
					}
					if strings.HasSuffix(path, "/"+route+".json") {
						return 200, `{"id":789,"type":"` + wireType + `","content":"Full content"` + parentField + `}`
					}
					return 404, `{}`
				}}
				_, stdout, _, err := runShowCmdCapture(t, transport, output.FormatJSON, "https://3.basecamp.com/99999/buckets/456/"+route, "--no-comments")
				if parentField != "" {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				data := decodeShowJSONDataMap(t, decodeShowJSONEnvelope(t, stdout).Data)
				assert.JSONEq(t, `"Full content"`, string(data["content"]))
				assert.JSONEq(t, `{"id":123}`, string(data["parent"]))
			})
		}
	}
}

func TestShowNullRefetchKeepsSparseRecording(t *testing.T) {
	transport := &showTrackingTransport{responder: func(path string) (int, string) {
		if strings.HasSuffix(path, "/recordings/789.json") {
			return 200, `{"id":789,"type":"Todo","title":"Sparse title"}`
		}
		return 200, `null`
	}}
	_, stdout, _, err := runShowCmdCapture(t, transport, output.FormatJSON, "789", "--no-comments")
	require.NoError(t, err)
	data := decodeShowJSONDataMap(t, decodeShowJSONEnvelope(t, stdout).Data)
	assert.JSONEq(t, `"Sparse title"`, string(data["title"]))
}

func TestShowChatContainerIsNotItsOwnParent(t *testing.T) {
	transport := &showTrackingTransport{responder: func(path string) (int, string) {
		return 200, `{"id":789,"type":"Chat::Transcript","bucket":{"id":456}}`
	}}
	_, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/456/chats/789")
	require.NoError(t, err)
}

func TestShowAcceptsNumericallyEquivalentURLContext(t *testing.T) {
	for _, route := range []string{
		"todos/789",
		"inbox_forwards/00123/replies/789",
		"chats/00123/lines/789",
	} {
		t.Run(route, func(t *testing.T) {
			transport := &showTrackingTransport{responder: func(path string) (int, string) {
				return 200, `{"id":789,"type":"Todo","parent":{"id":123},"bucket":{"id":456}}`
			}}
			_, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/00456/"+route)
			require.NoError(t, err)
		})
	}
}

func TestRecordingTypeEndpointOccurrenceNumericParent(t *testing.T) {
	data := map[string]any{"type": "Schedule::Entry::Occurrence", "parent": map[string]any{"id": json.Number("123")}, "url": "https://3.basecamp.com/99999/buckets/456/schedule_entries/00123/occurrences/20261109.json"}
	assert.Equal(t, "/schedule_entries/00123/occurrences/20261109.json", recordingTypeEndpoint(data, "789"))
}

func TestShowRejectsWrongForwardParent(t *testing.T) {
	transport := &showTrackingTransport{responder: func(path string) (int, string) {
		return 200, `{"id":789,"type":"Inbox::Reply","parent":{"id":999},"bucket":{"id":456}}`
	}}
	_, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/456/inbox_forwards/123/replies/789")
	require.Error(t, err)
}

func TestShowRejectsWrongChatParent(t *testing.T) {
	transport := &showTrackingTransport{responder: func(path string) (int, string) {
		return 200, `{"id":789,"type":"Chat::Lines::Text","parent":{"id":999},"bucket":{"id":456}}`
	}}
	_, err := runShowCmd(t, transport, "https://3.basecamp.com/99999/buckets/456/chats/123/lines/789")
	require.Error(t, err)
}

func TestShowRefetchesRealReplyContent(t *testing.T) {
	transport := &showTrackingTransport{responder: func(path string) (int, string) {
		if strings.Contains(path, "/recordings/") {
			return 200, `{"id":789,"type":"Inbox::Reply","parent":{"id":123}}`
		}
		if strings.Contains(path, "/inbox_forwards/123/replies/789.json") {
			return 200, `{"id":789,"type":"Inbox::Reply","content":"Thanks!"}`
		}
		return 404, `{}`
	}}
	_, stdout, _, err := runShowCmdCapture(t, transport, output.FormatJSON, "replies", "789", "--no-comments")
	require.NoError(t, err)
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &envelope))
	assert.Equal(t, "Thanks!", envelope.Data["content"])
}
