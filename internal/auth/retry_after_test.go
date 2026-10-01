package auth

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// A Retry-After is read in both of its forms, a date rounded up to the
// second, and a count too large to hold is held at the bound, never read as
// no wait at all.
func TestRetryAfterReadsBothForms(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for value, want := range map[string]int{
		"42":                      42,
		" 42 ":                    42,
		"0":                       0,
		"99999999999999999999999": maxRetryAfterSeconds,
		now.Add(90 * time.Second).Format(http.TimeFormat): 90,
		now.Add(-time.Minute).Format(http.TimeFormat):     0,
		"":     0,
		"-5":   0,
		"soon": 0,
	} {
		assert.Equal(t, want, retryAfterSeconds(value, now), "%q", value)
	}
}

// The wait a rate limit named travels with it, whichever form it came in.
func TestARateLimitCarriesItsWait(t *testing.T) {
	for _, value := range []string{"90", time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)} {
		err := statusFailure("minting an agent token", &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {value}}})
		assert.InDelta(t, 90, RetryAfter(err), 1, value)
	}
	err := statusFailure("minting an agent token", &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}})
	assert.Zero(t, RetryAfter(err))
}
