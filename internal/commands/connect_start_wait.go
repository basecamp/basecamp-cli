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
// it says so, which signals stop it, and how it sleeps — by default a timer
// that ctx cuts short.
type connectStartWait struct {
	Log     func(string)
	Signals <-chan os.Signal
	Sleep   func(ctx context.Context, d time.Duration) error
}

// awaitConnectToken holds a starting connector until the agent's token can
// be had. A failure the running feed would wait out — a rate limit, the
// token endpoint's own 5xx, no answer at all — is waited out here too, for
// the Retry-After it named or on the feed's backoff, and tried again; it
// never ends the start. A process that is alive and waiting does not use up
// a supervisor's start limit, which a process exiting on every rate limit
// does in seconds. Anything else, a refused credential first among them,
// is returned at once.
//
// A shutdown signal stops it whenever it arrives — mid-wait, or with a
// renewal in flight, which it cancels — and one that arrives as a renewal
// succeeds still wins: the connector was told to stop, and does not start.
func awaitConnectToken(parent context.Context, tokens basecamp.TokenProvider, w connectStartWait) error {
	ctx, cancel := context.WithCancel(parent)
	var stopped error
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		select {
		case sig := <-w.Signals:
			stopped = connectStoppedBySignal(sig)
			cancel()
		case <-ctx.Done():
		}
	}()
	err := awaitToken(ctx, tokens, w)
	cancel()
	<-watched
	if stopped != nil {
		return stopped
	}
	select {
	case sig := <-w.Signals:
		return connectStoppedBySignal(sig)
	default:
		return err
	}
}

func awaitToken(ctx context.Context, tokens basecamp.TokenProvider, w connectStartWait) error {
	sleep := w.Sleep
	if sleep == nil {
		sleep = sleepUnlessCanceled
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
		w.Log(fmt.Sprintf("connector: could not get the agent's token yet (%s); trying again in %s", output.AsError(err).Message, max(wait.Round(time.Second), time.Second)))
		if err := sleep(ctx, wait); err != nil {
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

// sleepUnlessCanceled waits d, or less when ctx ends.
func sleepUnlessCanceled(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// connectStoppedBySignal is the error a connector stopped by sig exits with.
func connectStoppedBySignal(sig os.Signal) error {
	if sig == syscall.SIGTERM {
		return output.ErrTerminated("connector terminated")
	}
	return output.ErrInterrupted("connector interrupted")
}
