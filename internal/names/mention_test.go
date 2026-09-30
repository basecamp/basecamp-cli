package names

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// mentionServer fakes the three endpoints mention resolution reads: the
// pingable set (people only — agents cannot be pinged), a project's people
// (people and agents alike), and a single person. It counts requests by path
// so tests can hold the resolver to one fetch per list per run.
type mentionServer struct {
	mu       sync.Mutex
	calls    map[string]int
	pingable []map[string]any
	project  map[string][]map[string]any
	people   map[string]map[string]any
}

func (s *mentionServer) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[path]
}

func (s *mentionServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.calls[r.URL.Path]++
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimPrefix(r.URL.Path, "/99999")
	switch {
	case path == "/circles/people.json":
		_ = json.NewEncoder(w).Encode(s.pingable)
	case strings.HasPrefix(path, "/projects/") && strings.HasSuffix(path, "/people.json"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/projects/"), "/people.json")
		people, ok := s.project[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(people)
	case strings.HasPrefix(path, "/people/"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/people/"), ".json")
		person, ok := s.people[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(person)
	default:
		http.NotFound(w, r)
	}
}

func person(id int64, name, sgid, personable string) map[string]any {
	return map[string]any{"id": id, "name": name, "attachable_sgid": sgid, "personable_type": personable}
}

func newMentionFixture(t *testing.T) (*Resolver, *mentionServer) {
	t.Helper()
	jane := person(1, "Jane Smith", "sgid-jane", "User")
	quincy := person(7, "Quincy", "sgid-quincy", "Agent")
	shy := person(2, "Shy Human", "sgid-shy", "User")
	s := &mentionServer{
		calls:    map[string]int{},
		pingable: []map[string]any{jane},
		project: map[string][]map[string]any{
			"123": {jane, quincy, shy},
		},
		people: map[string]map[string]any{"1": jane, "2": shy, "7": quincy},
	}
	server := httptest.NewServer(s)
	t.Cleanup(server.Close)

	sdkClient := basecamp.NewClient(&basecamp.Config{BaseURL: server.URL}, testTokenProvider{}, basecamp.WithMaxRetries(1))
	return NewResolver(sdkClient, nil, "99999"), s
}

func inProject(id int64) ProjectScope {
	return func(context.Context) (int64, error) { return id, nil }
}

const projectPeoplePath = "/99999/projects/123/people.json"

func TestResolveMentionByNameFindsAgentInProject(t *testing.T) {
	r, s := newMentionFixture(t)
	ctx := context.Background()

	p, err := r.ResolveMentionByName(ctx, "Quincy", inProject(123))
	require.NoError(t, err)
	assert.Equal(t, "sgid-quincy", p.AttachableSGID)

	p, err = r.ResolveMentionByName(ctx, "quincy", inProject(123))
	require.NoError(t, err)
	assert.Equal(t, "sgid-quincy", p.AttachableSGID)

	assert.Equal(t, 1, s.count(projectPeoplePath), "project people fetched once per run")
	assert.Equal(t, 1, s.count("/99999/circles/people.json"), "pingable fetched once per run")
}

func TestResolveMentionByNamePingableHitCostsNothingExtra(t *testing.T) {
	r, s := newMentionFixture(t)

	p, err := r.ResolveMentionByName(context.Background(), "Jane Smith", inProject(123))
	require.NoError(t, err)
	assert.Equal(t, "sgid-jane", p.AttachableSGID)
	assert.Zero(t, s.count(projectPeoplePath))
}

func TestResolveMentionByNameScopeIsLazy(t *testing.T) {
	r, _ := newMentionFixture(t)
	called := false
	scope := func(context.Context) (int64, error) { called = true; return 123, nil }

	_, err := r.ResolveMentionByName(context.Background(), "Jane Smith", scope)
	require.NoError(t, err)
	assert.False(t, called, "an exact pingable hit must not resolve the project")
}

func TestResolveMentionByNameLeavesPeopleSemanticsAlone(t *testing.T) {
	r, _ := newMentionFixture(t)

	// A person who is in the project but not pingable stays unmentionable:
	// only agents are unpingable by design.
	_, err := r.ResolveMentionByName(context.Background(), "Shy Human", inProject(123))
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "got %v", err)
	assert.Equal(t, output.CodeNotFound, outErr.Code)
}

func TestResolveMentionByNameWithoutScopeMisses(t *testing.T) {
	r, s := newMentionFixture(t)

	_, err := r.ResolveMentionByName(context.Background(), "Quincy", nil)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "got %v", err)
	assert.Equal(t, output.CodeNotFound, outErr.Code)
	assert.Zero(t, s.count(projectPeoplePath))
}

func TestResolveMentionByNamePrefersExactAgentOverPartialPerson(t *testing.T) {
	r, s := newMentionFixture(t)
	s.pingable = append(s.pingable, person(3, "Quincy Jones", "sgid-qj", "User"))

	p, err := r.ResolveMentionByName(context.Background(), "Quincy", inProject(123))
	require.NoError(t, err)
	assert.Equal(t, "sgid-quincy", p.AttachableSGID)

	// A partial name still reaches the person, as before.
	p, err = r.ResolveMentionByName(context.Background(), "Quincy J", inProject(123))
	require.NoError(t, err)
	assert.Equal(t, "sgid-qj", p.AttachableSGID)
}

func TestResolveMentionByNameAgentAmbiguity(t *testing.T) {
	r, s := newMentionFixture(t)
	s.project["123"] = append(s.project["123"],
		person(8, "Build Bot", "sgid-bb", "Agent"),
		person(9, "Deploy Bot", "sgid-db", "Agent"))

	_, err := r.ResolveMentionByName(context.Background(), "Bot", inProject(123))
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "got %v", err)
	assert.Equal(t, output.CodeAmbiguous, outErr.Code)

	p, err := r.ResolveMentionByName(context.Background(), "Build", inProject(123))
	require.NoError(t, err)
	assert.Equal(t, "sgid-bb", p.AttachableSGID)
}

func TestResolveMentionByNameProjectFetchFailureIsHard(t *testing.T) {
	r, _ := newMentionFixture(t)

	_, err := r.ResolveMentionByName(context.Background(), "Quincy", inProject(404))
	require.Error(t, err)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "got %v", err)
	assert.NotZero(t, outErr.HTTPStatus, "an API failure must carry its status so callers hard-fail")
}

func TestResolveMentionByIDFindsAgent(t *testing.T) {
	r, s := newMentionFixture(t)
	ctx := context.Background()

	p, err := r.ResolveMentionByID(ctx, 7)
	require.NoError(t, err)
	assert.Equal(t, "sgid-quincy", p.AttachableSGID)

	p, err = r.ResolveMentionByID(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, "sgid-jane", p.AttachableSGID)
	assert.Zero(t, s.count("/99999/people/1.json"), "a pingable hit needs no person lookup")
}

func TestResolveMentionByIDLeavesPeopleSemanticsAlone(t *testing.T) {
	r, _ := newMentionFixture(t)

	_, err := r.ResolveMentionByID(context.Background(), 2)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "got %v", err)
	assert.Equal(t, output.CodeNotFound, outErr.Code)

	_, err = r.ResolveMentionByID(context.Background(), 404)
	require.True(t, errors.As(err, &outErr), "got %v", err)
	assert.Equal(t, output.CodeNotFound, outErr.Code)
}

func TestResolveMentionByNameKeepsPingableMatchWhenAgentLookupFails(t *testing.T) {
	r, _ := newMentionFixture(t)

	// "Jane" partially matches a pingable person; the project cannot be
	// read, which costs only the exact-agent preference.
	p, err := r.ResolveMentionByName(context.Background(), "Jane", inProject(404))
	require.NoError(t, err)
	assert.Equal(t, "sgid-jane", p.AttachableSGID)
}

func TestResolveMentionByIDPingableFailureIsHard(t *testing.T) {
	r, s := newMentionFixture(t)
	s.pingable = nil
	// The pingable set fails while the person lookup would succeed: the
	// failure must surface rather than be read as "not pingable".
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/circles/people.json") {
			http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
			return
		}
		s.ServeHTTP(w, req)
	}))
	t.Cleanup(server.Close)
	r.sdk = basecamp.NewClient(&basecamp.Config{BaseURL: server.URL}, testTokenProvider{}, basecamp.WithMaxRetries(1))

	p, err := r.ResolveMentionByID(context.Background(), 7)
	require.Error(t, err, "resolved %+v despite the pingable failure", p)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "got %v", err)
	assert.Equal(t, http.StatusInternalServerError, outErr.HTTPStatus)
}

func TestResolveMentionByNameRemembersAFailedAgentFetch(t *testing.T) {
	r, s := newMentionFixture(t)

	for range 3 {
		_, err := r.ResolveMentionByName(context.Background(), "Jane", inProject(404))
		require.NoError(t, err)
	}
	assert.Equal(t, 1, s.count("/99999/projects/404/people.json"), "a failed fetch is not retried within a run")
}

func TestResolveMentionByNameReportsScopeErrorsAsSuch(t *testing.T) {
	r, _ := newMentionFixture(t)
	scopeErr := output.ErrNotFound("Project", "Nope")
	scope := func(context.Context) (int64, error) { return 0, scopeErr }

	_, err := r.ResolveMentionByName(context.Background(), "Quincy", scope)
	var se *ScopeError
	require.True(t, errors.As(err, &se), "got %v", err)
	assert.Same(t, scopeErr, se.Err)
}

func TestResolveMentionByNameScopeErrorBeatsAPartialPersonMatch(t *testing.T) {
	r, _ := newMentionFixture(t)
	scope := func(context.Context) (int64, error) { return 0, output.ErrNotFound("Project", "Nope") }

	_, err := r.ResolveMentionByName(context.Background(), "Jane", scope)
	var se *ScopeError
	require.True(t, errors.As(err, &se), "got %v", err)
}
