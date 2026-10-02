package fakebasecamp_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/connector/fakebasecamp"
)

// The connection ceremony, run by the CLI's own auth package against the
// fake: the intake, the poll, the handover of a freshly rotated secret, and
// the client_credentials mint that proves it.
func TestAgentConnectionCeremony(t *testing.T) {
	s := fakebasecamp.Start(t, fakebasecamp.DefaultWorld())
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("BASECAMP_TOKEN", "")
	t.Setenv("BASECAMP_OAUTH_ISSUER", s.URL())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	connect := func() (*auth.Manager, *auth.AgentConnectResult) {
		t.Helper()
		mgr := auth.NewManager(&config.Config{BaseURL: s.URL(), ActiveProfile: "agent", Sources: map[string]string{}}, nil)
		mgr.SetStore(auth.NewStore(t.TempDir()))
		result, err := mgr.ConnectAgent(bounded(t), auth.AgentConnectOptions{DeviceName: "build-box", NoBrowser: true})
		require.NoError(t, err)
		return mgr, result
	}
	mgr, result := connect()
	assert.Equal(t, fakebasecamp.AgentClientID, result.ClientID)
	assert.Equal(t, "999", result.AccountID)
	assert.Equal(t, fakebasecamp.ScopeFull, result.Scope)
	assert.Equal(t, 1, s.Count(fakebasecamp.RouteAgentConnections))
	assert.Equal(t, 1, s.Count(fakebasecamp.RouteToken), "one mint proves the handover")

	var secret string
	s.View(func(w *fakebasecamp.World) { secret = w.Agents[fakebasecamp.AgentClientID].Secret })
	assert.NotEqual(t, fakebasecamp.AgentSecret, secret, "the handover rotates the secret")
	creds, err := mgr.GetStore().Load("profile:agent")
	require.NoError(t, err)
	assert.Equal(t, secret, creds.ClientSecret)

	// The stored token is the agent's.
	token, err := mgr.AccessToken(bounded(t))
	require.NoError(t, err)
	me, err := accountClient(s, token).People().Me(bounded(t))
	require.NoError(t, err)
	assert.Equal(t, fakebasecamp.AgentID, me.ID)
	assert.Equal(t, "Agent", me.PersonableType)

	// Connecting again elsewhere rotates the secret once more, and the one
	// this computer holds stops minting, as Basecamp refuses it.
	connect()
	mint := func(secret string) (int, string) {
		form := url.Values{"grant_type": {"client_credentials"}, "client_id": {fakebasecamp.AgentClientID}, "client_secret": {secret}}
		status, _, body := do(t, s, http.MethodPost, "/oauth/tokens", "", form.Encode())
		return status, body
	}
	status, body := mint(secret)
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.JSONEq(t, `{"error":"invalid_client"}`, body)

	// A spent code is not handed over twice.
	poll := s.Requests()
	var code string
	for _, r := range poll {
		if r.Route == fakebasecamp.RouteAgentConnectionTokens {
			code = r.Form.Get("device_code")
		}
	}
	require.NotEmpty(t, code)
	status, _, body = do(t, s, http.MethodPost, "/oauth/agent_connection_tokens", "", url.Values{"device_code": {code}}.Encode())
	assert.Equal(t, http.StatusBadRequest, status)
	assert.JSONEq(t, `{"error":"invalid_grant"}`, body)
}

// The approval is the world's to say: a read-only approval provisions the
// client read-only, and its tokens say so.
func TestAgentConnectionHandsOverTheApprovedScope(t *testing.T) {
	s := fakebasecamp.Start(t, fakebasecamp.DefaultWorld())
	s.Update(func(w *fakebasecamp.World) { w.Connection.Scope = fakebasecamp.ScopeRead })
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("BASECAMP_TOKEN", "")
	t.Setenv("BASECAMP_OAUTH_ISSUER", s.URL())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	mgr := auth.NewManager(&config.Config{BaseURL: s.URL(), ActiveProfile: "agent", Sources: map[string]string{}}, nil)
	mgr.SetStore(auth.NewStore(t.TempDir()))
	result, err := mgr.ConnectAgent(bounded(t), auth.AgentConnectOptions{DeviceName: "build-box", NoBrowser: true})
	require.NoError(t, err)
	assert.Equal(t, fakebasecamp.ScopeRead, result.Scope)
	s.View(func(w *fakebasecamp.World) {
		assert.Equal(t, fakebasecamp.ScopeRead, w.Agents[fakebasecamp.AgentClientID].Scope)
	})
}

// mint asks the fake for a client_credentials token for the default agent's
// client with secret, and returns the status and the decoded body.
func mint(t *testing.T, s *fakebasecamp.Server, secret string) (int, map[string]any) {
	t.Helper()
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {fakebasecamp.AgentClientID}, "client_secret": {secret}}
	status, _, body := do(t, s, http.MethodPost, "/oauth/tokens", "", form.Encode())
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &decoded), body)
	return status, decoded
}

// A token says it lasts an hour, as Basecamp's do, unless the client is
// set to mint shorter ones.
func TestTokensSayHowLongTheyLast(t *testing.T) {
	s := fakebasecamp.Start(t, fakebasecamp.DefaultWorld())
	status, body := mint(t, s, fakebasecamp.AgentSecret)
	require.Equal(t, http.StatusOK, status)
	assert.InDelta(t, 3600, body["expires_in"], 0)

	s.Update(func(w *fakebasecamp.World) { w.Agents[fakebasecamp.AgentClientID].TokenLifetime = 2 * time.Second })
	_, body = mint(t, s, fakebasecamp.AgentSecret)
	assert.InDelta(t, 2, body["expires_in"], 0)

	s.Update(func(w *fakebasecamp.World) { w.Agents[fakebasecamp.AgentClientID].TokenLifetime = time.Millisecond })
	_, body = mint(t, s, fakebasecamp.AgentSecret)
	assert.InDelta(t, 1, body["expires_in"], 0, "never less than a second")
}

// Disconnecting an agent revokes every token its client minted and clears
// its secret, and leaves everyone else's tokens alone.
func TestDisconnectRevokesTheAgentsTokensAndSecret(t *testing.T) {
	s := fakebasecamp.Start(t, fakebasecamp.DefaultWorld())
	status, body := mint(t, s, fakebasecamp.AgentSecret)
	require.Equal(t, http.StatusOK, status)
	token, _ := body["access_token"].(string)
	_, err := accountClient(s, token).People().Me(bounded(t))
	require.NoError(t, err)

	s.Disconnect(fakebasecamp.AgentClientID)

	_, err = accountClient(s, token).People().Me(bounded(t))
	var apiErr *basecamp.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusUnauthorized, apiErr.HTTPStatus)
	status, body = mint(t, s, fakebasecamp.AgentSecret)
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.Equal(t, "invalid_client", body["error"])
	_, err = accountClient(s, fakebasecamp.OperatorToken).People().Me(bounded(t))
	assert.NoError(t, err, "the operator's own login stands")
}
