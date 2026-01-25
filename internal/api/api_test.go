package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/basecamp/bcq/internal/config"
)

func TestNewCache(t *testing.T) {
	cache := NewCache("/tmp/test-cache")
	if cache == nil {
		t.Fatal("NewCache returned nil")
	}
	if cache.dir != "/tmp/test-cache" {
		t.Errorf("cache.dir = %q, want %q", cache.dir, "/tmp/test-cache")
	}
}

func TestCacheKey(t *testing.T) {
	cache := NewCache("/tmp")

	// Same inputs should produce same key
	key1 := cache.Key("https://example.com/api", "account1", "token1")
	key2 := cache.Key("https://example.com/api", "account1", "token1")
	if key1 != key2 {
		t.Error("Same inputs should produce same cache key")
	}

	// Different URLs should produce different keys
	key3 := cache.Key("https://example.com/api2", "account1", "token1")
	if key1 == key3 {
		t.Error("Different URLs should produce different cache keys")
	}

	// Different accounts should produce different keys
	key4 := cache.Key("https://example.com/api", "account2", "token1")
	if key1 == key4 {
		t.Error("Different accounts should produce different cache keys")
	}

	// Different tokens should produce different keys
	key5 := cache.Key("https://example.com/api", "account1", "token2")
	if key1 == key5 {
		t.Error("Different tokens should produce different cache keys")
	}

	// Key should be 64 characters (sha256 hex)
	if len(key1) != 64 {
		t.Errorf("Cache key length = %d, want 64", len(key1))
	}
}

func TestCacheSetAndGet(t *testing.T) {
	tmpDir := t.TempDir()
	cache := NewCache(tmpDir)

	key := cache.Key("https://example.com/test", "acc", "tok")
	body := []byte(`{"data": "test"}`)
	etag := `"abc123"`

	// Set cache entry
	if err := cache.Set(key, body, etag); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Get ETag
	gotEtag := cache.GetETag(key)
	if gotEtag != etag {
		t.Errorf("GetETag() = %q, want %q", gotEtag, etag)
	}

	// Get Body
	gotBody := cache.GetBody(key)
	if string(gotBody) != string(body) {
		t.Errorf("GetBody() = %q, want %q", string(gotBody), string(body))
	}
}

func TestCacheGetMissing(t *testing.T) {
	tmpDir := t.TempDir()
	cache := NewCache(tmpDir)

	// Get non-existent ETag
	etag := cache.GetETag("nonexistent-key")
	if etag != "" {
		t.Errorf("GetETag for missing key = %q, want empty", etag)
	}

	// Get non-existent body
	body := cache.GetBody("nonexistent-key")
	if body != nil {
		t.Errorf("GetBody for missing key = %v, want nil", body)
	}
}

func TestCacheInvalidate(t *testing.T) {
	tmpDir := t.TempDir()
	cache := NewCache(tmpDir)

	key := cache.Key("https://example.com/invalidate", "acc", "tok")

	// Set cache entry
	cache.Set(key, []byte("data"), "etag")

	// Verify it exists
	if cache.GetETag(key) == "" {
		t.Fatal("Cache entry should exist before invalidation")
	}

	// Invalidate
	if err := cache.Invalidate(key); err != nil {
		t.Fatalf("Invalidate failed: %v", err)
	}

	// Verify it's gone
	if cache.GetETag(key) != "" {
		t.Error("ETag should be empty after invalidation")
	}
	if cache.GetBody(key) != nil {
		t.Error("Body should be nil after invalidation")
	}
}

func TestCacheClear(t *testing.T) {
	tmpDir := t.TempDir()
	cache := NewCache(tmpDir)

	// Set multiple entries
	cache.Set(cache.Key("url1", "acc", "tok"), []byte("data1"), "etag1")
	cache.Set(cache.Key("url2", "acc", "tok"), []byte("data2"), "etag2")

	// Clear
	if err := cache.Clear(); err != nil {
		t.Fatalf("Clear failed: %v", err)
	}

	// Verify everything is gone
	key1 := cache.Key("url1", "acc", "tok")
	key2 := cache.Key("url2", "acc", "tok")

	if cache.GetETag(key1) != "" || cache.GetETag(key2) != "" {
		t.Error("ETags should be empty after clear")
	}
	if cache.GetBody(key1) != nil || cache.GetBody(key2) != nil {
		t.Error("Bodies should be nil after clear")
	}
}

func TestCacheFilePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	cache := NewCache(tmpDir)

	key := cache.Key("https://example.com/perms", "acc", "tok")
	cache.Set(key, []byte("data"), "etag")

	// Check responses directory permissions
	responsesDir := filepath.Join(tmpDir, "responses")
	info, err := os.Stat(responsesDir)
	if err != nil {
		t.Fatalf("Responses dir not found: %v", err)
	}
	perms := info.Mode().Perm()
	if perms != 0700 {
		t.Errorf("Responses dir permissions = %o, want 0700", perms)
	}

	// Check body file permissions
	bodyFile := filepath.Join(responsesDir, key+".body")
	info, err = os.Stat(bodyFile)
	if err != nil {
		t.Fatalf("Body file not found: %v", err)
	}
	perms = info.Mode().Perm()
	if perms != 0600 {
		t.Errorf("Body file permissions = %o, want 0600", perms)
	}

	// Check etags file permissions
	etagsFile := filepath.Join(tmpDir, "etags.json")
	info, err = os.Stat(etagsFile)
	if err != nil {
		t.Fatalf("Etags file not found: %v", err)
	}
	perms = info.Mode().Perm()
	if perms != 0600 {
		t.Errorf("Etags file permissions = %o, want 0600", perms)
	}
}

func TestParseNextLink(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		expected string
	}{
		{
			name:     "empty header",
			header:   "",
			expected: "",
		},
		{
			name:     "next link only",
			header:   `<https://api.example.com/items?page=2>; rel="next"`,
			expected: "https://api.example.com/items?page=2",
		},
		{
			name:     "multiple links with next",
			header:   `<https://api.example.com/items?page=2>; rel="next", <https://api.example.com/items?page=5>; rel="last"`,
			expected: "https://api.example.com/items?page=2",
		},
		{
			name:     "multiple links next second",
			header:   `<https://api.example.com/items?page=1>; rel="prev", <https://api.example.com/items?page=3>; rel="next"`,
			expected: "https://api.example.com/items?page=3",
		},
		{
			name:     "no next link",
			header:   `<https://api.example.com/items?page=1>; rel="prev", <https://api.example.com/items?page=5>; rel="last"`,
			expected: "",
		},
		{
			name:     "complex URL",
			header:   `<https://api.example.com/buckets/123/todos?page=2&per_page=50>; rel="next"`,
			expected: "https://api.example.com/buckets/123/todos?page=2&per_page=50",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseNextLink(tt.header)
			if result != tt.expected {
				t.Errorf("parseNextLink(%q) = %q, want %q", tt.header, result, tt.expected)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		header   string
		expected int
	}{
		{"", 0},
		{"5", 5},
		{"60", 60},
		{"0", 0},
		{"invalid", 0},
		{"5.5", 0}, // Non-integer
	}

	for _, tt := range tests {
		t.Run(tt.header, func(t *testing.T) {
			result := parseRetryAfter(tt.header)
			if result != tt.expected {
				t.Errorf("parseRetryAfter(%q) = %d, want %d", tt.header, result, tt.expected)
			}
		})
	}
}

func TestResponseUnmarshalData(t *testing.T) {
	resp := &Response{
		Data:       json.RawMessage(`{"id": 123, "name": "test"}`),
		StatusCode: 200,
	}

	var result struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}

	if err := resp.UnmarshalData(&result); err != nil {
		t.Fatalf("UnmarshalData failed: %v", err)
	}

	if result.ID != 123 {
		t.Errorf("ID = %d, want 123", result.ID)
	}
	if result.Name != "test" {
		t.Errorf("Name = %q, want %q", result.Name, "test")
	}
}

func TestResponseUnmarshalDataInvalid(t *testing.T) {
	resp := &Response{
		Data:       json.RawMessage(`not valid json`),
		StatusCode: 200,
	}

	var result map[string]any
	if err := resp.UnmarshalData(&result); err == nil {
		t.Error("UnmarshalData should fail for invalid JSON")
	}
}

func TestBuildURL(t *testing.T) {
	cfg := &config.Config{
		BaseURL:   "https://3.basecampapi.com",
		AccountID: "12345",
	}
	client := &Client{cfg: cfg}

	tests := []struct {
		path     string
		expected string
	}{
		// Normal paths get account ID prepended
		{"/projects.json", "https://3.basecampapi.com/12345/projects.json"},
		{"/buckets/1/todos.json", "https://3.basecampapi.com/12345/buckets/1/todos.json"},
		{"projects.json", "https://3.basecampapi.com/12345/projects.json"}, // Missing leading slash

		// Paths already with account ID are used directly
		{"/12345/projects.json", "https://3.basecampapi.com/12345/projects.json"},

		// Special paths skip account ID
		{"/.well-known/oauth-authorization-server", "https://3.basecampapi.com/.well-known/oauth-authorization-server"},
		{"/authorization/token", "https://3.basecampapi.com/authorization/token"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := client.buildURL(tt.path)
			if result != tt.expected {
				t.Errorf("buildURL(%q) = %q, want %q", tt.path, result, tt.expected)
			}
		})
	}
}

func TestBuildURLNoAccount(t *testing.T) {
	cfg := &config.Config{
		BaseURL:   "https://3.basecampapi.com",
		AccountID: "", // No account
	}
	client := &Client{cfg: cfg}

	// Without account ID, path is used directly
	result := client.buildURL("/projects.json")
	if result != "https://3.basecampapi.com/projects.json" {
		t.Errorf("buildURL without account = %q", result)
	}
}

func TestProjectPath(t *testing.T) {
	cfg := &config.Config{
		ProjectID: "67890",
	}
	client := &Client{cfg: cfg}

	result := client.ProjectPath("/todos.json")
	expected := "/buckets/67890/todos.json"
	if result != expected {
		t.Errorf("ProjectPath() = %q, want %q", result, expected)
	}
}

func TestProjectPathNoProject(t *testing.T) {
	cfg := &config.Config{
		ProjectID: "",
	}
	client := &Client{cfg: cfg}

	result := client.ProjectPath("/todos.json")
	if result != "" {
		t.Errorf("ProjectPath with no project = %q, want empty", result)
	}
}

func TestRequireProject(t *testing.T) {
	// With project
	cfg := &config.Config{ProjectID: "12345"}
	client := &Client{cfg: cfg}
	if err := client.RequireProject(); err != nil {
		t.Errorf("RequireProject should succeed with project, got: %v", err)
	}

	// Without project
	cfg = &config.Config{ProjectID: ""}
	client = &Client{cfg: cfg}
	if err := client.RequireProject(); err == nil {
		t.Error("RequireProject should fail without project")
	}
}

func TestRequireAccount(t *testing.T) {
	// With account
	cfg := &config.Config{AccountID: "12345"}
	client := &Client{cfg: cfg}
	if err := client.RequireAccount(); err != nil {
		t.Errorf("RequireAccount should succeed with account, got: %v", err)
	}

	// Without account
	cfg = &config.Config{AccountID: ""}
	client = &Client{cfg: cfg}
	if err := client.RequireAccount(); err == nil {
		t.Error("RequireAccount should fail without account")
	}
}

func TestSetVerbose(t *testing.T) {
	client := &Client{}

	if client.verbose {
		t.Error("verbose should default to false")
	}

	client.SetVerbose(true)
	if !client.verbose {
		t.Error("SetVerbose(true) should set verbose to true")
	}

	client.SetVerbose(false)
	if client.verbose {
		t.Error("SetVerbose(false) should set verbose to false")
	}
}

func TestCacheKeyWithEmptyToken(t *testing.T) {
	cache := NewCache("/tmp")

	// Key with empty token should still work
	key := cache.Key("https://example.com/api", "account1", "")
	if key == "" {
		t.Error("Cache key should not be empty with empty token")
	}
	if len(key) != 64 {
		t.Errorf("Cache key length = %d, want 64", len(key))
	}
}

func TestCacheMultipleEntriesPreserved(t *testing.T) {
	tmpDir := t.TempDir()
	cache := NewCache(tmpDir)

	// Set first entry
	key1 := cache.Key("url1", "acc", "tok")
	cache.Set(key1, []byte("data1"), "etag1")

	// Set second entry
	key2 := cache.Key("url2", "acc", "tok")
	cache.Set(key2, []byte("data2"), "etag2")

	// Both should still exist
	if cache.GetETag(key1) != "etag1" {
		t.Error("First entry should still exist after adding second")
	}
	if cache.GetETag(key2) != "etag2" {
		t.Error("Second entry should exist")
	}
	if string(cache.GetBody(key1)) != "data1" {
		t.Error("First body should still exist")
	}
	if string(cache.GetBody(key2)) != "data2" {
		t.Error("Second body should exist")
	}
}
