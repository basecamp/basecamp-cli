package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp/eventfeed"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/auth"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// The error the auth manager returns while credstore refuses the keyring
// after a timed-out read, as the connector logged it.
func keyringLatchedError() error {
	timeout := fmt.Errorf("keyring read timed out after 10s (the operation may have completed or may still complete): %w", context.DeadlineExceeded)
	latched := fmt.Errorf("keyring unavailable after an earlier timeout: %w; to use plaintext credential storage explicitly, set BASECAMP_NO_KEYRING=1", timeout)
	return errors.Join(latched, auth.ErrKeyringUnavailable)
}

type stallFeed struct {
	live  *eventfeed.Live
	clock *time.Time
	mu    *sync.Mutex
	lines *[]string
	err   *error
	// reached counts requests that got to the server, which only a token
	// lets through.
	reached *atomic.Int32
}

func keyringStallFeed(t *testing.T) stallFeed {
	t.Helper()
	var reached atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)

	var (
		mu    sync.Mutex
		now   = time.Date(2026, 11, 6, 18, 15, 0, 0, time.UTC)
		lines []string
		fail  = keyringLatchedError()
	)
	tokens := &feedTokens{
		log: func(line string) { mu.Lock(); lines = append(lines, line); mu.Unlock() },
		now: func() time.Time { mu.Lock(); defer mu.Unlock(); return now },
		fetch: func(context.Context) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			if fail != nil {
				return "", fail
			}
			return "token", nil
		},
	}
	live, err := eventfeed.NewLive(&basecamp.Config{BaseURL: srv.URL}, tokens, "555", eventfeed.AccountLane, connectSDKOptions()...)
	require.NoError(t, err)
	return stallFeed{live: live, clock: &now, mu: &mu, lines: &lines, err: &fail, reached: &reached}
}

func (f stallFeed) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	*f.clock = f.clock.Add(d)
}

func (f stallFeed) said() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), *f.lines...)
}

func (f stallFeed) pollKind(t *testing.T) eventfeed.PollErrorKind {
	t.Helper()
	_, err := f.live.Polls().Poll(t.Context(), eventfeed.Cursor{Since: "now"}, eventfeed.Filters{})
	var pollErr *eventfeed.PollError
	require.ErrorAs(t, err, &pollErr)
	return pollErr.Kind
}

func (f stallFeed) mintKind(t *testing.T) eventfeed.MintErrorKind {
	t.Helper()
	_, err := f.live.Minter().MintStreamTicket(t.Context())
	var mintErr *eventfeed.MintError
	require.ErrorAs(t, err, &mintErr)
	return mintErr.Kind
}

// A keyring that stops answering is waited for, and said: the feed retries
// its poll and its reconnect, and the log says why once, when it starts.
func TestAKeyringStallIsRetriedAndSaid(t *testing.T) {
	f := keyringStallFeed(t)

	assert.Equal(t, eventfeed.PollTransient, f.pollKind(t))
	assert.Equal(t, eventfeed.MintTransient, f.mintKind(t))
	f.advance(time.Minute)
	assert.Equal(t, eventfeed.PollTransient, f.pollKind(t))

	said := f.said()
	require.Len(t, said, 1, "once per outage, not once per retry")
	assert.Zero(t, f.reached.Load())
	assert.Contains(t, said[0], "system keyring did not give up the agent's token")
}

// A keyring that stays silent past the limit ends the run, on the poll lane
// and the mint lane alike, instead of the feed retrying unseen for as long
// as the process lives. This is the stall: before it, a walk retried its poll
// and a dropped socket its mint forever, and the connector delivered nothing.
func TestAKeyringThatStaysSilentEndsTheRun(t *testing.T) {
	f := keyringStallFeed(t)
	assert.Equal(t, eventfeed.PollTransient, f.pollKind(t))

	f.advance(connectKeyringOutageLimit)
	assert.Equal(t, eventfeed.PollUnrecoverable, f.pollKind(t))
	assert.Equal(t, eventfeed.MintUnrecoverable, f.mintKind(t))

	_, err := f.live.Polls().Poll(t.Context(), eventfeed.Cursor{Since: "now"}, eventfeed.Filters{})
	assert.Contains(t, err.Error(), "system keyring has not given up the agent's token for 15m0s")
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "the keyring's own error stays in the chain")
}

// A token that arrives ends the outage: the next stall gets the full limit
// again, and is said again.
func TestAnAnsweringKeyringEndsTheOutage(t *testing.T) {
	f := keyringStallFeed(t)
	assert.Equal(t, eventfeed.PollTransient, f.pollKind(t))
	f.advance(connectKeyringOutageLimit - time.Minute)

	f.mu.Lock()
	*f.err = nil
	f.mu.Unlock()
	_, err := f.live.Minter().MintStreamTicket(t.Context())
	require.Error(t, err) // the teapot's answer
	assert.Equal(t, int32(1), f.reached.Load(), "the token was served")

	f.mu.Lock()
	*f.err = keyringLatchedError()
	f.mu.Unlock()
	f.advance(2 * time.Minute)
	assert.Equal(t, eventfeed.PollTransient, f.pollKind(t), "a new outage starts its own clock")
	assert.Len(t, f.said(), 2)
}

// A failure that is not the keyring's — a renewal the server could not
// serve, after the keyring gave up the credential — also ends the outage:
// the keyring answered. A later stall gets the full limit, not what is left
// of the old one.
func TestAFailureThatIsNotTheKeyringsEndsTheOutage(t *testing.T) {
	f := keyringStallFeed(t)
	assert.Equal(t, eventfeed.PollTransient, f.pollKind(t))
	f.advance(connectKeyringOutageLimit - time.Minute)

	f.mu.Lock()
	unavailable := output.ErrAPI(http.StatusServiceUnavailable, "minting an agent token: the server answered HTTP 503")
	unavailable.Retryable = true
	*f.err = unavailable
	f.mu.Unlock()
	_, _ = f.live.Polls().Poll(t.Context(), eventfeed.Cursor{Since: "now"}, eventfeed.Filters{})

	f.mu.Lock()
	*f.err = keyringLatchedError()
	f.mu.Unlock()
	f.advance(2 * time.Minute)
	assert.Equal(t, eventfeed.PollTransient, f.pollKind(t), "the old outage's clock does not carry over")
}

type scriptedTokens struct{ errs []error }

func (s *scriptedTokens) AccessToken(context.Context) (string, error) {
	if len(s.errs) == 0 {
		return "token", nil
	}
	err := s.errs[0]
	s.errs = s.errs[1:]
	return "", err
}

// A connector started while the keyring is not answering, whether timed
// out or off the bus, waits for it on the backoff, as the running feed
// does, rather than exiting on the first try.
func TestAStartWaitsOutASilentKeyring(t *testing.T) {
	offTheBus := errors.Join(fmt.Errorf("credentials not found: %w", io.EOF), auth.ErrKeyringUnavailable)
	tokens := &scriptedTokens{errs: []error{offTheBus, keyringLatchedError()}}
	var waits []time.Duration
	err := awaitToken(t.Context(), tokens, connectStartWait{Sleep: func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}})
	require.NoError(t, err)
	assert.Len(t, waits, 2, "each silence is waited out")
}

// A starting connector's first credential read waits out a keyring that
// does not answer, as its token wait does, instead of exiting with a
// remedy that would send its operator to log in again (2026-11-07).
func TestConnectStartWaitsOutAKeyringThatDoesNotAnswer(t *testing.T) {
	var reads int
	read := connectStartRead(func(context.Context) error {
		reads++
		if reads < 3 {
			return keyringLatchedError()
		}
		return nil
	})
	var waits []time.Duration
	var lines []string
	err := awaitConnectToken(t.Context(), read, connectStartWait{
		Log:   func(line string) { lines = append(lines, line) },
		Sleep: func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil },
	})
	require.NoError(t, err)
	assert.Equal(t, 3, reads)
	assert.Len(t, waits, 2)
	assert.Len(t, lines, 2)

	// A read the store answered is not waited on.
	refused := output.ErrAuth("refused")
	err = awaitConnectToken(t.Context(), connectStartRead(func(context.Context) error { return refused }), connectStartWait{
		Sleep: func(context.Context, time.Duration) error { t.Fatal("waited on an answer"); return nil },
	})
	assert.ErrorIs(t, err, refused)
}
