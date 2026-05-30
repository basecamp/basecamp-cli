package completion

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestSanitizeCompletionDesc(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"C0 control SOH stripped", "a\x01b", "ab"},
		{"C0 control US stripped", "a\x1fb", "ab"},
		{"DEL stripped", "a\x7fb", "ab"},
		{"C1 control 0x80 stripped", "a\u0080b", "ab"},
		{"C1 control 0x9f stripped", "a\u009fb", "ab"},
		{"valid accented rune passes through", "café", "café"},
		{"valid emoji rune passes through", "party🎉", "party🎉"},
		{"full ANSI escape sequence stripped whole", "\x1b[31mred\x1b[0m", "red"},
		{"internal double space preserved", "a  b", "a  b"},
		{"newline dropped", "a\nb", "ab"},
		{"tab dropped", "a\tb", "ab"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sanitizeCompletionDesc(tt.in))
		})
	}
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
		assert.Equal(t, id, ranked[i].ID, "position %d: expected ID %d, got %d (%s)", i, id, ranked[i].ID, ranked[i].Name)
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
		assert.Equal(t, name, ranked[i].Name, "position %d: expected %s, got %s", i, name, ranked[i].Name)
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
	require.NoError(t, store.UpdateProjects(projects), "failed to update projects")

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
			assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive, "expected NoFileComp directive")

			require.Len(t, completions, len(tt.wantIDs), "expected %d completions", len(tt.wantIDs))

			for i, wantID := range tt.wantIDs {
				// Completion format is "ID\tDescription"
				got := completions[i]
				assert.True(t, len(got) >= len(wantID) && got[:len(wantID)] == wantID, "completion %d: expected to start with %s, got %s", i, wantID, got)
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
	require.NoError(t, store.UpdatePeople(people), "failed to update people")

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
			assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive, "expected NoFileComp directive")

			require.Len(t, completions, tt.wantCount, "expected %d completions", tt.wantCount)

			if len(completions) > 0 {
				got := completions[0]
				assert.True(t, len(got) >= len(tt.wantFirst) && got[:len(tt.wantFirst)] == tt.wantFirst, "first completion: expected to start with %s, got %s", tt.wantFirst, got)
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
	assert.Len(t, completions, 0, "expected no completions with empty cache")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive, "expected NoFileComp directive")

	// People completion with empty cache still returns "me"
	peopleFn := completer.PeopleCompletion()
	completions, _ = peopleFn(newTestCmd(), nil, "")
	assert.Len(t, completions, 1, "expected 1 completion (me) with empty cache")
	if len(completions) > 0 {
		assert.Equal(t, "me\tCurrent authenticated user", completions[0], "expected 'me' completion")
	}
}

func TestCompleterMissingCacheFile(t *testing.T) {
	// Use a directory that doesn't have a cache file
	tmpDir := t.TempDir()
	nonExistentDir := filepath.Join(tmpDir, "nonexistent")

	completer := newTestCompleter(nonExistentDir)
	fn := completer.ProjectCompletion()

	completions, directive := fn(newTestCmd(), nil, "test")
	assert.Len(t, completions, 0, "expected no completions with missing cache")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive, "expected NoFileComp directive")
}

func TestCompleterCorruptedCache(t *testing.T) {
	tmpDir := t.TempDir()

	// Write corrupted JSON
	require.NoError(t, os.MkdirAll(tmpDir, 0700))
	cachePath := filepath.Join(tmpDir, CacheFileName)
	require.NoError(t, os.WriteFile(cachePath, []byte("{invalid json"), 0600))

	completer := newTestCompleter(tmpDir)
	fn := completer.ProjectCompletion()

	// Should return empty completions, not error
	completions, _ := fn(newTestCmd(), nil, "")
	assert.Len(t, completions, 0, "expected no completions with corrupted cache")
}

func TestProjectNameCompletionWithSpaces(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	projects := []CachedProject{
		{ID: 1, Name: "Simple"},
		{ID: 2, Name: "Has Spaces"},
	}
	require.NoError(t, store.UpdateProjects(projects))

	completer := newTestCompleter(tmpDir)
	fn := completer.ProjectNameCompletion()

	completions, _ := fn(newTestCmd(), nil, "")
	require.Len(t, completions, 2, "expected 2 completions")

	// First should be "Has Spaces" (alphabetically first H < S)
	// Names are returned as-is; Cobra's completion scripts handle escaping
	first := completions[0]
	assert.Equal(t, "Has Spaces", first)

	second := completions[1]
	assert.Equal(t, "Simple", second)
}

func TestCompleterAccountCompletion(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	accounts := []CachedAccount{
		{ID: 1234567, Name: "Acme Corp"},
		{ID: 9876543, Name: "Beta Inc"},
		{ID: 5555555, Name: "Zeta LLC"},
	}
	require.NoError(t, store.UpdateAccounts(accounts), "failed to update accounts")

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
			assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive, "expected NoFileComp directive")

			require.Len(t, completions, len(tt.wantIDs), "expected %d completions", len(tt.wantIDs))

			for i, wantID := range tt.wantIDs {
				// Completion format is "ID\tDescription"
				got := completions[i]
				assert.True(t, len(got) >= len(wantID) && got[:len(wantID)] == wantID, "completion %d: expected to start with %s, got %s", i, wantID, got)
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
	assert.Len(t, completions, 0, "expected no completions with empty cache")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive, "expected NoFileComp directive")
}

func TestHasControlChars(t *testing.T) {
	assert.False(t, hasControlChars("Normal Name"))
	assert.False(t, hasControlChars("café party🎉"))
	assert.False(t, hasControlChars("Double  Space"), "internal double space is benign, not a control char")
	assert.True(t, hasControlChars("Evil\x1b[31m"), "ESC (C0)")
	assert.True(t, hasControlChars("\x1b[31mred\x1b[0m"), "full ANSI SGR escape sequence")
	assert.True(t, hasControlChars("Evil\u009b31m"), "CSI (C1)")
	assert.True(t, hasControlChars("Evil\x9bRaw"), "raw C1 byte (invalid UTF-8)")
	assert.True(t, hasControlChars("Evil\x7f"), "DEL")
}

// Name-valued completions can't sanitize the value (resolution matches the
// exact name string), so candidates whose names carry control characters —
// ESC (C0) or CSI (C1) sequences that could inject into the user's terminal
// via the completion machinery — must be skipped entirely.
func TestProjectNameCompletionSkipsControlCharNames(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	require.NoError(t, store.UpdateProjects([]CachedProject{
		{ID: 1, Name: "Good Project"},
		{ID: 2, Name: "Evil\x1b]0;pwned\x07Project"},
		{ID: 3, Name: "Evil\u009b31mProject"},
	}))

	fn := newTestCompleter(tmpDir).ProjectNameCompletion()
	completions, _ := fn(newTestCmd(), nil, "")

	require.Len(t, completions, 1)
	assert.Equal(t, "Good Project", completions[0])
}

func TestPeopleNameCompletionSkipsControlCharNames(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	require.NoError(t, store.UpdatePeople([]CachedPerson{
		{ID: 1, Name: "Alice Smith"},
		{ID: 2, Name: "Bob\x1b[2JJones"},
		{ID: 3, Name: "Carol\u009b31mWilliams"},
	}))

	fn := newTestCompleter(tmpDir).PeopleNameCompletion()
	completions, _ := fn(newTestCmd(), nil, "a")

	// "a" matches Alice and the malicious C-a-rol name; only Alice survives
	// the control-char skip ("me" doesn't match "a", Bob doesn't contain "a").
	require.Len(t, completions, 1)
	assert.Equal(t, "Alice Smith", completions[0])
}

func TestProfileCompletionSkipsControlCharNames(t *testing.T) {
	// Profiles load from config files; isolate global config and make sure
	// no repo/local config is reachable from CWD.
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Chdir(t.TempDir())

	require.NoError(t, os.MkdirAll(filepath.Join(configHome, "basecamp"), 0o700))
	cfgJSON := `{"profiles":{` +
		`"good":{"base_url":"https://good.example.com"},` +
		`"evil\u001b[31m":{"base_url":"https://evil.example.com"},` +
		`"evil\u009bcsi":{"base_url":"https://evil2.example.com"}}}`
	require.NoError(t, os.WriteFile(
		filepath.Join(configHome, "basecamp", "config.json"), []byte(cfgJSON), 0o600))

	fn := newTestCompleter(t.TempDir()).ProfileCompletion()
	completions, _ := fn(newTestCmd(), nil, "")

	require.Len(t, completions, 1)
	assert.Equal(t, "good\thttps://good.example.com", completions[0])
}
