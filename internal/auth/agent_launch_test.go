package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

const testSessionID = "0123456789abcdef0123456789abcdef"

// liveAgentCredential is an agent profile whose shared token is comfortably
// inside its lifetime, so anything that mints did so on purpose.
func liveAgentCredential(tokenEndpoint string) *Credentials {
	creds := agentCredential(tokenEndpoint, time.Now().Add(time.Hour))
	creds.AccessToken = "shared-cached"
	return creds
}

// TestSessionTokenMintsFreshAndLeavesTheSharedTokenAlone: a session asks for
// its own token every time, with its id and label on the mint, and the
// profile's cached token is neither served nor overwritten.
func TestSessionTokenMintsFreshAndLeavesTheSharedTokenAlone(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(call int) (int, string) {
		return http.StatusOK, `{"access_token":"session-` + string(rune('1'+call)) + `","token_type":"Bearer","expires_in":3600,"scope":"full",` +
			`"launch_id":"` + testSessionID + `","launch_label":"coworker@box"}`
	}

	m := newDeviceTestManager(t, as.srv.URL)
	key := storeAgent(t, m, liveAgentCredential(as.srv.URL+"/oauth/token"))
	before, err := m.store.Load(key)
	require.NoError(t, err)

	first, err := m.SessionAccessToken(context.Background(), testSessionID, "coworker@box")
	require.NoError(t, err)
	assert.Equal(t, "session-1", first)

	second, err := m.SessionAccessToken(context.Background(), testSessionID, "coworker@box")
	require.NoError(t, err)
	assert.Equal(t, "session-2", second, "a session token is minted fresh, never cached")

	calls := as.tokenCalls()
	require.Len(t, calls, 2)
	assert.Equal(t, "client_credentials", calls[0].Get("grant_type"))
	assert.Equal(t, testSessionID, calls[0].Get("launch_id"))
	assert.Equal(t, "coworker@box", calls[0].Get("launch_label"))
	assert.Equal(t, AgentResourceURNPrefix+"42", calls[0].Get("resource"))

	after, err := m.store.Load(key)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a session mint must not touch the stored credential")

	shared, err := m.StoredAccessToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "shared-cached", shared)
	assert.Len(t, as.tokenCalls(), 2, "the shared token was still live and needed no mint")
}

// TestSessionTokenWithoutLabelSendsNoLabel: an id alone is a session; the
// label is optional and is not sent empty.
func TestSessionTokenWithoutLabelSendsNoLabel(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(int) (int, string) {
		return http.StatusOK, `{"access_token":"session","token_type":"Bearer","expires_in":3600,"launch_id":"` + testSessionID + `"}`
	}
	m := newDeviceTestManager(t, as.srv.URL)
	storeAgent(t, m, liveAgentCredential(as.srv.URL+"/oauth/token"))

	token, err := m.SessionAccessToken(context.Background(), testSessionID, "")
	require.NoError(t, err)
	assert.Equal(t, "session", token)

	calls := as.tokenCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, testSessionID, calls[0].Get("launch_id"))
	_, sent := calls[0]["launch_label"]
	assert.False(t, sent, "an absent label must not be sent as an empty one")
}

// TestSessionTokenFromAServerThatIgnoresTheSessionFailsClosed: a Basecamp
// that predates session attribution answers with an ordinary token. That
// token is not bound to the session, so it is discarded, not printed.
func TestSessionTokenFromAServerThatIgnoresTheSessionFailsClosed(t *testing.T) {
	for name, body := range map[string]string{
		"no echo":         `{"access_token":"unbound","token_type":"Bearer","expires_in":3600}`,
		"other id":        `{"access_token":"unbound","token_type":"Bearer","expires_in":3600,"launch_id":"ffffffffffffffffffffffffffffffff","launch_label":"coworker@box"}`,
		"other label":     `{"access_token":"unbound","token_type":"Bearer","expires_in":3600,"launch_id":"` + testSessionID + `","launch_label":"someone-else"}`,
		"label not given": `{"access_token":"unbound","token_type":"Bearer","expires_in":3600,"launch_id":"` + testSessionID + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			as := startDeviceAS(t)
			as.token = func(int) (int, string) { return http.StatusOK, body }
			m := newDeviceTestManager(t, as.srv.URL)
			key := storeAgent(t, m, liveAgentCredential(as.srv.URL+"/oauth/token"))

			token, err := m.SessionAccessToken(context.Background(), testSessionID, "coworker@box")
			require.Error(t, err)
			assert.Empty(t, token)
			assert.NotContains(t, err.Error(), "unbound", "the unbound token must not leak into the error")
			assert.Contains(t, err.Error(), testSessionID)
			assert.Contains(t, err.Error(), "predates agent session attribution")

			stored, loadErr := m.store.Load(key)
			require.NoError(t, loadErr)
			assert.Equal(t, "shared-cached", stored.AccessToken)
			assert.Nil(t, stored.MintHold, "an unattributing server is no verdict on the credential")
		})
	}
}

// TestSessionTokenRefusedForItsSessionNamesItAndHoldsNothing: a refusal of
// the session's own parameters is about this request, not the credential,
// so nothing is held against the profile.
func TestSessionTokenRefusedForItsSessionNamesItAndHoldsNothing(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(int) (int, string) {
		return http.StatusBadRequest, `{"error":"invalid_request","error_description":"launch_label is too long"}`
	}
	m := newDeviceTestManager(t, as.srv.URL)
	key := storeAgent(t, m, liveAgentCredential(as.srv.URL+"/oauth/token"))

	_, err := m.SessionAccessToken(context.Background(), testSessionID, "coworker@box")
	require.Error(t, err)
	assert.Contains(t, err.Error(), testSessionID)
	assert.Contains(t, err.Error(), "invalid_request")

	stored, loadErr := m.store.Load(key)
	require.NoError(t, loadErr)
	assert.Nil(t, stored.MintHold)
	assert.Equal(t, "shared-cached", stored.AccessToken)
}

// TestSessionTokenRefusalOfTheCredentialIsHeld: a session mint presents the
// same client secret the shared path does, so the token endpoint's verdict
// on that secret is remembered for both — a dozen sandbox launches must not
// each keep presenting a dead secret.
func TestSessionTokenRefusalOfTheCredentialIsHeld(t *testing.T) {
	as := startDeviceAS(t)
	as.token = func(int) (int, string) {
		return http.StatusUnauthorized, `{"error":"invalid_client"}`
	}
	m := newDeviceTestManager(t, as.srv.URL)
	key := storeAgent(t, m, liveAgentCredential(as.srv.URL+"/oauth/token"))

	_, err := m.SessionAccessToken(context.Background(), testSessionID, "coworker@box")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAgentCredentialRefused)

	stored, loadErr := m.store.Load(key)
	require.NoError(t, loadErr)
	require.NotNil(t, stored.MintHold)
	assert.Equal(t, mintHoldRefused, stored.MintHold.Kind)

	_, err = m.SessionAccessToken(context.Background(), testSessionID, "coworker@box")
	require.Error(t, err)
	assert.ErrorIs(t, err, errMintHeld)
	assert.Len(t, as.tokenCalls(), 1, "a held credential was presented again")
}

// TestSessionTokenNeedsAnAgentProfile: only an agent credential mints for
// itself; a person's login has no session to attribute and is not handed
// out in place of one.
func TestSessionTokenNeedsAnAgentProfile(t *testing.T) {
	as := startDeviceAS(t)
	m := newDeviceTestManager(t, as.srv.URL)
	storeAgent(t, m, &Credentials{AccessToken: "person-token", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour).Unix()})

	token, err := m.SessionAccessToken(context.Background(), testSessionID, "coworker@box")
	require.Error(t, err)
	assert.Empty(t, token)
	assert.Equal(t, output.CodeUsage, output.AsError(err).Code)
	assert.Empty(t, as.tokenCalls())
}

// TestSessionTokenValidatesItsIdentityBeforeSending: the same rules the
// server holds a session to are checked before anything goes out.
func TestSessionTokenValidatesItsIdentityBeforeSending(t *testing.T) {
	for name, tc := range map[string]struct{ id, label string }{
		"empty id":             {"", "coworker@box"},
		"short id":             {"0123456789abcdef", ""},
		"uppercase id":         {strings.ToUpper(testSessionID), ""},
		"non-hex id":           {"0123456789abcdef0123456789abcdeg", ""},
		"label too long":       {testSessionID, strings.Repeat("x", 101)},
		"blank label":          {testSessionID, "   "},
		"control in label":     {testSessionID, "coworker\nbox"},
		"bidi override":        {testSessionID, "coworker\u202ebox"},
		"line separator":       {testSessionID, "coworker\u2028box"},
		"invalid utf-8 label":  {testSessionID, "coworker\xffbox"},
		"unassigned codepoint": {testSessionID, "coworker\U000E0080box"},
	} {
		t.Run(name, func(t *testing.T) {
			as := startDeviceAS(t)
			m := newDeviceTestManager(t, as.srv.URL)
			storeAgent(t, m, liveAgentCredential(as.srv.URL+"/oauth/token"))

			_, err := m.SessionAccessToken(context.Background(), tc.id, tc.label)
			require.Error(t, err)
			assert.Equal(t, output.CodeUsage, output.AsError(err).Code)
			assert.Empty(t, as.tokenCalls())
		})
	}

	assert.NoError(t, ValidateSession(testSessionID, strings.Repeat("é", 100)), "100 characters, not bytes")
	assert.NoError(t, ValidateSession(testSessionID, "claude · coworker"))
}
