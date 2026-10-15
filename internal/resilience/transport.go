package resilience

import (
	"context"
	"net/http"
	"sync/atomic"
)

type prepaidKey struct{}

func withPrepaidRequest(ctx context.Context) context.Context {
	prepaid := &atomic.Bool{}
	prepaid.Store(true)
	return context.WithValue(ctx, prepaidKey{}, prepaid)
}

func spendPrepaidRequest(ctx context.Context) bool {
	prepaid, ok := ctx.Value(prepaidKey{}).(*atomic.Bool)
	return ok && prepaid.CompareAndSwap(true, false)
}

type pacedTransport struct {
	base        http.RoundTripper
	rateLimiter *RateLimiter
}

func (t *pacedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	if !spendPrepaidRequest(ctx) {
		if err := t.rateLimiter.Pace(ctx); err != nil {
			if req.Body != nil {
				_ = req.Body.Close()
			}
			return nil, err
		}
	}
	return t.base.RoundTrip(req)
}

func (h *GatingHooks) Transport(base http.RoundTripper) http.RoundTripper {
	if h.rateLimiter == nil {
		return base
	}
	return &pacedTransport{base: base, rateLimiter: h.rateLimiter}
}
