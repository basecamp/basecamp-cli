package resilience

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRateLimiterStartsWithFullBucket(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	rl := NewRateLimiter(store, RateLimiterConfig{
		MaxTokens:        5,
		RefillRate:       10,
		TokensPerRequest: 1,
	})

	tokens, err := rl.Tokens()
	require.NoError(t, err)
	assert.Equal(t, float64(5), tokens)
}

func TestRateLimiterAllowsRequests(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	rl := NewRateLimiter(store, RateLimiterConfig{
		MaxTokens:        5,
		RefillRate:       10,
		TokensPerRequest: 1,
	})

	// Should allow up to max tokens requests
	for i := range 5 {
		allowed, err := rl.Allow()
		require.NoError(t, err)
		assert.True(t, allowed, "expected request %d to be allowed", i+1)
	}

	// Next request should be rejected (no time to refill)
	allowed, err := rl.Allow()
	require.NoError(t, err)
	assert.False(t, allowed, "expected request to be rejected after tokens exhausted")
}

func TestRateLimiterRefillsOverTime(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	rl := NewRateLimiter(store, RateLimiterConfig{
		MaxTokens:        5,
		RefillRate:       100, // 100 tokens per second = 10 tokens per 100ms
		TokensPerRequest: 1,
	})

	// Exhaust all tokens
	for range 5 {
		rl.Allow()
	}

	// Wait for refill (100ms should add ~10 tokens, capped at 5)
	time.Sleep(100 * time.Millisecond)

	// Should have tokens again
	allowed, err := rl.Allow()
	require.NoError(t, err)
	assert.True(t, allowed, "expected request to be allowed after refill time")
}

func TestRateLimiterCapsAtMaxTokens(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	rl := NewRateLimiter(store, RateLimiterConfig{
		MaxTokens:        5,
		RefillRate:       1000, // Very high refill rate
		TokensPerRequest: 1,
	})

	// Use one token
	rl.Allow()

	// Wait for refill
	time.Sleep(50 * time.Millisecond)

	// Should be capped at max tokens
	tokens, err := rl.Tokens()
	require.NoError(t, err)
	assert.True(t, tokens <= 5, "expected tokens capped at 5, got %f", tokens)
}

func TestRateLimiterRetryAfter(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	rl := NewRateLimiter(store, RateLimiterConfig{
		MaxTokens:        5,
		RefillRate:       10,
		TokensPerRequest: 1,
	})

	// Set retry-after for a short duration
	require.NoError(t, rl.SetRetryAfterDuration(50*time.Millisecond))

	// Should reject requests during retry-after
	allowed, err := rl.Allow()
	require.NoError(t, err)
	assert.False(t, allowed, "expected request to be rejected during retry-after period")

	// Check remaining time
	remaining, err := rl.RetryAfterRemaining()
	require.NoError(t, err)
	assert.True(t, remaining > 0 && remaining <= 60*time.Millisecond, "expected remaining time between 0-60ms, got %v", remaining)

	// Wait past retry-after
	time.Sleep(60 * time.Millisecond)

	// Should allow requests now
	allowed, err = rl.Allow()
	require.NoError(t, err)
	assert.True(t, allowed, "expected request to be allowed after retry-after period")
}

func TestRateLimiterSetRetryAfter(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	rl := NewRateLimiter(store, RateLimiterConfig{
		MaxTokens:        5,
		RefillRate:       10,
		TokensPerRequest: 1,
	})

	// Set retry-after via absolute time
	until := time.Now().Add(100 * time.Millisecond)
	require.NoError(t, rl.SetRetryAfter(until))

	// Should reject requests
	allowed, _ := rl.Allow()
	assert.False(t, allowed, "expected request to be rejected during retry-after period")
}

// A Retry-After block only ever moves later: a second 429 carrying a nearer
// deadline must not shorten the one already stored.
//
// This is a decision about two timestamps, so it is asserted on the stored
// timestamp and not on a stopwatch. It used to set a 200ms block, set a 50ms
// one, and assert that 150ms still remained — which is an assertion that
// fewer than fifty milliseconds of real time passed between two statements.
// Under race instrumentation on a two-core runner that is not safe, and it
// failed on CI having said nothing about the behavior it is named after.
func TestRateLimiterRetryAfterOnlyUpdatesIfLater(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	rl := NewRateLimiter(store, RateLimiterConfig{
		MaxTokens:        5,
		RefillRate:       10,
		TokensPerRequest: 1,
	})

	base := time.Now()
	later, earlier := base.Add(time.Minute), base.Add(time.Second)

	require.NoError(t, rl.SetRetryAfter(later))
	require.NoError(t, rl.SetRetryAfter(earlier))

	state, err := store.Load()
	require.NoError(t, err)
	assert.True(t, state.RateLimiter.RetryAfterUntil.Equal(later),
		"the nearer deadline shortened the block: stored %v, want %v",
		state.RateLimiter.RetryAfterUntil, later)

	// And the other direction, so that "unchanged" is not passing for free:
	// a deadline further out does move the block.
	furtherOut := base.Add(2 * time.Minute)
	require.NoError(t, rl.SetRetryAfter(furtherOut))

	state, err = store.Load()
	require.NoError(t, err)
	assert.True(t, state.RateLimiter.RetryAfterUntil.Equal(furtherOut),
		"a later deadline did not extend the block: stored %v, want %v",
		state.RateLimiter.RetryAfterUntil, furtherOut)
}

func TestRateLimiterReset(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	rl := NewRateLimiter(store, RateLimiterConfig{
		MaxTokens:        5,
		RefillRate:       10,
		TokensPerRequest: 1,
	})

	// Exhaust all tokens
	for range 5 {
		rl.Allow()
	}

	// Set retry-after
	rl.SetRetryAfterDuration(10 * time.Second)

	// Reset
	require.NoError(t, rl.Reset())

	// Should have full bucket
	tokens, _ := rl.Tokens()
	assert.Equal(t, float64(5), tokens)

	// Should allow requests (retry-after cleared)
	allowed, _ := rl.Allow()
	assert.True(t, allowed, "expected request to be allowed after reset")
}

func TestRateLimiterPersistence(t *testing.T) {
	dir := t.TempDir()

	// First rate limiter instance
	store1 := NewStore(dir)
	rl1 := NewRateLimiter(store1, RateLimiterConfig{
		MaxTokens:        5,
		RefillRate:       0.1, // Very slow refill
		TokensPerRequest: 1,
	})

	// Use 3 tokens
	for range 3 {
		rl1.Allow()
	}

	// Second rate limiter instance (simulating new process)
	store2 := NewStore(dir)
	rl2 := NewRateLimiter(store2, RateLimiterConfig{
		MaxTokens:        5,
		RefillRate:       0.1,
		TokensPerRequest: 1,
	})

	// Should only have ~2 tokens left
	tokens, err := rl2.Tokens()
	require.NoError(t, err)
	// Allow for small refill during test execution
	assert.True(t, tokens <= 3, "expected ~2 tokens, got %f", tokens)
}

func TestRateLimiterAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Create with zero config - should apply defaults
	rl := NewRateLimiter(store, RateLimiterConfig{})

	// Should work with defaults (50 max tokens)
	tokens, err := rl.Tokens()
	require.NoError(t, err)
	assert.Equal(t, float64(50), tokens)
}

func TestRateLimiterRetryAfterRemainingWhenNoBlock(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	rl := NewRateLimiter(store, RateLimiterConfig{})

	remaining, err := rl.RetryAfterRemaining()
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), remaining)
}

func TestRateLimiterTokensPerRequest(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	rl := NewRateLimiter(store, RateLimiterConfig{
		MaxTokens:        10,
		RefillRate:       1,
		TokensPerRequest: 5, // Each request costs 5 tokens
	})

	// Should allow 2 requests (10 tokens / 5 per request)
	for i := range 2 {
		allowed, err := rl.Allow()
		require.NoError(t, err)
		assert.True(t, allowed, "expected request %d to be allowed", i+1)
	}

	// Third request should fail
	allowed, _ := rl.Allow()
	assert.False(t, allowed, "expected third request to be rejected")
}
