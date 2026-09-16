package connector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
)

// The generated operation must not follow a 3xx to a server-supplied target:
// zero egress to a foreign redirect, before any continuation check can run.
func TestTheLiveAdapterRefusesRedirectsBeforeAnyEgress(t *testing.T) {
	var foreignHits atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		foreignHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer foreign.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+r.URL.Path, http.StatusFound)
	}))
	defer origin.Close()

	adapter, err := NewLiveFeedAdapter(&basecamp.Config{BaseURL: origin.URL}, &basecamp.StaticTokenProvider{Token: "token"}, "2914079", nil,
		// One attempt: the SDK retries a refused hop as a network error, which
		// only repeats the same-origin request and slows the test down.
		basecamp.WithMaxRetries(0))
	require.NoError(t, err)

	_, err = adapter.Poll(context.Background(), eventfeed.Cursor{Since: "1"}, eventfeed.Filters{})
	var pollErr *eventfeed.PollError
	require.ErrorAs(t, err, &pollErr)
	assert.Equal(t, eventfeed.PollRedirectRefused, pollErr.Kind)

	_, err = adapter.MintStreamTicket(context.Background())
	require.Error(t, err)

	assert.Zero(t, foreignHits.Load(), "no request may reach the redirect target")
}
