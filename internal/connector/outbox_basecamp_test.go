package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// obServer is enough of Basecamp's API for the poster: boosts and comments on
// a recording, lines in a Campfire, each created by whoever the test says.
type obServer struct {
	*httptest.Server

	mu       sync.Mutex
	nextID   int64
	messages map[Destination][]obServerMessage
	posts    int
	// onPost runs after a message is stored and before the answer is
	// written; a non-zero status answers with it instead.
	onPost func(r *http.Request, id int64) int
	// beforeStore runs before a message is stored; a non-zero status answers
	// with it and stores nothing.
	beforeStore func(r *http.Request) int
	pageSize    int
}

type obServerMessage struct {
	ID        int64
	Content   string
	CreatedAt time.Time
	Creator   int64
}

var obServerPath = regexp.MustCompile(`^/999/(recordings|chats)/(\d+)/(boosts|comments|lines)\.json$`)

func newOBServer(t *testing.T) *obServer {
	t.Helper()
	s := &obServer{nextID: 70000, messages: map[Destination][]obServerMessage{}, pageSize: 2}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *obServer) serve(w http.ResponseWriter, r *http.Request) {
	m := obServerPath.FindStringSubmatch(r.URL.Path)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	recording, _ := strconv.ParseInt(m[2], 10, 64)
	kind := map[string]MessageKind{"boosts": MessageBoost, "comments": MessageComment, "lines": MessageChatLine}[m[3]]
	dest := Destination{Kind: kind, RecordingID: recording}

	switch r.Method {
	case http.MethodPost:
		var body struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.posts++
		before := s.beforeStore
		s.mu.Unlock()
		if before != nil {
			if status := before(r); status != 0 {
				w.WriteHeader(status)
				return
			}
		}
		id := s.add(dest, adapterAgentID, body.Content)
		s.mu.Lock()
		hook := s.onPost
		s.mu.Unlock()
		if hook != nil {
			if status := hook(r, id); status != 0 {
				w.WriteHeader(status)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(s.render(kind, s.find(dest, id)))
	case http.MethodGet:
		s.mu.Lock()
		all := append([]obServerMessage(nil), s.messages[dest]...)
		pageSize := s.pageSize
		s.mu.Unlock()
		if kind == MessageChatLine {
			sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if page < 1 {
				page = 1
			}
			start := (page - 1) * pageSize
			switch {
			case start >= len(all):
				all = nil
			case start+pageSize < len(all):
				all = all[start : start+pageSize]
			default:
				all = all[start:]
			}
		}
		out := make([]any, 0, len(all))
		for _, msg := range all {
			out = append(out, s.render(kind, msg))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *obServer) setOnPost(fn func(r *http.Request, id int64) int) {
	s.mu.Lock()
	s.onPost = fn
	s.mu.Unlock()
}

func (s *obServer) setPageSize(n int) {
	s.mu.Lock()
	s.pageSize = n
	s.mu.Unlock()
}

func (s *obServer) add(dest Destination, creator int64, content string) int64 {
	return s.addAt(dest, creator, content, time.Now().UTC())
}

func (s *obServer) addAt(dest Destination, creator int64, content string, at time.Time) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	key := Destination{Kind: dest.Kind, RecordingID: dest.RecordingID}
	s.messages[key] = append(s.messages[key], obServerMessage{ID: s.nextID, Content: content, CreatedAt: at, Creator: creator})
	return s.nextID
}

func (s *obServer) find(dest Destination, id int64) obServerMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.messages[Destination{Kind: dest.Kind, RecordingID: dest.RecordingID}] {
		if m.ID == id {
			return m
		}
	}
	return obServerMessage{}
}

func (s *obServer) at(dest Destination) []obServerMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]obServerMessage(nil), s.messages[Destination{Kind: dest.Kind, RecordingID: dest.RecordingID}]...)
}

func (s *obServer) postCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.posts
}

func (s *obServer) render(kind MessageKind, m obServerMessage) map[string]any {
	person := map[string]any{"id": m.Creator, "name": "Person " + strconv.FormatInt(m.Creator, 10)}
	out := map[string]any{"id": m.ID, "content": m.Content, "created_at": m.CreatedAt.Format(time.RFC3339Nano)}
	if kind == MessageBoost {
		out["booster"] = person
	} else {
		out["creator"] = person
		out["status"] = "active"
	}
	return out
}

func (s *obServer) poster(t *testing.T) *BasecampPoster {
	t.Helper()
	client := basecamp.NewClient(&basecamp.Config{BaseURL: s.URL}, &basecamp.StaticTokenProvider{Token: "test-token-not-real"})
	poster, err := NewBasecampPoster(client.ForAccount("999"), adapterAgentID)
	require.NoError(t, err)
	return poster
}

func TestBasecampPosterPostsEachKindAsTheAgent(t *testing.T) {
	server := newOBServer(t)
	poster := server.poster(t)
	ctx := context.Background()

	for _, dest := range []Destination{
		{Kind: MessageBoost, RecordingID: obEventRecording},
		{Kind: MessageComment, RecordingID: obReplyRecording},
		{Kind: MessageChatLine, RecordingID: obCampfire},
	} {
		id, err := poster.Post(ctx, dest, "body for "+string(dest.Kind))
		require.NoError(t, err, dest.Kind)
		stored := server.at(dest)
		require.Len(t, stored, 1, dest.Kind)
		assert.Equal(t, stored[0].ID, id)
		assert.Equal(t, "body for "+string(dest.Kind), stored[0].Content)
	}
}

// A create is one request: a failed answer is never retried by the SDK, since
// a retry would be a second message.
func TestBasecampPosterMakesOneRequestPerPost(t *testing.T) {
	server := newOBServer(t)
	server.setOnPost(func(*http.Request, int64) int { return http.StatusServiceUnavailable })
	poster := server.poster(t)

	for _, kind := range []MessageKind{MessageBoost, MessageComment, MessageChatLine} {
		before := server.postCount()
		_, err := poster.Post(context.Background(), Destination{Kind: kind, RecordingID: 5}, "x")
		require.Error(t, err, kind)
		assert.Equal(t, before+1, server.postCount(), kind)
	}
}

func TestBasecampPosterListsOnlyTheAgentsMessagesSince(t *testing.T) {
	server := newOBServer(t)
	poster := server.poster(t)
	ctx := context.Background()
	since := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	for _, kind := range []MessageKind{MessageBoost, MessageComment, MessageChatLine} {
		dest := Destination{Kind: kind, RecordingID: 42}
		server.addAt(dest, adapterAgentID, "old", since.Add(-time.Hour))
		server.addAt(dest, obOtherPersonID, "someone else", since.Add(time.Minute))
		want := server.addAt(dest, adapterAgentID, "mine", since.Add(2*time.Minute))
		server.addAt(dest, obOtherPersonID, "someone else again", since.Add(3*time.Minute))
		server.addAt(dest, obOtherPersonID, "and again", since.Add(4*time.Minute))

		listed, err := poster.List(ctx, dest, since)
		require.NoError(t, err, kind)
		require.Len(t, listed, 1, kind)
		assert.Equal(t, want, listed[0].ID, kind)
		assert.Equal(t, "mine", listed[0].Content, kind)
	}
}

// A Campfire listing that cannot reach back to the sending time is an error,
// never a shorter answer that would read as "nothing was posted".
func TestBasecampPosterRefusesAShortCampfireListing(t *testing.T) {
	server := newOBServer(t)
	server.setPageSize(1)
	poster := server.poster(t)
	dest := Destination{Kind: MessageChatLine, RecordingID: obCampfire}
	since := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	for i := range linePageLimit + 1 {
		server.addAt(dest, obOtherPersonID, "chatter", since.Add(time.Duration(i+1)*time.Second))
	}
	_, err := poster.List(context.Background(), dest, since)
	require.Error(t, err)
}
