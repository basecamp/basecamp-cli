package summarize

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestValidateEndpoint exercises ValidateEndpoint, the fail-closed gate that
// runs at llm_endpoint's sole point of consumption (workspace.NewSession,
// just before DetectProvider). Providers that neither send llm_api_key to
// llm_endpoint nor consume the endpoint (apple, none, disabled, anthropic,
// auto, "") skip ALL validation — including the structural scheme/host check —
// so a stale or malformed endpoint they never consume can't lock the LLM path
// out. ollama is credential-less but DOES consume the endpoint, so it runs the
// structural check (rejecting malformed endpoints) while its always-empty key
// keeps a plain-HTTP LAN endpoint valid. For openai (which sends the key to
// the endpoint) and unknown provider names, the structural check runs, plus
// the HTTPS gate when a key is present.
func TestValidateEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		provider string
		apiKey   string
		wantOK   bool
	}{
		// Empty provider auto-detects local-only providers and never consumes
		// the endpoint, so even structurally malformed values are accepted.
		{"file scheme accepted for empty provider", "file:///etc/passwd", "", "", true},
		{"ssh scheme accepted for empty provider", "ssh://host", "", "", true},
		{"hostless https accepted for empty provider", "https:example.com", "", "", true},
		{"http remote with credential accepted (empty provider auto-detects local only)", "http://remote-host:1234", "", "secret", true},
		{"http remote without credential accepted", "http://remote-host:1234", "", "", true},
		{"https remote accepted", "https://remote-host", "", "", true},
		{"https remote with credential accepted", "https://remote-host", "", "secret", true},
		{"localhost with credential accepted", "http://localhost:11434", "", "secret", true},
		{"empty endpoint no-op", "", "", "", true},
		{"empty endpoint with key no-op", "", "", "secret", true},
		// ollama is credential-less but consumes the endpoint, so it runs the
		// structural check. A plain-HTTP LAN endpoint is valid (empty key never
		// trips the HTTPS gate); malformed endpoints fail closed at config time.
		{"ollama plain-http LAN accepted", "http://192.168.1.5:11434", "ollama", "", true},
		// A stale llm_api_key from a prior provider must NOT gate ollama: it never
		// transmits the key, so the plain-HTTP LAN endpoint stays valid and the TUI
		// is not blocked over a secret that is never sent.
		{"ollama plain-http LAN with stale key accepted", "http://192.168.1.10:11434", "ollama", "stale-key", true},
		{"ollama empty endpoint no-op", "", "ollama", "", true},
		{"ollama file scheme rejected", "file:///x", "ollama", "", false},
		{"ollama hostless https rejected", "https:example.com", "ollama", "", false},
		{"ollama hostless http rejected", "http://:11434", "ollama", "", false},
		{"ollama ssh scheme rejected", "ssh://host", "ollama", "", false},
		// Anthropic ignores the custom endpoint, so the key never reaches it.
		{"anthropic remote http with key accepted", "http://remote:1234", "anthropic", "secret", true},
		// Auto-detection never passes the endpoint or key to a provider.
		{"auto remote http with key accepted", "http://remote:1234", "auto", "secret", true},
		// Endpoint-unused providers skip the structural check too: a malformed
		// leftover value must not block a path that never consumes it.
		{"anthropic file scheme accepted", "file:///etc/passwd", "anthropic", "secret", true},
		{"disabled hostless https accepted", "https:example.com", "disabled", "secret", true},
		{"auto hostless https accepted", "https:example.com", "auto", "", true},
		// openai sends the key to the endpoint: key still gates remote http.
		{"openai remote http with key rejected", "http://remote:1234", "openai", "secret", false},
		// openai runs the structural check regardless of key presence.
		{"openai hostless https with key rejected", "https:example.com", "openai", "secret", false},
		{"openai file scheme rejected", "file:///etc/passwd", "openai", "", false},
		{"openai https remote with key accepted", "https://remote-host", "openai", "secret", true},
		// Unknown provider names fail closed like openai.
		{"unknown provider hostless https rejected", "https:example.com", "mystery", "", false},
		{"unknown provider remote http with key rejected", "http://remote:1234", "mystery", "secret", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEndpoint(tt.endpoint, tt.provider, tt.apiKey)
			if tt.wantOK {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}
