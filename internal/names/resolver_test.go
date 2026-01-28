package names

import (
	"context"
	"errors"
	"testing"

	"github.com/basecamp/bcq/internal/output"
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
				if match == nil {
					t.Errorf("expected match with ID %d, got nil", tt.wantID)
				} else if match.ID != tt.wantID {
					t.Errorf("expected ID %d, got %d", tt.wantID, match.ID)
				}
			} else {
				if match != nil {
					t.Errorf("expected no match, got ID %d", match.ID)
				}
				if len(matches) != tt.wantMatches {
					t.Errorf("expected %d ambiguous matches, got %d", tt.wantMatches, len(matches))
				}
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

			if tt.wantAny && len(suggestions) == 0 {
				t.Error("expected suggestions, got none")
			}
			if !tt.wantAny && len(suggestions) > 0 {
				t.Errorf("expected no suggestions, got %v", suggestions)
			}
			if len(suggestions) > tt.wantMax && tt.wantMax > 0 {
				t.Errorf("expected max %d suggestions, got %d", tt.wantMax, len(suggestions))
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
			if got != tt.want {
				t.Errorf("containsWord(%q, %q) = %v, want %v", tt.haystack, tt.needle, got, tt.want)
			}
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
				if match == nil {
					t.Errorf("expected match with ID %d, got nil", tt.wantID)
				} else if match.ID != tt.wantID {
					t.Errorf("expected ID %d, got %d", tt.wantID, match.ID)
				}
			} else {
				if match != nil {
					t.Errorf("expected no match, got ID %d", match.ID)
				}
				if len(matches) != tt.wantMatches {
					t.Errorf("expected %d ambiguous matches, got %d", tt.wantMatches, len(matches))
				}
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
				if match == nil {
					t.Errorf("expected match with ID %d, got nil", tt.wantID)
				} else if match.ID != tt.wantID {
					t.Errorf("expected ID %d, got %d", tt.wantID, match.ID)
				}
			} else {
				if match != nil {
					t.Errorf("expected no match, got ID %d", match.ID)
				}
				if len(matches) != tt.wantMatches {
					t.Errorf("expected %d ambiguous matches, got %d", tt.wantMatches, len(matches))
				}
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
	if len(suggestions) > 3 {
		t.Errorf("suggest should return max 3 suggestions, got %d", len(suggestions))
	}
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
			if tt.wantAny && len(suggestions) == 0 {
				t.Error("expected suggestions, got none")
			}
			if !tt.wantAny && len(suggestions) > 0 {
				t.Errorf("expected no suggestions, got %v", suggestions)
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
	if match == nil || match.ID != 1 {
		t.Errorf("exact match 'test' should return ID 1, got %v", match)
	}

	// Exact match with different case
	match, _ = resolve("Test", projects, extract)
	if match == nil || match.ID != 2 {
		t.Errorf("exact match 'Test' should return ID 2, got %v", match)
	}
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
	if match != nil {
		t.Errorf("should be ambiguous, got match ID %d", match.ID)
	}
	if len(matches) != 3 {
		t.Errorf("expected 3 ambiguous matches, got %d", len(matches))
	}
}

// =============================================================================
// Cache Tests
// =============================================================================

func TestResolverClearCache(t *testing.T) {
	r := &Resolver{
		projects:  []Project{{ID: 1, Name: "Test"}},
		people:    []Person{{ID: 2, Name: "Alice"}},
		todolists: map[string][]Todolist{"123": {{ID: 3, Name: "Tasks"}}},
	}

	r.ClearCache()

	if r.projects != nil {
		t.Error("projects should be nil after ClearCache")
	}
	if r.people != nil {
		t.Error("people should be nil after ClearCache")
	}
	if len(r.todolists) != 0 {
		t.Error("todolists should be empty after ClearCache")
	}
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
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "12345" {
		t.Errorf("ID = %q, want %q", id, "12345")
	}
	if name != "Project Alpha" {
		t.Errorf("Name = %q, want %q", name, "Project Alpha")
	}
}

func TestResolverResolveProjectByName(t *testing.T) {
	r := newMockResolver()
	r.setProjects([]Project{
		{ID: 111, Name: "Project Alpha"},
		{ID: 222, Name: "Project Beta"},
	})

	ctx := context.Background()
	id, name, err := r.ResolveProject(ctx, "Beta")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "222" {
		t.Errorf("ID = %q, want %q", id, "222")
	}
	if name != "Project Beta" {
		t.Errorf("Name = %q, want %q", name, "Project Beta")
	}
}

func TestResolverResolveProjectAmbiguous(t *testing.T) {
	r := newMockResolver()
	r.setProjects([]Project{
		{ID: 111, Name: "Acme Corp"},
		{ID: 222, Name: "Acme Labs"},
	})

	ctx := context.Background()
	_, _, err := r.ResolveProject(ctx, "Acme")
	if err == nil {
		t.Fatal("expected error for ambiguous match")
	}

	// Verify it's an ambiguous error
	var outErr *output.Error
	if !errors.As(err, &outErr) {
		t.Fatalf("expected *output.Error, got %T", err)
	}
	if outErr.Code != output.CodeAmbiguous {
		t.Errorf("Code = %q, want %q", outErr.Code, output.CodeAmbiguous)
	}
}

func TestResolverResolveProjectNotFound(t *testing.T) {
	r := newMockResolver()
	r.setProjects([]Project{
		{ID: 111, Name: "Project Alpha"},
	})

	ctx := context.Background()
	_, _, err := r.ResolveProject(ctx, "Nonexistent")
	if err == nil {
		t.Fatal("expected error for not found")
	}

	// Verify it's a not found error
	var outErr *output.Error
	if !errors.As(err, &outErr) {
		t.Fatalf("expected *output.Error, got %T", err)
	}
	if outErr.Code != output.CodeNotFound {
		t.Errorf("Code = %q, want %q", outErr.Code, output.CodeNotFound)
	}
}

func TestResolverResolvePersonNumericID(t *testing.T) {
	r := newMockResolver()
	r.setPeople([]Person{
		{ID: 111, Name: "Alice Smith", Email: "alice@example.com"},
	})

	ctx := context.Background()
	id, name, err := r.ResolvePerson(ctx, "111")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "111" {
		t.Errorf("ID = %q, want %q", id, "111")
	}
	if name != "Alice Smith" {
		t.Errorf("Name = %q, want %q", name, "Alice Smith")
	}
}

func TestResolverResolvePersonByEmail(t *testing.T) {
	r := newMockResolver()
	r.setPeople([]Person{
		{ID: 111, Name: "Alice Smith", Email: "alice@example.com"},
		{ID: 222, Name: "Bob Jones", Email: "bob@example.com"},
	})

	ctx := context.Background()
	id, name, err := r.ResolvePerson(ctx, "bob@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "222" {
		t.Errorf("ID = %q, want %q", id, "222")
	}
	if name != "Bob Jones" {
		t.Errorf("Name = %q, want %q", name, "Bob Jones")
	}
}

func TestResolverResolvePersonByEmailCaseInsensitive(t *testing.T) {
	r := newMockResolver()
	r.setPeople([]Person{
		{ID: 111, Name: "Alice Smith", Email: "Alice@Example.COM"},
	})

	ctx := context.Background()
	id, _, err := r.ResolvePerson(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "111" {
		t.Errorf("ID = %q, want %q", id, "111")
	}
}

func TestResolverResolvePersonByName(t *testing.T) {
	r := newMockResolver()
	r.setPeople([]Person{
		{ID: 111, Name: "Alice Smith", Email: "alice@example.com"},
	})

	ctx := context.Background()
	id, _, err := r.ResolvePerson(ctx, "Alice Smith")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "111" {
		t.Errorf("ID = %q, want %q", id, "111")
	}
}

func TestResolverResolvePersonAmbiguous(t *testing.T) {
	r := newMockResolver()
	r.setPeople([]Person{
		{ID: 111, Name: "Alice Smith", Email: "alices@example.com"},
		{ID: 222, Name: "Alice Johnson", Email: "alicej@example.com"},
	})

	ctx := context.Background()
	_, _, err := r.ResolvePerson(ctx, "Alice")
	if err == nil {
		t.Fatal("expected error for ambiguous match")
	}

	var outErr *output.Error
	if !errors.As(err, &outErr) {
		t.Fatalf("expected *output.Error, got %T", err)
	}
	if outErr.Code != output.CodeAmbiguous {
		t.Errorf("Code = %q, want %q", outErr.Code, output.CodeAmbiguous)
	}
}

func TestResolverResolveTodolistNumericID(t *testing.T) {
	r := newMockResolver()
	r.setTodolists("12345", []Todolist{
		{ID: 111, Name: "Sprint Tasks"},
		{ID: 222, Name: "Bug Fixes"},
	})

	ctx := context.Background()
	id, name, err := r.ResolveTodolist(ctx, "111", "12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "111" {
		t.Errorf("ID = %q, want %q", id, "111")
	}
	if name != "Sprint Tasks" {
		t.Errorf("Name = %q, want %q", name, "Sprint Tasks")
	}
}

func TestResolverResolveTodolistByName(t *testing.T) {
	r := newMockResolver()
	r.setTodolists("12345", []Todolist{
		{ID: 111, Name: "Sprint Tasks"},
		{ID: 222, Name: "Bug Fixes"},
	})

	ctx := context.Background()
	id, name, err := r.ResolveTodolist(ctx, "Bug Fixes", "12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "222" {
		t.Errorf("ID = %q, want %q", id, "222")
	}
	if name != "Bug Fixes" {
		t.Errorf("Name = %q, want %q", name, "Bug Fixes")
	}
}

func TestResolverResolveTodolistNotFound(t *testing.T) {
	r := newMockResolver()
	r.setTodolists("12345", []Todolist{
		{ID: 111, Name: "Sprint Tasks"},
	})

	ctx := context.Background()
	_, _, err := r.ResolveTodolist(ctx, "Nonexistent", "12345")
	if err == nil {
		t.Fatal("expected error for not found")
	}

	var outErr *output.Error
	if !errors.As(err, &outErr) {
		t.Fatalf("expected *output.Error, got %T", err)
	}
	if outErr.Code != output.CodeNotFound {
		t.Errorf("Code = %q, want %q", outErr.Code, output.CodeNotFound)
	}
}

// =============================================================================
// Edge Case Tests
// =============================================================================

// Test SetAccountID method
func TestResolverSetAccountID(t *testing.T) {
	r := newMockResolver()
	r.setProjects([]Project{{ID: 1, Name: "Test"}})
	r.setPeople([]Person{{ID: 2, Name: "Alice"}})
	r.setTodolists("123", []Todolist{{ID: 3, Name: "Tasks"}})

	// Set same account ID - should not clear cache
	r.accountID = "12345"
	r.SetAccountID("12345")

	r.mu.RLock()
	if r.projects == nil {
		t.Error("projects should not be cleared when setting same account ID")
	}
	r.mu.RUnlock()

	// Set different account ID - should clear cache
	r.SetAccountID("67890")

	r.mu.RLock()
	if r.projects != nil {
		t.Error("projects should be nil after changing account ID")
	}
	if r.people != nil {
		t.Error("people should be nil after changing account ID")
	}
	if len(r.todolists) != 0 {
		t.Error("todolists should be empty after changing account ID")
	}
	if r.accountID != "67890" {
		t.Errorf("accountID should be 67890, got %s", r.accountID)
	}
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
	if match != nil {
		t.Error("empty input should be ambiguous, not single match")
	}
	if len(matches) != 2 {
		t.Errorf("empty input should match all items, got %d matches", len(matches))
	}
}

func TestResolveEmptyList(t *testing.T) {
	var projects []Project

	extract := func(p Project) (int64, string) {
		return p.ID, p.Name
	}

	match, matches := resolve("anything", projects, extract)
	if match != nil {
		t.Error("empty list should not match")
	}
	if len(matches) != 0 {
		t.Errorf("empty list should have no matches, got %d", len(matches))
	}
}

func TestSuggestEmptyList(t *testing.T) {
	var projects []Project

	getName := func(p Project) string { return p.Name }

	suggestions := suggest("test", projects, getName)
	if len(suggestions) != 0 {
		t.Errorf("empty list should have no suggestions, got %d", len(suggestions))
	}
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
			if match == nil {
				t.Fatalf("expected match for %q", tt.input)
			}
			if match.ID != tt.wantID {
				t.Errorf("ID = %d, want %d", match.ID, tt.wantID)
			}
		})
	}
}
