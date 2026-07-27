package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/cli/credstore"
)

// swapNewCredStore replaces the credstore constructor seam for the test.
func swapNewCredStore(t *testing.T, fn func(credstore.StoreOptions) *credstore.Store) {
	t.Helper()
	orig := newCredStore
	newCredStore = fn
	t.Cleanup(func() { newCredStore = orig })
}

// TestNewStoreIsLazy proves the constructor never touches credstore:
// credstore.NewStore probes the OS keyring with a write, and that probe can
// block forever on a locked headless keychain. This test pins constructor
// laziness only — the "credential-free commands never probe" conclusion
// additionally rests on those command paths (setup agents, skill install,
// version, help) not performing credential operations, which is audited at
// the command layer, not asserted here.
func TestNewStoreIsLazy(t *testing.T) {
	t.Setenv("BASECAMP_NO_KEYRING", "1") // keep the delegated construction off the real keyring

	calls := 0
	swapNewCredStore(t, func(opts credstore.StoreOptions) *credstore.Store {
		calls++
		return credstore.NewStore(opts)
	})

	store := NewStore(t.TempDir())
	require.NotNil(t, store)
	assert.Equal(t, 0, calls, "NewStore must not construct the credstore")

	_, err := store.Load("https://test.example.com")
	require.Error(t, err, "no credentials saved")
	assert.Equal(t, 1, calls, "first operation constructs the credstore")

	store.UsingKeyring()
	assert.Equal(t, 1, calls, "construction happens once")
}
