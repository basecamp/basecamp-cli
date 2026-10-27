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
	readLine   = "GET /99999/recordings/111.json"
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

	deleted, err := h.RemoveRecording(context.Background(), "99999", 111, "Chat::Lines::RichText", 222)

	require.NoError(t, err)
	assert.True(t, deleted)
	assert.Equal(t, []string{deleteLine}, transport.recorded())
}

// A line whose Campfire the caller lacks, or a recording whose type it knows
// only by a label or not at all, is read first, and a line found that way is
// deleted, never trashed.
func TestRemoveRecording_ReadsWhatItLacks(t *testing.T) {
	for _, recordingType := range []string{"Chat::Lines::Text", ""} {
		t.Run("type "+recordingType, func(t *testing.T) {
			h, transport := removeHub(map[string]removeReply{
				readLine:   {http.StatusOK, lineInPing},
				deleteLine: {http.StatusNoContent, ""},
			})

			deleted, err := h.RemoveRecording(context.Background(), "99999", 111, recordingType, 0)

			require.NoError(t, err)
			assert.True(t, deleted)
			assert.Equal(t, []string{readLine, deleteLine}, transport.recorded())
		})
	}
}

// Anything that isn't a line is trashed: read first when its type is unknown,
// straight away when the caller has it.
func TestRemoveRecording_OtherRecordingsTrash(t *testing.T) {
	const trashTodo = "PUT /99999/recordings/100/status/trashed.json"
	const readTodo = "GET /99999/recordings/100.json"
	cases := []struct {
		recordingType string
		want          []string
	}{
		{"Todo", []string{trashTodo}},
		{"", []string{readTodo, trashTodo}},
	}
	for _, c := range cases {
		t.Run("type "+c.recordingType, func(t *testing.T) {
			h, transport := removeHub(map[string]removeReply{
				readTodo:  {http.StatusOK, `{"id":100,"type":"Todo","parent":{"id":50,"type":"Todolist"}}`},
				trashTodo: {http.StatusNoContent, ""},
			})

			deleted, err := h.RemoveRecording(context.Background(), "99999", 100, c.recordingType, 0)

			require.NoError(t, err)
			assert.False(t, deleted)
			assert.Equal(t, c.want, transport.recorded())
		})
	}
}

// When the read can't say what the recording is, or a line names no
// Campfire, nothing is written: trashing on a guess is how a line would reach
// the generic route.
func TestRemoveRecording_WritesNothingItCantDecide(t *testing.T) {
	cases := []struct {
		name string
		read removeReply
	}{
		{"unreadable", removeReply{http.StatusNotFound, `{"error":"Not Found"}`}},
		{"no content", removeReply{http.StatusNoContent, ""}},
		{"no type", removeReply{http.StatusOK, `{"id":111}`}},
		{"line without a parent", removeReply{http.StatusOK, `{"id":111,"type":"Chat::Lines::Text"}`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, transport := removeHub(map[string]removeReply{
				readLine:   c.read,
				deleteLine: {http.StatusNoContent, ""},
				trashLine:  {http.StatusNoContent, ""},
			})

			_, err := h.RemoveRecording(context.Background(), "99999", 111, "", 0)

			require.Error(t, err)
			assert.Equal(t, []string{readLine}, transport.recorded())
		})
	}
}

func TestIsCampfireLine(t *testing.T) {
	for _, line := range []string{"Chat::Lines::Text", "Chat::Lines::RichText", "Chat::Lines::Upload"} {
		assert.True(t, IsCampfireLine(line), line)
	}
	for _, other := range []string{"Chat::Transcript", "Campfire::Line", "Chat", "Todo", ""} {
		assert.False(t, IsCampfireLine(other), other)
	}
}
