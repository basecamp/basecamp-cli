package data

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	lineInPing = `{"id":111,"type":"Chat::Lines::Text","parent":{"id":222,"type":"Chat::Transcript"},"bucket":{"id":333,"type":"Circle"}}`
	deleteLine = "DELETE /99999/chats/222/lines/111"
	trashLine  = "PUT /99999/recordings/111/status/trashed.json"
	readLine   = "GET /99999/buckets/333/recordings/111.json"
)

type removeReply struct {
	status int
	body   string
}

// removeTransport answers from a table keyed by "METHOD path" and records
// every request; anything without a route answers 404.
type removeTransport struct {
	mu       sync.Mutex
	routes   map[string]removeReply
	requests []string
}

func (rt *removeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	call := req.Method + " " + req.URL.Path
	rt.mu.Lock()
	rt.requests = append(rt.requests, call)
	rt.mu.Unlock()
	reply, ok := rt.routes[call]
	if !ok {
		reply = removeReply{http.StatusNotFound, `{"error":"Not Found"}`}
	}
	return &http.Response{
		StatusCode: reply.status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(reply.body)),
		Request:    req,
	}, nil
}

func (rt *removeTransport) recorded() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]string(nil), rt.requests...)
}

func removeHub(routes map[string]removeReply) (*Hub, *removeTransport) {
	transport := &removeTransport{routes: routes}
	sdk := basecamp.NewClient(&basecamp.Config{BaseURL: "https://3.basecampapi.com"}, hubCheckinsTestTokenProvider{},
		basecamp.WithTransport(transport), basecamp.WithMaxRetries(1))
	return NewHub(NewMultiStore(sdk), ""), transport
}

// A line whose type and Campfire the caller has is deleted from that Campfire
// without another read.
func TestRemoveRecording_KnownLineDeletes(t *testing.T) {
	h, transport := removeHub(map[string]removeReply{deleteLine: {http.StatusNoContent, ""}})

	deleted, err := h.RemoveRecording(context.Background(), "99999", 333, 111, "Chat::Lines::RichText", 222)

	require.NoError(t, err)
	assert.True(t, deleted)
	assert.Equal(t, []string{deleteLine}, transport.recorded())
}

// A line whose Campfire the caller lacks is read to find it, and deleted from
// it, never trashed.
func TestRemoveRecording_ReadsALinesCampfire(t *testing.T) {
	h, transport := removeHub(map[string]removeReply{
		readLine:   {http.StatusOK, lineInPing},
		deleteLine: {http.StatusNoContent, ""},
	})

	deleted, err := h.RemoveRecording(context.Background(), "99999", 333, 111, "Chat::Lines::Text", 0)

	require.NoError(t, err)
	assert.True(t, deleted)
	assert.Equal(t, []string{readLine, deleteLine}, transport.recorded())
}

// Anything that isn't a line is trashed, with no read.
func TestRemoveRecording_OtherRecordingsTrash(t *testing.T) {
	const trashTodo = "PUT /99999/recordings/100/status/trashed.json"
	h, transport := removeHub(map[string]removeReply{trashTodo: {http.StatusNoContent, ""}})

	deleted, err := h.RemoveRecording(context.Background(), "99999", 333, 100, "Todo", 0)

	require.NoError(t, err)
	assert.False(t, deleted)
	assert.Equal(t, []string{trashTodo}, transport.recorded())
}

// Without the recording's type nothing is sent: the caller's confirmation
// can't have said whether this would trash it or delete it for good.
func TestRemoveRecording_WritesNothingWithoutAType(t *testing.T) {
	h, transport := removeHub(map[string]removeReply{
		deleteLine: {http.StatusNoContent, ""},
		trashLine:  {http.StatusNoContent, ""},
	})

	_, err := h.RemoveRecording(context.Background(), "99999", 333, 111, "", 0)

	require.Error(t, err)
	assert.Empty(t, transport.recorded())
}

// When the read can't name a line's Campfire, nothing is written: trashing it
// instead is how a line would reach the generic route.
func TestRemoveRecording_WritesNothingWithoutALinesCampfire(t *testing.T) {
	cases := []struct {
		name string
		read removeReply
	}{
		{"unreadable", removeReply{http.StatusNotFound, `{"error":"Not Found"}`}},
		{"no content", removeReply{http.StatusNoContent, ""}},
		{"no parent", removeReply{http.StatusOK, `{"id":111,"type":"Chat::Lines::Text"}`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, transport := removeHub(map[string]removeReply{
				readLine:   c.read,
				deleteLine: {http.StatusNoContent, ""},
				trashLine:  {http.StatusNoContent, ""},
			})

			_, err := h.RemoveRecording(context.Background(), "99999", 333, 111, "Chat::Lines::Text", 0)

			require.Error(t, err)
			assert.Equal(t, []string{readLine}, transport.recorded())
		})
	}
}

// RecordingKind reads the API's type and the parent for a caller that has
// only a label.
func TestRecordingKind(t *testing.T) {
	h, _ := removeHub(map[string]removeReply{readLine: {http.StatusOK, lineInPing}})

	recordingType, parentID, err := h.RecordingKind(context.Background(), "99999", 333, 111)

	require.NoError(t, err)
	assert.Equal(t, "Chat::Lines::Text", recordingType)
	assert.Equal(t, int64(222), parentID)
}

func TestIsCampfireLine(t *testing.T) {
	for _, line := range []string{"Chat::Lines::Text", "Chat::Lines::RichText", "Chat::Lines::Upload"} {
		assert.True(t, IsCampfireLine(line), line)
	}
	for _, other := range []string{"Chat::Transcript", "Campfire::Line", "Chat", "Todo", ""} {
		assert.False(t, IsCampfireLine(other), other)
	}
}
