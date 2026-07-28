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

// swapStderrIsTerminal replaces the interactivity seam for the test.
func swapStderrIsTerminal(t *testing.T, interactive bool) {
	t.Helper()
	orig := stderrIsTerminal
	stderrIsTerminal = func() bool { return interactive }
	t.Cleanup(func() { stderrIsTerminal = orig })
}

// ensureOptions constructs a store's credstore through the seams and returns
// the options it was built with.
func ensureOptions(t *testing.T, interactive bool) credstore.StoreOptions {
	t.Helper()
	t.Setenv("BASECAMP_NO_KEYRING", "1") // keep the delegated construction off the real keyring

	var got credstore.StoreOptions
	swapNewCredStore(t, func(opts credstore.StoreOptions) *credstore.Store {
		got = opts
		return credstore.NewStore(opts)
	})
	swapStderrIsTerminal(t, interactive)

	NewStore(t.TempDir()).UsingKeyring()
	return got
}

// Headless sessions can never answer a keychain unlock prompt, so the probe
// must be bounded there — the #568 incident class. Interactive sessions keep
// the unbounded probe so a legitimate unlock prompt is never cut off
// mid-answer (which would silently degrade to plaintext file storage).
func TestEnsureBoundsProbeOnlyWhenHeadless(t *testing.T) {
	assert.Equal(t, headlessProbeTimeout, ensureOptions(t, false).ProbeTimeout)
	assert.Zero(t, ensureOptions(t, true).ProbeTimeout)
}
