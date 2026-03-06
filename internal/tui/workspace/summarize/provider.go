package summarize

import (
	"context"
	"net/http"
	"time"
)

// Provider generates summaries via an LLM or similar service.
type Provider interface {
	Complete(ctx context.Context, prompt string, maxTokens int) (string, error)
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
	case "":
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
		return NewOllamaProvider("http://localhost:11434", "")
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
