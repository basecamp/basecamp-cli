package workspace

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
)

// newSessionTestConfig builds an isolated config for NewSession tests.
func newSessionTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.CacheDir = t.TempDir()
	return cfg
}

// NewSession is the sole consumer of llm_endpoint (via summarize.DetectProvider),
// so it must fail closed on an endpoint that would leak llm_api_key — with the
// same repair-hint error the root command used to emit at startup.
func TestNewSessionRejectsBadLLMEndpoint(t *testing.T) {
	cfg := newSessionTestConfig(t)
	cfg.LLMProvider = "openai"
	cfg.LLMAPIKey = "secret"
	cfg.LLMEndpoint = "http://remote-host:1234"
	cfg.Sources["llm_endpoint"] = "env"

	session, err := NewSession(appctx.NewApp(cfg))

	require.Error(t, err)
	assert.Nil(t, session)
	assert.Contains(t, err.Error(), "llm_endpoint (env)")
	assert.Contains(t, err.Error(), "Fix with: basecamp config unset llm_endpoint")
}

// A malformed endpoint for a provider that never consumes it must not block
// the session — mirroring the provider exemption in summarize.ValidateEndpoint.
// anthropic ignores the custom endpoint entirely, so a stale malformed value
// must not lock the LLM path out. (ollama, by contrast, does consume the
// endpoint and so is not exempt — see TestValidateEndpoint.)
func TestNewSessionAllowsUnconsumedLLMEndpoint(t *testing.T) {
	cfg := newSessionTestConfig(t)
	cfg.LLMProvider = "anthropic"
	cfg.LLMAPIKey = "secret"
	cfg.LLMEndpoint = "file:///etc/passwd"

	session, err := NewSession(appctx.NewApp(cfg))

	require.NoError(t, err)
	require.NotNil(t, session)
	session.Shutdown()
}
