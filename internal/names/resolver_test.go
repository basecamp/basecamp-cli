package names

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/output"
)

func TestResolve(t *testing.T) {
	projects := []Project{
		{ID: 1, Name: "Marketing Campaign"},
		{ID: 2, Name: "Marketing Site"},
		{ID: 3, Name: "Engineering"},
		{ID: 4, Name: "engineering-infra"},
		{ID: 5, Name: "Product"},
	}

	extract := func(p Project) (int64, string) {
		return p.ID, p.Name
	}

	tests := []struct {
		name        string
		input       string
		wantID      int64
		wantMatch   bool
		wantMatches int // number of ambiguous matches
	}{
		// Exact match
		{"exact match", "Engineering", 3, true, 0},
		{"case insensitive matches one", "engineering", 3, true, 0}, // matches Engineering (case-insensitive)

		// Case-insensitive single match
		{"case insensitive single", "product", 5, true, 0},
		{"case insensitive single 2", "PRODUCT", 5, true, 0},

		// Partial match single
		{"partial single", "infra", 4, true, 0},
		{"partial single 2", "Campaign", 1, true, 0},

		// Ambiguous - multiple partial matches
		{"ambiguous partial", "Marketing", 0, false, 2},

		// No match
		{"no match", "Finance", 0, false, 0},
		{"no match 2", "xyz", 0, false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match, matches := resolve(tt.input, projects, extract)

			if tt.wantMatch {
				require.NotNil(t, match, "expected match with ID %d, got nil", tt.wantID)
				assert.Equal(t, tt.wantID, match.ID)
			} else {
				assert.Nil(t, match, "expected no match, got ID %d", match)
				assert.Len(t, matches, tt.wantMatches)
			}
		})
	}
}

func TestSuggest(t *testing.T) {
	projects := []Project{
		{ID: 1, Name: "Marketing Campaign"},
		{ID: 2, Name: "Marketing Site"},
		{ID: 3, Name: "Engineering"},
		{ID: 4, Name: "Product Launch"},
		{ID: 5, Name: "Product Design"},
	}

	getName := func(p Project) string { return p.Name }

	tests := []struct {
		name    string
		input   string
		wantAny bool // expect at least one suggestion
		wantMax int  // maximum suggestions
	}{
		{"prefix match", "Mark", true, 3},
		{"word match", "Product", true, 3},
		{"partial word", "Eng", true, 3},
		{"no suggestions", "xyz", false, 0},
		{"too short", "a", false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			suggestions := suggest(tt.input, projects, getName)

			if tt.wantAny {
				assert.NotEmpty(t, suggestions, "expected suggestions, got none")
			} else {
				assert.Empty(t, suggestions, "expected no suggestions, got %v", suggestions)
			}
			if tt.wantMax > 0 {
				assert.LessOrEqual(t, len(suggestions), tt.wantMax, "expected max %d suggestions, got %d", tt.wantMax, len(suggestions))
			}
		})
	}
}

func TestContainsWord(t *testing.T) {
	tests := []struct {
		haystack string
		needle   string
		want     bool
	}{
		{"marketing campaign", "market", true},
		{"marketing campaign", "campaign", true},
		{"marketing campaign", "xyz", false},
		{"marketing campaign", "a", false}, // too short
		{"engineering infra", "infra", true},
		{"engineering infra", "eng", true},
		{"project alpha", "alpha", true},
		{"project alpha", "project", true},
		{"hello world", "wor", true},
		{"hello world", "wo", true},
		{"hello world", "w", false}, // single char - too short
		{"", "test", false},
		{"test", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.haystack+"_"+tt.needle, func(t *testing.T) {
			got := containsWord(tt.haystack, tt.needle)
			assert.Equal(t, tt.want, got, "containsWord(%q, %q) = %v, want %v", tt.haystack, tt.needle, got, tt.want)
		})
	}
}

// =============================================================================
// Person Resolution Tests
// =============================================================================

func TestResolveWithPersons(t *testing.T) {
	people := []Person{
		{ID: 111, Name: "Alice Smith", Email: "alice@example.com"},
		{ID: 222, Name: "Bob Jones", Email: "bob@example.com"},
		{ID: 333, Name: "Alice Johnson", Email: "alicej@example.com"},
	}

	extract := func(p Person) (int64, string) {
		return p.ID, p.Name
	}

	tests := []struct {
		name        string
		input       string
		wantID      int64
		wantMatch   bool
		wantMatches int
	}{
		// Exact match
		{"exact name", "Alice Smith", 111, true, 0},
		{"exact name 2", "Bob Jones", 222, true, 0},

		// Case-insensitive
		{"case insensitive", "alice smith", 111, true, 0},
		{"case insensitive 2", "BOB JONES", 222, true, 0},

		// Partial match single
		{"partial single", "Jones", 222, true, 0},
		{"partial single 2", "Smith", 111, true, 0},

		// Ambiguous
		{"ambiguous alice", "Alice", 0, false, 2},

		// No match
		{"no match", "Charlie", 0, false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match, matches := resolve(tt.input, people, extract)

			if tt.wantMatch {
				require.NotNil(t, match, "expected match with ID %d, got nil", tt.wantID)
				assert.Equal(t, tt.wantID, match.ID)
			} else {
				assert.Nil(t, match, "expected no match, got ID %d", match)
				assert.Len(t, matches, tt.wantMatches)
			}
		})
	}
}

// =============================================================================
// Todolist Resolution Tests
// =============================================================================

func TestResolveWithTodolists(t *testing.T) {
	todolists := []Todolist{
		{ID: 111, Name: "Sprint Tasks"},
		{ID: 222, Name: "Bug Fixes"},
		{ID: 333, Name: "Ideas"},
		{ID: 444, Name: "Sprint Planning"},
	}

	extract := func(tl Todolist) (int64, string) {
		return tl.ID, tl.Name
	}

	tests := []struct {
		name        string
		input       string
		wantID      int64
		wantMatch   bool
		wantMatches int
	}{
		// Exact match
		{"exact name", "Bug Fixes", 222, true, 0},
		{"exact name 2", "Ideas", 333, true, 0},

		// Case-insensitive
		{"case insensitive", "bug fixes", 222, true, 0},
		{"case insensitive 2", "IDEAS", 333, true, 0},

		// Partial match single
		{"partial single", "Fixes", 222, true, 0},

		// Ambiguous
		{"ambiguous sprint", "Sprint", 0, false, 2},

		// No match
		{"no match", "Backlog", 0, false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match, matches := resolve(tt.input, todolists, extract)

			if tt.wantMatch {
				require.NotNil(t, match, "expected match with ID %d, got nil", tt.wantID)
				assert.Equal(t, tt.wantID, match.ID)
			} else {
				assert.Nil(t, match, "expected no match, got ID %d", match)
				assert.Len(t, matches, tt.wantMatches)
			}
		})
	}
}

// =============================================================================
// Suggestion Tests - Extended
// =============================================================================

func TestSuggestLimit(t *testing.T) {
	// Create many projects to test limit
	projects := []Project{
		{ID: 1, Name: "Alpha One"},
		{ID: 2, Name: "Alpha Two"},
		{ID: 3, Name: "Alpha Three"},
		{ID: 4, Name: "Alpha Four"},
		{ID: 5, Name: "Alpha Five"},
	}

	getName := func(p Project) string { return p.Name }

	suggestions := suggest("Alp", projects, getName)
	assert.LessOrEqual(t, len(suggestions), 3, "suggest should return max 3 suggestions, got %d", len(suggestions))
}

func TestSuggestPeople(t *testing.T) {
	people := []Person{
		{ID: 1, Name: "Alice Smith", Email: "alice@example.com"},
		{ID: 2, Name: "Alice Johnson", Email: "alicej@example.com"},
		{ID: 3, Name: "Bob Wilson", Email: "bob@example.com"},
	}

	getName := func(p Person) string { return p.Name }

	tests := []struct {
		name    string
		input   string
		wantAny bool
	}{
		{"prefix match", "Ali", true},
		{"word match", "Smith", true},
		{"no match", "xyz", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			suggestions := suggest(tt.input, people, getName)
			if tt.wantAny {
				assert.NotEmpty(t, suggestions, "expected suggestions, got none")
			} else {
				assert.Empty(t, suggestions, "expected no suggestions, got %v", suggestions)
			}
		})
	}
}

// =============================================================================
// Resolution Priority Tests
// =============================================================================

func TestResolutionPriority(t *testing.T) {
	// Test that exact match takes priority over case-insensitive and partial
	projects := []Project{
		{ID: 1, Name: "test"},         // lowercase
		{ID: 2, Name: "Test"},         // titlecase
		{ID: 3, Name: "testing"},      // contains "test"
		{ID: 4, Name: "Test Project"}, // contains "Test"
	}

	extract := func(p Project) (int64, string) {
		return p.ID, p.Name
	}

	// Exact match should win
	match, _ := resolve("test", projects, extract)
	require.NotNil(t, match, "exact match 'test' should return ID 1, got nil")
	assert.Equal(t, int64(1), match.ID, "exact match 'test' should return ID 1")

	// Exact match with different case
	match, _ = resolve("Test", projects, extract)
	require.NotNil(t, match, "exact match 'Test' should return ID 2, got nil")
	assert.Equal(t, int64(2), match.ID, "exact match 'Test' should return ID 2")
}

func TestCaseInsensitiveAmbiguity(t *testing.T) {
	// When multiple case-insensitive matches exist, should be ambiguous
	projects := []Project{
		{ID: 1, Name: "Test"},
		{ID: 2, Name: "TEST"},
		{ID: 3, Name: "test"},
	}

	extract := func(p Project) (int64, string) {
		return p.ID, p.Name
	}

	// Searching for "TeSt" should be ambiguous (3 case-insensitive matches)
	match, matches := resolve("TeSt", projects, extract)
	assert.Nil(t, match, "should be ambiguous, got match ID %d", match)
	assert.Equal(t, 3, len(matches), "expected 3 ambiguous matches, got %d", len(matches))
}

// =============================================================================
// Cache Tests
// =============================================================================

func TestResolverClearCache(t *testing.T) {
	r := &Resolver{
		projects:  []Project{{ID: 1, Name: "Test"}},
		people:    []Person{{ID: 2, Name: "Alice"}},
		pingable:  []Person{{ID: 4, Name: "Client"}},
		todolists: map[string][]Todolist{"123": {{ID: 3, Name: "Tasks"}}},
	}

	r.ClearCache()

	assert.Nil(t, r.projects, "projects should be nil after ClearCache")
	assert.Nil(t, r.people, "people should be nil after ClearCache")
	assert.Nil(t, r.pingable, "pingable should be nil after ClearCache")
	assert.Empty(t, r.todolists, "todolists should be empty after ClearCache")
}

// =============================================================================
// mockResolver for testing Resolver methods
// =============================================================================

type mockResolver struct {
	Resolver
}

func newMockResolver() *mockResolver {
	r := &mockResolver{}
	r.todolists = make(map[string][]Todolist)
	return r
}

func (m *mockResolver) setProjects(projects []Project) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.projects = projects
}

func (m *mockResolver) setPeople(people []Person) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.people = people
}

func (m *mockResolver) setPingable(pingable []Person) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pingable = pingable
}

func (m *mockResolver) setTodolists(projectID string, todolists []Todolist) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.todolists[projectID] = todolists
}

// =============================================================================
// Resolver Method Tests (with pre-populated cache)
// =============================================================================

func TestResolverResolveProjectNumericID(t *testing.T) {
	r := newMockResolver()
	r.setProjects([]Project{
		{ID: 12345, Name: "Project Alpha"},
		{ID: 67890, Name: "Project Beta"},
	})

	ctx := context.Background()
	id, name, err := r.ResolveProject(ctx, "12345")
	require.NoError(t, err)
	assert.Equal(t, "12345", id)
	assert.Equal(t, "Project Alpha", name)
}

func TestResolverResolveProjectByName(t *testing.T) {
	r := newMockResolver()
	r.setProjects([]Project{
		{ID: 111, Name: "Project Alpha"},
		{ID: 222, Name: "Project Beta"},
	})

	ctx := context.Background()
	id, name, err := r.ResolveProject(ctx, "Beta")
	require.NoError(t, err)
	assert.Equal(t, "222", id)
	assert.Equal(t, "Project Beta", name)
}

func TestResolverResolveProjectAmbiguous(t *testing.T) {
	r := newMockResolver()
	r.setProjects([]Project{
		{ID: 111, Name: "Acme Corp"},
		{ID: 222, Name: "Acme Labs"},
	})

	ctx := context.Background()
	_, _, err := r.ResolveProject(ctx, "Acme")
	require.Error(t, err, "expected error for ambiguous match")

	// Verify it's an ambiguous error
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "expected *output.Error, got %T", err)
	assert.Equal(t, output.CodeAmbiguous, outErr.Code)
}

func TestResolverResolveProjectNotFound(t *testing.T) {
	r := newMockResolver()
	r.setProjects([]Project{
		{ID: 111, Name: "Project Alpha"},
	})

	ctx := context.Background()
	_, _, err := r.ResolveProject(ctx, "Nonexistent")
	require.Error(t, err, "expected error for not found")

	// Verify it's a not found error
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "expected *output.Error, got %T", err)
	assert.Equal(t, output.CodeNotFound, outErr.Code)
}

func TestResolverResolvePersonNumericID(t *testing.T) {
	r := newMockResolver()
	r.setPeople([]Person{
		{ID: 111, Name: "Alice Smith", Email: "alice@example.com"},
	})

	ctx := context.Background()
	id, name, err := r.ResolvePerson(ctx, "111")
	require.NoError(t, err)
	assert.Equal(t, "111", id)
	assert.Equal(t, "Alice Smith", name)
}

func TestResolverResolvePersonByEmail(t *testing.T) {
	r := newMockResolver()
	r.setPeople([]Person{
		{ID: 111, Name: "Alice Smith", Email: "alice@example.com"},
		{ID: 222, Name: "Bob Jones", Email: "bob@example.com"},
	})

	ctx := context.Background()
	id, name, err := r.ResolvePerson(ctx, "bob@example.com")
	require.NoError(t, err)
	assert.Equal(t, "222", id)
	assert.Equal(t, "Bob Jones", name)
}

func TestResolverResolvePersonByEmailCaseInsensitive(t *testing.T) {
	r := newMockResolver()
	r.setPeople([]Person{
		{ID: 111, Name: "Alice Smith", Email: "Alice@Example.COM"},
	})

	ctx := context.Background()
	id, _, err := r.ResolvePerson(ctx, "alice@example.com")
	require.NoError(t, err)
	assert.Equal(t, "111", id)
}

func TestResolverResolvePersonByName(t *testing.T) {
	r := newMockResolver()
	r.setPeople([]Person{
		{ID: 111, Name: "Alice Smith", Email: "alice@example.com"},
	})

	ctx := context.Background()
	id, _, err := r.ResolvePerson(ctx, "Alice Smith")
	require.NoError(t, err)
	assert.Equal(t, "111", id)
}

func TestResolverResolvePersonAmbiguous(t *testing.T) {
	r := newMockResolver()
	r.setPeople([]Person{
		{ID: 111, Name: "Alice Smith", Email: "alices@example.com"},
		{ID: 222, Name: "Alice Johnson", Email: "alicej@example.com"},
	})

	ctx := context.Background()
	_, _, err := r.ResolvePerson(ctx, "Alice")
	require.Error(t, err, "expected error for ambiguous match")

	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "expected *output.Error, got %T", err)
	assert.Equal(t, output.CodeAmbiguous, outErr.Code)
}

// =============================================================================
// "me" Resolution Tests
//
// "me" resolves via /my/profile.json (SDK People().Me) which returns the
// authenticated user's account-scoped person record in a single request.
// =============================================================================

func TestResolverResolvePerson_Me_Success(t *testing.T) {
	r := newMockResolver()
	r.resolveMeFn = func(_ context.Context) (int64, string, error) {
		return 42000, "Alice Smith", nil
	}

	ctx := context.Background()
	id, name, err := r.ResolvePerson(ctx, "me")
	require.NoError(t, err)
	assert.Equal(t, "42000", id)
	assert.Equal(t, "Alice Smith", name)
}

func TestResolverResolvePerson_Me_Error(t *testing.T) {
	r := newMockResolver()
	r.resolveMeFn = func(_ context.Context) (int64, string, error) {
		return 0, "", errors.New("API error")
	}

	ctx := context.Background()
	_, _, err := r.ResolvePerson(ctx, "me")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API error")
}

func TestResolverResolvePerson_Me_CaseVariants(t *testing.T) {
	for _, input := range []string{"me", "Me", "ME", "mE"} {
		t.Run(input, func(t *testing.T) {
			r := newMockResolver()
			r.resolveMeFn = func(_ context.Context) (int64, string, error) {
				return 42000, "Alice Smith", nil
			}

			ctx := context.Background()
			id, name, err := r.ResolvePerson(ctx, input)
			require.NoError(t, err)
			assert.Equal(t, "42000", id)
			assert.Equal(t, "Alice Smith", name)
		})
	}
}

// testTokenProvider implements basecamp.TokenProvider for tests.
type testTokenProvider struct{}

func (testTokenProvider) AccessToken(context.Context) (string, error) {
	return "test-token", nil
}

// TestResolverResolvePerson_Me_SDK exercises the real SDK path (no resolveMeFn
// override) against an httptest server, verifying correct resolution and that
// the in-memory cache prevents duplicate requests.
func TestResolverResolvePerson_Me_SDK(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, "/99999/my/profile.json", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":            42000,
			"name":          "Alice Smith",
			"email_address": "alice@example.com",
		})
	}))
	t.Cleanup(server.Close)

	sdkClient := basecamp.NewClient(
		&basecamp.Config{BaseURL: server.URL},
		testTokenProvider{},
		basecamp.WithMaxRetries(0),
	)
	r := NewResolver(sdkClient, nil, "99999")

	ctx := context.Background()

	// First call hits the server.
	id, name, err := r.ResolvePerson(ctx, "me")
	require.NoError(t, err)
	assert.Equal(t, "42000", id)
	assert.Equal(t, "Alice Smith", name)

	// Second call should use in-memory cache — no additional request.
	id2, name2, err := r.ResolvePerson(ctx, "me")
	require.NoError(t, err)
	assert.Equal(t, "42000", id2)
	assert.Equal(t, "Alice Smith", name2)
	assert.Equal(t, int32(1), calls.Load(), "expected exactly 1 HTTP request due to caching")

	// Switching account clears the cache.
	r.SetAccountID("88888")
	r.mu.RLock()
	assert.Nil(t, r.me, "me cache should be nil after SetAccountID")
	r.mu.RUnlock()
}

func TestResolverResolveTodolistNumericID(t *testing.T) {
	r := newMockResolver()
	r.setTodolists("12345", []Todolist{
		{ID: 111, Name: "Sprint Tasks"},
		{ID: 222, Name: "Bug Fixes"},
	})

	ctx := context.Background()
	id, name, err := r.ResolveTodolist(ctx, "111", "12345")
	require.NoError(t, err)
	assert.Equal(t, "111", id)
	assert.Equal(t, "Sprint Tasks", name)
}

func TestResolverResolveTodolistByName(t *testing.T) {
	r := newMockResolver()
	r.setTodolists("12345", []Todolist{
		{ID: 111, Name: "Sprint Tasks"},
		{ID: 222, Name: "Bug Fixes"},
	})

	ctx := context.Background()
	id, name, err := r.ResolveTodolist(ctx, "Bug Fixes", "12345")
	require.NoError(t, err)
	assert.Equal(t, "222", id)
	assert.Equal(t, "Bug Fixes", name)
}

func TestResolverResolveTodolistNotFound(t *testing.T) {
	r := newMockResolver()
	r.setTodolists("12345", []Todolist{
		{ID: 111, Name: "Sprint Tasks"},
	})

	ctx := context.Background()
	_, _, err := r.ResolveTodolist(ctx, "Nonexistent", "12345")
	require.Error(t, err, "expected error for not found")

	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "expected *output.Error, got %T", err)
	assert.Equal(t, output.CodeNotFound, outErr.Code)
}

// =============================================================================
// Edge Case Tests
// =============================================================================

// Test SetAccountID method
func TestResolverSetAccountID(t *testing.T) {
	r := newMockResolver()
	r.setProjects([]Project{{ID: 1, Name: "Test"}})
	r.setPeople([]Person{{ID: 2, Name: "Alice"}})
	r.setPingable([]Person{{ID: 4, Name: "Client"}})
	r.setTodolists("123", []Todolist{{ID: 3, Name: "Tasks"}})

	// Set same account ID - should not clear cache
	r.accountID = "12345"
	r.SetAccountID("12345")

	r.mu.RLock()
	assert.NotNil(t, r.projects, "projects should not be cleared when setting same account ID")
	r.mu.RUnlock()

	// Set different account ID - should clear cache
	r.SetAccountID("67890")

	r.mu.RLock()
	assert.Nil(t, r.projects, "projects should be nil after changing account ID")
	assert.Nil(t, r.people, "people should be nil after changing account ID")
	assert.Nil(t, r.pingable, "pingable should be nil after changing account ID")
	assert.Empty(t, r.todolists, "todolists should be empty after changing account ID")
	assert.Equal(t, "67890", r.accountID)
	r.mu.RUnlock()
}

func TestResolveEmptyInput(t *testing.T) {
	projects := []Project{
		{ID: 1, Name: "Project Alpha"},
		{ID: 2, Name: "Project Beta"},
	}

	extract := func(p Project) (int64, string) {
		return p.ID, p.Name
	}

	// Empty string matches everything via Contains (strings.Contains(s, "") is always true)
	// So we should get all items as ambiguous matches
	match, matches := resolve("", projects, extract)
	assert.Nil(t, match, "empty input should be ambiguous, not single match")
	assert.Equal(t, 2, len(matches), "empty input should match all items, got %d matches", len(matches))
}

func TestResolveEmptyList(t *testing.T) {
	var projects []Project

	extract := func(p Project) (int64, string) {
		return p.ID, p.Name
	}

	match, matches := resolve("anything", projects, extract)
	assert.Nil(t, match, "empty list should not match")
	assert.Empty(t, matches, "empty list should have no matches, got %d", len(matches))
}

func TestSuggestEmptyList(t *testing.T) {
	var projects []Project

	getName := func(p Project) string { return p.Name }

	suggestions := suggest("test", projects, getName)
	assert.Empty(t, suggestions, "empty list should have no suggestions, got %d", len(suggestions))
}

// TestResolverGetTodolists_FollowsPagination verifies that getTodolists uses
// GetAll to follow Link pagination headers, so todolists beyond the first page
// are resolved correctly.
func TestResolverGetTodolists_FollowsPagination(t *testing.T) {
	const accountID = "99999"
	const projectID = "100"
	const todosetID = 200

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case fmt.Sprintf("/%s/projects/%s.json", accountID, projectID):
			json.NewEncoder(w).Encode(map[string]any{
				"id":   100,
				"name": "Test Project",
				"dock": []map[string]any{
					{"name": "todoset", "id": todosetID},
				},
			})
		case fmt.Sprintf("/%s/todosets/%d/todolists.json", accountID, todosetID):
			if r.URL.Query().Get("page") == "2" {
				json.NewEncoder(w).Encode([]map[string]any{
					{"id": 333, "name": "Page Two List"},
				})
			} else {
				page2URL := fmt.Sprintf("http://%s/%s/todosets/%d/todolists.json?page=2",
					r.Host, accountID, todosetID)
				w.Header().Set("Link", fmt.Sprintf("<%s>; rel=\"next\"", page2URL))
				json.NewEncoder(w).Encode([]map[string]any{
					{"id": 111, "name": "Page One List"},
				})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	sdkClient := basecamp.NewClient(
		&basecamp.Config{BaseURL: server.URL},
		testTokenProvider{},
		basecamp.WithMaxRetries(1),
	)
	r := NewResolver(sdkClient, nil, accountID)

	ctx := context.Background()
	id, name, err := r.ResolveTodolist(ctx, "Page Two List", projectID)
	require.NoError(t, err)
	assert.Equal(t, "333", id)
	assert.Equal(t, "Page Two List", name)
}

func TestResolveSpecialCharacters(t *testing.T) {
	projects := []Project{
		{ID: 1, Name: "Project (Alpha)"},
		{ID: 2, Name: "Project [Beta]"},
		{ID: 3, Name: "Project-Gamma"},
		{ID: 4, Name: "Project_Delta"},
	}

	extract := func(p Project) (int64, string) {
		return p.ID, p.Name
	}

	tests := []struct {
		input  string
		wantID int64
	}{
		{"Project (Alpha)", 1},
		{"(Alpha)", 1},
		{"[Beta]", 2},
		{"Gamma", 3},
		{"Delta", 4},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			match, _ := resolve(tt.input, projects, extract)
			require.NotNil(t, match, "expected match for %q", tt.input)
			assert.Equal(t, tt.wantID, match.ID)
		})
	}
}

// =============================================================================
// Pingable Fallback Tests
// =============================================================================

func TestResolverResolvePerson_PingableFallback_ByName(t *testing.T) {
	r := newMockResolver()
	r.setPeople([]Person{
		{ID: 111, Name: "Alice Smith", Email: "alice@example.com"},
	})
	r.setPingable([]Person{
		{ID: 999, Name: "External Client", Email: "client@external.com"},
	})

	ctx := context.Background()
	id, name, err := r.ResolvePerson(ctx, "External Client")
	require.NoError(t, err)
	assert.Equal(t, "999", id)
	assert.Equal(t, "External Client", name)
}

func TestResolverResolvePerson_PingableFallback_ByEmail(t *testing.T) {
	r := newMockResolver()
	r.setPeople([]Person{
		{ID: 111, Name: "Alice Smith", Email: "alice@example.com"},
	})
	r.setPingable([]Person{
		{ID: 999, Name: "External Client", Email: "client@external.com"},
	})

	ctx := context.Background()
	id, name, err := r.ResolvePerson(ctx, "client@external.com")
	require.NoError(t, err)
	assert.Equal(t, "999", id)
	assert.Equal(t, "External Client", name)
}

func TestResolverResolvePerson_PeoplePreferredOverPingable(t *testing.T) {
	r := newMockResolver()
	r.setPeople([]Person{
		{ID: 111, Name: "Alice Smith", Email: "alice@example.com"},
	})
	r.setPingable([]Person{
		{ID: 222, Name: "Alice Smith", Email: "alice2@example.com"},
	})

	ctx := context.Background()
	id, _, err := r.ResolvePerson(ctx, "Alice Smith")
	require.NoError(t, err)
	// People list is checked first, so ID 111 wins
	assert.Equal(t, "111", id)
}

func TestResolverResolvePerson_PingableFallback_NotFound(t *testing.T) {
	r := newMockResolver()
	r.setPeople([]Person{
		{ID: 111, Name: "Alice Smith", Email: "alice@example.com"},
	})
	r.setPingable([]Person{
		{ID: 222, Name: "Bob Jones", Email: "bob@example.com"},
	})

	ctx := context.Background()
	_, _, err := r.ResolvePerson(ctx, "Charlie Nobody")
	require.Error(t, err)

	var outErr *output.Error
	require.True(t, errors.As(err, &outErr))
	assert.Equal(t, output.CodeNotFound, outErr.Code)
}

func TestGetTodolistsMultiTodosetMergesAll(t *testing.T) {
	accountID := "99999"
	projectID := "123"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/" + accountID + "/projects/" + projectID + ".json":
			json.NewEncoder(w).Encode(map[string]any{
				"id": 123,
				"dock": []map[string]any{
					{"name": "todoset", "id": 100},
					{"name": "todoset", "id": 200},
				},
			})
		case fmt.Sprintf("/%s/todosets/100/todolists.json", accountID):
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 10, "name": "Sprint 1"},
			})
		case fmt.Sprintf("/%s/todosets/200/todolists.json", accountID):
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 20, "name": "UI Tasks"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	sdkClient := basecamp.NewClient(
		&basecamp.Config{BaseURL: server.URL},
		testTokenProvider{},
		basecamp.WithMaxRetries(1),
	)
	r := NewResolver(sdkClient, nil, accountID)

	ctx := context.Background()

	// Should find "Sprint 1" from todoset 100
	id, name, err := r.ResolveTodolist(ctx, "Sprint 1", projectID)
	require.NoError(t, err)
	assert.Equal(t, "10", id)
	assert.Equal(t, "Sprint 1", name)

	// Should find "UI Tasks" from todoset 200
	id, name, err = r.ResolveTodolist(ctx, "UI Tasks", projectID)
	require.NoError(t, err)
	assert.Equal(t, "20", id)
	assert.Equal(t, "UI Tasks", name)
}

func TestGetTodolistsPartialFetchFailsHard(t *testing.T) {
	accountID := "99999"
	projectID := "123"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/" + accountID + "/projects/" + projectID + ".json":
			json.NewEncoder(w).Encode(map[string]any{
				"id": 123,
				"dock": []map[string]any{
					{"name": "todoset", "id": 100},
					{"name": "todoset", "id": 200},
				},
			})
		case fmt.Sprintf("/%s/todosets/100/todolists.json", accountID):
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 10, "name": "Sprint 1"},
			})
		case fmt.Sprintf("/%s/todosets/200/todolists.json", accountID):
			// Simulate a server error for the second todoset
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"internal server error"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	sdkClient := basecamp.NewClient(
		&basecamp.Config{BaseURL: server.URL},
		testTokenProvider{},
		basecamp.WithMaxRetries(1),
	)
	r := NewResolver(sdkClient, nil, accountID)

	ctx := context.Background()

	// Should fail — partial data from only one of two todosets is unreliable
	_, _, err := r.ResolveTodolist(ctx, "Sprint 1", projectID)
	require.Error(t, err, "partial todoset fetch should fail hard, not return partial results")
	assert.Contains(t, err.Error(), "todoset 200")
}
