package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
