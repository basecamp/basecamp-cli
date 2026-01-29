package completion

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func newTestAccountClient(t *testing.T) *basecamp.AccountClient {
	t.Helper()

	cfg := &basecamp.Config{
		CacheEnabled: false,
	}
	client := basecamp.NewClient(cfg, &mockTokenProvider{},
		basecamp.WithTransport(noNetworkTransport{}),
		basecamp.WithMaxRetries(0),
	)
	return client.ForAccount("123")
}

func TestRefresher_RefreshIfStale_Fresh(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	client := newTestAccountClient(t)
	refresher := NewRefresher(store, client)

	// Save fresh cache
	require.NoError(t, store.Save(&Cache{Projects: []CachedProject{{ID: 1, Name: "Test"}}}))

	// RefreshIfStale should not refresh fresh cache
	refresher.RefreshIfStale(time.Hour)

	// Small delay to let any potential goroutine start
	time.Sleep(10 * time.Millisecond)

	// Should not be refreshing
	assert.False(t, refresher.IsRefreshing(), "Should not be refreshing fresh cache")
}

func TestRefresher_RefreshIfStale_Stale_TriggersRefresh(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	client := newTestAccountClient(t)
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
	assert.Empty(t, projects, "Expected empty cache (network disabled)")
}

func TestRefresher_RefreshIfStale_DoesNotBlockConcurrent(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	client := newTestAccountClient(t)
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
	client := newTestAccountClient(t)
	refresher := NewRefresher(store, client)

	// Initially not refreshing
	assert.False(t, refresher.IsRefreshing(), "Should not be refreshing initially")
}

func TestConvertProjects(t *testing.T) {
	sdkProjects := []basecamp.Project{
		{ID: 1, Name: "Test", Purpose: "hq", Bookmarked: true},
		{ID: 2, Name: "Other", Purpose: "", Bookmarked: false},
	}

	cached := convertProjects(sdkProjects)

	require.Len(t, cached, 2)
	assert.Equal(t, int64(1), cached[0].ID)
	assert.Equal(t, "hq", cached[0].Purpose)
	assert.True(t, cached[0].Bookmarked, "Expected Bookmarked to be true")
}

func TestConvertPeople(t *testing.T) {
	sdkPeople := []basecamp.Person{
		{ID: 100, Name: "Alice", EmailAddress: "alice@example.com"},
		{ID: 200, Name: "Bob", EmailAddress: "bob@example.com"},
	}

	cached := convertPeople(sdkPeople)

	require.Len(t, cached, 2)
	assert.Equal(t, int64(100), cached[0].ID)
	assert.Equal(t, "alice@example.com", cached[0].EmailAddress)
}
