package commands

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// TestAuthTokenStoredKeepsTheLoginHintUnderEnvToken: auth token --stored
// deliberately ignores BASECAMP_TOKEN, so its stored-credential failure
// must keep the login remedy rather than be rewritten to talk about the
// environment token.
func TestAuthTokenStoredKeepsTheLoginHintUnderEnvToken(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "bc_at_env")
	app, out := setupProjectsMockApp(t, unauthorizedTransport{})

	err := executeCommand(NewAuthCmd(), app, "token", "--stored")
	require.Error(t, err)
	assert.Equal(t, output.CodeAuth, output.AsError(err).Code)
	require.NoError(t, app.Err(err))

	var envelope struct {
		Hint string `json:"hint"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope), out.String())
	assert.Equal(t, "Run: basecamp auth login", envelope.Hint)
}
