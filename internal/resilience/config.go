package resilience

import (
	"time"
)

// Config holds configuration for all resilience primitives.
type Config struct {
	// StateDir is the directory where state files are stored.
	// If empty, uses the default (~/.cache/bcq/resilience/).
	StateDir string

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

	// StaleProcessTimeout is how long before a process is considered dead
	// and its permit can be reclaimed.
	// Default: 60 seconds
	StaleProcessTimeout time.Duration
}

// DefaultConfig returns a Config with sensible defaults for the Basecamp API.
func DefaultConfig() *Config {
	return &Config{
		StateDir: "", // Use default location
		CircuitBreaker: CircuitBreakerConfig{
			FailureThreshold:    5,
			SuccessThreshold:    2,
			OpenTimeout:         30 * time.Second,
			HalfOpenMaxRequests: 1,
		},
		RateLimiter: RateLimiterConfig{
			MaxTokens:        50,
			RefillRate:       10,
			TokensPerRequest: 1,
		},
		Bulkhead: BulkheadConfig{
			MaxConcurrent:       10,
			StaleProcessTimeout: 60 * time.Second,
		},
	}
}

// WithStateDir returns a copy of the config with a custom state directory.
func (c *Config) WithStateDir(dir string) *Config {
	copy := *c
	copy.StateDir = dir
	return &copy
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

// WithStaleProcessTimeout sets the stale process timeout for the bulkhead.
func (bh BulkheadConfig) WithStaleProcessTimeout(d time.Duration) BulkheadConfig {
	bh.StaleProcessTimeout = d
	return bh
}
