package completion

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// newTestCompleter creates a Completer for testing with a fixed cache directory.
func newTestCompleter(cacheDir string) *Completer {
	return NewCompleter(func(cmd *cobra.Command) string { return cacheDir })
}

// newTestCmd creates a minimal cobra.Command with a context for testing completion functions.
func newTestCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return cmd
}

func TestRankProjects(t *testing.T) {
	now := time.Now()
	projects := []CachedProject{
		{ID: 1, Name: "Alpha Project", UpdatedAt: now.Add(-24 * time.Hour)},
		{ID: 2, Name: "Beta Project", Bookmarked: true, UpdatedAt: now.Add(-48 * time.Hour)},
		{ID: 3, Name: "HQ", Purpose: "hq", UpdatedAt: now.Add(-72 * time.Hour)},
		{ID: 4, Name: "Zeta Project", UpdatedAt: now.Add(-1 * time.Hour)},
		{ID: 5, Name: "Gamma Bookmarked", Bookmarked: true, UpdatedAt: now.Add(-2 * time.Hour)},
	}

	ranked := rankProjects(projects)

	// Expected order:
	// 1. HQ (purpose=hq)
	// 2. Gamma Bookmarked (bookmarked, more recent)
	// 3. Beta Project (bookmarked, less recent)
	// 4. Zeta Project (recent)
	// 5. Alpha Project (older)

	expected := []int64{3, 5, 2, 4, 1}
	for i, id := range expected {
		if ranked[i].ID != id {
			t.Errorf("position %d: expected ID %d, got %d (%s)", i, id, ranked[i].ID, ranked[i].Name)
		}
	}
}

func TestRankProjectsAlphabetical(t *testing.T) {
	// When all else is equal, should sort alphabetically
	projects := []CachedProject{
		{ID: 1, Name: "Zebra"},
		{ID: 2, Name: "Apple"},
		{ID: 3, Name: "Banana"},
	}

	ranked := rankProjects(projects)

	expected := []string{"Apple", "Banana", "Zebra"}
	for i, name := range expected {
		if ranked[i].Name != name {
			t.Errorf("position %d: expected %s, got %s", i, name, ranked[i].Name)
		}
	}
}

func TestCompleterProjectCompletion(t *testing.T) {
	// Create temp dir for cache
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Populate cache
	now := time.Now()
	projects := []CachedProject{
		{ID: 100, Name: "Engineering", Purpose: "hq"},
		{ID: 200, Name: "Marketing Campaign", Bookmarked: true},
		{ID: 300, Name: "Sales Pipeline", UpdatedAt: now},
	}
	if err := store.UpdateProjects(projects); err != nil {
		t.Fatalf("failed to update projects: %v", err)
	}

	completer := newTestCompleter(tmpDir)
	fn := completer.ProjectCompletion()

	tests := []struct {
		name       string
		toComplete string
		wantIDs    []string // Expected IDs in order
	}{
		{
			name:       "empty prefix returns all ranked",
			toComplete: "",
			wantIDs:    []string{"100", "200", "300"}, // HQ, Bookmarked, Recent
		},
		{
			name:       "prefix filter",
			toComplete: "eng",
			wantIDs:    []string{"100"},
		},
		{
			name:       "contains filter",
			toComplete: "campaign",
			wantIDs:    []string{"200"},
		},
		{
			name:       "no matches",
			toComplete: "xyz",
			wantIDs:    []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			completions, directive := fn(newTestCmd(), nil, tt.toComplete)
			if directive != cobra.ShellCompDirectiveNoFileComp {
				t.Errorf("expected NoFileComp directive, got %v", directive)
			}

			if len(completions) != len(tt.wantIDs) {
				t.Errorf("expected %d completions, got %d", len(tt.wantIDs), len(completions))
				return
			}

			for i, wantID := range tt.wantIDs {
				// Completion format is "ID\tDescription"
				got := string(completions[i])
				if len(got) < len(wantID) || got[:len(wantID)] != wantID {
					t.Errorf("completion %d: expected to start with %s, got %s", i, wantID, got)
				}
			}
		})
	}
}

func TestCompleterPeopleCompletion(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	people := []CachedPerson{
		{ID: 1, Name: "Alice Smith", EmailAddress: "alice@example.com"},
		{ID: 2, Name: "Bob Jones", EmailAddress: "bob@example.com"},
		{ID: 3, Name: "Carol Williams"},
	}
	if err := store.UpdatePeople(people); err != nil {
		t.Fatalf("failed to update people: %v", err)
	}

	completer := newTestCompleter(tmpDir)
	fn := completer.PeopleCompletion()

	tests := []struct {
		name       string
		toComplete string
		wantFirst  string // First completion should be this ID or "me"
		wantCount  int
	}{
		{
			name:       "empty includes me",
			toComplete: "",
			wantFirst:  "me",
			wantCount:  4, // me + 3 people
		},
		{
			name:       "me prefix",
			toComplete: "me",
			wantFirst:  "me",
			wantCount:  1,
		},
		{
			name:       "name prefix",
			toComplete: "ali",
			wantFirst:  "1",
			wantCount:  1,
		},
		{
			name:       "email prefix",
			toComplete: "bob@",
			wantFirst:  "2",
			wantCount:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			completions, directive := fn(newTestCmd(), nil, tt.toComplete)
			if directive != cobra.ShellCompDirectiveNoFileComp {
				t.Errorf("expected NoFileComp directive, got %v", directive)
			}

			if len(completions) != tt.wantCount {
				t.Errorf("expected %d completions, got %d", tt.wantCount, len(completions))
				return
			}

			if len(completions) > 0 {
				got := string(completions[0])
				if len(got) < len(tt.wantFirst) || got[:len(tt.wantFirst)] != tt.wantFirst {
					t.Errorf("first completion: expected to start with %s, got %s", tt.wantFirst, got)
				}
			}
		})
	}
}

func TestCompleterEmptyCache(t *testing.T) {
	tmpDir := t.TempDir()
	// Initialize empty store
	_ = NewStore(tmpDir)

	completer := newTestCompleter(tmpDir)

	// Project completion with empty cache returns nothing
	projectFn := completer.ProjectCompletion()
	completions, directive := projectFn(newTestCmd(), nil, "")
	if len(completions) != 0 {
		t.Errorf("expected no completions with empty cache, got %d", len(completions))
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("expected NoFileComp directive, got %v", directive)
	}

	// People completion with empty cache still returns "me"
	peopleFn := completer.PeopleCompletion()
	completions, _ = peopleFn(newTestCmd(), nil, "")
	if len(completions) != 1 {
		t.Errorf("expected 1 completion (me) with empty cache, got %d", len(completions))
	}
	if len(completions) > 0 && string(completions[0]) != "me\tCurrent authenticated user" {
		t.Errorf("expected 'me' completion, got %q", completions[0])
	}
}

func TestCompleterMissingCacheFile(t *testing.T) {
	// Use a directory that doesn't have a cache file
	tmpDir := t.TempDir()
	nonExistentDir := filepath.Join(tmpDir, "nonexistent")

	completer := newTestCompleter(nonExistentDir)
	fn := completer.ProjectCompletion()

	completions, directive := fn(newTestCmd(), nil, "test")
	if len(completions) != 0 {
		t.Errorf("expected no completions with missing cache, got %d", len(completions))
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("expected NoFileComp directive")
	}
}

func TestCompleterCorruptedCache(t *testing.T) {
	tmpDir := t.TempDir()

	// Write corrupted JSON
	if err := os.MkdirAll(tmpDir, 0700); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(tmpDir, CacheFileName)
	if err := os.WriteFile(cachePath, []byte("{invalid json"), 0600); err != nil {
		t.Fatal(err)
	}

	completer := newTestCompleter(tmpDir)
	fn := completer.ProjectCompletion()

	// Should return empty completions, not error
	completions, _ := fn(newTestCmd(), nil, "")
	if len(completions) != 0 {
		t.Errorf("expected no completions with corrupted cache, got %d", len(completions))
	}
}

func TestProjectNameCompletionWithSpaces(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	projects := []CachedProject{
		{ID: 1, Name: "Simple"},
		{ID: 2, Name: "Has Spaces"},
	}
	if err := store.UpdateProjects(projects); err != nil {
		t.Fatal(err)
	}

	completer := newTestCompleter(tmpDir)
	fn := completer.ProjectNameCompletion()

	completions, _ := fn(newTestCmd(), nil, "")
	if len(completions) != 2 {
		t.Fatalf("expected 2 completions, got %d", len(completions))
	}

	// First should be "Has Spaces" (alphabetically first H < S)
	// Names are returned as-is; Cobra's completion scripts handle escaping
	first := string(completions[0])
	if first != "Has Spaces" {
		t.Errorf("expected 'Has Spaces', got %s", first)
	}

	second := string(completions[1])
	if second != "Simple" {
		t.Errorf("expected 'Simple', got %s", second)
	}
}

func TestCompleterAccountCompletion(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	accounts := []CachedAccount{
		{ID: 1234567, Name: "Acme Corp"},
		{ID: 9876543, Name: "Beta Inc"},
		{ID: 5555555, Name: "Zeta LLC"},
	}
	if err := store.UpdateAccounts(accounts); err != nil {
		t.Fatalf("failed to update accounts: %v", err)
	}

	completer := newTestCompleter(tmpDir)
	fn := completer.AccountCompletion()

	tests := []struct {
		name       string
		toComplete string
		wantIDs    []string // Expected IDs in alphabetical order by name
	}{
		{
			name:       "empty prefix returns all sorted",
			toComplete: "",
			wantIDs:    []string{"1234567", "9876543", "5555555"}, // Acme, Beta, Zeta
		},
		{
			name:       "name prefix filter",
			toComplete: "acme",
			wantIDs:    []string{"1234567"},
		},
		{
			name:       "name contains filter",
			toComplete: "inc",
			wantIDs:    []string{"9876543"},
		},
		{
			name:       "ID prefix filter",
			toComplete: "123",
			wantIDs:    []string{"1234567"},
		},
		{
			name:       "no matches",
			toComplete: "xyz",
			wantIDs:    []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			completions, directive := fn(newTestCmd(), nil, tt.toComplete)
			if directive != cobra.ShellCompDirectiveNoFileComp {
				t.Errorf("expected NoFileComp directive, got %v", directive)
			}

			if len(completions) != len(tt.wantIDs) {
				t.Errorf("expected %d completions, got %d", len(tt.wantIDs), len(completions))
				return
			}

			for i, wantID := range tt.wantIDs {
				// Completion format is "ID\tDescription"
				got := string(completions[i])
				if len(got) < len(wantID) || got[:len(wantID)] != wantID {
					t.Errorf("completion %d: expected to start with %s, got %s", i, wantID, got)
				}
			}
		})
	}
}

func TestCompleterAccountEmptyCache(t *testing.T) {
	tmpDir := t.TempDir()
	// Initialize empty store
	_ = NewStore(tmpDir)

	completer := newTestCompleter(tmpDir)
	fn := completer.AccountCompletion()

	completions, directive := fn(newTestCmd(), nil, "")
	if len(completions) != 0 {
		t.Errorf("expected no completions with empty cache, got %d", len(completions))
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("expected NoFileComp directive, got %v", directive)
	}
}
