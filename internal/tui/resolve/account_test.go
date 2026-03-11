package resolve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
)

// accountTestTokenProvider returns a configurable token for tests.
type accountTestTokenProvider struct{ token string }

func (p accountTestTokenProvider) AccessToken(_ context.Context) (string, error) {
	return p.token, nil
}

// TestFetchAccounts_BC3Token verifies that the account resolver correctly
// routes to the BC3 authorization endpoint when BASECAMP_TOKEN has a bc_at_ prefix.
func TestFetchAccounts_BC3Token(t *testing.T) {
	// Start mock BC3 server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/authorization.json", r.URL.Path)
		resp := basecamp.AuthorizationInfo{
			Identity: basecamp.Identity{ID: 1, FirstName: "Test"},
			Accounts: []basecamp.AuthorizedAccount{
				{Product: "bc3", ID: 100, Name: "TestCo"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)

	t.Setenv("BASECAMP_TOKEN", "bc_at_resolver_test")
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	// Empty credential store
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "basecamp"), 0700))

	cfg := &config.Config{BaseURL: server.URL}
	authMgr := auth.NewManager(cfg, nil)

	sdkCfg := &basecamp.Config{BaseURL: server.URL}
	sdkClient := basecamp.NewClient(sdkCfg, accountTestTokenProvider{token: "bc_at_resolver_test"},
		basecamp.WithMaxRetries(0),
	)

	r := New(sdkClient, authMgr, cfg,
		WithFlags(&Flags{Agent: true}), // Disable interactive prompts
	)

	accounts, err := r.ListAccounts(context.Background())
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	assert.Equal(t, int64(100), accounts[0].ID)
	assert.Equal(t, "TestCo", accounts[0].Name)
}

// TestFetchAccounts_LaunchpadToken verifies that the account resolver routes
// to the Launchpad endpoint when BASECAMP_TOKEN lacks the bc_at_ prefix.
func TestFetchAccounts_LaunchpadToken(t *testing.T) {
	// Start mock Launchpad server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/authorization.json", r.URL.Path)
		resp := basecamp.AuthorizationInfo{
			Identity: basecamp.Identity{ID: 2, FirstName: "LP"},
			Accounts: []basecamp.AuthorizedAccount{
				{Product: "bc3", ID: 200, Name: "LP Corp"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)

	t.Setenv("BASECAMP_TOKEN", "some-launchpad-token")
	t.Setenv("BASECAMP_LAUNCHPAD_URL", server.URL)
	t.Setenv("BASECAMP_NO_KEYRING", "1")

	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "basecamp"), 0700))

	cfg := &config.Config{BaseURL: "https://3.basecampapi.com"}
	authMgr := auth.NewManager(cfg, nil)

	sdkCfg := &basecamp.Config{}
	sdkClient := basecamp.NewClient(sdkCfg, accountTestTokenProvider{token: "some-launchpad-token"},
		basecamp.WithMaxRetries(0),
	)

	r := New(sdkClient, authMgr, cfg,
		WithFlags(&Flags{Agent: true}),
	)

	accounts, err := r.ListAccounts(context.Background())
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	assert.Equal(t, int64(200), accounts[0].ID)
}
