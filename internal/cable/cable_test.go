package cable

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Deployed, the cable server has a host of its own and the account's slug is
// the whole path — the same URL the web client is handed.
func TestTheCableURLForADeployedBasecamp(t *testing.T) {
	url, err := URL("https://app.basecamp.com/2914079", "2914079")
	require.NoError(t, err)
	assert.Equal(t, "wss://chat.app.basecamp.com/2914079", url)

	// A BC3-era account's web home gives a BC3-era cable host.
	url, err = URL("https://3.basecamp.com/1234567", "1234567")
	require.NoError(t, err)
	assert.Equal(t, "wss://chat.3.basecamp.com/1234567", url)
}

// A development server has no separate cable host — it mounts /cable on the app
// itself — so the path carries it, port and all.
func TestTheCableURLForADevelopmentServer(t *testing.T) {
	url, err := URL("http://3.basecamp.localhost:3001", "2914079")
	require.NoError(t, err)
	assert.Equal(t, "ws://3.basecamp.localhost:3001/2914079/cable", url)
}

// Only a development server may go unencrypted. A websocket to a deployed
// Basecamp carries the bearer token, so plain ws:// is refused rather than
// quietly downgraded.
func TestPlainWebsocketsAreOnlyForLocalhost(t *testing.T) {
	_, err := URL("http://app.basecamp.com/2914079", "2914079")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unencrypted")
}

// The override is the escape hatch for a deployment whose cable host is not
// chat.<host>, and it is handed back untouched.
func TestTheEnvironmentOverridesTheEndpoint(t *testing.T) {
	t.Setenv(CableURLEnv, "wss://cable.example.com/7654321")

	url, err := URL("https://app.basecamp.com/2914079", "2914079")
	require.NoError(t, err)
	assert.Equal(t, "wss://cable.example.com/7654321", url)
}

// The slug is how the cable server knows which account a connection is for, and
// Basecamp pads it to seven digits.
func TestTheAccountSlug(t *testing.T) {
	slug, err := Slug("2914079")
	require.NoError(t, err)
	assert.Equal(t, "2914079", slug)

	slug, err = Slug("999")
	require.NoError(t, err)
	assert.Equal(t, "0000999", slug)

	// An id longer than seven digits keeps its own length.
	slug, err = Slug("123456789")
	require.NoError(t, err)
	assert.Equal(t, "123456789", slug)

	for _, notAnID := range []string{"", "  ", "0", "-5", "abc", "29 14079"} {
		_, err := Slug(notAnID)
		assert.Error(t, err, notAnID)
	}
}

func TestAMalformedWebURLIsRefused(t *testing.T) {
	for _, notAURL := range []string{"", "app.basecamp.com", "ftp://app.basecamp.com"} {
		_, err := URL(notAURL, "2914079")
		assert.Error(t, err, notAURL)
	}
}

// Every recording and notification carries the account's own app_url, which is
// how a command learns the web host without being told it.
func TestTheWebHostIsReadFromAnAppURL(t *testing.T) {
	assert.Equal(t, "https://app.basecamp.com",
		AppURLHost("https://app.basecamp.com/2914079/buckets/1/messages/2"))
	assert.Equal(t, "http://3.basecamp.localhost:3001",
		AppURLHost("http://3.basecamp.localhost:3001/2914079/projects/3"))

	// Nothing to read is not an error — the caller has other places to look.
	for _, nothing := range []string{"", "   ", "not a url", "urn:bc:account:2914079"} {
		assert.Empty(t, AppURLHost(nothing), nothing)
	}
}
