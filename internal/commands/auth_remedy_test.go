package commands

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

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

// TestAuthRemedyKeepsGuidanceAppendedToTheDefaultHint: a command that
// appended its own note to the default hint (a partial reorder's rerun
// guidance) keeps that note; only the login prefix is replaced.
func TestAuthRemedyKeepsGuidanceAppendedToTheDefaultHint(t *testing.T) {
	t.Setenv("BASECAMP_TOKEN", "")
	app, out := setupProjectsMockApp(t, unauthorizedTransport{})
	app.Config.ActiveProfile = "work"

	sdkErr := &basecamp.Error{Code: basecamp.CodeAuth, Message: "authentication required", HTTPStatus: 401}
	composed := &output.Error{
		Code:    output.CodeAuth,
		Message: "Reordered 1 of 3 todolists; failed at #2: authentication required",
		Hint:    output.DefaultAuthHint + " Rerun the whole command once the cause is fixed.",
		Cause:   sdkErr,
	}
	require.NoError(t, app.Err(composed))

	var envelope struct {
		Hint string `json:"hint"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &envelope), out.String())
	assert.Equal(t, "Run: basecamp auth login -P work Rerun the whole command once the cause is fixed.", envelope.Hint)
}
