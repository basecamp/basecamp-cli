package summarize

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/hostutil"
)

// Provider generates summaries via an LLM or similar service.
type Provider interface {
	Complete(ctx context.Context, prompt string, maxTokens int) (string, error)
}

// ValidateEndpoint validates llm_endpoint only for providers that could
// send llm_api_key to it: the scheme/host structural check plus an HTTPS gate
// when a credential is present. It lives here, next to DetectProvider, because
// this package owns the provider→endpoint routing it reasons about — and it
// must run before DetectProvider on every LLM path, since config keeps file-
// and env-sourced endpoints even if malformed. llm_endpoint is consumed ONLY
// on this path, so callers elsewhere (e.g. root command setup) must not gate
// unrelated commands on it.
//
// The provider exemption runs FIRST, before any structural check. Providers
// that neither send llm_api_key to llm_endpoint nor consume the endpoint at
// all skip ALL validation: "apple", "none", and "disabled" transmit no key
// and ignore the endpoint; "anthropic" ignores the custom endpoint entirely
// (NewAnthropicProvider takes no endpoint and always calls
// https://api.anthropic.com); and "auto"/"" auto-detect only Apple or a
// hardcoded localhost Ollama (autoDetectProvider never receives the endpoint
// or key). Rejecting a malformed endpoint for these providers would block the
// LLM path over a value that is never consumed — e.g. a stale llm_endpoint
// left over from a previous provider — a repair lockout with no security
// payoff.
//
// "ollama" is credential-less but DOES consume llm_endpoint (OllamaProvider
// posts to endpoint+"/api/generate"), so it does NOT skip validation: it falls
// through to the structural IsHTTPURL check, which fails closed on a malformed
// endpoint (file://, hostless, non-http(s)) at config time rather than deep in
// the summarizer at use time. But ollama IS exempt from the HTTPS/apiKey gate:
// OllamaProvider never transmits llm_api_key (DetectProvider calls
// NewOllamaProvider(endpoint, model), dropping the key), yet ValidateEndpoint
// receives the RAW Config.LLMAPIKey, which may be a stale value left from a
// previous provider. Gating ollama on that key would reject a plain-HTTP LAN
// Ollama (http://192.168.x.x:11434) and block the TUI over a secret that is
// never sent, so the apiKey branch below explicitly skips ollama — it stays
// endpoint-consuming but credential-less, subject only to the structural check.
//
// For "openai" (which sends the key to the configured endpoint) and any
// unknown provider name (fail closed on names we can't reason about), the
// structural check rejects non-http(s)/hostless endpoints, and when an
// llm_api_key is present the endpoint must also pass RequireSecureURL so the
// secret can't leak in cleartext to a non-localhost http:// endpoint.
// RequireSecureURL only blocks http:// for non-localhost — it would let
// file://, ssh:// etc. through — so the IsHTTPURL scheme/host check runs even
// without a key. An empty endpoint is a no-op.
func ValidateEndpoint(endpoint, provider, apiKey string) error {
	if endpoint == "" {
		return nil
	}
	switch provider {
	case "apple", "none", "disabled", "anthropic", "auto", "":
		// These providers never send the key to llm_endpoint and never consume
		// it — apple/none/disabled are credential-less and ignore the endpoint,
		// anthropic ignores the custom endpoint, and auto/"" detect only local
		// providers without endpoint or key — so skip all endpoint validation.
		return nil
	}
	if !config.IsHTTPURL(endpoint) {
		return fmt.Errorf("must be an http:// or https:// URL with a host")
	}
	if apiKey != "" && provider != "ollama" {
		return hostutil.RequireSecureURL(endpoint)
	}
	return nil
}

// DetectProvider returns the best available provider based on configuration.
func DetectProvider(providerName, endpoint, apiKey, model string) Provider {
	switch providerName {
	case "anthropic":
		return NewAnthropicProvider(apiKey, model)
	case "openai":
		return NewOpenAIProvider(endpoint, apiKey, model)
	case "ollama":
		return NewOllamaProvider(endpoint, model)
	case "apple":
		if IsAppleMLAvailable() {
			return NewAppleProvider()
		}
		return nil
	case "none", "disabled":
		return nil
	case "", "auto":
		return autoDetectProvider()
	default:
		// Unknown provider name — fail closed rather than silently
		// falling through to auto-detect.
		return nil
	}
}

// autoDetectProvider tries providers in priority order: Apple -> Ollama -> nil.
func autoDetectProvider() Provider {
	if IsAppleMLAvailable() {
		return NewAppleProvider()
	}
	if ollamaReachable() {
		return NewOllamaProvider("http://localhost:11434", "llama3.2")
	}
	return nil
}

// ollamaReachable does a quick health check against the local Ollama server.
func ollamaReachable() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost:11434/api/tags", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
