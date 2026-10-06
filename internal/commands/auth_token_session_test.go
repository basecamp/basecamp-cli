package commands

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/output"
)

const tokenSessionID = "0123456789abcdef0123456789abcdef"

// storeAgentProfile seeds the "agent" profile with a live shared token.
func storeAgentProfile(t *testing.T, store *auth.Store, tokenEndpoint string) {
	t.Helper()
	require.NoError(t, store.Save("profile:agent", &auth.Credentials{
		AccessToken:   "shared-cached",
		OAuthType:     "agent",
		ClientID:      "agent-client",
		ClientSecret:  "agent-secret",
		TokenEndpoint: tokenEndpoint,
		Scope:         "full",
		ExpiresAt:     time.Now().Add(time.Hour).Unix(),
	}))
}

func TestAuthTokenSessionMintsForTheSession(t *testing.T) {
	as := startAgentAS(t)
	as.token = func() (int, string) {
		return http.StatusOK, `{"access_token":"session-token","token_type":"Bearer","expires_in":3600,"scope":"full",` +
			`"launch_id":"` + tokenSessionID + `","launch_label":"coworker@box"}`
	}
	app, buf := agentLoginApp(t, as, &config.Config{ActiveProfile: "agent"})
	storeAgentProfile(t, app.Auth.GetStore(), as.srv.URL+"/oauth/tokens")
	app.Flags.JSON = true

	err := executeCommand(NewAuthCmd(), app, "token", "--stored", "--session-id", tokenSessionID, "--session-label", "coworker@box")
	require.NoError(t, err)

	var envelope struct {
		Data map[string]string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope), buf.String())
	assert.Equal(t, map[string]string{
		"token":         "session-token",
		"session_id":    tokenSessionID,
		"session_label": "coworker@box",
	}, envelope.Data)

	calls := as.calls()
	require.Len(t, calls, 1)
	assert.Equal(t, tokenSessionID, calls[0].Get("launch_id"))
	assert.Equal(t, "coworker@box", calls[0].Get("launch_label"))
}

func TestAuthTokenWithoutSessionKeepsItsShape(t *testing.T) {
	as := startAgentAS(t)
	app, buf := agentLoginApp(t, as, &config.Config{ActiveProfile: "agent"})
	storeAgentProfile(t, app.Auth.GetStore(), as.srv.URL+"/oauth/tokens")
	app.Flags.JSON = true

	require.NoError(t, executeCommand(NewAuthCmd(), app, "token", "--stored"))

	var envelope struct {
		Data map[string]string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &envelope), buf.String())
	assert.Equal(t, map[string]string{"token": "shared-cached"}, envelope.Data)
	assert.Empty(t, as.calls())
}

func TestAuthTokenSessionFlagUsage(t *testing.T) {
	for name, args := range map[string][]string{
		"label without id":  {"token", "--stored", "--session-label", "coworker@box"},
		"id without stored": {"token", "--session-id", tokenSessionID},
		"malformed id":      {"token", "--stored", "--session-id", "not-hex"},
	} {
		t.Run(name, func(t *testing.T) {
			as := startAgentAS(t)
			app, _ := agentLoginApp(t, as, &config.Config{ActiveProfile: "agent"})
			storeAgentProfile(t, app.Auth.GetStore(), as.srv.URL+"/oauth/tokens")

			err := executeCommand(NewAuthCmd(), app, args...)
			require.Error(t, err)
			assert.Equal(t, output.CodeUsage, output.AsError(err).Code, err.Error())
			assert.Empty(t, as.calls())
		})
	}
}
