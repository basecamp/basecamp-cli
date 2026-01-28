package resilience

import (
	"time"
)

// Config holds configuration for all resilience primitives.
type Config struct {
	// CircuitBreaker configures the circuit breaker pattern.
	CircuitBreaker CircuitBreakerConfig

	// RateLimiter configures the token bucket rate limiter.
	RateLimiter RateLimiterConfig

	// Bulkhead configures concurrent request limiting.
	Bulkhead BulkheadConfig
}

// CircuitBreakerConfig configures the circuit breaker pattern.
type CircuitBreakerConfig struct {
	// FailureThreshold is the number of consecutive failures before opening.
	// Default: 5
	FailureThreshold int

	// SuccessThreshold is the number of consecutive successes in half-open
	// state before closing the circuit.
	// Default: 2
	SuccessThreshold int

	// OpenTimeout is how long to wait before transitioning from open to half-open.
	// Default: 30 seconds
	OpenTimeout time.Duration

	// HalfOpenMaxRequests is the max concurrent requests allowed in half-open state.
	// Default: 1
	HalfOpenMaxRequests int

	// StaleAttemptTimeout is how long to wait before considering half-open attempts
	// as stale (from crashed processes). This should be longer than the expected
	// duration of slow/large operations to avoid resetting legitimate in-flight requests.
	// Default: 2 minutes (4x OpenTimeout)
	StaleAttemptTimeout time.Duration
}

// RateLimiterConfig configures the token bucket rate limiter.
type RateLimiterConfig struct {
	// MaxTokens is the maximum number of tokens in the bucket.
	// Default: 50
	MaxTokens float64

	// RefillRate is how many tokens are added per second.
	// Default: 10
	RefillRate float64

	// TokensPerRequest is how many tokens each request consumes.
	// Default: 1
	TokensPerRequest float64
}

// BulkheadConfig configures the bulkhead pattern for concurrent request limiting.
type BulkheadConfig struct {
	// MaxConcurrent is the maximum number of concurrent requests across all processes.
	// Default: 10
	MaxConcurrent int
}

// DefaultConfig returns a Config with sensible defaults for the Basecamp API.
func DefaultConfig() *Config {
	return &Config{
		CircuitBreaker: CircuitBreakerConfig{
			FailureThreshold:    5,
			SuccessThreshold:    2,
			OpenTimeout:         30 * time.Second,
			HalfOpenMaxRequests: 1,
			StaleAttemptTimeout: 2 * time.Minute, // 4x OpenTimeout to allow slow operations
		},
		RateLimiter: RateLimiterConfig{
			MaxTokens:        50,
			RefillRate:       10,
			TokensPerRequest: 1,
		},
		Bulkhead: BulkheadConfig{
			MaxConcurrent: 10,
		},
	}
}

// WithCircuitBreaker returns a copy of the config with custom circuit breaker settings.
func (c *Config) WithCircuitBreaker(cb CircuitBreakerConfig) *Config {
	copy := *c
	copy.CircuitBreaker = cb
	return &copy
}

// WithRateLimiter returns a copy of the config with custom rate limiter settings.
func (c *Config) WithRateLimiter(rl RateLimiterConfig) *Config {
	copy := *c
	copy.RateLimiter = rl
	return &copy
}

// WithBulkhead returns a copy of the config with custom bulkhead settings.
func (c *Config) WithBulkhead(bh BulkheadConfig) *Config {
	copy := *c
	copy.Bulkhead = bh
	return &copy
}

// CircuitBreaker builder methods

// WithFailureThreshold sets the failure threshold for the circuit breaker.
func (cb CircuitBreakerConfig) WithFailureThreshold(n int) CircuitBreakerConfig {
	cb.FailureThreshold = n
	return cb
}

// WithSuccessThreshold sets the success threshold for the circuit breaker.
func (cb CircuitBreakerConfig) WithSuccessThreshold(n int) CircuitBreakerConfig {
	cb.SuccessThreshold = n
	return cb
}

// WithOpenTimeout sets the open timeout for the circuit breaker.
func (cb CircuitBreakerConfig) WithOpenTimeout(d time.Duration) CircuitBreakerConfig {
	cb.OpenTimeout = d
	return cb
}

// RateLimiter builder methods

// WithMaxTokens sets the maximum tokens for the rate limiter.
func (rl RateLimiterConfig) WithMaxTokens(n float64) RateLimiterConfig {
	rl.MaxTokens = n
	return rl
}

// WithRefillRate sets the refill rate for the rate limiter.
func (rl RateLimiterConfig) WithRefillRate(n float64) RateLimiterConfig {
	rl.RefillRate = n
	return rl
}

// Bulkhead builder methods

// WithMaxConcurrent sets the maximum concurrent requests for the bulkhead.
func (bh BulkheadConfig) WithMaxConcurrent(n int) BulkheadConfig {
	bh.MaxConcurrent = n
	return bh
}
