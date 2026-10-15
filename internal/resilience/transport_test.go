package resilience

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func recordingsServer(t *testing.T, pages int, failFirst int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	var failures atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if int(failures.Add(1)) <= failFirst {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 0 {
			page = 1
		}
		if page < pages {
			q := r.URL.Query()
			q.Set("page", strconv.Itoa(page+1))
			w.Header().Set("Link", fmt.Sprintf("<%s%s?%s>; rel=\"next\"", srv.URL, r.URL.Path, q.Encode()))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id": page, "status": "active", "type": "Todo", "title": "t",
			"created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z",
		}})
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func pacedClient(t *testing.T, baseURL string, opts ...basecamp.ClientOption) (*basecamp.AccountClient, *RateLimiter) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.RateLimiter.MaxTokens = 100
	cfg.RateLimiter.RefillRate = 0.0001
	hooks := NewGatingHooksFromConfig(NewStore(t.TempDir()), cfg)
	opts = append([]basecamp.ClientOption{
		basecamp.WithHooks(hooks),
		basecamp.WithTransport(hooks.Transport(http.DefaultTransport)),
	}, opts...)
	client := basecamp.NewClient(&basecamp.Config{BaseURL: baseURL}, &basecamp.StaticTokenProvider{Token: "test-token"}, opts...)
	return client.ForAccount("99999"), hooks.rateLimiter
}

func tokensSpent(t *testing.T, rl *RateLimiter, run func()) float64 {
	t.Helper()
	before, err := rl.Tokens()
	require.NoError(t, err)
	run()
	after, err := rl.Tokens()
	require.NoError(t, err)
	return before - after
}

func TestEveryPageOfAWalkSpendsAToken(t *testing.T) {
	srv, hits := recordingsServer(t, 5, 0)
	account, rl := pacedClient(t, srv.URL)

	spent := tokensSpent(t, rl, func() {
		result, err := account.Recordings().List(context.Background(), basecamp.RecordingTypeTodo, &basecamp.RecordingsListOptions{Limit: -1})
		require.NoError(t, err)
		require.Len(t, result.Recordings, 5)
	})

	assert.Equal(t, int32(5), hits.Load())
	assert.InDelta(t, 5, spent, 0.01)
}

func TestARawRequestSpendsAToken(t *testing.T) {
	srv, hits := recordingsServer(t, 1, 0)
	account, rl := pacedClient(t, srv.URL)

	spent := tokensSpent(t, rl, func() {
		for range 3 {
			_, err := account.Get(context.Background(), "/projects/recordings.json?type=Todo")
			require.NoError(t, err)
		}
	})

	assert.Equal(t, int32(3), hits.Load())
	assert.InDelta(t, 3, spent, 0.01)
}

func TestARetrySpendsAToken(t *testing.T) {
	srv, hits := recordingsServer(t, 1, 1)
	account, rl := pacedClient(t, srv.URL, basecamp.WithBaseDelay(time.Millisecond))

	spent := tokensSpent(t, rl, func() {
		_, err := account.Get(context.Background(), "/projects/recordings.json?type=Todo")
		require.NoError(t, err)
	})

	assert.Equal(t, int32(2), hits.Load())
	assert.InDelta(t, 2, spent, 0.01)
}

func TestASinglePageOperationSpendsOneToken(t *testing.T) {
	srv, hits := recordingsServer(t, 1, 0)
	account, rl := pacedClient(t, srv.URL)

	spent := tokensSpent(t, rl, func() {
		_, err := account.Recordings().List(context.Background(), basecamp.RecordingTypeTodo, nil)
		require.NoError(t, err)
	})

	assert.Equal(t, int32(1), hits.Load())
	assert.InDelta(t, 1, spent, 0.01)
}

func TestPaceWaitsForARefillInsteadOfFailing(t *testing.T) {
	clock := holdClock(t, 0)
	rl := NewRateLimiter(NewStore(t.TempDir()), RateLimiterConfig{MaxTokens: 1, RefillRate: 2, TokensPerRequest: 1})
	rl.clock = clock.Now
	start := clock.Now()
	waits := 0
	rl.onWait = func() { waits++ }

	require.NoError(t, rl.Pace(context.Background()))
	require.NoError(t, rl.Pace(context.Background()))
	require.NoError(t, rl.Pace(context.Background()))

	assert.Equal(t, 2, waits)
	assert.Equal(t, start.Add(time.Second), clock.Now())
}

func TestPaceStopsWhenTheCallerCancels(t *testing.T) {
	rl := NewRateLimiter(NewStore(t.TempDir()), RateLimiterConfig{MaxTokens: 1, RefillRate: 0.0001, TokensPerRequest: 1})
	require.NoError(t, rl.Pace(context.Background()))

	ctx, cancel := context.WithCancel(context.Background())
	previousPause := pause
	pause = func(ctx context.Context, d time.Duration) error {
		cancel()
		return previousPause(ctx, d)
	}
	t.Cleanup(func() { pause = previousPause })

	assert.ErrorIs(t, rl.Pace(ctx), context.Canceled)
}
