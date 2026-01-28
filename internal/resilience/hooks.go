package resilience

import (
	"context"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// Verify GatingHooks implements basecamp.GatingHooks at compile time.
var _ basecamp.GatingHooks = (*GatingHooks)(nil)

// releaseKey is the context key for the bulkhead release function.
type releaseKey struct{}

// GatingHooks implements basecamp.GatingHooks to provide resilience patterns
// for SDK operations. It gates requests through circuit breaker, rate limiter,
// and bulkhead before they execute.
type GatingHooks struct {
	circuitBreaker *CircuitBreaker
	rateLimiter    *RateLimiter
	bulkhead       *Bulkhead
}

// NewGatingHooks creates a new GatingHooks with the given primitives.
func NewGatingHooks(cb *CircuitBreaker, rl *RateLimiter, bh *Bulkhead) *GatingHooks {
	return &GatingHooks{
		circuitBreaker: cb,
		rateLimiter:    rl,
		bulkhead:       bh,
	}
}

// NewGatingHooksFromConfig creates a GatingHooks using the provided config and store.
func NewGatingHooksFromConfig(store *Store, cfg *Config) *GatingHooks {
	cb := NewCircuitBreaker(store, cfg.CircuitBreaker)
	rl := NewRateLimiter(store, cfg.RateLimiter)
	bh := NewBulkhead(store, cfg.Bulkhead)
	return NewGatingHooks(cb, rl, bh)
}

// OnOperationGate is called before OnOperationStart.
// It checks rate limiter, bulkhead, and circuit breaker before allowing
// the operation to proceed.
//
// Gate order is important: rate limiter and bulkhead are checked BEFORE
// circuit breaker because the circuit breaker reserves a half-open slot
// atomically. If we checked circuit breaker first and then rate limiter
// rejected, the half-open slot would leak (never released).
//
// Tradeoff: This ordering means rate limiter tokens are consumed even if
// bulkhead is full or circuit is open. This is acceptable for a CLI tool
// where occasional token waste is preferable to half-open slot leaks.
//
// Returns a context that should be used for the operation and an error
// if the operation should be rejected.
func (h *GatingHooks) OnOperationGate(ctx context.Context, op basecamp.OperationInfo) (context.Context, error) {
	// Check rate limiter first (no state reservation, safe to reject)
	if h.rateLimiter != nil {
		allowed, _ := h.rateLimiter.Allow() // Fail open on error
		if !allowed {
			return ctx, basecamp.ErrRateLimited
		}
	}

	// Acquire bulkhead slot (PID-based, released in OnOperationEnd)
	if h.bulkhead != nil {
		acquired, _ := h.bulkhead.Acquire() // Fail open on error
		if !acquired {
			return ctx, basecamp.ErrBulkheadFull
		}
		// Store marker in context so OnOperationEnd knows to release the slot
		ctx = context.WithValue(ctx, releaseKey{}, true)
	}

	// Check circuit breaker LAST (reserves half-open slot atomically)
	// By checking this last, we ensure that if we reserve a slot,
	// the request WILL proceed (rate limiter and bulkhead already passed).
	if h.circuitBreaker != nil {
		allowed, _ := h.circuitBreaker.Allow() // Fail open on error
		if !allowed {
			// Release bulkhead slot if we acquired one
			if _, ok := ctx.Value(releaseKey{}).(bool); ok && h.bulkhead != nil {
				_ = h.bulkhead.Release()
			}
			return ctx, basecamp.ErrCircuitOpen
		}
	}

	return ctx, nil
}

// OnOperationStart is called when a semantic SDK operation begins.
func (h *GatingHooks) OnOperationStart(ctx context.Context, op basecamp.OperationInfo) context.Context {
	// No additional setup needed; gating already happened in OnOperationGate
	return ctx
}

// OnOperationEnd is called when a semantic SDK operation completes.
// It releases the bulkhead slot and records success/failure for circuit breaker.
func (h *GatingHooks) OnOperationEnd(ctx context.Context, op basecamp.OperationInfo, err error, duration time.Duration) {
	// Release bulkhead slot if we acquired one
	if _, ok := ctx.Value(releaseKey{}).(bool); ok && h.bulkhead != nil {
		_ = h.bulkhead.Release()
	}

	// Record success/failure for circuit breaker
	if h.circuitBreaker != nil {
		if err != nil {
			// Check if this is a retryable/server error that should trip the circuit
			if isCircuitBreakerError(err) {
				_ = h.circuitBreaker.RecordFailure()
			}
		} else {
			_ = h.circuitBreaker.RecordSuccess()
		}
	}

	// Handle rate limiting from API response
	if h.rateLimiter != nil && err != nil {
		apiErr := basecamp.AsError(err)
		if apiErr != nil && apiErr.Code == basecamp.CodeRateLimit {
			// Default to 60 seconds if no Retry-After specified
			retryAfter := 60 * time.Second
			if apiErr.HTTPStatus == 429 {
				// The SDK might have already handled this, but set it just in case
				_ = h.rateLimiter.SetRetryAfterDuration(retryAfter)
			}
		}
	}
}

// OnRequestStart is called before an HTTP request is sent.
func (h *GatingHooks) OnRequestStart(ctx context.Context, info basecamp.RequestInfo) context.Context {
	return ctx
}

// OnRequestEnd is called after an HTTP request completes.
// It honors Retry-After headers from 429/503 responses to back off the rate limiter.
func (h *GatingHooks) OnRequestEnd(ctx context.Context, info basecamp.RequestInfo, result basecamp.RequestResult) {
	if h.rateLimiter == nil {
		return
	}

	// Honor Retry-After header from rate-limited or overloaded responses
	if result.RetryAfter > 0 {
		_ = h.rateLimiter.SetRetryAfterDuration(time.Duration(result.RetryAfter) * time.Second)
	} else if result.StatusCode == 429 {
		// Default to 60 seconds if no Retry-After specified (SDK parity for 429 only)
		// Note: 503 requires explicit Retry-After header per SDK behavior
		_ = h.rateLimiter.SetRetryAfterDuration(60 * time.Second)
	}
}

// OnRetry is called before a retry attempt.
func (h *GatingHooks) OnRetry(ctx context.Context, info basecamp.RequestInfo, attempt int, err error) {
	// Nothing to do; the SDK handles retries automatically
}

// isCircuitBreakerError returns true if the error should trip the circuit breaker.
// We only trip on server errors (5xx) and network errors, not on client errors (4xx).
func isCircuitBreakerError(err error) bool {
	if err == nil {
		return false
	}

	apiErr := basecamp.AsError(err)
	if apiErr == nil {
		// Unknown error type - treat as a failure
		return true
	}

	switch apiErr.Code {
	case basecamp.CodeNetwork:
		// Network errors should trip the circuit
		return true
	case basecamp.CodeAPI:
		// Server errors (5xx) should trip the circuit
		return apiErr.HTTPStatus >= 500
	case basecamp.CodeRateLimit:
		// Rate limiting is expected behavior, not a failure
		return false
	case basecamp.CodeAuth, basecamp.CodeForbidden, basecamp.CodeNotFound, basecamp.CodeUsage, basecamp.CodeAmbiguous:
		// Client errors shouldn't trip the circuit
		return false
	default:
		return false
	}
}
