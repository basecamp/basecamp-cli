package resilience

import (
	"context"
	"slices"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/basecamp/basecamp-sdk/go/pkg/generated"
)

// Verify GatingHooks implements basecamp.GatingHooks at compile time.
var _ basecamp.GatingHooks = (*GatingHooks)(nil)

// releaseKey is the context key for the bulkhead release function.
type releaseKey struct{}

// operationKey carries the operation name from OnOperationStart to the
// request hooks, which only see method and URL.
type operationKey struct{}

// GatingHooks implements basecamp.GatingHooks to provide resilience patterns
// for SDK operations. It gates requests through circuit breaker, rate limiter,
// and bulkhead before they execute.
type GatingHooks struct {
	circuitBreaker *CircuitBreaker
	rateLimiter    *RateLimiter
	bulkhead       *Bulkhead
	maxWait        time.Duration
}

// NewGatingHooks creates a new GatingHooks with the given primitives and the
// default queueing bound.
func NewGatingHooks(cb *CircuitBreaker, rl *RateLimiter, bh *Bulkhead) *GatingHooks {
	return &GatingHooks{
		circuitBreaker: cb,
		rateLimiter:    rl,
		bulkhead:       bh,
		maxWait:        DefaultMaxWait,
	}
}

// NewGatingHooksFromConfig creates a GatingHooks using the provided config and store.
func NewGatingHooksFromConfig(store *Store, cfg *Config) *GatingHooks {
	cb := NewCircuitBreaker(store, cfg.CircuitBreaker)
	rl := NewRateLimiter(store, cfg.RateLimiter)
	bh := NewBulkhead(store, cfg.Bulkhead)
	hooks := NewGatingHooks(cb, rl, bh)
	if cfg.MaxWait > 0 {
		hooks.maxWait = cfg.MaxWait
	}
	return hooks
}

// OnOperationGate is called before OnOperationStart.
// It checks rate limiter, bulkhead, and circuit breaker before allowing
// the operation to proceed. The rate limiter and bulkhead queue rather than
// reject: parallel CLI invocations share both, and a burst that briefly
// exceeds a limit waits its turn within one maxWait budget shared by the
// two, failing with a *GateError only when the budget runs out.
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
	// A canceled caller is answered before anything is read or reserved.
	if err := ctx.Err(); err != nil {
		return ctx, err
	}

	// An open circuit fails fast before any queueing: waiting for a token
	// only to be refused by the breaker would defeat its purpose during an
	// outage. Nothing is reserved here; the reserving check still runs last.
	if h.circuitBreaker != nil && h.circuitBreaker.Tripped() {
		return ctx, basecamp.ErrCircuitOpen
	}

	start := time.Now()
	deadline := start.Add(h.maxWait)

	// Rate limiter first: it queues for a refill and consumes a token on
	// success, which is the only state it holds, so a later rejection wastes
	// at most that token.
	if h.rateLimiter != nil {
		if err := h.rateLimiter.waitSince(ctx, start, deadline); err != nil {
			return ctx, err
		}
	}

	// Acquire bulkhead slot (PID-based, released in OnOperationEnd)
	if h.bulkhead != nil {
		if err := h.bulkhead.waitSince(ctx, start, deadline); err != nil {
			return ctx, err
		}
		// A cancellation that landed while the slot was being taken must not
		// go on to reserve a half-open attempt.
		if err := ctx.Err(); err != nil {
			_ = h.bulkhead.Release()
			return ctx, err
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

// OnOperationStart is called when a semantic SDK operation begins. Gating
// already happened in OnOperationGate; this only names the operation for the
// request hooks, which otherwise see method and URL alone.
func (h *GatingHooks) OnOperationStart(ctx context.Context, op basecamp.OperationInfo) context.Context {
	return context.WithValue(ctx, operationKey{}, op.Operation)
}

// OnOperationEnd is called when a semantic SDK operation completes.
// It releases the bulkhead slot and records success/failure for circuit breaker.
func (h *GatingHooks) OnOperationEnd(ctx context.Context, op basecamp.OperationInfo, err error, duration time.Duration) {
	// Release bulkhead slot if we acquired one
	if _, ok := ctx.Value(releaseKey{}).(bool); ok && h.bulkhead != nil {
		_ = h.bulkhead.Release() //nolint:contextcheck // lock acquisition is context-independent by design
	}

	// Record success/failure for circuit breaker
	if h.circuitBreaker != nil {
		if err != nil {
			// Check if this is a retryable/server error that should trip the circuit
			if isCircuitBreakerError(err) {
				_ = h.circuitBreaker.RecordFailure() //nolint:contextcheck // lock acquisition is context-independent by design
			}
		} else {
			_ = h.circuitBreaker.RecordSuccess() //nolint:contextcheck // lock acquisition is context-independent by design
		}
	}

	// Note: Retry-After handling is done in OnRequestEnd which has access to the
	// HTTP response headers. We don't duplicate it here to avoid overriding a
	// shorter Retry-After from the server with the 60s default.
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
		_ = h.rateLimiter.SetRetryAfterDuration(time.Duration(result.RetryAfter) * time.Second) //nolint:contextcheck // lock acquisition is context-independent by design
	} else if result.StatusCode == 429 && !isVerdict429(ctx) {
		// Default to 60 seconds if no Retry-After specified (SDK parity for 429 only)
		// Note: 503 requires explicit Retry-After header per SDK behavior
		_ = h.rateLimiter.SetRetryAfterDuration(60 * time.Second) //nolint:contextcheck // lock acquisition is context-independent by design
	}
}

// isVerdict429 reports whether a headerless 429 on the operation in ctx is an
// answer rather than throttling. The SDK's behavior model declares which
// statuses each operation retries on, and an operation whose set excludes 429
// (UpdateProjectClientAccess: its 429 is the account seat-limit verdict) is
// one the server answers 429 deterministically. Blocking every later command
// for a minute on such an answer would gate unrelated work on a fact about
// one request's input. Operations the model does not name keep the default.
func isVerdict429(ctx context.Context) bool {
	operation, _ := ctx.Value(operationKey{}).(string)
	if operation == "" {
		return false
	}
	retryOn, ok := generated.GetOperationRetryOn(operation)
	return ok && !slices.Contains(retryOn, 429)
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
	case basecamp.CodeValidation, basecamp.CodeLimitExceeded:
		// A 422 is the caller's input; a 507 is a plan limit no retry can
		// satisfy. Neither says the server is unhealthy. (A 507 tripped the
		// circuit before SDK v0.14.0 only because it arrived as CodeAPI.)
		return false
	default:
		return false
	}
}
