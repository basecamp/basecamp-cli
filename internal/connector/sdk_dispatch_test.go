package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

// repliesServer serves n comments by the agent, newest last.
func repliesServer(t *testing.T, n int) *basecamp.AccountClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		comments := make([]map[string]any, 0, n)
		for i := range n {
			comments = append(comments, map[string]any{
				"id":         100 + i,
				"created_at": time.Date(2026, 9, 17, 12, i, 0, 0, time.UTC).Format(time.RFC3339),
				"creator":    map[string]any{"id": adapterAgentID},
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(comments)
	}))
	t.Cleanup(server.Close)
	client := basecamp.NewClient(&basecamp.Config{BaseURL: server.URL}, &basecamp.StaticTokenProvider{Token: "test-token-not-real"})
	return client.ForAccount("2914079")
}

// Copilot r3: a listing the scan limit cut short adopts nothing, because it
// cannot say there is exactly one candidate.
func TestATruncatedReplyListingIsRefused(t *testing.T) {
	replies := SDKReplies{Client: repliesServer(t, AdoptionScanLimit+5), AgentID: adapterAgentID}
	_, err := replies.AgentReplies(context.Background(), adapterBucketID, string(admission.ReplyComment), 10304028989, time.Time{})
	assert.ErrorIs(t, err, ErrRepliesTruncated)

	replies = SDKReplies{Client: repliesServer(t, 3), AgentID: adapterAgentID}
	found, err := replies.AgentReplies(context.Background(), adapterBucketID, string(admission.ReplyComment), 10304028989, time.Time{})
	require.NoError(t, err)
	assert.Len(t, found, 3)
}

// Copilot r4: an agent may add its own environment to the one the connector
// declared, so the server drops what was not declared before it does anything.
func TestAWorkerServerKeepsOnlyTheEnvironmentTheConnectorDeclared(t *testing.T) {
	t.Setenv("HOME", "/home/agent")
	t.Setenv("BASECAMP_NO_KEYRING", "1")
	t.Setenv("ANTHROPIC_API_KEY", "test-key-not-real")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "test-token-not-real")

	removed := SanitizeWorkerServerEnv()
	assert.Contains(t, removed, "ANTHROPIC_API_KEY")
	assert.Contains(t, removed, "CLAUDE_CODE_MESSAGING_TOKEN")
	_, ok := os.LookupEnv("ANTHROPIC_API_KEY")
	assert.False(t, ok, "the agent's own credential does not outlive the handshake")
	_, ok = os.LookupEnv("CLAUDE_CODE_MESSAGING_TOKEN")
	assert.False(t, ok)
	assert.Equal(t, "/home/agent", os.Getenv("HOME"), "what the connector declared is kept")
	assert.Equal(t, "1", os.Getenv("BASECAMP_NO_KEYRING"))
}
