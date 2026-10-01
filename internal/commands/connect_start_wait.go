package commands

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"syscall"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// The feed's own reconnect backoff (EVENT_FEED_BACKOFF_BASE and its cap),
// which a start waits on when the token endpoint names no wait of its own.
const (
	connectStartBackoffBase = time.Second
	connectStartBackoffCap  = 60 * time.Second
)

// connectStartWait is how a starting connector waits for its token: where
// it says so, which signals end the wait, and how it sleeps — the default
// a timer that a signal or ctx cuts short.
type connectStartWait struct {
	Log     func(string)
	Signals <-chan os.Signal
	Sleep   func(ctx context.Context, signals <-chan os.Signal, d time.Duration) error
}

// awaitConnectToken holds a starting connector until the agent's token can
// be had. A failure the running feed would wait out — a rate limit, the
// token endpoint's own 5xx, no answer at all — is waited out here too, for
// the Retry-After it named or on the feed's backoff, and tried again; it
// never ends the start. A process that is alive and waiting does not use up
// a supervisor's start limit, which a process exiting on every rate limit
// does in seconds. Anything else, a refused credential first among them,
// is returned at once. So is a signal or a canceled ctx, mid-wait.
func awaitConnectToken(ctx context.Context, tokens basecamp.TokenProvider, w connectStartWait) error {
	sleep := w.Sleep
	if sleep == nil {
		sleep = sleepUnlessStopped
	}
	for attempt := 1; ; attempt++ {
		_, err := tokens.AccessToken(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		asked, ok := tokenRetry(err)
		if !ok {
			return err
		}
		wait := asked
		if wait <= 0 {
			wait = connectStartBackoff(attempt)
		}
		w.Log(fmt.Sprintf("connector: could not get the agent's token yet (%s); trying again in %s", output.AsError(err).Message, wait.Round(time.Second)))
		if err := sleep(ctx, w.Signals, wait); err != nil {
			return err
		}
	}
}

// connectStartBackoff is the feed's full-jitter draw for the attempt'th
// consecutive failure: uniform in (0, min(base × 2^(attempt−1), cap)].
func connectStartBackoff(attempt int) time.Duration {
	envelope := connectStartBackoffCap
	if attempt <= 6 {
		envelope = min(connectStartBackoffBase<<(attempt-1), connectStartBackoffCap)
	}
	return time.Duration(rand.Int64N(int64(envelope))) + 1 //nolint:gosec // jitter spreads retries; nothing depends on it being unpredictable
}

// sleepUnlessStopped waits d, or less when ctx ends or a shutdown signal
// arrives, and says which stopped it.
func sleepUnlessStopped(ctx context.Context, signals <-chan os.Signal, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case sig := <-signals:
		return connectStoppedBySignal(sig)
	}
}

// connectStoppedBySignal is the error a connector stopped by sig exits with.
func connectStoppedBySignal(sig os.Signal) error {
	if sig == syscall.SIGTERM {
		return output.ErrTerminated("connector terminated")
	}
	return output.ErrInterrupted("connector interrupted")
}
