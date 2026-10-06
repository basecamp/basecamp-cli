package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// pretendContainer makes this process look like it runs in a container, or
// not, for one test.
func pretendContainer(t *testing.T, in bool) {
	t.Helper()
	was := inContainer
	inContainer = func() bool { return in }
	t.Cleanup(func() { inContainer = was })
}

// A login refused as revoked — "Token reuse detected" the first time a
// replaced refresh token comes back, "Token has been revoked" every time
// after — is almost always one login used from two places: two installs or
// containers holding copies of one credentials.json, where the second to
// refresh presents a token the first already replaced, and Basecamp signs
// the whole login out as stolen. Saying so is what stops it happening again.
func TestRefresh_RevokedLoginSaysItWasUsedFromTwoPlaces(t *testing.T) {
	pretendContainer(t, false)
	for _, desc := range []string{"Token has been revoked", "Token reuse detected, session terminated", "Token has been revoked\n"} {
		t.Run(desc, func(t *testing.T) {
			m, key := refreshRefusedBy(t, http.StatusBadRequest, fmt.Sprintf(`{"error":"invalid_grant","error_description":%q}`, desc))

			err := m.Refresh(context.Background())
			var cliErr *output.Error
			require.ErrorAs(t, err, &cliErr)
			assert.Equal(t, output.CodeAuth, cliErr.Code)
			assert.Contains(t, cliErr.Message, "This login was revoked, probably because it was used from more than one place")
			assert.Contains(t, cliErr.Message, "("+strings.TrimSpace(desc)+")")
			assert.Equal(t, "Run: basecamp auth login -P work", cliErr.Hint)
			_, loadErr := m.store.Load(key)
			assert.Error(t, loadErr, "the revoked login is still forgotten")
		})
	}
}

// Any other invalid_grant keeps the general message: an expired login was
// not used twice.
func TestRefresh_ExpiredLoginKeepsTheGeneralMessage(t *testing.T) {
	pretendContainer(t, false)
	m, _ := refreshRefusedBy(t, http.StatusBadRequest, `{"error":"invalid_grant","error_description":"Token has expired"}`)

	err := m.Refresh(context.Background())
	var cliErr *output.Error
	require.ErrorAs(t, err, &cliErr)
	assert.Equal(t, "Your session has expired or was revoked (Token has expired)", cliErr.Message)
}

// In a container the revoked message says what to do instead of copying the
// login, since a container is where the copy usually came from.
func TestRefresh_RevokedLoginInAContainerSaysWhatToDoInstead(t *testing.T) {
	pretendContainer(t, true)
	m, _ := refreshRefusedBy(t, http.StatusBadRequest, `{"error":"invalid_grant","error_description":"Token has been revoked"}`)
	m.Warnf = func(string, ...any) {}

	err := m.Refresh(context.Background())
	var cliErr *output.Error
	require.ErrorAs(t, err, &cliErr)
	assert.Contains(t, cliErr.Message, "This login was revoked")
	assert.Contains(t, cliErr.Message, containerLoginAdvice)
}

// refreshableIn is a token endpoint that rotates every refresh, and a Manager
// whose BC5 login refreshes against it, collecting what it warns.
func refreshableIn(t *testing.T) (*Manager, string, func() []string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"new-tok","refresh_token":"new-ref","expires_in":3600}`)
	}))
	t.Cleanup(srv.Close)

	var mu sync.Mutex
	var warned []string
	m := &Manager{cfg: config.Default(), httpClient: srv.Client(), store: newTestStore(t, t.TempDir())}
	m.Warnf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		warned = append(warned, fmt.Sprintf(format, args...))
	}
	key := m.credentialKey()
	require.NoError(t, m.store.Save(key, &Credentials{
		AccessToken: "old-tok", RefreshToken: "old-ref", OAuthType: "bc5",
		TokenEndpoint: srv.URL + "/oauth/tokens", ExpiresAt: time.Now().Add(-time.Hour).Unix(),
	}))
	return m, key, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), warned...)
	}
}

// A login that refreshes, used inside a container, gets one notice: the
// first refresh there says not to copy it into other containers, and what to
// do instead. Once — a scheduled job must not print it every run.
func TestRefresh_InAContainerNoticesOnce(t *testing.T) {
	pretendContainer(t, true)
	m, key, warned := refreshableIn(t)

	require.NoError(t, m.Refresh(context.Background()))
	got := warned()
	require.Len(t, got, 1)
	assert.Contains(t, got[0], "copied")
	assert.Contains(t, got[0], containerLoginAdvice)

	creds, err := m.store.Load(key)
	require.NoError(t, err)
	creds.ExpiresAt = time.Now().Add(-time.Hour).Unix()
	require.NoError(t, m.store.Save(key, creds))
	require.NoError(t, m.Refresh(context.Background()))
	assert.Len(t, warned(), 1, "the notice is given once")
}

// Outside a container there is nothing to say.
func TestRefresh_OutsideAContainerSaysNothing(t *testing.T) {
	pretendContainer(t, false)
	m, _, warned := refreshableIn(t)

	require.NoError(t, m.Refresh(context.Background()))
	assert.Empty(t, warned())
}

// The advice names both ways out that need no copy: one shared directory,
// or a token that never rotates.
func TestContainerLoginAdvice(t *testing.T) {
	assert.True(t, strings.Contains(containerLoginAdvice, "BASECAMP_TOKEN"), containerLoginAdvice)
	assert.True(t, strings.Contains(containerLoginAdvice, "volume"), containerLoginAdvice)
	assert.True(t, strings.Contains(containerLoginAdvice, "BASECAMP_NO_KEYRING=1"), "a keyring would keep each container's own copy")
	assert.True(t, strings.Contains(containerLoginAdvice, "On one host"), "a shared volume is only safe where its flock is")
}
