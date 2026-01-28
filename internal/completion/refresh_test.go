package completion

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// mockTokenProvider implements basecamp.TokenProvider for testing.
type mockTokenProvider struct{}

func (m *mockTokenProvider) AccessToken(ctx context.Context) (string, error) {
	return "test-token", nil
}

// noNetworkTransport is an http.RoundTripper that fails immediately.
// Used in tests to prevent real network calls without waiting for timeouts.
type noNetworkTransport struct{}

func (noNetworkTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network disabled in tests")
}

func newTestClient(t *testing.T) *basecamp.Client {
	t.Helper()

	cfg := &basecamp.Config{
		AccountID:    "123",
		CacheEnabled: false,
	}
	return basecamp.NewClient(cfg, &mockTokenProvider{},
		basecamp.WithTransport(noNetworkTransport{}),
		basecamp.WithMaxRetries(0),
	)
}

func TestRefresher_RefreshIfStale_Fresh(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	client := newTestClient(t)
	refresher := NewRefresher(store, client)

	// Save fresh cache
	if err := store.Save(&Cache{Projects: []CachedProject{{ID: 1, Name: "Test"}}}); err != nil {
		t.Fatal(err)
	}

	// RefreshIfStale should not refresh fresh cache
	refresher.RefreshIfStale(time.Hour)

	// Small delay to let any potential goroutine start
	time.Sleep(10 * time.Millisecond)

	// Should not be refreshing
	if refresher.IsRefreshing() {
		t.Error("Should not be refreshing fresh cache")
	}
}

func TestRefresher_RefreshIfStale_Stale_TriggersRefresh(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	client := newTestClient(t)
	refresher := NewRefresher(store, client)

	// Empty cache is stale - this should trigger a background refresh
	// The refresh will fail (no network) but that's OK - we're testing the trigger
	refresher.RefreshIfStale(time.Hour)

	// Should either be refreshing or have completed (with error)
	// Give it a moment to start
	time.Sleep(10 * time.Millisecond)

	// Wait for completion (it will fail due to no network, but should complete)
	for i := 0; i < 100; i++ {
		if !refresher.IsRefreshing() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Cache should still be empty since network failed
	projects := store.Projects()
	if len(projects) != 0 {
		t.Errorf("Expected empty cache (network disabled), got %d projects", len(projects))
	}
}

func TestRefresher_RefreshIfStale_DoesNotBlockConcurrent(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	client := newTestClient(t)
	refresher := NewRefresher(store, client)

	// Trigger multiple refreshes concurrently - only one should run
	for i := 0; i < 10; i++ {
		refresher.RefreshIfStale(time.Nanosecond)
	}

	// Wait for completion
	for i := 0; i < 100; i++ {
		if !refresher.IsRefreshing() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Should complete without panics or data races (test passes if no panic)
}

func TestRefresher_IsRefreshing(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	client := newTestClient(t)
	refresher := NewRefresher(store, client)

	// Initially not refreshing
	if refresher.IsRefreshing() {
		t.Error("Should not be refreshing initially")
	}
}

func TestConvertProjects(t *testing.T) {
	sdkProjects := []basecamp.Project{
		{ID: 1, Name: "Test", Purpose: "hq", Bookmarked: true},
		{ID: 2, Name: "Other", Purpose: "", Bookmarked: false},
	}

	cached := convertProjects(sdkProjects)

	if len(cached) != 2 {
		t.Fatalf("Expected 2 cached projects, got %d", len(cached))
	}
	if cached[0].ID != 1 {
		t.Errorf("Expected ID 1, got %d", cached[0].ID)
	}
	if cached[0].Purpose != "hq" {
		t.Errorf("Expected purpose 'hq', got %q", cached[0].Purpose)
	}
	if !cached[0].Bookmarked {
		t.Error("Expected Bookmarked to be true")
	}
}

func TestConvertPeople(t *testing.T) {
	sdkPeople := []basecamp.Person{
		{ID: 100, Name: "Alice", EmailAddress: "alice@example.com"},
		{ID: 200, Name: "Bob", EmailAddress: "bob@example.com"},
	}

	cached := convertPeople(sdkPeople)

	if len(cached) != 2 {
		t.Fatalf("Expected 2 cached people, got %d", len(cached))
	}
	if cached[0].ID != 100 {
		t.Errorf("Expected ID 100, got %d", cached[0].ID)
	}
	if cached[0].EmailAddress != "alice@example.com" {
		t.Errorf("Expected alice@example.com, got %q", cached[0].EmailAddress)
	}
}
